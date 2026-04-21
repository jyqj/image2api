// Package chatgpt - 图像生成协议
//
// 完整链路(和文字聊天共用 f/conversation,只通过 system_hints=["picture_v2"] 区分):
//
//  0. (可选) GET /                              → 拿 oai-did cookie
//  1. POST /backend-api/sentinel/chat-requirements → chat_token + 可选 POW 挑战
//  2. POST /backend-api/f/conversation/prepare      → conduit_token(灰度分桶关键)
//  3. POST /backend-api/f/conversation (SSE)         → 边解析边收 file-service://
//  4. 灰度命中判据:SSE 没直出 file-service 时轮询
//     GET /backend-api/conversation/{id}
//     - file-service 或 metadata.generation.gen_size_v2 → IMG2 终稿
//     - IMG2 tool 消息 ≥ 2 条 → 灰度命中,聚合所有 refs
//     - 只 1 条且无 gen_size_v2 → preview_only(非灰度,同会话重试)
//  5. GET /backend-api/files/download/{fid}?conversation_id=... → 签名 URL / estuary
//     fallback: /files/{fid}/download 或 /conversation/{cid}/attachment/{sid}/download
//  6. GET 签名 URL → 图片字节
//
// 注意:不要调用 /backend-api/conversation/init——这是老客户端路径,在免费账号上会
// 直接 404 让整条链路失败,上游把 picture_v2 路由完全交给 f/conversation 的 payload。
package chatgpt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ImageConvOpts 是图像会话的入参。
type ImageConvOpts struct {
	Prompt         string          // 用户提示词(已处理完的,含可选 CLARITY_SUFFIX)
	UpstreamModel  string          // 默认 "gpt-5-3"
	ConvID         string          // 复用会话时填,空则新建
	ParentMsgID    string          // 复用会话时从 GetConversationHead 取;新会话随机
	MessageID      string          // 可选,留空自动生成
	ChatToken      string          // 必传,来自 ChatRequirements
	ProofToken     string          // 可选
	ConduitToken   string          // 可选,来自 PrepareFConversation
	TimezoneOffset int             // 默认 -480(Asia/Shanghai)
	SSETimeout     time.Duration   // 默认 120s
	References     []*UploadedFile // 图生图/编辑:已上传的参考图,会插到 prompt 前面
}

// InitConversation 对应 /backend-api/conversation/init。
// 新会话必须调用,否则后续 /f/conversation 会 404。
// systemHints 为空串数组表示文字聊天;图像场景传 []string{"picture_v2"}。
func (c *Client) InitConversation(ctx context.Context, systemHints ...string) error {
	if systemHints == nil {
		systemHints = []string{}
	}
	payload := map[string]interface{}{
		"gizmo_id":                nil,
		"requested_default_model": nil,
		"conversation_id":         nil,
		"timezone_offset_min":     -480,
		"system_hints":            systemHints,
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.opts.BaseURL+"/backend-api/conversation/init",
		strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	c.commonHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")

	res, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		buf, _ := io.ReadAll(res.Body)
		return &UpstreamError{Status: res.StatusCode, Message: "conversation/init failed", Body: string(buf)}
	}
	_, _ = io.Copy(io.Discard, res.Body)
	return nil
}

