// Package gateway 实现本地 OpenAI 兼容的 /v1/* 入口。
//
// 职责:
//  1. 查模型 slug 映射
//  2. 通过调度器拿账号 Lease
//  3. 转译请求体并调用 chatgpt.com 上游
//  4. 转译响应(流式或聚合)为 OpenAI 协议
//  5. 写入本地 usage/image 运行日志
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	modelpkg "github.com/432539/gpt2api/internal/model"
	"github.com/432539/gpt2api/internal/scheduler"
	"github.com/432539/gpt2api/internal/upstream/chatgpt"
	"github.com/432539/gpt2api/internal/usage"
	"github.com/432539/gpt2api/pkg/logger"
)

// Handler 聚合网关需要的所有依赖。
type Handler struct {
	Models    *modelpkg.Registry
	Scheduler *scheduler.Scheduler
	Usage     *usage.Logger
	AccSvc    interface {
		DecryptCookies(ctx context.Context, accountID uint64) (string, error)
	}
	// Images 可选:若挂载,chat/completions 里指定图像模型会自动转派。
	Images *ImagesHandler

	// Settings 可选:若注入则在构造上游 client 时应用动态超时。
	Settings interface {
		GatewayUpstreamTimeoutSec() int
		GatewaySSEReadTimeoutSec() int
	}
}

