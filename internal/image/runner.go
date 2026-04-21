package image

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/432539/gpt2api/internal/scheduler"
	"github.com/432539/gpt2api/internal/upstream/chatgpt"
	"github.com/432539/gpt2api/pkg/logger"
)

// Runner 单次/多次生图的执行器。封装完整的 chatgpt.com image2 主链路:
//
//	ChatRequirementsV2 → PrepareFConversation → StreamFConversation(SSE) →
//	ParseImageSSE → PollConversationForImages → ImageDownloadURL(签名 URL)
//
// 注意:/backend-api/conversation/init 不在 image2 主链路内,只用于账号 quota / blocked_features 弱诊断。
// 灰度桶未命中(preview_only)会自动换账号重试。
type Runner struct {
	sched *scheduler.Scheduler
	dao   *DAO
}

// NewRunner 构造 Runner。
func NewRunner(sched *scheduler.Scheduler, dao *DAO) *Runner {
	return &Runner{sched: sched, dao: dao}
}

// ReferenceImage 是图生图/编辑的一张参考图输入。
// 只需要提供原始字节 + 可选的文件名,Runner 会在运行时调用 chatgpt Client 上传。
type ReferenceImage struct {
	Data     []byte
	FileName string // 可选,未填时按长度 + 嗅探扩展名生成
}

// RunOptions 是单次生图的输入。
type RunOptions struct {
	TaskID            string
	ModelID           uint64
	UpstreamModel     string // 默认 "auto"(由上游根据 system_hints 挑选图像模型)
	Prompt            string
	N                 int              // 期望张数;实际由上游返回决定
	MaxAttempts       int              // 灰度未命中时最大重试,默认 2
	PerAttemptTimeout time.Duration    // 单次尝试总超时,默认 5min
	PollMaxWait       time.Duration    // 轮询最长等待,默认 300s
	References        []ReferenceImage // 图生图/编辑:参考图
}

// RunResult 是单次生图的输出。
type RunResult struct {
	Status         string // success / failed
	ConversationID string
	AccountID      uint64
	FileIDs        []string // chatgpt.com 侧的原始 ref("sed:" 前缀表示 sediment)
	SignedURLs     []string // 直接可访问的签名 URL(15 分钟有效)
	ContentTypes   []string
	ErrorCode      string
	ErrorMessage   string
	Attempts       int  // 跨账号尝试次数(runOnce 次数)
	TurnsInConv    int  // 当前账号内同会话 picture_v2 轮次
	IsPreview      bool // true=返回的是 IMG1 sediment 预览(3 轮均未命中 IMG2 灰度,已尽力)
	DurationMs     int64
}

