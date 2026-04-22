package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/432539/gpt2api/internal/image"
	modelpkg "github.com/432539/gpt2api/internal/model"
	"github.com/432539/gpt2api/internal/upstream/chatgpt"
	"github.com/432539/gpt2api/internal/usage"
)

const maxReferenceImageBytes = 20 * 1024 * 1024
const maxReferenceImages = 4

type chatMsg = chatgpt.ChatMessage

// ImagesHandler 挂载在 /v1/images/* 下的处理器。
type ImagesHandler struct {
	*Handler
	Runner *image.Runner
	DAO    *image.DAO
	// ImageAccResolver 可选:代理下载上游图片时用于解出账号 AT/cookies/proxy。
	ImageAccResolver ImageAccountResolver
}

// ImageGenRequest OpenAI 兼容入参。
type ImageGenRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n"`
	Size           string `json:"size"`
	Quality        string `json:"quality,omitempty"`
	Style          string `json:"style,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
	User           string `json:"user,omitempty"`

	// reference_images 是控制台/兼容接口的主入口;同时兼容 image_urls/images/
	// reference_image,便于接收其它客户端或图床包装后的对象。
	ReferenceImages ReferenceList  `json:"reference_images,omitempty"`
	ImageURLs       ReferenceList  `json:"image_urls,omitempty"`
	Images          ReferenceList  `json:"images,omitempty"`
	ReferenceImage  ReferenceInput `json:"reference_image,omitempty"`
	ImageURL        ReferenceInput `json:"image_url,omitempty"`
	InputImage      ReferenceInput `json:"input_image,omitempty"`
}

// ReferenceInput 是一张参考图输入。Value 可以是 http(s) URL、data URL、
// 或裸 base64;Name 可选,用于上传到 ChatGPT 文件服务时保留文件名。
type ReferenceInput struct {
	Value string
	Name  string
}

func (r *ReferenceInput) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		r.Value = strings.TrimSpace(s)
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	r.Name = firstJSONStr(obj, "file_name", "filename", "name")
	for _, key := range []string{"url", "data_url", "image_url", "input_image", "source", "data", "b64_json", "base64"} {
		if v := referenceValueFromRaw(obj[key]); strings.TrimSpace(v) != "" {
			r.Value = strings.TrimSpace(v)
			return nil
		}
	}
	return nil
}

// ReferenceList 兼容数组、单个字符串、单个对象三种 JSON 写法。
type ReferenceList []ReferenceInput

func (l *ReferenceList) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*l = nil
		return nil
	}
	if data[0] == '[' {
		var arr []ReferenceInput
		if err := json.Unmarshal(data, &arr); err != nil {
			return err
		}
		*l = arr
		return nil
	}
	var one ReferenceInput
	if err := json.Unmarshal(data, &one); err != nil {
		return err
	}
	*l = []ReferenceInput{one}
	return nil
}

type ImageGenData struct {
	URL           string `json:"url,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
	FileID        string `json:"file_id,omitempty"`
}

type ImageGenResponse struct {
	Created   int64          `json:"created"`
	Data      []ImageGenData `json:"data"`
	TaskID    string         `json:"task_id,omitempty"`
	IsPreview bool           `json:"is_preview,omitempty"`
}

func (r ImageGenRequest) AllReferences() []ReferenceInput {
	out := make([]ReferenceInput, 0, len(r.ReferenceImages)+len(r.ImageURLs)+len(r.Images)+3)
	seen := map[string]struct{}{}
	add := func(in ReferenceInput) {
		in.Value = strings.TrimSpace(in.Value)
		in.Name = strings.TrimSpace(in.Name)
		if in.Value == "" {
			return
		}
		if _, ok := seen[in.Value]; ok {
			return
		}
		seen[in.Value] = struct{}{}
		out = append(out, in)
	}
	for _, in := range r.ReferenceImages {
		add(in)
	}
	for _, in := range r.ImageURLs {
		add(in)
	}
	for _, in := range r.Images {
		add(in)
	}
	add(r.ReferenceImage)
	add(r.ImageURL)
	add(r.InputImage)
	return out
}