// PrepareFConversation 对应 /backend-api/f/conversation/prepare,返回 conduit_token。
//
// payload 对齐 HAR 抓包 /f/conversation/prepare(image 通路):
//   - client_prepare_state: "success"
//   - fork_from_shared_post: false
//   - partial_query: 完整的 user message(id+author+content,包含当前 prompt)
//   - system_hints: ["picture_v2"]   ← image 通路特有
//   - client_contextual_info: { "app_name": "chatgpt.com" }   ← prepare 阶段只带 app_name
func (c *Client) PrepareFConversation(ctx context.Context, opt ImageConvOpts) (string, error) {
	if opt.UpstreamModel == "" {
		opt.UpstreamModel = "auto"
	}
	if opt.MessageID == "" {
		opt.MessageID = uuid.NewString()
	}
	msgContent, msgMeta := buildImageUserMessage(opt.Prompt, opt.References)
	partialQuery := map[string]interface{}{
		"id":      opt.MessageID,
		"author":  map[string]string{"role": "user"},
		"content": msgContent,
	}
	if len(msgMeta) > 0 {
		partialQuery["metadata"] = msgMeta
	}
	payload := map[string]interface{}{
		"action":                "next",
		"fork_from_shared_post": false,
		"parent_message_id":     opt.ParentMsgID,
		"model":                 opt.UpstreamModel,
		"client_prepare_state":  "success",
		"timezone_offset_min":   -480,
		"timezone":              "Asia/Shanghai",
		"conversation_mode":     map[string]string{"kind": "primary_assistant"},
		"system_hints":          []string{"picture_v2"},
		"partial_query":         partialQuery,
		"supports_buffering":    true,
		"supported_encodings":   []string{"v1"},
		"client_contextual_info": map[string]interface{}{
			"app_name": "chatgpt.com",
		},
	}
	// 只有已有会话才带 conversation_id;新会话不带这个 key(对齐浏览器抓包,
	// 带陌生 UUID 上游会 404)
	if opt.ConvID != "" {
		payload["conversation_id"] = opt.ConvID
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.opts.BaseURL+"/backend-api/f/conversation/prepare",
		strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	c.commonHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Openai-Sentinel-Chat-Requirements-Token", opt.ChatToken)
	if opt.ProofToken != "" {
		req.Header.Set("Openai-Sentinel-Proof-Token", opt.ProofToken)
	}

	res, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	buf, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		return "", &UpstreamError{Status: res.StatusCode, Message: "f/conversation/prepare failed", Body: string(buf)}
	}
	var out struct {
		ConduitToken string `json:"conduit_token"`
	}
	_ = json.Unmarshal(buf, &out)
	return out.ConduitToken, nil
}

func buildImageUserMessage(prompt string, refs []*UploadedFile) (map[string]interface{}, map[string]interface{}) {
	msgContent := map[string]interface{}{"content_type": "text", "parts": []string{prompt}}
	msgMeta := map[string]interface{}{
		"developer_mode_connector_ids": []interface{}{},
		"selected_github_repos":        []interface{}{},
		"selected_all_github_repos":    false,
		"system_hints":                 []string{"picture_v2"},
		"serialization_metadata": map[string]interface{}{
			"custom_symbol_offsets": []interface{}{},
		},
	}
	if len(refs) == 0 {
		return msgContent, msgMeta
	}
	parts := make([]interface{}, 0, len(refs)+1)
	atts := make([]Attachment, 0, len(refs))
	for _, u := range refs {
		if u == nil || u.FileID == "" {
			continue
		}
		parts = append(parts, u.ToAssetPointerPart())
		atts = append(atts, u.ToAttachment())
	}
	parts = append(parts, prompt)
	msgContent = map[string]interface{}{
		"content_type": "multimodal_text",
		"parts":        parts,
	}
	msgMeta["attachments"] = atts
	return msgContent, msgMeta
}

