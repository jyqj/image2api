package chatgpt

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ChatMessage 是 OpenAI 风格的一条消息。
// Content 支持纯字符串和多模态数组(OpenAI vision 格式)两种形式。
type ChatMessage struct {
	Role       string          `json:"role"`
	RawContent json.RawMessage `json:"content"`

	// 解析后的字段(非 JSON)
	Content   string   `json:"-"` // 纯文本内容
	ImageURLs []string `json:"-"` // image_url 类型的 URL 列表
}

// UnmarshalJSON 自定义反序列化,兼容 string 和 array 两种 content 格式。
func (m *ChatMessage) UnmarshalJSON(data []byte) error {
	type alias struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	m.Role = a.Role
	m.RawContent = a.Content
	m.Content = ""
	m.ImageURLs = nil

	parseContentParts(a.Content, &m.Content, &m.ImageURLs)
	return nil
}

func parseContentParts(raw json.RawMessage, textOut *string, imageOut *[]string) {
	if len(raw) == 0 {
		return
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		*textOut = s
		return
	}

	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return
	}
	var texts []string
	seenImages := map[string]struct{}{}
	for _, p := range parts {
		typeName := rawString(p["type"])
		switch typeName {
		case "text", "input_text", "":
			if v := strings.TrimSpace(rawString(p["text"])); v != "" {
				texts = append(texts, v)
			}
		case "image_url", "input_image", "image":
			// URL 会在下方统一提取。
		}

		for _, key := range []string{"image_url", "url", "input_image", "source", "data_url"} {
			for _, u := range rawURLCandidates(p[key]) {
				u = strings.TrimSpace(u)
				if u == "" {
					continue
				}
				if _, ok := seenImages[u]; ok {
					continue
				}
				seenImages[u] = struct{}{}
				*imageOut = append(*imageOut, u)
			}
		}
	}
	*textOut = strings.Join(texts, "\n")
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}