// ImageGenerations POST /v1/images/generations。
func (h *ImagesHandler) ImageGenerations(c *gin.Context) {
	startAt := time.Now()
	var req ImageGenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", "请求参数错误:"+err.Error())
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", "prompt 不能为空")
		return
	}
	if req.Model == "" {
		req.Model = "gpt-image-2"
	}
	if req.N <= 0 {
		req.N = 1
	}
	if req.N > 4 {
		req.N = 4
	}
	if req.Size == "" {
		req.Size = "1024x1024"
	}

	refID := uuid.NewString()
	rec := &usage.Log{RequestID: refID, Type: usage.TypeImage, IP: c.ClientIP(), UA: c.Request.UserAgent()}
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
		openAIError(c, http.StatusBadRequest, "model_not_found", fmt.Sprintf("模型 %q 不存在或未启用", req.Model))
		return
	}
	if m.Type != modelpkg.TypeImage {
		fail("model_type_mismatch")
		openAIError(c, http.StatusBadRequest, "model_type_mismatch", fmt.Sprintf("模型 %q 不是图像模型", req.Model))
		return
	}
	rec.ModelID = m.ID

	refs, err := decodeReferenceInputs(c.Request.Context(), req.AllReferences())
	if err != nil {
		fail("invalid_reference_image")
		openAIError(c, http.StatusBadRequest, "invalid_reference_image", "参考图解析失败:"+err.Error())
		return
	}

	taskID := image.GenerateTaskID()
	if h.DAO != nil {
		if err := h.DAO.Create(c.Request.Context(), &image.Task{
			TaskID: taskID, ModelID: m.ID, Prompt: req.Prompt,
			N: req.N, Size: req.Size, Quality: req.Quality, Style: req.Style,
			Status: image.StatusDispatched, UserID: req.User,
		}); err != nil {
			fail("internal_error")
			openAIError(c, http.StatusInternalServerError, "internal_error", "创建任务失败:"+err.Error())
			return
		}
	}

	runCtx, cancel := context.WithTimeout(c.Request.Context(), 6*time.Minute)
	defer cancel()
	maxAttempts := 2
	if len(refs) > 0 {
		maxAttempts = 1
	}
	res := h.Runner.Run(runCtx, image.RunOptions{
		TaskID:        taskID,
		ModelID:       m.ID,
		UpstreamModel: m.UpstreamModelSlug,
		Prompt:        maybeAppendClaritySuffix(req.Prompt),
		N:             req.N,
		MaxAttempts:   maxAttempts,
		References:    refs,
	})
	rec.AccountID = res.AccountID

	if res.Status != image.StatusSuccess {
		fail(ifEmpty(res.ErrorCode, "upstream_error"))
		httpStatus := http.StatusBadGateway
		if res.ErrorCode == image.ErrNoAccount || res.ErrorCode == image.ErrRateLimited {
			httpStatus = http.StatusServiceUnavailable
		}
		if res.ErrorCode == image.ErrContentPolicy {
			httpStatus = http.StatusBadRequest
		}
		openAIError(c, httpStatus, ifEmpty(res.ErrorCode, "upstream_error"), localizeImageErr(res.ErrorCode, res.ErrorMessage))
		return
	}

	rec.Status = usage.StatusSuccess
	rec.ImageCount = len(res.SignedURLs)
	c.JSON(http.StatusOK, imageResponse(taskID, res, RequestBaseURL(c)))
}

// ImageTask GET /v1/images/tasks/:id。
func (h *ImagesHandler) ImageTask(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", "task id 不能为空")
		return
	}
	if h.DAO == nil {
		openAIError(c, http.StatusInternalServerError, "not_configured", "图片任务存储未初始化")
		return
	}
	t, err := h.DAO.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, image.ErrNotFound) {
			openAIError(c, http.StatusNotFound, "not_found", "任务不存在")
			return
		}
		openAIError(c, http.StatusInternalServerError, "internal_error", "查询任务失败:"+err.Error())
		return
	}
	urls := t.DecodeResultURLs()
	fileIDs := t.DecodeFileIDs()
	data := make([]ImageGenData, 0, len(urls))
	for i := range urls {
		d := ImageGenData{URL: BuildImageProxyURL(t.TaskID, i, ImageProxyTTL, RequestBaseURL(c))}
		if i < len(fileIDs) {
			d.FileID = strings.TrimPrefix(fileIDs[i], "sed:")
		}
		data = append(data, d)
	}
	c.JSON(http.StatusOK, gin.H{
		"task_id":         t.TaskID,
		"status":          t.Status,
		"conversation_id": t.ConversationID,
		"created":         t.CreatedAt.Unix(),
		"finished_at":     nullableUnix(t.FinishedAt),
		"error":           t.Error,
		"data":            data,
	})
}