// StreamFConversation 对应 /backend-api/f/conversation(图像走和文字同一端点)。
//
// payload 字段集参考社区维护的 OpenaiChat provider(它在免费/付费账号上实测可用):
// 不带 client_prepare_state / force_parallel_switch;message.metadata 只带
// serialization_metadata + system_hints(有图时再补 attachments)。
func (c *Client) StreamFConversation(ctx context.Context, opt ImageConvOpts) (<-chan SSEEvent, error) {
	if opt.UpstreamModel == "" {
		opt.UpstreamModel = "auto"
	}
	if opt.MessageID == "" {
		opt.MessageID = uuid.NewString()
	}
	if opt.ParentMsgID == "" {
		opt.ParentMsgID = uuid.NewString()
	}
	if opt.TimezoneOffset == 0 {
		opt.TimezoneOffset = -480
	}
	if opt.SSETimeout == 0 {
		opt.SSETimeout = 180 * time.Second
	}

	// 构造 messages[0] 的 content 与 metadata,按是否有 reference 图决定 multimodal_text。
	msgContent, msgMeta := buildImageUserMessage(opt.Prompt, opt.References)

	// 顶层 payload 对齐 HAR /f/conversation(image 通路):
	//   client_prepare_state: "sent"
	//   system_hints: ["picture_v2"]
	//   force_parallel_switch: "auto"            ← 必带
	//   client_contextual_info: 7 个字段 + app_name
	payload := map[string]interface{}{
		"action": "next",
		"messages": []map[string]interface{}{{
			"id":          opt.MessageID,
			"author":      map[string]string{"role": "user"},
			"create_time": float64(time.Now().UnixMilli()) / 1000.0,
			"content":     msgContent,
			"metadata":    msgMeta,
		}},
		"parent_message_id":        opt.ParentMsgID,
		"model":                    opt.UpstreamModel,
		"client_prepare_state":     "sent",
		"timezone_offset_min":      opt.TimezoneOffset,
		"timezone":                 "Asia/Shanghai",
		"conversation_mode":        map[string]string{"kind": "primary_assistant"},
		"enable_message_followups": true,
		"system_hints":             []string{"picture_v2"},
		"supports_buffering":       true,
		"supported_encodings":      []string{"v1"},
		"client_contextual_info": map[string]interface{}{
			"is_dark_mode":      false,
			"time_since_loaded": 1200,
			"page_height":       1072,
			"page_width":        1724,
			"pixel_ratio":       1.2,
			"screen_height":     1440,
			"screen_width":      2560,
			"app_name":          "chatgpt.com",
		},
		"paragen_cot_summary_display_override": "allow",
		"force_parallel_switch":                "auto",
	}
	// 新会话不带 conversation_id(对齐浏览器抓包);已有会话才带
	if opt.ConvID != "" {
		payload["conversation_id"] = opt.ConvID
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.opts.BaseURL+"/backend-api/f/conversation",
		strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	c.commonHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	// X-Oai-Turn-Trace-Id:每 turn 一个新 UUID。见 fchat.go 说明。
	req.Header.Set("X-Oai-Turn-Trace-Id", uuid.NewString())
	req.Header.Set("Openai-Sentinel-Chat-Requirements-Token", opt.ChatToken)
	if opt.ProofToken != "" {
		req.Header.Set("Openai-Sentinel-Proof-Token", opt.ProofToken)
	}
	if opt.ConduitToken != "" {
		req.Header.Set("X-Conduit-Token", opt.ConduitToken)
	}

	local := *c.hc
	local.Timeout = 0 // 由 ctx 控制
	c.injectHelloID(req)

	res, err := local.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		buf, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return nil, &UpstreamError{Status: res.StatusCode, Message: "f/conversation failed", Body: string(buf)}
	}
	out := make(chan SSEEvent, 64)
	go parseSSE(res.Body, out, opt.SSETimeout)
	return out, nil
}

// ImageSSEResult 是 ParseImageSSE 的扫描结果。
type ImageSSEResult struct {
	ConversationID  string   // SSE 里捕获到的新会话 id(可能和入参不同)
	FileIDs         []string // file-service:// 引用(直出灰度图就在这里)
	SedimentIDs     []string // sediment:// 引用(可能是预览,也可能是 IMG2 终稿)
	IMG2SedimentIDs []string // SSE 中带 gen_size_v2 等 IMG2 元数据的 sediment 引用
	FinishType      string   // finish_details.type(interrupted/stop/...)
	ImageGenTaskID  string
	PolicyBlocked   bool   // 上游因内容策略拒绝生成
	AssistantText   string // 助手回复的文本(用于提取拒绝原因)
}

var (
	reFileRef = regexp.MustCompile(`file-service://([A-Za-z0-9_-]+)`)
	reSedRef  = regexp.MustCompile(`sediment://([A-Za-z0-9_-]+)`)
)

