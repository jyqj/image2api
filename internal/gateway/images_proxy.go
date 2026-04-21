// images_proxy.go —— 图片返回防盗链代理。
//
// 方案:后端生成自家签名 URL:
//
//	GET /p/img/<task_id>/<idx>?exp=<unix_ms>&sig=<hex>
//
// 请求到达时,后端:
//  1. 校验 exp 未过期 + sig 匹配(HMAC-SHA256);
//  2. 用 DAO 按 task_id 查任务,取 result_urls[idx](上游 estuary 永久 URL);
//  3. 直接反代该 URL(简单 HTTP GET 转发,不需要 AT);
//  4. 若 result_urls 为空则 fallback 到旧逻辑(用账号 AT 现取)。
//
// imageProxySecret 从配置的 aes_key 派生,进程重启后签名不变。
package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/432539/gpt2api/internal/config"
	"github.com/432539/gpt2api/internal/upstream/chatgpt"
	"github.com/432539/gpt2api/pkg/logger"
)

// ImageAccountResolver 按账号 ID 解出构造 chatgpt client 所需的敏感字段。
// 由 main.go 注入。接口里不直接依赖 account 包,保持本层解耦。
type ImageAccountResolver interface {
	AuthToken(ctx context.Context, accountID uint64) (at, deviceID, cookies string, err error)
	ProxyURL(ctx context.Context, accountID uint64) string
}

// imageProxySecret 从配置的 aes_key 派生,进程重启后签名保持有效。
var imageProxySecret []byte

// InitImageProxySecret 从 aes_key 派生图片代理签名密钥。由 main.go 在配置加载后调用。
func InitImageProxySecret(aesKeyHex string) {
	h := sha256.Sum256([]byte("image-proxy-secret:" + aesKeyHex))
	imageProxySecret = h[:]
}

// ImageProxyTTL 单条签名 URL 的默认有效期。
// 密钥从 aes_key 派生(重启不变),设 30 天;estuary URL 本身永久有效。
const ImageProxyTTL = 30 * 24 * time.Hour

// BuildImageProxyURL 生成代理 URL(绝对路径)。
//
// 优先级:
//  1. 配置的 app.base_url(非空且不含 localhost);
//  2. 从请求 Host 推导(requestHost 由 handler 从 gin.Context 传入);
//  3. 配置的 app.base_url(含 localhost,仅开发环境);
//  4. 兜底:相对路径。
func BuildImageProxyURL(taskID string, idx int, ttl time.Duration, requestHost ...string) string {
	if ttl <= 0 {
		ttl = ImageProxyTTL
	}
	expMs := time.Now().Add(ttl).UnixMilli()
	sig := computeImgSig(taskID, idx, expMs)
	path := fmt.Sprintf("/p/img/%s/%d?exp=%d&sig=%s", taskID, idx, expMs, sig)

	// 1) 配置的 base_url(非 localhost 优先)
	if cfg := config.Get(); cfg != nil && cfg.App.BaseURL != "" {
		if !strings.Contains(cfg.App.BaseURL, "localhost") && !strings.Contains(cfg.App.BaseURL, "127.0.0.1") {
			return strings.TrimRight(cfg.App.BaseURL, "/") + path
		}
	}
	// 2) 从请求 Host 推导
	if len(requestHost) > 0 && requestHost[0] != "" {
		host := requestHost[0]
		scheme := "http"
		if strings.HasPrefix(host, "https://") || strings.HasPrefix(host, "http://") {
			return strings.TrimRight(host, "/") + path
		}
		return scheme + "://" + host + path
	}
	// 3) localhost base_url 兜底(开发环境)
	if cfg := config.Get(); cfg != nil && cfg.App.BaseURL != "" {
		return strings.TrimRight(cfg.App.BaseURL, "/") + path
	}
	return path
}