// handleChatAsImage 是 /v1/chat/completions 发现 model.type=image 时的转派点。
// 支持多模态消息: content 为 [{"type":"text","text":"..."}, {"type":"image_url","image_url":{"url":"..."}}]
func (h *ImagesHandler) handleChatAsImage(c *gin.Context, rec *usage.Log, m *modelpkg.Model, req *ChatCompletionsRequest, startAt time.Time) {
	rec.ModelID = m.ID
	rec.Type = usage.TypeImage
	prompt, imageURLs := extractLastUserContent(req.Messages)
	if strings.TrimSpace(prompt) == "" {
		rec.Status = usage.StatusFailed
		rec.ErrorCode = "invalid_request_error"
		openAIError(c, http.StatusBadRequest, "invalid_request_error", "图像模型需要 user role 消息作为 prompt")
		return
	}

	// 把 image_url 转成参考图
	var refs []image.ReferenceImage
	if len(imageURLs) > 0 {
		decoded, err := decodeReferenceStrings(c.Request.Context(), imageURLs)
		if err != nil {
			rec.Status = usage.StatusFailed
			rec.ErrorCode = "invalid_reference_image"
			openAIError(c, http.StatusBadRequest, "invalid_reference_image", "参考图解析失败:"+err.Error())
			return
		}
		refs = decoded
	}

	// 存储 prompt 时附加参考图信息,方便查找记录
	storedPrompt := prompt
	if len(imageURLs) > 0 {
		var refNote strings.Builder
		refNote.WriteString(prompt)
		refNote.WriteString("\n\n[参考图: ")
		for i, u := range imageURLs {
			if i > 0 {
				refNote.WriteString(", ")
			}
			if len(u) > 100 {
				refNote.WriteString(u[:100] + "...")
			} else {
				refNote.WriteString(u)
			}
		}
		refNote.WriteString("]")
		storedPrompt = refNote.String()
	}

	taskID := image.GenerateTaskID()
	if h.DAO != nil {
		_ = h.DAO.Create(c.Request.Context(), &image.Task{
			TaskID: taskID, ModelID: m.ID, Prompt: storedPrompt,
			N: 1, Size: "1024x1024", Status: image.StatusDispatched,
		})
	}

	runCtx, cancel := context.WithTimeout(c.Request.Context(), 6*time.Minute)
	defer cancel()
	maxAttempts := 2
	if len(refs) > 0 {
		maxAttempts = 1
	}
	res := h.Runner.Run(runCtx, image.RunOptions{
		TaskID:        taskID,
		ModelID:       m.ID,
		UpstreamModel: m.UpstreamModelSlug,
		Prompt:        maybeAppendClaritySuffix(prompt),
		N:             1,
		MaxAttempts:   maxAttempts,
		References:    refs,
	})
	rec.AccountID = res.AccountID
	if res.Status != image.StatusSuccess {
		rec.Status = usage.StatusFailed
		rec.ErrorCode = ifEmpty(res.ErrorCode, "upstream_error")
		httpStatus := http.StatusBadGateway
		if res.ErrorCode == image.ErrNoAccount || res.ErrorCode == image.ErrRateLimited {
			httpStatus = http.StatusServiceUnavailable
		}
		if res.ErrorCode == image.ErrContentPolicy {
			httpStatus = http.StatusBadRequest
		}
		openAIError(c, httpStatus, ifEmpty(res.ErrorCode, "upstream_error"), localizeImageErr(res.ErrorCode, res.ErrorMessage))
		return
	}

	rec.Status = usage.StatusSuccess
	rec.ImageCount = len(res.SignedURLs)
	rec.DurationMs = int(time.Since(startAt).Milliseconds())

	baseURL := RequestBaseURL(c)
	var sb strings.Builder
	for i := range res.SignedURLs {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(fmt.Sprintf("![generated](%s)", BuildImageProxyURL(taskID, i, ImageProxyTTL, baseURL)))
	}
	content := sb.String()
	id := "chatcmpl-" + uuid.NewString()

	if req.Stream {
		// 流式返回: SSE 格式
		w := c.Writer
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		// 发送内容 chunk
		chunk := ChatCompletionChunk{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   m.Slug,
			Choices: []ChatCompletionChunkChoice{{
				Index: 0,
				Delta: DeltaMsg{Role: "assistant", Content: content},
			}},
		}
		chunkJSON, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", chunkJSON)
		if flusher != nil {
			flusher.Flush()
		}

		// 发送 stop chunk
		stopReason := "stop"
		stopChunk := ChatCompletionChunk{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   m.Slug,
			Choices: []ChatCompletionChunkChoice{{
				Index:        0,
				Delta:        DeltaMsg{},
				FinishReason: &stopReason,
			}},
		}
		stopJSON, _ := json.Marshal(stopChunk)
		fmt.Fprintf(w, "data: %s\n\n", stopJSON)
		fmt.Fprintf(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	} else {
		resp := ChatCompletionResponse{
			ID:      id,
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   m.Slug,
			Choices: []ChatCompletionChoice{{
				Index:        0,
				Message:      chatMsg{Role: "assistant", Content: content},
				FinishReason: "stop",
			}},
			Usage: ChatCompletionUsage{},
		}
		c.JSON(http.StatusOK, resp)
	}
}

// extractLastUserContent 从消息列表中提取最后一条 user 消息的文本和图片 URL。
func extractLastUserContent(msgs []chatMsg) (prompt string, imageURLs []string) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "user" {
			continue
		}
		text := strings.TrimSpace(msgs[i].Content)
		urls := msgs[i].ImageURLs
		if text != "" || len(urls) > 0 {
			return text, urls
		}
	}
	return "", nil
}