// ParseImageSSE 消费 SSE 事件流,把图像相关的字段提取出来。
// 调用方可以根据返回的 FileIDs 判断是否已灰度直出。
func ParseImageSSE(stream <-chan SSEEvent) ImageSSEResult {
	var r ImageSSEResult
	seenFile := map[string]struct{}{}
	seenSed := map[string]struct{}{}
	seenIMG2Sed := map[string]struct{}{}

	for ev := range stream {
		if ev.Err != nil {
			return r
		}
		data := ev.Data
		if len(data) == 0 {
			continue
		}
		if string(data) == "[DONE]" {
			return r
		}
		// 文本正则先扫一遍(比 JSON 解析更健壮)。
		// 注意:2026 抓包里 IMG2 也可能是单条 sediment:// 终稿,
		// 关键区别在同一 SSE/mapping 片段里带 metadata.generation.gen_size_v2。
		eventSedIDs := make([]string, 0, 2)
		for _, m := range reFileRef.FindAllSubmatch(data, -1) {
			fid := string(m[1])
			if _, ok := seenFile[fid]; !ok {
				seenFile[fid] = struct{}{}
				r.FileIDs = append(r.FileIDs, fid)
			}
		}
		for _, m := range reSedRef.FindAllSubmatch(data, -1) {
			sid := string(m[1])
			if _, ok := seenSed[sid]; !ok {
				seenSed[sid] = struct{}{}
				r.SedimentIDs = append(r.SedimentIDs, sid)
			}
			eventSedIDs = append(eventSedIDs, sid)
		}

		if len(eventSedIDs) > 0 && strings.Contains(string(data), "gen_size_v2") {
			for _, sid := range eventSedIDs {
				if _, ok := seenIMG2Sed[sid]; !ok {
					seenIMG2Sed[sid] = struct{}{}
					r.IMG2SedimentIDs = append(r.IMG2SedimentIDs, sid)
				}
			}
		}

		var obj map[string]interface{}
		if err := json.Unmarshal(data, &obj); err != nil {
			continue
		}
		if v, ok := obj["v"].(map[string]interface{}); ok {
			if cid, ok := v["conversation_id"].(string); ok && cid != "" && r.ConversationID == "" {
				r.ConversationID = cid
			}
			if msg, ok := v["message"].(map[string]interface{}); ok {
				if meta, ok := msg["metadata"].(map[string]interface{}); ok {
					if tid, ok := meta["image_gen_task_id"].(string); ok {
						r.ImageGenTaskID = tid
					}
					if fd, ok := meta["finish_details"].(map[string]interface{}); ok {
						if ft, ok := fd["type"].(string); ok {
							r.FinishType = ft
						}
					}
				}
				// 捕获助手文本消息(每个 SSE 事件 parts 含到目前的完整文本,取最后一次即可)
				if author, ok := msg["author"].(map[string]interface{}); ok {
					if role, _ := author["role"].(string); role == "assistant" {
						if content, ok := msg["content"].(map[string]interface{}); ok {
							if parts, ok := content["parts"].([]interface{}); ok {
								var text string
								for _, p := range parts {
									if s, ok := p.(string); ok {
										text += s
									}
								}
								if len(text) > 0 {
									r.AssistantText = text // 覆盖,保留最新完整文本
								}
							}
						}
					}
				}
			}
		}
	}
	return r
}

// ImageToolMsg 是 conversation.mapping 里一条 IMG2 tool 消息的关键字段。
type ImageToolMsg struct {
	MessageID     string
	CreateTime    float64
	ModelSlug     string
	Recipient     string
	AuthorName    string
	ImageGenTitle string
	FileIDs       []string // file-service
	SedimentIDs   []string // sediment
	IMG2Hint      bool     // true=asset metadata.generation.gen_size_v2 存在,sediment-only 也按 IMG2 终稿处理
	GenSizeV2s    []string // 抓包里的 generation.gen_size_v2,例如 "48"
	MaxWidth      int
	MaxHeight     int
	MaxSizeBytes  int64
}