// RequestBaseURL 从 gin.Context 提取请求的 scheme + host,用于构建绝对 URL。
func RequestBaseURL(c *gin.Context) string {
	scheme := "http"
	if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := c.Request.Host
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

func computeImgSig(taskID string, idx int, expMs int64) string {
	mac := hmac.New(sha256.New, imageProxySecret)
	fmt.Fprintf(mac, "%s|%d|%d", taskID, idx, expMs)
	return hex.EncodeToString(mac.Sum(nil))[:24]
}

func verifyImgSig(taskID string, idx int, expMs int64, sig string) bool {
	if expMs < time.Now().UnixMilli() {
		return false
	}
	want := computeImgSig(taskID, idx, expMs)
	return hmac.Equal([]byte(sig), []byte(want))
}

// ImageProxy 按签名代理下载上游图片。
// 优先使用 DB 中的 result_urls(estuary 永久 URL)直接反代;
// 若 result_urls 为空则 fallback 到旧逻辑(用账号 AT 现取)。
func (h *ImagesHandler) ImageProxy(c *gin.Context) {
	taskID := c.Param("task_id")
	idxStr := c.Param("idx")
	expStr := c.Query("exp")
	sig := c.Query("sig")

	if taskID == "" || idxStr == "" || expStr == "" || sig == "" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	idx, err := strconv.Atoi(idxStr)
	if err != nil || idx < 0 || idx > 64 {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	expMs, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if !verifyImgSig(taskID, idx, expMs, sig) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	if h.DAO == nil {
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}

	t, err := h.DAO.Get(c.Request.Context(), taskID)
	if err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	// —— 优先路径:result_urls 里有 estuary URL,直接反代,无需 AT ——
	resultURLs := t.DecodeResultURLs()
	if idx < len(resultURLs) && resultURLs[idx] != "" {
		body, ct, err := proxyEstuaryURL(ctx, resultURLs[idx])
		if err == nil {
			if ct == "" {
				ct = "image/png"
			}
			c.Header("Cache-Control", "public, max-age=86400")
			c.Data(http.StatusOK, ct, body)
			return
		}
		logger.L().Warn("image proxy estuary direct failed, fallback to AT",
			zap.Error(err), zap.String("task_id", taskID), zap.String("url", resultURLs[idx]))
	}

	// —— Fallback:用账号 AT 现取 ——
	fids := t.DecodeFileIDs()
	if idx >= len(fids) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	ref := fids[idx]
	if t.AccountID == 0 || h.ImageAccResolver == nil {
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}

	at, deviceID, cookies, err := h.ImageAccResolver.AuthToken(ctx, t.AccountID)
	if err != nil {
		logger.L().Warn("image proxy resolve account",
			zap.Error(err), zap.Uint64("account_id", t.AccountID))
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	proxyURL := h.ImageAccResolver.ProxyURL(ctx, t.AccountID)

	cli, err := chatgpt.New(chatgpt.Options{
		AuthToken: at,
		DeviceID:  deviceID,
		ProxyURL:  proxyURL,
		Cookies:   cookies,
		Timeout:   h.upstreamTimeout(),
	})
	if err != nil {
		logger.L().Warn("image proxy build client", zap.Error(err))
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}

	signedURL, err := cli.ImageDownloadURL(ctx, t.ConversationID, ref)
	if err != nil {
		logger.L().Warn("image proxy download_url",
			zap.Error(err), zap.String("task_id", taskID), zap.String("ref", ref))
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}

	body, ct, err := cli.FetchImage(ctx, signedURL, 16*1024*1024)
	if err != nil {
		logger.L().Warn("image proxy fetch",
			zap.Error(err), zap.String("task_id", taskID))
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	if ct == "" {
		ct = "image/png"
	}
	c.Header("Cache-Control", "public, max-age=86400")
	c.Data(http.StatusOK, ct, body)
}

// proxyEstuaryURL 直接反代 estuary URL,不需要任何认证。
func proxyEstuaryURL(ctx context.Context, estuaryURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, estuaryURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", chatgpt.DefaultUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("estuary returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
	if err != nil {
		return nil, "", err
	}
	ct := resp.Header.Get("Content-Type")
	return body, ct, nil
}