// ImageEdits 实现 POST /v1/images/edits,按 OpenAI multipart/form-data 形式接收参考图。
func (h *ImagesHandler) ImageEdits(c *gin.Context) {
	startAt := time.Now()
	if err := c.Request.ParseMultipartForm(int64(maxReferenceImageBytes) * int64(maxReferenceImages+1)); err != nil {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", "解析 multipart 失败:"+err.Error())
		return
	}
	prompt := strings.TrimSpace(c.Request.FormValue("prompt"))
	if prompt == "" {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", "prompt 不能为空")
		return
	}
	model := c.Request.FormValue("model")
	if model == "" {
		model = "gpt-image-2"
	}
	n := 1
	if s := c.Request.FormValue("n"); s != "" {
		if v, err := parseIntClamp(s, 1, 4); err == nil {
			n = v
		}
	}
	size := c.Request.FormValue("size")
	if size == "" {
		size = "1024x1024"
	}

	files, err := collectEditFiles(c.Request.MultipartForm)
	if err != nil {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if len(files) == 0 {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", "至少需要上传一张 image 作为参考图")
		return
	}
	if len(files) > maxReferenceImages {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("最多支持 %d 张参考图", maxReferenceImages))
		return
	}
	refs := make([]image.ReferenceImage, 0, len(files))
	for _, fh := range files {
		data, err := readMultipart(fh)
		if err != nil {
			openAIError(c, http.StatusBadRequest, "invalid_reference_image", fmt.Sprintf("读取 %q 失败:%s", fh.Filename, err.Error()))
			return
		}
		if len(data) == 0 {
			openAIError(c, http.StatusBadRequest, "invalid_reference_image", fmt.Sprintf("参考图 %q 为空", fh.Filename))
			return
		}
		if len(data) > maxReferenceImageBytes {
			openAIError(c, http.StatusBadRequest, "invalid_reference_image", fmt.Sprintf("参考图 %q 超过 %dMB 上限", fh.Filename, maxReferenceImageBytes/1024/1024))
			return
		}
		refs = append(refs, image.ReferenceImage{Data: data, FileName: filepath.Base(fh.Filename)})
	}

	refID := uuid.NewString()
	rec := &usage.Log{RequestID: refID, Type: usage.TypeImage, IP: c.ClientIP(), UA: c.Request.UserAgent()}
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

	m, err := h.Models.BySlug(c.Request.Context(), model)
	if err != nil || m == nil || !m.Enabled {
		fail("model_not_found")
		openAIError(c, http.StatusBadRequest, "model_not_found", fmt.Sprintf("模型 %q 不存在或未启用", model))
		return
	}
	if m.Type != modelpkg.TypeImage {
		fail("model_type_mismatch")
		openAIError(c, http.StatusBadRequest, "model_type_mismatch", fmt.Sprintf("模型 %q 不是图像模型", model))
		return
	}
	rec.ModelID = m.ID

	taskID := image.GenerateTaskID()
	if h.DAO != nil {
		_ = h.DAO.Create(c.Request.Context(), &image.Task{
			TaskID: taskID, ModelID: m.ID, Prompt: prompt,
			N: n, Size: size, Status: image.StatusDispatched,
		})
	}

	runCtx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Minute)
	defer cancel()
	res := h.Runner.Run(runCtx, image.RunOptions{
		TaskID:        taskID,
		ModelID:       m.ID,
		UpstreamModel: m.UpstreamModelSlug,
		Prompt:        maybeAppendClaritySuffix(prompt),
		N:             n,
		MaxAttempts:   1,
		References:    refs,
	})
	rec.AccountID = res.AccountID
	if res.Status != image.StatusSuccess {
		fail(ifEmpty(res.ErrorCode, "upstream_error"))
		httpStatus := http.StatusBadGateway
		if res.ErrorCode == image.ErrNoAccount || res.ErrorCode == image.ErrRateLimited {
			httpStatus = http.StatusServiceUnavailable
		}
		if res.ErrorCode == image.ErrContentPolicy {
			httpStatus = http.StatusBadRequest
		}
		openAIError(c, httpStatus, ifEmpty(res.ErrorCode, "upstream_error"), localizeImageErr(res.ErrorCode, res.ErrorMessage))
		return
	}

	rec.Status = usage.StatusSuccess
	rec.ImageCount = len(res.SignedURLs)
	c.JSON(http.StatusOK, imageResponse(taskID, res, RequestBaseURL(c)))
}