// GetConversationMapping 读取会话全量 mapping(轮询用)。
func (c *Client) GetConversationMapping(ctx context.Context, convID string) (map[string]interface{}, error) {
	if convID == "" {
		return nil, fmt.Errorf("conv_id required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.opts.BaseURL+"/backend-api/conversation/"+convID, nil)
	if err != nil {
		return nil, err
	}
	c.commonHeaders(req)
	req.Header.Set("Accept", "*/*")

	res, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	buf, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		return nil, &UpstreamError{Status: res.StatusCode, Message: "conversation get failed", Body: string(buf)}
	}
	var out map[string]interface{}
	if err := json.Unmarshal(buf, &out); err != nil {
		return nil, fmt.Errorf("decode conversation: %w", err)
	}
	return out, nil
}

func imagePartString(m map[string]interface{}, path ...string) string {
	var cur interface{} = m
	for _, key := range path {
		mm, ok := cur.(map[string]interface{})
		if !ok {
			return ""
		}
		cur = mm[key]
	}
	s, _ := cur.(string)
	return s
}

func imagePartInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

func imagePartInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func appendUniqueString(xs []string, v string) []string {
	if v == "" {
		return xs
	}
	for _, x := range xs {
		if x == v {
			return xs
		}
	}
	return append(xs, v)
}

func inspectImageAssetPart(p map[string]interface{}, tm *ImageToolMsg) {
	if tm == nil {
		return
	}
	if w := imagePartInt(p["width"]); w > tm.MaxWidth {
		tm.MaxWidth = w
	}
	if h := imagePartInt(p["height"]); h > tm.MaxHeight {
		tm.MaxHeight = h
	}
	if sz := imagePartInt64(p["size_bytes"]); sz > tm.MaxSizeBytes {
		tm.MaxSizeBytes = sz
	}
	// 浏览器实测 IMG2 可以只给一条 sediment asset,但该 part 会带
	// metadata.generation.gen_size_v2(例如 "48") 与高清尺寸。旧逻辑只按
	// file-service 或 tool 条数判断,会把这种真正的 IMG2 误判为 preview_only。
	if genSizeV2 := imagePartString(p, "metadata", "generation", "gen_size_v2"); genSizeV2 != "" {
		tm.IMG2Hint = true
		tm.GenSizeV2s = appendUniqueString(tm.GenSizeV2s, genSizeV2)
	}
}

// ExtractImageToolMsgs 从 conversation.mapping 里提取所有 IMG2 tool 消息。
func ExtractImageToolMsgs(mapping map[string]interface{}) []ImageToolMsg {
	out := make([]ImageToolMsg, 0, 4)
	for mid, raw := range mapping {
		node, _ := raw.(map[string]interface{})
		if node == nil {
			continue
		}
		msg, _ := node["message"].(map[string]interface{})
		if msg == nil {
			continue
		}
		author, _ := msg["author"].(map[string]interface{})
		meta, _ := msg["metadata"].(map[string]interface{})
		content, _ := msg["content"].(map[string]interface{})
		if author == nil || meta == nil || content == nil {
			continue
		}
		if s, _ := author["role"].(string); s != "tool" {
			continue
		}
		if s, _ := meta["async_task_type"].(string); s != "image_gen" {
			continue
		}
		if s, _ := content["content_type"].(string); s != "multimodal_text" {
			continue
		}

		tm := ImageToolMsg{MessageID: mid}
		if v, ok := msg["create_time"].(float64); ok {
			tm.CreateTime = v
		}
		if v, ok := meta["model_slug"].(string); ok {
			tm.ModelSlug = v
		}
		if v, ok := msg["recipient"].(string); ok {
			tm.Recipient = v
		}
		if v, ok := author["name"].(string); ok {
			tm.AuthorName = v
		}
		if v, ok := meta["image_gen_title"].(string); ok {
			tm.ImageGenTitle = v
		}

		parts, _ := content["parts"].([]interface{})
		seenF := map[string]struct{}{}
		seenS := map[string]struct{}{}
		extractAsset := func(text string) {
			for _, m := range reFileRef.FindAllStringSubmatch(text, -1) {
				if _, ok := seenF[m[1]]; !ok {
					seenF[m[1]] = struct{}{}
					tm.FileIDs = append(tm.FileIDs, m[1])
				}
			}
			for _, m := range reSedRef.FindAllStringSubmatch(text, -1) {
				if _, ok := seenS[m[1]]; !ok {
					seenS[m[1]] = struct{}{}
					tm.SedimentIDs = append(tm.SedimentIDs, m[1])
				}
			}
		}
		for _, p := range parts {
			switch v := p.(type) {
			case map[string]interface{}:
				inspectImageAssetPart(v, &tm)
				if aid, _ := v["asset_pointer"].(string); aid != "" {
					extractAsset(aid)
				}
			case string:
				extractAsset(v)
			}
		}
		out = append(out, tm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreateTime < out[j].CreateTime })
	return out
}

// PollOpts 控制 PollConversationForImages 的等待策略。
type PollOpts struct {
	BaselineToolIDs map[string]struct{} // 发送前已存在的 tool 消息 id;本次回合只看新增
	MaxWait         time.Duration       // 总超时,默认 300s
	Interval        time.Duration       // 轮询间隔,默认 6s
	StableRounds    int                 // 稳定轮数(连续 N 次 sed 不变视为稳定),默认 4
	PreviewWait     time.Duration       // 第 1 条 tool 出现后等第 2 条的窗口,默认 30s
}

// PollStatus 是 PollConversationForImages 的结果状态。
type PollStatus string

const (
	PollStatusIMG2        PollStatus = "img2"         // 灰度命中,images 可用
	PollStatusPreviewOnly PollStatus = "preview_only" // 只出 1 条 tool,判定非灰度
	PollStatusTimeout     PollStatus = "timeout"
	PollStatusError       PollStatus = "error"
	PollStatus429         PollStatus = "rate_limited" // 连续 429,上游 RPM 限流
)

// PollConversationForImages 轮询会话直到灰度图出现。
// 返回 (status, file_ids, sediment_ids)。状态为 img2 时 file_ids 或 sediment_ids 至少一个非空。
func (c *Client) PollConversationForImages(ctx context.Context, convID string, opt PollOpts) (PollStatus, []string, []string) {
	if opt.MaxWait == 0 {
		opt.MaxWait = 300 * time.Second
	}
	if opt.Interval == 0 {
		opt.Interval = 6 * time.Second
	}
	if opt.StableRounds == 0 {
		opt.StableRounds = 4
	}
	if opt.PreviewWait == 0 {
		opt.PreviewWait = 30 * time.Second
	}
	baseline := opt.BaselineToolIDs

	deadline := time.Now().Add(opt.MaxWait)
	var (
		stableCount    int
		lastSedSig     string
		firstToolTs    time.Time
		consecutive429 int
	)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return PollStatusError, nil, nil
		default:
		}

		mapping, err := c.getMappingRaw(ctx, convID)
		if err != nil {
			if ue, ok := err.(*UpstreamError); ok && ue.Status == 429 {
				consecutive429++
				if consecutive429 >= 3 {
					return PollStatus429, nil, nil
				}
				sleep(ctx, 10*time.Second)
				continue
			}
			sleep(ctx, opt.Interval)
			continue
		}
		consecutive429 = 0

		msgs := ExtractImageToolMsgs(mapping)
		// baseline diff:只看本回合新增
		var newMsgs []ImageToolMsg
		if len(baseline) > 0 {
			for _, m := range msgs {
				if _, ok := baseline[m.MessageID]; !ok {
					newMsgs = append(newMsgs, m)
				}
			}
		} else {
			newMsgs = msgs
		}

		// 汇总所有新 tool 消息的 sed/file(**跨消息聚合**)。
		// IMG2 灰度命中时,上游通常会发 2 条 tool 消息 —— 例如 1 条 sediment
		// 预览 + 1 条 file-service 终稿,或者同一条消息里带多张 file id。
		// 以前只取 newMsgs[last] 会丢掉前一条 preview / 另一张图;这里收集
		// 全部 tool 消息里出现过的 id,调用方拿到几张就可以输出几张。
		var allSed []string
		var allFile []string
		img2Hint := false
		seenFile := map[string]struct{}{}
		seenSed := map[string]struct{}{}
		for _, m := range newMsgs {
			if m.IMG2Hint {
				img2Hint = true
			}
			for _, f := range m.FileIDs {
				if _, ok := seenFile[f]; !ok {
					seenFile[f] = struct{}{}
					allFile = append(allFile, f)
				}
			}
			for _, s := range m.SedimentIDs {
				if _, ok := seenSed[s]; !ok {
					seenSed[s] = struct{}{}
					allSed = append(allSed, s)
				}
			}
		}

		// 分支 1:file-service 直出 = IMG2 终稿。
		// 有 file-service 直出就算命中,把所有 tool 消息累计到的 fid/sid 都带出去。
		if len(allFile) > 0 {
			return PollStatusIMG2, allFile, allSed
		}

		// 分支 1.5:2026 抓包确认 IMG2 可能是单条 sediment asset,
		// 但 part.metadata.generation.gen_size_v2 存在(例如 "48")。这种不是
		// IMG1 预览,不应等待第二条 tool 或重试。
		if img2Hint && len(allSed) > 0 {
			return PollStatusIMG2, allFile, allSed
		}

		// 没有 tool 消息 → 继续等
		if len(newMsgs) == 0 {
			sleep(ctx, opt.Interval)
			continue
		}

		// 首次出现第 1 条 tool,记时间
		if firstToolTs.IsZero() && len(newMsgs) >= 1 {
			firstToolTs = time.Now()
		}

		// 分支 2:已经 2+ 条 tool 且有 IMG2 特征(gen_size_v2) → 灰度命中,等 sed 稳定后一并返回。
		// 没有 IMG2 特征的多条 tool 只是多轮 preview,不算真正的 IMG2。
		if len(newMsgs) >= 2 && img2Hint {
			sortedSed := append([]string(nil), allSed...)
			sort.Strings(sortedSed)
			sig := strings.Join(sortedSed, ",")
			if sig == lastSedSig && sig != "" {
				stableCount++
				if stableCount >= opt.StableRounds {
					return PollStatusIMG2, allFile, allSed
				}
			} else {
				stableCount = 0
				lastSedSig = sig
			}
		} else if !firstToolTs.IsZero() && time.Since(firstToolTs) >= opt.PreviewWait {
			// 分支 3:只 1 条 tool 且超过窗口 → 非灰度预览。
			// 把这条 tool 的 fids / sids 一并带出,外层可以用作 IMG1 预览兜底。
			return PollStatusPreviewOnly, allFile, allSed
		}

		sleep(ctx, opt.Interval)
	}

	return PollStatusTimeout, nil, nil
}