// Run 执行生图。会同步阻塞直到完成/失败;调用方自行做超时控制(传 ctx)。
func (r *Runner) Run(ctx context.Context, opt RunOptions) *RunResult {
	start := time.Now()
	if opt.MaxAttempts <= 0 {
		opt.MaxAttempts = 2
	}
	if opt.PerAttemptTimeout <= 0 {
		opt.PerAttemptTimeout = 5 * time.Minute
	}
	if opt.PollMaxWait <= 0 {
		opt.PollMaxWait = 300 * time.Second
	}
	if opt.UpstreamModel == "" {
		// 对齐浏览器抓包 + 参考实现:图像走 f/conversation 时 model 字段和
		// 普通 chat 一致用 "auto",通过 system_hints=["picture_v2"] 让上游知道
		// 这是图像任务。硬写 "gpt-5-3" 在免费/新账号上会直接 404。
		opt.UpstreamModel = "auto"
	}
	if opt.N <= 0 {
		opt.N = 1
	}

	result := &RunResult{Status: StatusFailed, ErrorCode: ErrUnknown}

	// 仅当有 DAO 和 taskID 时才落库
	if r.dao != nil && opt.TaskID != "" {
		_ = r.dao.MarkRunning(ctx, opt.TaskID, 0)
	}

	// 排除集:跨账号重试时跳过已尝试过的账号
	var excludeAccountIDs map[uint64]struct{}

	// 代理切换和 preview_only 换号不算 attempt
	attempt := 0
	previewRetries := 0
	const maxPreviewRetries = 5 // preview_only 最多换 5 个号

	for attempt < opt.MaxAttempts {
		attempt++
		result.Attempts = attempt
		if err := ctx.Err(); err != nil {
			result.ErrorCode = ErrUnknown
			result.ErrorMessage = err.Error()
			break
		}

		attemptCtx, cancel := context.WithTimeout(ctx, opt.PerAttemptTimeout)
		ok, status, err := r.runOnce(attemptCtx, opt, result, excludeAccountIDs)
		cancel()

		if ok {
			result.Status = StatusSuccess
			result.ErrorCode = ""
			result.ErrorMessage = ""
			break
		}
		// 记录本次失败原因
		if err != nil {
			result.ErrorMessage = err.Error()
		}
		result.ErrorCode = status

		// ---- 静默退避策略 ----

		// 1) 代理错误:切换代理,不计入 attempt,不换账号
		if isProxyError(err) && result.AccountID > 0 {
			if b, _ := r.sched.AccountBinding(ctx, result.AccountID); b != nil && b.ProxyID > 0 {
				newURL, newPID := r.sched.SwitchProxy(ctx, result.AccountID, b.ProxyID)
				if newURL != "" {
					attempt-- // 不计入重试次数
					logger.L().Info("image runner proxy failed, silently switched",
						zap.String("task_id", opt.TaskID),
						zap.Uint64("account_id", result.AccountID),
						zap.Uint64("old_proxy_id", b.ProxyID),
						zap.Uint64("new_proxy_id", newPID))
					continue
				}
			}
		}

		// 2) 临时性上游错误(非内容策略、非认证失败):静默重试一次
		if status == ErrUpstream || status == ErrPollTimeout {
			if result.AccountID > 0 {
				if excludeAccountIDs == nil {
					excludeAccountIDs = make(map[uint64]struct{})
				}
				excludeAccountIDs[result.AccountID] = struct{}{}
			}
			logger.L().Info("image runner upstream error, silently retrying",
				zap.String("task_id", opt.TaskID),
				zap.String("status", status),
				zap.Int("attempt", attempt))
			continue
		}

		// 3) preview_only:降低账号置信度 + 换号重试(不计入 attempt)
		if status == ErrPreviewOnly {
			previewRetries++
			if result.AccountID > 0 {
				if excludeAccountIDs == nil {
					excludeAccountIDs = make(map[uint64]struct{})
				}
				excludeAccountIDs[result.AccountID] = struct{}{}
			}
			if previewRetries >= maxPreviewRetries {
				logger.L().Warn("image runner preview_only exhausted all retries",
					zap.String("task_id", opt.TaskID),
					zap.Int("preview_retries", previewRetries))
				break
			}
			attempt-- // 不计入 attempt,给真正的错误留重试机会
			logger.L().Info("image runner preview_only, switching account",
				zap.String("task_id", opt.TaskID),
				zap.Uint64("failed_account", result.AccountID),
				zap.Int("preview_retry", previewRetries),
				zap.Int("max", maxPreviewRetries))
			continue
		}

		// 4) 确定性错误(内容策略/认证失败/无账号):直接报给用户
		break
	}

	result.DurationMs = time.Since(start).Milliseconds()

	// 落库
	if r.dao != nil && opt.TaskID != "" {
		if result.Status == StatusSuccess {
			_ = r.dao.MarkSuccess(ctx, opt.TaskID, result.ConversationID,
				result.FileIDs, result.SignedURLs)
		} else {
			_ = r.dao.MarkFailed(ctx, opt.TaskID, result.ErrorCode)
		}
	}
	return result
}