func imageResponse(taskID string, res *image.RunResult, baseURL string) ImageGenResponse {
	out := ImageGenResponse{Created: time.Now().Unix(), TaskID: taskID, IsPreview: res.IsPreview, Data: make([]ImageGenData, 0, len(res.SignedURLs))}
	for i := range res.SignedURLs {
		d := ImageGenData{URL: BuildImageProxyURL(taskID, i, ImageProxyTTL, baseURL)}
		if i < len(res.FileIDs) {
			d.FileID = strings.TrimPrefix(res.FileIDs[i], "sed:")
		}
		out.Data = append(out.Data, d)
	}
	return out
}

func ifEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func localizeImageErr(code, raw string) string {
	var zh string
	switch code {
	case image.ErrNoAccount:
		zh = "账号池暂无可用账号,请稍后重试"
	case image.ErrRateLimited:
		zh = "上游风控,请稍后再试"
	case image.ErrPreviewOnly:
		zh = "上游仅返回预览,请稍后重试"
	case image.ErrContentPolicy:
		// 直接透传上游拒绝原文
		if raw != "" {
			return raw
		}
		zh = "内容策略限制,无法生成该图片"
	case image.ErrUnknown, "":
		zh = "图片生成失败"
	case "upstream_error":
		zh = "上游返回错误"
	default:
		zh = "图片生成失败(" + code + ")"
	}
	if raw != "" && raw != code {
		return zh + ":" + raw
	}
	return zh
}

func nullableUnix(t *time.Time) int64 {
	if t == nil || t.IsZero() {
		return 0
	}
	return t.Unix()
}

var textHintKeywords = []string{
	"文字", "对话", "台词", "旁白", "标语", "字幕", "标题", "文案",
	"招牌", "横幅", "海报文字", "弹幕", "气泡", "字体",
	"text:", "caption", "subtitle", "title:", "label", "banner", "poster text",
}

const claritySuffix = "\n\nclean readable Chinese text, prioritize text clarity over image details"