// getMappingRaw 拉 conversation 并返回 mapping。
func (c *Client) getMappingRaw(ctx context.Context, convID string) (map[string]interface{}, error) {
	full, err := c.GetConversationMapping(ctx, convID)
	if err != nil {
		return nil, err
	}
	mapping, _ := full["mapping"].(map[string]interface{})
	if mapping == nil {
		mapping = map[string]interface{}{}
	}
	return mapping, nil
}

// GetConversationHead 返回会话最新叶子消息 id(current_node),复用会话时做 parent_message_id。
func (c *Client) GetConversationHead(ctx context.Context, convID string) (string, error) {
	full, err := c.GetConversationMapping(ctx, convID)
	if err != nil {
		return "", err
	}
	head, _ := full["current_node"].(string)
	if head == "" {
		return "", fmt.Errorf("current_node missing")
	}
	return head, nil
}

// ImageDownloadURL 取单张图的签名下载 URL。
// fileRef 可以是:
//   - "xxxxxx"      → file-service id
//   - "sed:xxxxxx"  → sediment id(需要 convID)
//
// 兼容两类浏览器实测路径:
//   - 新路径: /backend-api/files/download/{fid}?conversation_id={cid}&inline=false
//   - 旧路径: /backend-api/files/{fid}/download 或 /conversation/{cid}/attachment/{sid}/download
func (c *Client) ImageDownloadURL(ctx context.Context, convID, fileRef string) (string, error) {
	isSediment := strings.HasPrefix(fileRef, "sed:")
	rawID := strings.TrimPrefix(fileRef, "sed:")
	if rawID == "" {
		return "", fmt.Errorf("empty file ref")
	}
	if isSediment && convID == "" {
		return "", fmt.Errorf("conv_id required for sediment")
	}

	var candidates []string
	if convID != "" {
		// 2026 浏览器抓包优先使用这一类 /files/download/{fid}?conversation_id=... 路径,
		// 返回的 download_url 往往会落到 /backend-api/estuary/content?...。
		candidates = append(candidates, fmt.Sprintf("%s/backend-api/files/download/%s?conversation_id=%s&inline=false",
			c.opts.BaseURL, url.PathEscape(rawID), url.QueryEscape(convID)))
	}
	if isSediment {
		candidates = append(candidates, fmt.Sprintf("%s/backend-api/conversation/%s/attachment/%s/download",
			c.opts.BaseURL, url.PathEscape(convID), url.PathEscape(rawID)))
	} else {
		candidates = append(candidates,
			fmt.Sprintf("%s/backend-api/files/download/%s", c.opts.BaseURL, url.PathEscape(rawID)),
			fmt.Sprintf("%s/backend-api/files/%s/download", c.opts.BaseURL, url.PathEscape(rawID)),
		)
	}

	var lastErr error
	for _, apiURL := range candidates {
		signed, err := c.fetchImageDownloadURL(ctx, apiURL)
		if err == nil && signed != "" {
			return signed, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("empty download_url")
}

func (c *Client) fetchImageDownloadURL(ctx context.Context, apiURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}
	c.commonHeaders(req)
	req.Header.Set("Accept", "*/*")

	// 某些路径直接 302 到 files.oaiusercontent.com / estuary content。不要自动跟随,
	// 否则我们会把图片二进制当 JSON 解析。
	local := *c.hc
	local.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	c.injectHelloID(req)
	res, err := local.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 && res.StatusCode < 400 {
		loc := res.Header.Get("Location")
		if loc == "" {
			return "", &UpstreamError{Status: res.StatusCode, Message: "files/download redirect without location"}
		}
		base, _ := url.Parse(apiURL)
		u, err := url.Parse(loc)
		if err != nil {
			return "", err
		}
		return base.ResolveReference(u).String(), nil
	}

	buf, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		return "", &UpstreamError{Status: res.StatusCode, Message: "files/download failed", Body: string(buf)}
	}

	ct := strings.ToLower(res.Header.Get("Content-Type"))
	if strings.HasPrefix(ct, "image/") || strings.Contains(ct, "octet-stream") {
		// 端点本身已经能回源图片;交给 FetchImage 再带鉴权下载一次。
		return apiURL, nil
	}

	var out struct {
		DownloadURL string `json:"download_url"`
		URL         string `json:"url"`
		Status      string `json:"status"`
		FileName    string `json:"file_name"`
	}
	if err := json.Unmarshal(buf, &out); err == nil {
		if out.DownloadURL != "" {
			return out.DownloadURL, nil
		}
		if out.URL != "" {
			return out.URL, nil
		}
		return "", fmt.Errorf("empty download_url (status=%s)", out.Status)
	}

	// 兜底:有些实验路径可能直接返回纯文本 URL。
	text := strings.TrimSpace(string(buf))
	if strings.HasPrefix(text, "http://") || strings.HasPrefix(text, "https://") || strings.HasPrefix(text, "/") {
		if strings.HasPrefix(text, "/") {
			return c.opts.BaseURL + text, nil
		}
		return text, nil
	}
	return "", fmt.Errorf("decode files/download: unexpected content-type=%s body=%s", ct, truncateForErr(text, 200))
}

