// files.go —— chatgpt.com 文件上传协议,图生图/图像编辑的前置步骤。
//
// 三步协议(对齐 chatgpt.com 浏览器真实抓包):
//
//  1. POST /backend-api/files
//     body: {file_name, file_size, use_case: "multimodal"}
//     resp: {file_id, upload_url, status: "success"}
//
//  2. PUT <upload_url>                 (Azure Blob SAS URL)
//     headers: Content-Type / x-ms-blob-type: BlockBlob / x-ms-version: 2020-04-08 / Origin
//     body: 原始字节
//
//  3. POST /backend-api/files/{file_id}/uploaded
//     body: {}
//     resp: {status: "success", download_url, ...}
//
// 上传完成后,在 f/conversation.messages 里:
//   - content 从 text 变 multimodal_text;parts 前面加上
//     {"asset_pointer": "file-service://<file_id>", "height":.., "width":.., "size_bytes":..}
//   - metadata.attachments 加一项 {id, mimeType, name, size, height?, width?}
//
// 注意:upload_url 指向 Azure,不要走同一个 chatgpt.com 代理/utls transport,
// 这里用单独的一个 http.Client(沿用 Client 内部的 Transport 走代理,但不带 Auth/Oai-* 头)。

package chatgpt

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // register decoders
	_ "image/jpeg" //
	_ "image/png"  //
	"io"
	"net/http"
	"strings"
	"time"
)

// UploadedFile 是三步上传后沉淀的"可 attach 给 messages"的元数据。
// 字段命名对齐 chatgpt.com 的 attachment payload,序列化时直接当 map 用。
type UploadedFile struct {
	FileID      string `json:"file_id"`
	FileName    string `json:"file_name"`
	FileSize    int    `json:"file_size"`
	MimeType    string `json:"mime_type"`
	UseCase     string `json:"use_case"`         // 图片: multimodal, 文件: my_files
	Width       int    `json:"width,omitempty"`  // 仅图片
	Height      int    `json:"height,omitempty"` // 仅图片
	DownloadURL string `json:"download_url"`     // POST /uploaded 返回,通常不直接用
}

// UploadFile 执行完整三步上传。调用方传入原始字节 + 建议的文件名即可。
// 识别到 image/* 时会尝试 Decode 拿到宽高(Decode 失败不致命,按 0 处理)。
//
// 实践经验:步骤 1、3 走 chatgpt.com(uTLS / 代理 / auth 头),步骤 2 走 Azure,
// 用同一条 http.Client 但是请求头手动裁剪;Azure 的 SAS URL 本身带鉴权。
func (c *Client) UploadFile(ctx context.Context, data []byte, fileName string) (*UploadedFile, error) {
	if len(data) == 0 {
		return nil, errors.New("empty file data")
	}
	mime, ext := sniffMime(data)
	useCase := "multimodal"
	if !strings.HasPrefix(mime, "image/") {
		useCase = "my_files"
	}
	if fileName == "" {
		fileName = fmt.Sprintf("file-%d%s", len(data), ext)
	}

	out := &UploadedFile{
		FileName: fileName,
		FileSize: len(data),
		MimeType: mime,
		UseCase:  useCase,
	}
	if strings.HasPrefix(mime, "image/") {
		if img, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
			out.Width = img.Width
			out.Height = img.Height
		} else if mime == "image/webp" {
			out.Width, out.Height = webpDimensions(data)
		}
	}

	// ---- Step 1: POST /backend-api/files ----
	step1Body := map[string]interface{}{
		"file_name": fileName,
		"file_size": len(data),
		"use_case":  useCase,
	}
	if out.Width > 0 && out.Height > 0 {
		step1Body["height"] = out.Height
		step1Body["width"] = out.Width
	}
	b1, _ := json.Marshal(step1Body)
	req1, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.opts.BaseURL+"/backend-api/files",
		bytes.NewReader(b1))
	if err != nil {
		return nil, err
	}
	c.commonHeaders(req1)
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("Accept", "application/json")
	res1, err := c.hc.Do(req1)
	if err != nil {
		return nil, fmt.Errorf("create file: %w", err)
	}
	defer res1.Body.Close()
	buf1, _ := io.ReadAll(res1.Body)
	if res1.StatusCode >= 400 {
		return nil, &UpstreamError{Status: res1.StatusCode, Message: "create file failed", Body: string(buf1)}
	}
	var step1Resp struct {
		FileID    string `json:"file_id"`
		UploadURL string `json:"upload_url"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(buf1, &step1Resp); err != nil {
		return nil, fmt.Errorf("decode create-file resp: %w (body=%s)", err, truncateStr(string(buf1), 200))
	}
	if step1Resp.FileID == "" || step1Resp.UploadURL == "" {
		return nil, fmt.Errorf("create-file empty: %s", truncateStr(string(buf1), 200))
	}
	out.FileID = step1Resp.FileID

	// chatgpt 浏览器行为:step1 和 step2 之间会 sleep 一小会儿,避免 Azure 那边
	// 还没完成 SAS 分发。参考实现是 1s,这里保守点给 500ms。
	select {
	case <-time.After(500 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// ---- Step 2: PUT upload_url (Azure Blob) ----
	req2, err := http.NewRequestWithContext(ctx, http.MethodPut, step1Resp.UploadURL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req2.Header.Set("Content-Type", mime)
	req2.Header.Set("x-ms-blob-type", "BlockBlob")
	req2.Header.Set("x-ms-version", "2020-04-08")
	req2.Header.Set("Origin", c.opts.BaseURL)
	req2.Header.Set("User-Agent", c.opts.UserAgent)
	req2.Header.Set("Accept", "application/json, text/plain, */*")
	req2.Header.Set("Accept-Language", "en-US,en;q=0.8")
	req2.Header.Set("Referer", c.opts.BaseURL+"/")
	res2, err := c.hc.Do(req2)
	if err != nil {
		return nil, fmt.Errorf("upload PUT: %w", err)
	}
	defer res2.Body.Close()
	if res2.StatusCode >= 400 {
		buf2, _ := io.ReadAll(res2.Body)
		return nil, &UpstreamError{Status: res2.StatusCode, Message: "upload PUT failed", Body: string(buf2)}
	}
	_, _ = io.Copy(io.Discard, res2.Body)

	// ---- Step 3: POST /backend-api/files/{file_id}/uploaded ----
	req3, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.opts.BaseURL+"/backend-api/files/"+step1Resp.FileID+"/uploaded",
		strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	c.commonHeaders(req3)
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set("Accept", "application/json")
	res3, err := c.hc.Do(req3)
	if err != nil {
		return nil, fmt.Errorf("register uploaded: %w", err)
	}
	defer res3.Body.Close()
	buf3, _ := io.ReadAll(res3.Body)
	if res3.StatusCode >= 400 {
		return nil, &UpstreamError{Status: res3.StatusCode, Message: "register uploaded failed", Body: string(buf3)}
	}
	var step3Resp struct {
		Status      string `json:"status"`
		DownloadURL string `json:"download_url"`
	}
	_ = json.Unmarshal(buf3, &step3Resp)
	out.DownloadURL = step3Resp.DownloadURL

	return out, nil
}

// Attachment 是 messages[*].metadata.attachments[*] 的序列化对象。
type Attachment struct {
	ID       string `json:"id"`
	MimeType string `json:"mimeType"`
	Name     string `json:"name"`
	Size     int    `json:"size"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
}