func collectEditFiles(form *multipart.Form) ([]*multipart.FileHeader, error) {
	if form == nil {
		return nil, errors.New("empty multipart form")
	}
	var out []*multipart.FileHeader
	seen := map[string]bool{}
	add := func(fhs []*multipart.FileHeader) {
		for _, fh := range fhs {
			if fh == nil {
				continue
			}
			key := fh.Filename + "|" + fmt.Sprint(fh.Size)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, fh)
		}
	}
	for _, key := range []string{"image", "image[]", "images", "images[]", "mask"} {
		if fhs := form.File[key]; len(fhs) > 0 {
			add(fhs)
		}
	}
	for k, fhs := range form.File {
		if strings.HasPrefix(k, "image_") {
			add(fhs)
		}
	}
	return out, nil
}

func readMultipart(fh *multipart.FileHeader) ([]byte, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func decodeReferenceStrings(ctx context.Context, inputs []string) ([]image.ReferenceImage, error) {
	refs := make([]ReferenceInput, 0, len(inputs))
	for _, s := range inputs {
		refs = append(refs, ReferenceInput{Value: s})
	}
	return decodeReferenceInputs(ctx, refs)
}

func decodeReferenceInputs(ctx context.Context, inputs []ReferenceInput) ([]image.ReferenceImage, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	if len(inputs) > maxReferenceImages {
		return nil, fmt.Errorf("最多支持 %d 张参考图", maxReferenceImages)
	}
	out := make([]image.ReferenceImage, 0, len(inputs))
	for i, in := range inputs {
		in.Value = strings.TrimSpace(in.Value)
		in.Name = strings.TrimSpace(in.Name)
		if in.Value == "" {
			return nil, fmt.Errorf("第 %d 张参考图为空", i+1)
		}
		data, name, err := fetchReferenceBytes(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("第 %d 张参考图:%w", i+1, err)
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("第 %d 张参考图解码后为空", i+1)
		}
		if len(data) > maxReferenceImageBytes {
			return nil, fmt.Errorf("第 %d 张参考图超过 %dMB 上限", i+1, maxReferenceImageBytes/1024/1024)
		}
		_, ext := sniffReferenceMime(data)
		if name == "" {
			name = fmt.Sprintf("reference-%d%s", i+1, ext)
		} else if filepath.Ext(name) == "" && ext != "" {
			name += ext
		}
		out = append(out, image.ReferenceImage{Data: data, FileName: name})
	}
	return out, nil
}

func fetchReferenceBytes(ctx context.Context, in ReferenceInput) ([]byte, string, error) {
	s := strings.TrimSpace(in.Value)
	name := sanitizeReferenceFileName(in.Name)
	low := strings.ToLower(s)
	switch {
	case strings.HasPrefix(low, "data:"):
		comma := strings.IndexByte(s, ',')
		if comma < 0 {
			return nil, "", errors.New("无效 data URL")
		}
		meta := s[5:comma]
		payload := strings.TrimSpace(s[comma+1:])
		if name == "" {
			name = dataURLFileName(meta)
		}
		if strings.Contains(strings.ToLower(meta), ";base64") {
			b, err := decodeBase64Flexible(payload)
			if err != nil {
				if unescaped, uerr := url.PathUnescape(payload); uerr == nil && unescaped != payload {
					if b2, err2 := decodeBase64Flexible(unescaped); err2 == nil {
						return b2, name, nil
					}
				}
				return nil, "", fmt.Errorf("base64 解码失败:%w", err)
			}
			return b, name, nil
		}
		if unescaped, err := url.PathUnescape(payload); err == nil {
			payload = unescaped
		}
		return []byte(payload), name, nil
	case strings.HasPrefix(low, "http://"), strings.HasPrefix(low, "https://"):
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s, nil)
		if err != nil {
			return nil, "", err
		}
		req.Header.Set("User-Agent", chatgpt.DefaultUserAgent)
		req.Header.Set("Accept", "image/*,*/*;q=0.8")
		hc := &http.Client{Timeout: 30 * time.Second}
		res, err := hc.Do(req)
		if err != nil {
			return nil, "", err
		}
		defer res.Body.Close()
		if res.StatusCode >= 400 {
			return nil, "", fmt.Errorf("下载失败 HTTP %d", res.StatusCode)
		}
		if res.ContentLength > int64(maxReferenceImageBytes) {
			return nil, "", fmt.Errorf("远程图片超过 %dMB 上限", maxReferenceImageBytes/1024/1024)
		}
		body, err := io.ReadAll(io.LimitReader(res.Body, int64(maxReferenceImageBytes)+1))
		if err != nil {
			return nil, "", err
		}
		if name == "" {
			name = sanitizeReferenceFileName(filepath.Base(req.URL.Path))
		}
		if name == "" {
			name = "reference" + extensionFromContentType(res.Header.Get("Content-Type"))
		} else if filepath.Ext(name) == "" {
			name += extensionFromContentType(res.Header.Get("Content-Type"))
		}
		return body, name, nil
	default:
		b, err := decodeBase64Flexible(s)
		if err != nil {
			return nil, "", fmt.Errorf("既非 URL 也非可解析的 base64:%w", err)
		}
		return b, name, nil
	}
}