// upstreamTimeout 返回当前应使用的上游非流式超时。未注入时回退 60s。
func (h *Handler) upstreamTimeout() time.Duration {
	if h.Settings != nil {
		if n := h.Settings.GatewayUpstreamTimeoutSec(); n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 60 * time.Second
}

// mapUpstreamModelSlug 把本地 slug 映射到 chatgpt.com 后端实际认的灰度 slug。
func mapUpstreamModelSlug(s string) string {
	return s
}

// roughEstimateTokens 估算 messages prompt tokens(无 tiktoken,简单 len/4)。
func roughEstimateTokens(msgs []chatgpt.ChatMessage) int {
	n := 0
	for _, m := range msgs {
		n += (len(m.Content) + 3) / 4
		n += 4
	}
	return n + 2
}

// ChatCompletions 是 POST /v1/chat/completions 入口。
func (h *Handler) ChatCompletions(c *gin.Context) {
	startAt := time.Now()

	var req ChatCompletionsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", "请求参数错误:"+err.Error())
		return
	}

	refID := uuid.NewString()
	rec := &usage.Log{
		RequestID: refID,
		Type:      usage.TypeChat,
		IP:        c.ClientIP(),
		UA:        c.Request.UserAgent(),
	}
	defer func() {
		rec.DurationMs = int(time.Since(startAt).Milliseconds())
		if rec.Status == "" {
			rec.Status = usage.StatusFailed
		}
		if h.Usage != nil {
			h.Usage.Write(rec)
		}
	}()
	fail := func(code string) { rec.Status = usage.StatusFailed; rec.ErrorCode = code }

	m, err := h.Models.BySlug(c.Request.Context(), req.Model)
	if err != nil || m == nil || !m.Enabled {
		fail("model_not_found")
		openAIError(c, http.StatusBadRequest, "model_not_found",
			fmt.Sprintf("模型 %q 不存在或未启用", req.Model))
		return
	}
	if m.Type == modelpkg.TypeImage {
		if h.Images == nil {
			fail("image_not_wired")
			openAIError(c, http.StatusNotImplemented, "image_not_wired", "图片生成能力未开启")
			return
		}
		h.Images.handleChatAsImage(c, rec, m, &req, startAt)
		return
	}
	rec.ModelID = m.ID
	promptTokens := roughEstimateTokens(req.Messages)

	lease, err := h.Scheduler.Dispatch(c.Request.Context(), modelpkg.TypeChat)
	if err != nil {
		fail("no_account_available")
		openAIError(c, http.StatusServiceUnavailable, "no_account_available", "账号池暂无可用账号,请稍后重试")
		return
	}
	rec.AccountID = lease.Account.ID
	defer func() { _ = lease.Release(context.Background()) }()

	cookies, _ := h.AccSvc.DecryptCookies(c.Request.Context(), lease.Account.ID)
	cli, err := chatgpt.New(chatgpt.Options{
		AuthToken: lease.AuthToken,
		DeviceID:  lease.DeviceID,
		SessionID: lease.SessionID,
		ProxyURL:  lease.ProxyURL,
		Cookies:   cookies,
		Timeout:   h.upstreamTimeout(),
	})
	if err != nil {
		fail("upstream_init_error")
		openAIError(c, http.StatusInternalServerError, "upstream_init_error", "上游客户端初始化失败:"+err.Error())
		return
	}

	upstreamModel := m.UpstreamModelSlug
	if upstreamModel == "" {
		upstreamModel = "auto"
	}
	upstreamModel = mapUpstreamModelSlug(upstreamModel)

	bootCtx, cancelBoot := context.WithTimeout(c.Request.Context(), 15*time.Second)
	_ = cli.Bootstrap(bootCtx)
	cancelBoot()

	reqCtx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	cr, err := cli.ChatRequirementsV2(reqCtx)
	if err != nil {
		h.handleUpstreamErr(c, lease, err, func() { fail("upstream_error") })
		return
	}

	var proofToken string
	if cr.Proofofwork.Required {
		proofCtx, cancelProof := context.WithTimeout(c.Request.Context(), 5*time.Second)
		proofCh := make(chan string, 1)
		go func() { proofCh <- cr.SolveProof("") }()
		select {
		case <-proofCtx.Done():
			cancelProof()
			h.Scheduler.MarkWarned(c.Request.Context(), lease.Account.ID)
			fail("pow_timeout")
			openAIError(c, http.StatusServiceUnavailable, "pow_timeout", "上游风控(PoW)未在规定时间内完成,请重试")
			return
		case proofToken = <-proofCh:
			cancelProof()
		}
		if proofToken == "" {
			h.Scheduler.MarkWarned(c.Request.Context(), lease.Account.ID)
			fail("pow_failed")
			openAIError(c, http.StatusServiceUnavailable, "pow_failed", "上游风控(PoW)校验失败,请稍后重试")
			return
		}
	}
	if cr.Turnstile.Required {
		logger.L().Warn("chat turnstile required, continue anyway", zap.Uint64("account_id", lease.Account.ID))
	}

	if cr.IsFreeAccount() && upstreamModel != "auto" {
		logger.L().Warn("free account requesting premium model, downgrade to auto",
			zap.Uint64("account_id", lease.Account.ID), zap.String("requested_model", upstreamModel))
		upstreamModel = "auto"
	}

	chatOpt := chatgpt.FChatOpts{
		UpstreamModel: upstreamModel,
		Messages:      req.Messages,
		ChatToken:     cr.Token,
		ProofToken:    proofToken,
	}

	prepCtx, cancelPrep := context.WithTimeout(c.Request.Context(), 30*time.Second)
	conduit, err := cli.PrepareFChat(prepCtx, chatOpt)
	cancelPrep()
	if err != nil {
		logger.L().Warn("f/conversation/prepare failed, continue without conduit",
			zap.Uint64("account_id", lease.Account.ID),
			zap.String("upstream_model", upstreamModel),
			zap.Error(err))
		conduit = ""
	}
	chatOpt.ConduitToken = conduit

	logger.L().Info("chat f/conversation send",
		zap.Uint64("account_id", lease.Account.ID),
		zap.String("upstream_model", upstreamModel),
		zap.Int("chat_token_len", len(cr.Token)),
		zap.Int("proof_token_len", len(proofToken)),
		zap.Int("conduit_len", len(conduit)),
		zap.Bool("turnstile_required", cr.Turnstile.Required),
		zap.String("persona", cr.Persona),
	)

	stream, err := cli.StreamFChat(c.Request.Context(), chatOpt)
	if err != nil {
		h.handleUpstreamErr(c, lease, err, func() { fail("upstream_error") })
		return
	}

	id := "chatcmpl-" + uuid.NewString()
	if req.Stream {
		h.streamOpenAI(c, id, req.Model, stream, cr.IsFreeAccount())
	} else {
		h.collectOpenAI(c, id, req.Model, stream, cr.IsFreeAccount())
	}

	completionTokens := h.lastCompletionTokens(c)
	rec.Status = usage.StatusSuccess
	rec.InputTokens = promptTokens
	rec.OutputTokens = completionTokens
}