func truncateForErr(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n]
}

// FetchImage 下载指定 URL 的图片字节(调用 ImageDownloadURL 返回的签名 URL)。
// 返回 (bytes, content_type)。超过 maxBytes 的响应会被截断报错。
//
// 鉴权策略:
//   - files.oaiusercontent.com / cdn.oaistatic.com 等**预签名直链**:不带 Authorization,
//     它们依赖 URL 自带的 sig 参数鉴权;带 Bearer 反而会被某些 CDN 因"双鉴权"400。
//   - chatgpt.com/backend-api/estuary/... (sediment 回源 URL):**必须** 带 Authorization:
//     Bearer 和完整 Oai-* 头,否则上游 403。这就是 IMG2 sediment 图 502 的根因。
func (c *Client) FetchImage(ctx context.Context, signedURL string, maxBytes int64) ([]byte, string, error) {
	if maxBytes <= 0 {
		maxBytes = 16 * 1024 * 1024 // 16MB 默认
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, signedURL, nil)
	if err != nil {
		return nil, "", err
	}

	// 判断是否需要完整 chatgpt 鉴权头:以 BaseURL(通常 https://chatgpt.com)开头的
	// estuary / attachment 回源 URL 必须带 Bearer + Oai-* 头;外部 CDN 直链不带。
	needAuth := strings.HasPrefix(signedURL, c.opts.BaseURL+"/")
	if needAuth {
		c.commonHeaders(req)
		req.Header.Set("Accept", "image/*,*/*;q=0.8")
	} else {
		req.Header.Set("User-Agent", c.opts.UserAgent)
	}

	res, err := c.hc.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil, "", &UpstreamError{Status: res.StatusCode, Message: "fetch image failed"}
	}
	ct := res.Header.Get("Content-Type")
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBytes+1))
	if err != nil {
		return nil, ct, err
	}
	if int64(len(body)) > maxBytes {
		return nil, ct, fmt.Errorf("image exceeds max bytes (%d)", maxBytes)
	}
	return body, ct, nil
}

// sleep 可取消的 sleep。
func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