func firstJSONStr(obj map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		if v := referenceValueFromRaw(obj[key]); v != "" {
			return sanitizeReferenceFileName(v)
		}
	}
	return ""
}

func referenceValueFromRaw(raw json.RawMessage) string {
	if len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	for _, key := range []string{"url", "data_url", "image_url", "input_image", "source", "data", "b64_json", "base64"} {
		if v := referenceValueFromRaw(obj[key]); v != "" {
			return v
		}
	}
	return ""
}

func decodeBase64Flexible(s string) ([]byte, error) {
	clean := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\n', '\r', '\t':
			return -1
		default:
			return r
		}
	}, s)
	encs := []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding}
	var last error
	for _, enc := range encs {
		b, err := enc.DecodeString(clean)
		if err == nil {
			return b, nil
		}
		last = err
	}
	return nil, last
}

func sanitizeReferenceFileName(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if name == "" {
		return ""
	}
	name = filepath.Base(name)
	if name == "." || name == "/" || name == ".." {
		return ""
	}
	name = strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n', '\t', '/', '\\':
			return '-'
		default:
			return r
		}
	}, name)
	if len(name) > 120 {
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		if len(ext) > 16 {
			ext = ""
		}
		maxBase := 120 - len(ext)
		if maxBase < 32 {
			maxBase = 32
		}
		if len(base) > maxBase {
			base = base[:maxBase]
		}
		name = base + ext
	}
	return name
}

func dataURLFileName(meta string) string {
	mime := strings.TrimSpace(strings.Split(meta, ";")[0])
	if mime == "" {
		return ""
	}
	return "reference" + extensionFromMime(mime)
}

func extensionFromContentType(ct string) string {
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = ct[:i]
	}
	return extensionFromMime(strings.TrimSpace(ct))
}

func extensionFromMime(mime string) string {
	switch strings.ToLower(mime) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/avif":
		return ".avif"
	default:
		return ""
	}
}

func sniffReferenceMime(data []byte) (string, string) {
	n := 512
	if len(data) < n {
		n = len(data)
	}
	mime := http.DetectContentType(data[:n])
	return mime, extensionFromContentType(mime)
}

func parseIntClamp(s string, min, max int) (int, error) {
	var v int
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil {
		return 0, err
	}
	if v < min {
		v = min
	}
	if v > max {
		v = max
	}
	return v, nil
}

func maybeAppendClaritySuffix(prompt string) string {
	lower := strings.ToLower(prompt)
	need := false
	for _, kw := range textHintKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			need = true
			break
		}
	}
	if !need {
		for _, pair := range [][2]string{{"\"", "\""}, {"'", "'"}, {"“", "”"}, {"‘", "’"}, {"「", "」"}, {"『", "』"}} {
			if idx := strings.Index(prompt, pair[0]); idx >= 0 {
				rest := prompt[idx+len(pair[0]):]
				if end := strings.Index(rest, pair[1]); end >= 2 {
					need = true
					break
				}
			}
		}
	}
	if need && !strings.Contains(prompt, strings.TrimSpace(claritySuffix)) {
		return prompt + claritySuffix
	}
	return prompt
}