// runOnce 一次完整的尝试。返回 (ok, errorCode, err)。
// result 会被就地更新(ConversationID / FileIDs / SignedURLs / AccountID 等)。
func (r *Runner) runOnce(ctx context.Context, opt RunOptions, result *RunResult, excludeIDs map[uint64]struct{}) (bool, string, error) {
	// 1) 调度账号(带排除集,跨账号重试时跳过已尝试的)
	lease, err := r.sched.DispatchWithExclude(ctx, "image", excludeIDs)
	if err != nil {
		if errors.Is(err, scheduler.ErrNoAvailable) {
			return false, ErrNoAccount, err
		}
		return false, ErrUnknown, err
	}
	defer func() {
		_ = lease.Release(context.Background())
	}()
	result.AccountID = lease.Account.ID
	// 立刻把 account_id 写回 image_tasks,供后续图片代理端点按 task_id 解出 AT。
	// MarkRunning 在 status=running 时 WHERE 不命中,所以用专门的 SetAccount。
	if r.dao != nil && opt.TaskID != "" {
		_ = r.dao.SetAccount(ctx, opt.TaskID, lease.Account.ID)
	}

	// 2) 构造上游 client
	cli, err := chatgpt.New(chatgpt.Options{
		AuthToken: lease.AuthToken,
		DeviceID:  lease.DeviceID,
		SessionID: lease.SessionID,
		ProxyURL:  lease.ProxyURL,
		Cookies:   lease.Cookies,
	})
	if err != nil {
		return false, ErrUnknown, fmt.Errorf("chatgpt client: %w", err)
	}

	// 2.5) 轻量 quota 预检:调用 conversation/init(picture_v2) 做 best-effort quota 诊断。
	// 它不是 image 能力主判据,只是为了尽早跳过“明确 blocked / 明确耗尽”的账号,
	// 减少无意义的 ChatRequirements / PoW 消耗。遇到 404/解析失败等情况会降级放行,
	// 最终是否能出图仍以后续 f/conversation 实际结果为准。
	hasQuota, blockedReason, probeErr := cli.ImageQuotaProbe(ctx)
	if probeErr != nil {
		logger.L().Warn("image runner quota probe error, continue anyway",
			zap.Uint64("account_id", lease.Account.ID), zap.Error(probeErr))
		// 预检网络/解析错误不阻断,降级放行
	} else if !hasQuota {
		logger.L().Info("image runner quota exhausted, skip account",
			zap.Uint64("account_id", lease.Account.ID),
			zap.String("blocked_reason", blockedReason))
		r.sched.MarkRateLimited(context.Background(), lease.Account.ID)
		return false, ErrRateLimited, fmt.Errorf("quota probe: %s", blockedReason)
	}

	// 3) ChatRequirements + POW(新两步 sentinel 流程,solver 未配置时内部自动
	// 回退到单步接口)
	cr, err := cli.ChatRequirementsV2(ctx)
	if err != nil {
		return false, r.classifyUpstream(err), err
	}
	var proofToken string
	if cr.Proofofwork.Required {
		proofCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		ch := make(chan string, 1)
		go func() { ch <- cr.SolveProof(chatgpt.DefaultUserAgent) }()
		select {
		case <-proofCtx.Done():
			cancel()
			r.sched.MarkWarned(context.Background(), lease.Account.ID)
			return false, ErrPOWTimeout, proofCtx.Err()
		case proofToken = <-ch:
			cancel()
		}
		if proofToken == "" {
			r.sched.MarkWarned(context.Background(), lease.Account.ID)
			return false, ErrPOWFailed, errors.New("pow solver returned empty")
		}
	}
	// Turnstile 是"建议性"信号:即使服务端声明 required,只要 chat_token + proof_token
	// 齐全,绝大多数账号的 f/conversation 仍然会正常下发图片结果。与 chat 流程(gateway/chat.go)
	// 保持一致——只打 warn,不阻断;真正拿不到 IMG2 终稿时由后续 poll 逻辑判定为失败。
	if cr.Turnstile.Required {
		logger.L().Warn("image turnstile required, continue anyway",
			zap.Uint64("account_id", lease.Account.ID))
	}

	// 4) 不再调用 /backend-api/conversation/init:
	// 浏览器实测路径是 prepare → chat-requirements → f/conversation 三步,init 是
	// 过时/冗余调用,在免费账号上还会返回 404 让整条链路 fail。system_hints=picture_v2
	// 会通过 f/conversation 的 payload 字段传达。

	// 4.5) 图生图:上传参考图。任何一张失败都直接整体 fail(上游后续会对不上 attachment)。
	var refs []*chatgpt.UploadedFile
	if len(opt.References) > 0 {
		for idx, r0 := range opt.References {
			upCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			up, err := cli.UploadFile(upCtx, r0.Data, r0.FileName)
			cancel()
			if err != nil {
				logger.L().Warn("image runner upload reference failed",
					zap.Int("idx", idx), zap.Error(err))
				if ue, ok := err.(*chatgpt.UpstreamError); ok && ue.IsRateLimited() {
					r.sched.MarkRateLimited(context.Background(), lease.Account.ID)
					return false, ErrRateLimited, err
				}
				return false, ErrUpstream, fmt.Errorf("upload reference %d: %w", idx, err)
			}
			refs = append(refs, up)
		}
		logger.L().Info("image runner references uploaded",
			zap.String("task_id", opt.TaskID), zap.Int("count", len(refs)))
	}

	// 注意:新会话不要本地生成 conversation_id,上游会 404。
	// 真正的 conv_id 由 SSE 的 resume_conversation_token / sseResult.ConversationID 返回。
	var convID string
	parentID := uuid.NewString()
	messageID := uuid.NewString()

	// 统一把 model 强制为 "auto":对齐参考实现(只通过 system_hints=["picture_v2"]
	// 区分图像任务),避免 chatgpt-freeaccount / chatgpt-paid 之间的 model slug 差异。
	upstreamModel := "auto"
	if opt.UpstreamModel != "" && opt.UpstreamModel != "auto" && !cr.IsFreeAccount() {
		// 付费账号如果明确传了 upstream slug 且不是 auto,可以尊重调用传入
		// (但我们现有模型库没有 image 专用 slug,保留扩展点)
		upstreamModel = opt.UpstreamModel
	} else if cr.IsFreeAccount() && opt.UpstreamModel != "" && opt.UpstreamModel != "auto" {
		logger.L().Warn("image: free account requesting premium model, downgrade to auto",
			zap.Uint64("account_id", lease.Account.ID),
			zap.String("requested_model", opt.UpstreamModel))
	}

	// 5-7) 同账号 + 同会话多轮发起 picture_v2,命中 IMG2 即返回;
	// 连续 sameConvMax 轮只拿到预览(preview_only)时,用最后一轮的 sediment 作为 IMG1 返回。
	// 协议/网络级错误(非 preview_only)会直接退出,由外层 Run 换账号。
	const sameConvMax = 1 // 同号只试 1 轮,不命中立即换号(比同号多轮重试更快)

	var (
		fileRefs      []string
		previewRounds int
		// baselineTools 记录上一轮结束时会话里已有的 image_gen tool 消息 id,
		// 下一轮 PollConversationForImages 只看新增,避免把旧 preview 当本轮结果。
		baselineTools = map[string]struct{}{}
		// excludeSids 记录之前轮次产出的 preview sediment ID,
		// IMG2 命中时从结果中排除,避免把旧预览图混入终稿。
		excludeSids = map[string]struct{}{}
	)

loop:
	for turn := 1; turn <= sameConvMax; turn++ {
		result.TurnsInConv = turn

		// 每轮重新拉 chat_token + proof_token(之前那张已经消耗过)。
		// 复用外层 cr / proofToken 的首次结果(turn==1 直接用),后续重取。
		if turn > 1 {
			cr, err = cli.ChatRequirementsV2(ctx)
			if err != nil {
				return false, r.classifyUpstream(err), err
			}
			proofToken = ""
			if cr.Proofofwork.Required {
				proofCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
				ch := make(chan string, 1)
				go func() { ch <- cr.SolveProof(chatgpt.DefaultUserAgent) }()
				select {
				case <-proofCtx.Done():
					cancel()
					r.sched.MarkWarned(context.Background(), lease.Account.ID)
					return false, ErrPOWTimeout, proofCtx.Err()
				case proofToken = <-ch:
					cancel()
				}
				if proofToken == "" {
					r.sched.MarkWarned(context.Background(), lease.Account.ID)
					return false, ErrPOWFailed, errors.New("pow solver returned empty")
				}
			}
		}

		convOpt := chatgpt.ImageConvOpts{
			Prompt:        opt.Prompt,
			UpstreamModel: upstreamModel,
			ConvID:        convID, // 第 1 轮空串=新会话,后续轮复用
			ParentMsgID:   parentID,
			MessageID:     messageID,
			ChatToken:     cr.Token,
			ProofToken:    proofToken,
			References:    refs,
		}
		if turn > 1 {
			// 续聊:每轮用新的 message_id,parent 来自上轮会话头
			convOpt.MessageID = uuid.NewString()
		}

		// Prepare(conduit_token;不成功也能降级跑 conversation)
		if ct, err := cli.PrepareFConversation(ctx, convOpt); err == nil {
			convOpt.ConduitToken = ct
		} else if ue, ok := err.(*chatgpt.UpstreamError); ok && ue.IsRateLimited() {
			r.sched.MarkRateLimited(context.Background(), lease.Account.ID)
			return false, ErrRateLimited, err
		}

		// f/conversation SSE
		stream, err := cli.StreamFConversation(ctx, convOpt)
		if err != nil {
			code := r.classifyUpstream(err)
			if code == ErrRateLimited {
				r.sched.MarkRateLimited(context.Background(), lease.Account.ID)
			}
			return false, code, err
		}
		sseResult := chatgpt.ParseImageSSE(stream)
		if sseResult.ConversationID != "" {
			convID = sseResult.ConversationID
			result.ConversationID = convID
		}

		// 每轮 SSE 解析完的原始产物:FileIDs(file-service://,IMG2 直出时有)、
		// SedimentIDs(sediment://,preview 或 IMG1 常见)、FinishType。用于诊断
		// "这轮到底返回了什么"。
		logger.L().Info("image runner SSE parsed",
			zap.String("task_id", opt.TaskID),
			zap.Uint64("account_id", lease.Account.ID),
			zap.Int("turn", turn),
			zap.String("conv_id", convID),
			zap.String("finish_type", sseResult.FinishType),
			zap.String("image_gen_task_id", sseResult.ImageGenTaskID),
			zap.Int("sse_fids", len(sseResult.FileIDs)),
			zap.Strings("sse_fids_list", sseResult.FileIDs),
			zap.Int("sse_sids", len(sseResult.SedimentIDs)),
			zap.Strings("sse_sids_list", sseResult.SedimentIDs),
			zap.Int("sse_img2_sids", len(sseResult.IMG2SedimentIDs)),
			zap.Strings("sse_img2_sids_list", sseResult.IMG2SedimentIDs),
		)

		// 内容策略拒绝:SSE 结束后上游根本没发起图片生成(无 ImageGenTaskID),
		// 也没有任何 file/sediment 引用 —— 说明上游拒绝了该 prompt。
		// 立即返回错误,不进入 poll 等待。
		if sseResult.ImageGenTaskID == "" && len(sseResult.FileIDs) == 0 && len(sseResult.SedimentIDs) == 0 {
			reason := sseResult.AssistantText
			if reason == "" {
				reason = "上游拒绝生成该图片"
			}
			if len(reason) > 300 {
				reason = reason[:300]
			}
			logger.L().Warn("image runner rejected by upstream (no image_gen_task_id)",
				zap.String("task_id", opt.TaskID),
				zap.Uint64("account_id", lease.Account.ID),
				zap.String("reason", reason),
			)
			return false, ErrContentPolicy, errors.New(reason)
		}

		// SSE 直出 file-service = IMG2 命中。2026 抓包还确认:IMG2 可能
		// 只在 SSE 中给单条 sediment,但同片段带 gen_size_v2；这种也应直返。
		// 注意:同一次灰度也可能同时带 sediment(例如 preview + final 各一张),
		// 都要收进来,不然调用方会少看到图。
		if len(sseResult.FileIDs) > 0 || len(sseResult.IMG2SedimentIDs) > 0 {
			fileRefs = append(fileRefs, sseResult.FileIDs...)
			sidsToUse := sseResult.SedimentIDs
			if len(sseResult.FileIDs) == 0 && len(sseResult.IMG2SedimentIDs) > 0 {
				sidsToUse = sseResult.IMG2SedimentIDs
			}
			for _, s := range sidsToUse {
				fileRefs = append(fileRefs, "sed:"+s)
			}
			logger.L().Info("image runner IMG2 direct hit (from SSE)",
				zap.String("task_id", opt.TaskID),
				zap.Uint64("account_id", lease.Account.ID),
				zap.Int("turn", turn),
				zap.String("conv_id", convID),
				zap.Int("total_refs", len(fileRefs)),
				zap.Strings("refs", fileRefs),
			)
			break loop
		}

		// 没直出就轮询当前会话
		pollOpt := chatgpt.PollOpts{
			MaxWait:         opt.PollMaxWait,
			BaselineToolIDs: baselineTools,
		}
		status, fids, sids := cli.PollConversationForImages(ctx, convID, pollOpt)
		// 每轮 Poll 的产物,无论 status 如何都打印一条,用于诊断"第几轮拿到了什么"。
		logger.L().Info("image runner poll done",
			zap.String("task_id", opt.TaskID),
			zap.Uint64("account_id", lease.Account.ID),
			zap.Int("turn", turn),
			zap.String("conv_id", convID),
			zap.String("poll_status", string(status)),
			zap.Int("poll_fids", len(fids)),
			zap.Strings("poll_fids_list", fids),
			zap.Int("poll_sids", len(sids)),
			zap.Strings("poll_sids_list", sids),
		)
		switch status {
		case chatgpt.PollStatusIMG2:
			fileRefs = append(fileRefs, fids...)
			for _, s := range sids {
				if _, old := excludeSids[s]; old {
					continue // 跳过之前轮次的预览 sediment
				}
				fileRefs = append(fileRefs, "sed:"+s)
			}
			logger.L().Info("image runner IMG2 poll hit",
				zap.String("task_id", opt.TaskID),
				zap.Uint64("account_id", lease.Account.ID),
				zap.Int("turn", turn),
				zap.String("conv_id", convID),
				zap.Int("total_refs", len(fileRefs)),
				zap.Strings("refs", fileRefs),
			)
			break loop

		case chatgpt.PollStatusPreviewOnly:
			previewRounds++
			// 把预览的 sediment ID 加入排除集,后续轮次 IMG2 命中时不会混入旧预览
			for _, s := range sids {
				excludeSids[s] = struct{}{}
			}
			for _, f := range fids {
				excludeSids[f] = struct{}{}
			}
			logger.L().Info("image runner preview_only, retry in same conversation",
				zap.String("task_id", opt.TaskID),
				zap.Uint64("account_id", lease.Account.ID),
				zap.String("conv_id", convID),
				zap.Int("turn", turn),
				zap.Int("max_turns", sameConvMax),
				zap.Int("preview_fids", len(fids)),
				zap.Strings("preview_fids_list", fids),
				zap.Int("preview_sids", len(sids)),
				zap.Strings("preview_sids_list", sids),
			)

			// 不是最后一轮:更新 baseline + 取会话头作为下轮的 parent_message_id
			if turn < sameConvMax {
				if mapping, merr := cli.GetConversationMapping(ctx, convID); merr == nil {
					// 把当前所有 tool 消息都塞进 baseline,下轮 poll 只看新增
					if newBL := buildToolBaseline(mapping); newBL != nil {
						baselineTools = newBL
					}
					if head, _ := mapping["current_node"].(string); head != "" {
						parentID = head
					}
				} else {
					logger.L().Warn("image runner fetch mapping for retry failed",
						zap.Uint64("account_id", lease.Account.ID), zap.Error(merr))
				}
			}

		case chatgpt.PollStatusTimeout:
			r.sched.RecordIMG2Outcome(context.Background(), lease.Account.ID, "miss")
			return false, ErrPollTimeout, errors.New("poll timeout")

		case chatgpt.PollStatus429:
			// 上游 RPM 限流,图可能还在生成中,增大 poll 间隔后继续 poll 同一个会话
			logger.L().Warn("image runner poll hit 429, increasing interval and retrying poll",
				zap.String("task_id", opt.TaskID),
				zap.Uint64("account_id", lease.Account.ID),
				zap.Int("turn", turn))
			// 用更长的间隔重新 poll 同一个 convID
			pollOpt2 := chatgpt.PollOpts{
				MaxWait:         2 * time.Minute,
				Interval:        15 * time.Second, // 拉长间隔避免再 429
				BaselineToolIDs: baselineTools,
			}
			status2, fids2, sids2 := cli.PollConversationForImages(ctx, convID, pollOpt2)
			if status2 == chatgpt.PollStatusIMG2 {
				fileRefs = append(fileRefs, fids2...)
				for _, s := range sids2 {
					if _, old := excludeSids[s]; old {
						continue
					}
					fileRefs = append(fileRefs, "sed:"+s)
				}
				break loop
			}
			// 二次 poll 还是失败,当作本轮未出图
			r.sched.RecordIMG2Outcome(context.Background(), lease.Account.ID, "miss")
			return false, ErrUpstream, errors.New("poll 429 retry exhausted")

		default:
			r.sched.RecordIMG2Outcome(context.Background(), lease.Account.ID, "miss")
			return false, ErrUpstream, errors.New("poll error")
		}
	}

	// 若循环结束仍未拿到 IMG2,不兜底,直接失败并降低账号置信度
	if len(fileRefs) == 0 {
		r.sched.RecordIMG2Outcome(context.Background(), lease.Account.ID, "miss")
		logger.L().Warn("image runner all turns preview_only, no IMG2 hit",
			zap.String("task_id", opt.TaskID),
			zap.Uint64("account_id", lease.Account.ID),
			zap.String("conv_id", convID),
			zap.Int("preview_rounds", previewRounds))
		return false, ErrPreviewOnly, errors.New("未命中 IMG2 灰度,请重试")
	}

	fileRefs = dedupeImageRefs(fileRefs)

	// 到这里说明 fileRefs 不为空 = IMG2 真正命中
	r.sched.RecordIMG2Outcome(context.Background(), lease.Account.ID, "hit")

	// 8) 对每个 ref 取签名 URL
	var signedURLs []string
	var contentTypes []string
	for _, ref := range fileRefs {
		url, err := cli.ImageDownloadURL(ctx, convID, ref)
		if err != nil {
			logger.L().Warn("image runner download url failed",
				zap.String("ref", ref), zap.Error(err))
			continue
		}
		signedURLs = append(signedURLs, url)
		contentTypes = append(contentTypes, "image/png")
	}
	if len(signedURLs) == 0 {
		r.sched.RecordIMG2Delivery(context.Background(), lease.Account.ID, "fail")
		logger.L().Warn("image runner delivery failed after refs",
			zap.String("task_id", opt.TaskID),
			zap.Uint64("account_id", lease.Account.ID),
			zap.String("conv_id", convID),
			zap.Int("refs", len(fileRefs)))
		return false, ErrDownload, errors.New("all download urls failed")
	}
	deliveryStatus := "success"
	if len(signedURLs) < len(fileRefs) {
		deliveryStatus = "partial"
	}
	r.sched.RecordIMG2Delivery(context.Background(), lease.Account.ID, deliveryStatus)

	logger.L().Info("image runner result summary",
		zap.String("task_id", opt.TaskID),
		zap.Uint64("account_id", lease.Account.ID),
		zap.String("conv_id", convID),
		zap.Int("turns_used", result.TurnsInConv),
		zap.Int("refs", len(fileRefs)),
		zap.Strings("refs_list", fileRefs),
		zap.Int("signed_count", len(signedURLs)),
	)

	result.FileIDs = fileRefs
	result.SignedURLs = signedURLs
	result.ContentTypes = contentTypes
	return true, "", nil
}

