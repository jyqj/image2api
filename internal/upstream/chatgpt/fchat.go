// fchat.go —— 文字聊天走 /backend-api/f/conversation 新协议。
//
// 背景:chatgpt.com 近期新账号在老 /backend-api/conversation 端点上会直接
// 回一条 author=system、content 为空、status=finished_successfully 的消息,
// 等同于"被静默拒绝"。对齐浏览器抓包与社区维护的 OpenaiChat provider,
// 文字聊天正确顺序是:
//
//	1. GET /                            → 拿 __cf_bm / oai-did / _cfuvid cookie
//	2. sentinel/chat-requirements       → 拿 chat_token + proofofwork 挑战
//	3. f/conversation/prepare           → 带 chat_token(!) + proof_token,拿 conduit_token
//	4. f/conversation (SSE)             → 带 chat_token + proof_token + conduit_token
//
// 要点:prepare 必须在 chat-requirements 之后,并且要把 `openai-sentinel-chat-
// requirements-token` 带进 header。不调用 /backend-api/conversation/init,
// 该端点在免费/新账号上会直接 404。

package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// FChatOpts 是 StreamFChat 的入参。
type FChatOpts struct {
	UpstreamModel string        // 默认 "auto"
	Messages      []ChatMessage // OpenAI 风格
	ChatToken     string        // 必传(StreamFChat 时,PrepareFChat 不需要)
	ProofToken    string        // 可选
	ConduitToken  string        // 可选(PrepareFChat 返回)
	ConvID        string        // 复用会话时传
	ParentMsgID   string        // 复用会话时传 GetConversationHead 结果
	SSETimeout    time.Duration // 默认 120s
}

// PrepareFChat 对 /backend-api/f/conversation/prepare 发一条文字 prepare,返回 conduit_token。
//
// payload 严格对齐浏览器抓包(HAR 中 /f/conversation/prepare 请求):
//   - client_prepare_state: "success"
//   - fork_from_shared_post: false
//   - partial_query: 一条完整的 user message(id+author+content),不是空字符串
//   - system_hints: []  ← text 通路是空数组(注意:image 通路是 ["picture_v2"])
//   - client_contextual_info: { "app_name": "chatgpt.com" }  ← prepare 只带 app_name
//
// header 带 Openai-Sentinel-Chat-Requirements-Token(必须)+ 可选 Proof-Token。
func (c *Client) PrepareFChat(ctx context.Context, opt FChatOpts) (string, error) {
	if opt.ChatToken == "" {
		return "", errors.New("chat_token required for prepare")
	}
	if opt.UpstreamModel == "" {
		opt.UpstreamModel = "auto"
	}
	if len(opt.Messages) == 0 {
		return "", errors.New("messages required")
	}
	if opt.ParentMsgID == "" {
		opt.ParentMsgID = uuid.NewString()
	}

	// partial_query 用最后一条 user message。浏览器的做法是"用户一输完字就发
	// prepare",此时还没 send,所以 partial_query = 当前正在输入的内容。
	var userPart string
	for i := len(opt.Messages) - 1; i >= 0; i-- {
		if opt.Messages[i].Role == "user" {
			userPart = opt.Messages[i].Content
			break
		}
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
		"system_hints":          []string{},
		"partial_query": map[string]interface{}{
			"id":     uuid.NewString(),
			"author": map[string]string{"role": "user"},
			"content": map[string]interface{}{
				"content_type": "text",
				"parts":        []string{userPart},
			},
		},
		"supports_buffering":  true,
		"supported_encodings": []string{"v1"},
		"client_contextual_info": map[string]interface{}{
			"app_name": "chatgpt.com",
		},
	}
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

// StreamFChat 发起一次文字 f/conversation SSE。
// 调用前请确保:新会话场景先调用 InitConversation(ctx)(空 system_hints)。
func (c *Client) StreamFChat(ctx context.Context, opt FChatOpts) (<-chan SSEEvent, error) {
	if opt.ChatToken == "" {
		return nil, errors.New("chat_token required")
	}
	if opt.UpstreamModel == "" {
		opt.UpstreamModel = "auto"
	}
	if len(opt.Messages) == 0 {
		return nil, errors.New("messages required")
	}
	if opt.ParentMsgID == "" {
		opt.ParentMsgID = uuid.NewString()
	}
	if opt.SSETimeout == 0 {
		opt.SSETimeout = 120 * time.Second
	}

	// messages[].metadata 严格对齐 HAR 抓包的 **text 通路**:
	//   developer_mode_connector_ids / selected_sources / selected_github_repos /
	//   selected_all_github_repos / serialization_metadata
	// 其中 **selected_sources 只有 text 通路有,image 通路没有**;反之
	// system_hints: ["picture_v2"] 只有 image 通路有,text 完全不写这个 key。
	// 写错会导致上游认为客户端类型不匹配,触发 silent rejection。
	msgs := make([]map[string]interface{}, 0, len(opt.Messages))
	for _, m := range opt.Messages {
		msgs = append(msgs, map[string]interface{}{
			"id":          uuid.NewString(),
			"author":      map[string]string{"role": m.Role},
			"create_time": float64(time.Now().UnixMilli()) / 1000.0,
			"content":     map[string]interface{}{"content_type": "text", "parts": []string{m.Content}},
			"metadata": map[string]interface{}{
				"developer_mode_connector_ids": []interface{}{},
				"selected_sources":             []interface{}{},
				"selected_github_repos":        []interface{}{},
				"selected_all_github_repos":    false,
				"serialization_metadata": map[string]interface{}{
					"custom_symbol_offsets": []interface{}{},
				},
			},
		})
	}

	// 顶层 payload 对齐 HAR /f/conversation 抓包(text 通路):
	//   client_prepare_state: "sent"            ← prepare 已发过一次
	//   system_hints: []                         ← text 空数组
	//   force_parallel_switch: "auto"            ← 必带
	//   client_contextual_info: 7 个字段 + app_name
	payload := map[string]interface{}{
		"action":                   "next",
		"messages":                 msgs,
		"parent_message_id":        opt.ParentMsgID,
		"model":                    opt.UpstreamModel,
		"client_prepare_state":     "sent",
		"timezone_offset_min":      -480,
		"timezone":                 "Asia/Shanghai",
		"conversation_mode":        map[string]string{"kind": "primary_assistant"},
		"enable_message_followups": true,
		"system_hints":             []string{},
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
	// 只有已有会话才带 conversation_id;新会话完全不带这个 key(对齐浏览器抓包)。
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
	// X-Oai-Turn-Trace-Id:真实浏览器每 turn 一个新 UUID。风控会据此做请求配对
	// (prepare 与 finalize / f-prepare 与 f-conversation 的 trace 对得上)。
	req.Header.Set("X-Oai-Turn-Trace-Id", uuid.NewString())
	req.Header.Set("Openai-Sentinel-Chat-Requirements-Token", opt.ChatToken)
	if opt.ProofToken != "" {
		req.Header.Set("Openai-Sentinel-Proof-Token", opt.ProofToken)
	}
	if opt.ConduitToken != "" {
		req.Header.Set("X-Conduit-Token", opt.ConduitToken)
	}

	local := *c.hc
	local.Timeout = 0
	c.injectHelloID(req)

	res, err := local.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		buf, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return nil, &UpstreamError{Status: res.StatusCode, Message: "f/conversation(chat) failed", Body: string(buf)}
	}
	out := make(chan SSEEvent, 64)
	go parseSSE(res.Body, out, opt.SSETimeout)
	return out, nil
}