// ToAttachment 把一个已上传的 file 转成 messages.metadata.attachments 里的条目。
func (u *UploadedFile) ToAttachment() Attachment {
	a := Attachment{ID: u.FileID, MimeType: u.MimeType, Name: u.FileName, Size: u.FileSize}
	if u.UseCase == "multimodal" {
		a.Width = u.Width
		a.Height = u.Height
	}
	return a
}

// AssetPointerPart 是 messages[*].content.parts 里的一项(图片),
// 用于把 file-service:// 挂到多模态消息最前面。
type AssetPointerPart struct {
	ContentType  string `json:"content_type,omitempty"` // "image_asset_pointer"
	AssetPointer string `json:"asset_pointer"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	SizeBytes    int    `json:"size_bytes,omitempty"`
}

// ToAssetPointerPart 返回 multimodal_text.parts 里 insert 在 prompt 前的那一项。
func (u *UploadedFile) ToAssetPointerPart() AssetPointerPart {
	return AssetPointerPart{
		ContentType:  "image_asset_pointer",
		AssetPointer: "file-service://" + u.FileID,
		Width:        u.Width,
		Height:       u.Height,
		SizeBytes:    u.FileSize,
	}
}

// sniffMime 用前 512 字节识别 mime 和推荐扩展名。
// net/http 的 DetectContentType 已足够覆盖 png/jpg/gif/webp 的主流场景。
func sniffMime(data []byte) (mime, ext string) {
	n := 512
	if len(data) < n {
		n = len(data)
	}
	mime = http.DetectContentType(data[:n])
	// DetectContentType 可能附带 charset,去掉
	if i := strings.Index(mime, ";"); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	switch {
	case strings.EqualFold(mime, "image/jpeg"):
		ext = ".jpg"
	case strings.EqualFold(mime, "image/png"):
		ext = ".png"
	case strings.EqualFold(mime, "image/gif"):
		ext = ".gif"
	case strings.EqualFold(mime, "image/webp"):
		ext = ".webp"
	case strings.EqualFold(mime, "application/pdf"):
		ext = ".pdf"
	default:
		ext = ""
	}
	return
}

func webpDimensions(data []byte) (int, int) {
	if len(data) < 30 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0
	}
	pos := 12
	for pos+8 <= len(data) {
		fourcc := string(data[pos : pos+4])
		sz := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		chunkStart := pos + 8
		if chunkStart+sz > len(data) {
			return 0, 0
		}
		switch fourcc {
		case "VP8X":
			if sz >= 10 {
				w := 1 + int(data[chunkStart+4]) + int(data[chunkStart+5])<<8 + int(data[chunkStart+6])<<16
				h := 1 + int(data[chunkStart+7]) + int(data[chunkStart+8])<<8 + int(data[chunkStart+9])<<16
				return w, h
			}
		case "VP8L":
			if sz >= 5 && data[chunkStart] == 0x2f {
				b0, b1, b2, b3 := data[chunkStart+1], data[chunkStart+2], data[chunkStart+3], data[chunkStart+4]
				w := 1 + int(b0) + (int(b1&0x3f) << 8)
				h := 1 + int(b1>>6) + (int(b2) << 2) + (int(b3&0x0f) << 10)
				return w, h
			}
		case "VP8 ":
			if sz >= 10 {
				off := chunkStart + 6
				if off+4 <= len(data) {
					w := int(binary.LittleEndian.Uint16(data[off:off+2]) & 0x3fff)
					h := int(binary.LittleEndian.Uint16(data[off+2:off+4]) & 0x3fff)
					return w, h
				}
			}
		}
		pos = chunkStart + sz
		if sz%2 == 1 {
			pos++
		}
	}
	return 0, 0
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