// buildToolBaseline 从 conversation mapping 里提取所有已存在的 image_gen tool 消息 id,
// 作为下一轮 PollConversationForImages 的 baseline。
func buildToolBaseline(mapping map[string]interface{}) map[string]struct{} {
	tools := chatgpt.ExtractImageToolMsgs(mapping)
	if len(tools) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		out[t.MessageID] = struct{}{}
	}
	return out
}

func dedupeImageRefs(refs []string) []string {
	if len(refs) <= 1 {
		return refs
	}
	out := make([]string, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}
	return out
}

// classifyUpstream 把上游错误转成内部 error code。
func (r *Runner) classifyUpstream(err error) string {
	if err == nil {
		return ""
	}
	var ue *chatgpt.UpstreamError
	if errors.As(err, &ue) {
		if ue.IsRateLimited() {
			return ErrRateLimited
		}
		if ue.IsUnauthorized() {
			return ErrAuthRequired
		}
		return ErrUpstream
	}
	if strings.Contains(err.Error(), "deadline exceeded") {
		return ErrPollTimeout
	}
	return ErrUpstream
}

// isProxyError 判断错误是否是代理级错误(连接失败/超时/407/SOCKS 握手等)。
func isProxyError(err error) bool {
	if err == nil {
		return false
	}
	var ue *chatgpt.UpstreamError
	if errors.As(err, &ue) && ue.Status == 407 {
		return true
	}
	msg := err.Error()
	proxyKeywords := []string{
		"proxy", "SOCKS", "connection refused", "connection reset",
		"no such host", "i/o timeout", "dial tcp", "EOF",
		"connect: network is unreachable",
	}
	for _, kw := range proxyKeywords {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// GenerateTaskID 生成对外 task_id。
func GenerateTaskID() string {
	return "img_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
}