func rawURLCandidates(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	if s := rawString(raw); s != "" {
		return []string{s}
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	out := make([]string, 0, 3)
	for _, key := range []string{"url", "image_url", "data_url", "source", "data", "b64_json", "base64"} {
		if v := rawString(obj[key]); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// MarshalJSON 序列化时保留原始格式。
func (m ChatMessage) MarshalJSON() ([]byte, error) {
	type alias struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	a := alias{Role: m.Role, Content: m.RawContent}
	if len(a.Content) == 0 && m.Content != "" {
		c, _ := json.Marshal(m.Content)
		a.Content = c
	}
	return json.Marshal(a)
}

// ConversationOpts 是 StreamConversation 的参数。
type ConversationOpts struct {
	Model       string        // 上游模型 slug(如 auto / gpt-4o / o4-mini)
	Messages    []ChatMessage // OpenAI 风格消息
	ParentMsgID string        // 可选,为空自动生成
	ConvID      string        // 可选,为空则新会话
	ProofToken  string        // 可选,POW 解出后填入
	ChatToken   string        // 必传(来自 ChatRequirements)
	ReadTimeout time.Duration // SSE 读超时(单次事件间隔),默认 60s
}

// conversationPayload 对齐 chatgpt.com 请求体(文本模式)。
type conversationPayload struct {
	Action                     string                 `json:"action"`
	Messages                   []upstreamMsg          `json:"messages"`
	ParentMessageID            string                 `json:"parent_message_id"`
	ConversationID             string                 `json:"conversation_id,omitempty"`
	Model                      string                 `json:"model"`
	TimezoneOffsetMin          int                    `json:"timezone_offset_min"`
	Suggestions                []string               `json:"suggestions"`
	HistoryAndTrainingDisabled bool                   `json:"history_and_training_disabled"`
	ConversationMode           map[string]interface{} `json:"conversation_mode"`
	ForceParagen               bool                   `json:"force_paragen"`
	ForceParagenModelSlug      string                 `json:"force_paragen_model_slug"`
	ForceNulligen              bool                   `json:"force_nulligen"`
	ForceRateLimit             bool                   `json:"force_rate_limit"`
	WebsocketRequestID         string                 `json:"websocket_request_id"`
	ClientContextualInfo       map[string]interface{} `json:"client_contextual_info,omitempty"`
	PluginIDs                  []string               `json:"plugin_ids,omitempty"`
}

type upstreamMsg struct {
	ID         string          `json:"id"`
	Author     upstreamAuthor  `json:"author"`
	Content    upstreamContent `json:"content"`
	Metadata   map[string]any  `json:"metadata,omitempty"`
	CreateTime float64         `json:"create_time,omitempty"`
}

type upstreamAuthor struct {
	Role string `json:"role"`
}

type upstreamContent struct {
	ContentType string   `json:"content_type"`
	Parts       []string `json:"parts"`
}

// StreamConversation 向 /backend-api/conversation 发 SSE,返回事件 channel。
// 调用方必须消费完 channel(或 cancel ctx)以释放连接。
func (c *Client) StreamConversation(ctx context.Context, opt ConversationOpts) (<-chan SSEEvent, error) {
	if opt.ChatToken == "" {
		return nil, errors.New("chat_token required")
	}
	if opt.Model == "" {
		opt.Model = "auto"
	}
	if opt.ParentMsgID == "" {
		opt.ParentMsgID = uuid.NewString()
	}
	if opt.ReadTimeout == 0 {
		opt.ReadTimeout = c.opts.SSETimeout
	}

	payload := conversationPayload{
		Action:                     "next",
		Model:                      opt.Model,
		ParentMessageID:            opt.ParentMsgID,
		ConversationID:             opt.ConvID,
		TimezoneOffsetMin:          -480, // UTC+8
		HistoryAndTrainingDisabled: false,
		ConversationMode:           map[string]interface{}{"kind": "primary_assistant"},
		WebsocketRequestID:         uuid.NewString(),
	}
	for _, m := range opt.Messages {
		payload.Messages = append(payload.Messages, upstreamMsg{
			ID:         uuid.NewString(),
			Author:     upstreamAuthor{Role: m.Role},
			Content:    upstreamContent{ContentType: "text", Parts: []string{m.Content}},
			CreateTime: float64(time.Now().Unix()),
		})
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx,
		http.MethodPost,
		c.opts.BaseURL+"/backend-api/conversation",
		strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	c.commonHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Openai-Sentinel-Chat-Requirements-Token", opt.ChatToken)
	if opt.ProofToken != "" {
		req.Header.Set("Openai-Sentinel-Proof-Token", opt.ProofToken)
	}

	// 对 SSE 请求取消客户端整体 timeout,改为 per-event 读超时控制。
	localClient := *c.hc
	localClient.Timeout = 0
	c.injectHelloID(req)

	res, err := localClient.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		buf, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return nil, &UpstreamError{Status: res.StatusCode, Message: "conversation failed", Body: string(buf)}
	}

	out := make(chan SSEEvent, 32)
	go parseSSE(res.Body, out, opt.ReadTimeout)
	return out, nil
}

// parseSSE 读取 SSE 流,把每个 data: 事件推入 channel。
// chatgpt.com 的事件格式:
//
//	event: delta\n
//	data: {"p":"...","o":"append","v":"..."}\n\n
//
//	data: [DONE]\n\n
//
// readTimeout 控制 per-event 读超时:如果两次换行之间的间隔超过此值,
// 视为上游卡住,发送 Err 并关闭流,防止 goroutine 无限泄漏。
func parseSSE(r io.ReadCloser, out chan<- SSEEvent, readTimeout time.Duration) {
	defer r.Close()
	defer close(out)

	if readTimeout <= 0 {
		readTimeout = 60 * time.Second
	}

	rd := bufio.NewReaderSize(r, 32*1024)
	var event string
	var dataBuf strings.Builder

	// lineChan 在独立 goroutine 中阻塞读行,主循环通过 timer 检测超时。
	type lineResult struct {
		line string
		err  error
	}
	lineCh := make(chan lineResult, 1)

	go func() {
		defer close(lineCh)
		for {
			line, err := rd.ReadString('\n')
			lineCh <- lineResult{line: line, err: err}
			if err != nil {
				return
			}
		}
	}()

	flush := func() {
		if dataBuf.Len() == 0 {
			event = ""
			return
		}
		data := strings.TrimRight(dataBuf.String(), "\n")
		dataBuf.Reset()
		out <- SSEEvent{Event: event, Data: []byte(data)}
		event = ""
	}

	for {
		timer := time.NewTimer(readTimeout)
		select {
		case lr, ok := <-lineCh:
			timer.Stop()
			if !ok {
				// lineCh 已关闭(读 goroutine 退出)
				flush()
				return
			}
			if lr.err != nil {
				if lr.err != io.EOF {
					out <- SSEEvent{Err: fmt.Errorf("sse read: %w", lr.err)}
				} else {
					flush()
				}
				return
			}
			line := strings.TrimRight(lr.line, "\r\n")
			if line == "" {
				// 事件边界
				flush()
				continue
			}
			if strings.HasPrefix(line, ":") {
				// 注释/心跳,忽略
				continue
			}
			if strings.HasPrefix(line, "event:") {
				event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
				continue
			}
			if strings.HasPrefix(line, "data:") {
				s := strings.TrimPrefix(line, "data:")
				if len(s) > 0 && s[0] == ' ' {
					s = s[1:]
				}
				if dataBuf.Len() > 0 {
					dataBuf.WriteByte('\n')
				}
				dataBuf.WriteString(s)
				continue
			}
			// 其他行忽略

		case <-timer.C:
			// per-event 超时:上游 SSE 停顿太久,主动关闭防止 goroutine 泄漏。
			out <- SSEEvent{Err: fmt.Errorf("sse read timeout (%v)", readTimeout)}
			return
		}
	}
}