// streamOpenAI 将上游 SSE 事件转为 OpenAI 风格流式响应。
func (h *Handler) streamOpenAI(c *gin.Context, id, model string, stream <-chan chatgpt.SSEEvent, freeAccount bool) {
	w := c.Writer
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	writeChunk(w, flusher, id, model, DeltaMsg{Role: "assistant"}, nil)

	var extr deltaExtractor
	var total strings.Builder
	evCount := 0
	silentlyRejected := false
	for ev := range stream {
		if ev.Err != nil {
			logger.L().Warn("upstream stream err", zap.Error(ev.Err))
			break
		}
		if len(ev.Data) == 0 {
			continue
		}
		evCount++
		if evCount <= 16 {
			logger.L().Info("chat sse raw", zap.Int("n", evCount),
				zap.String("event", ev.Event), zap.String("data", truncate(string(ev.Data), 2048)))
		}
		if !silentlyRejected && isSilentRejection(ev.Data) {
			silentlyRejected = true
		}
		delta, final, err := extr.Extract(ev.Data)
		if err != nil {
			continue
		}
		if delta != "" {
			total.WriteString(delta)
			writeChunk(w, flusher, id, model, DeltaMsg{Content: delta}, nil)
		}
		if final {
			break
		}
	}
	logger.L().Info("chat sse done", zap.Int("events", evCount), zap.Int("content_len", total.Len()), zap.Bool("silently_rejected", silentlyRejected))

	if total.Len() == 0 && evCount > 0 {
		msg := emptyReplyMessage(freeAccount, silentlyRejected)
		total.WriteString(msg)
		writeChunk(w, flusher, id, model, DeltaMsg{Content: msg}, nil)
	}

	stop := "stop"
	writeChunk(w, flusher, id, model, DeltaMsg{}, &stop)
	fmt.Fprintf(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
	c.Set("completion_tokens", (total.Len()+3)/4)
}

func (h *Handler) collectOpenAI(c *gin.Context, id, model string, stream <-chan chatgpt.SSEEvent, freeAccount bool) {
	var extr deltaExtractor
	var content strings.Builder
	evCount := 0
	silentlyRejected := false
	for ev := range stream {
		if ev.Err != nil {
			logger.L().Warn("upstream collect err", zap.Error(ev.Err))
			break
		}
		if len(ev.Data) == 0 {
			continue
		}
		evCount++
		if evCount <= 16 {
			logger.L().Info("chat collect raw", zap.Int("n", evCount),
				zap.String("event", ev.Event), zap.String("data", truncate(string(ev.Data), 2048)))
		}
		if !silentlyRejected && isSilentRejection(ev.Data) {
			silentlyRejected = true
		}
		delta, final, _ := extr.Extract(ev.Data)
		if delta != "" {
			content.WriteString(delta)
		}
		if final {
			break
		}
	}
	logger.L().Info("chat collect done", zap.Int("events", evCount), zap.Int("content_len", content.Len()), zap.Bool("silently_rejected", silentlyRejected))

	if content.Len() == 0 && evCount > 0 {
		content.WriteString(emptyReplyMessage(freeAccount, silentlyRejected))
	}

	completionTokens := (content.Len() + 3) / 4
	c.Set("completion_tokens", completionTokens)

	resp := ChatCompletionResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []ChatCompletionChoice{{
			Index:        0,
			Message:      chatgpt.ChatMessage{Role: "assistant", Content: content.String()},
			FinishReason: "stop",
		}},
		Usage: ChatCompletionUsage{
			PromptTokens:     0,
			CompletionTokens: completionTokens,
			TotalTokens:      completionTokens,
		},
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) lastCompletionTokens(c *gin.Context) int {
	if v, ok := c.Get("completion_tokens"); ok {
		if i, ok := v.(int); ok {
			return i
		}
	}
	return 0
}

// handleUpstreamErr 根据上游错误降级账号并回传 OpenAI 错误。
func (h *Handler) handleUpstreamErr(c *gin.Context, lease *scheduler.Lease, err error, onFail func()) {
	var ue *chatgpt.UpstreamError
	if errors.As(err, &ue) {
		switch {
		case ue.IsRateLimited():
			h.Scheduler.MarkRateLimited(c.Request.Context(), lease.Account.ID)
		case ue.IsUnauthorized():
			h.Scheduler.MarkWarned(c.Request.Context(), lease.Account.ID)
		}
		onFail()
		logger.L().Error("chat upstream error",
			zap.Int("status", ue.Status), zap.Uint64("account_id", lease.Account.ID), zap.String("body", truncate(ue.Body, 1500)))
		openAIError(c, http.StatusBadGateway, "upstream_error", fmt.Sprintf("上游返回错误(HTTP %d):%s", ue.Status, truncate(ue.Body, 200)))
		return
	}
	onFail()
	openAIError(c, http.StatusBadGateway, "upstream_error", "上游请求失败:"+err.Error())
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func isSilentRejection(data []byte) bool {
	s := string(data)
	return strings.Contains(s, `"is_visually_hidden_from_conversation": true`) &&
		strings.Contains(s, `"role": "system"`) &&
		strings.Contains(s, `"end_turn": true`)
}

// emptyReplyMessage 根据账号类型和上游信号,返回给调用方看的兜底文案。
func emptyReplyMessage(freeAccount, silentlyRejected bool) string {
	switch {
	case silentlyRejected && freeAccount:
		return "上游检测到当前账号为免费版(chatgpt-freeaccount),已静默拒绝本次请求。请更换 ChatGPT Plus / Team 账号后再试。"
	case silentlyRejected:
		return "上游已接受请求但静默终止对话(常见于账号被限流或触发内容审核),请稍后重试,若仍失败请更换模型或账号。"
	case freeAccount:
		return "当前账号为 ChatGPT 免费版,上游未产出内容。请更换 Plus/Team 账号后再试。"
	default:
		return "上游未产出回答内容,可能触发了内容审核或账号被临时限流,请稍后重试。"
	}
}

func writeChunk(w io.Writer, f http.Flusher, id, model string, delta DeltaMsg, finish *string) {
	chunk := ChatCompletionChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []ChatCompletionChunkChoice{{Index: 0, Delta: delta, FinishReason: finish}},
	}
	b, _ := json.Marshal(chunk)
	fmt.Fprintf(w, "data: %s\n\n", b)
	if f != nil {
		f.Flush()
	}
}

// openAIError 按 OpenAI 规范返回错误。
func openAIError(c *gin.Context, httpStatus int, code, msg string) {
	c.AbortWithStatusJSON(httpStatus, gin.H{
		"error": gin.H{
			"message": msg,
			"type":    "invalid_request_error",
			"code":    code,
		},
	})
}

// ListModels GET /v1/models。
func (h *Handler) ListModels(c *gin.Context) {
	list, err := h.Models.ListEnabled(c.Request.Context())
	if err != nil {
		openAIError(c, http.StatusInternalServerError, "list_models_error", "获取模型列表失败:"+err.Error())
		return
	}
	data := make([]gin.H, 0, len(list))
	for _, m := range list {
		data = append(data, gin.H{
			"id":       m.Slug,
			"object":   "model",
			"created":  m.CreatedAt.Unix(),
			"owned_by": "chatgpt",
		})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}
