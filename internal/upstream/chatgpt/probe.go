package chatgpt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ModelsRaw fetches /backend-api/models using the same ChatGPT web client
// fingerprint as f/conversation: uTLS transport, common browser/Oai-* headers,
// stable device/session IDs, proxy, cookie jar, and bearer token.
func (c *Client) ModelsRaw(ctx context.Context) ([]byte, error) {
	// Bootstrap is intentionally best-effort. It lets the cookie jar collect
	// Cloudflare/OpenAI first-party cookies, but a transient home-page failure
	// should not hide the real /models response.
	_ = c.Bootstrap(ctx)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.opts.BaseURL+"/backend-api/models", nil)
	if err != nil {
		return nil, err
	}
	c.commonHeaders(req)
	req.Header.Set("Accept", "application/json")

	res, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	buf, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		return nil, &UpstreamError{Status: res.StatusCode, Message: "models probe failed", Body: string(buf)}
	}
	return buf, nil
}

// ConversationInitRaw fetches /backend-api/conversation/init with the same
// client fingerprint. This endpoint is kept as a quota/diagnostic weak probe;
// it must not be used as the primary image capability decision.
func (c *Client) ConversationInitRaw(ctx context.Context, timezoneOffsetMin int, systemHints []string) ([]byte, error) {
	if systemHints == nil {
		systemHints = []string{"picture_v2"}
	}
	body, _ := json.Marshal(map[string]interface{}{
		"gizmo_id":                nil,
		"requested_default_model": nil,
		"conversation_id":         nil,
		"timezone_offset_min":     timezoneOffsetMin,
		"system_hints":            systemHints,
	})
	req, err := http.NewRequestWithContext(ctx,
		http.MethodPost,
		c.opts.BaseURL+"/backend-api/conversation/init",
		bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.commonHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	res, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	buf, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		return nil, &UpstreamError{Status: res.StatusCode, Message: "conversation/init probe failed", Body: string(buf)}
	}
	return buf, nil
}

// ImageQuotaProbe 轻量级 image quota 预检。
// 调用 /backend-api/conversation/init(system_hints=["picture_v2"]) 检查:
//   - limits_progress 中 image_gen 的 remaining 是否 > 0
//   - blocked_features 是否包含 image_gen
//
// 返回 (hasQuota, blockedReason, error)。
// 如果 init 接口 404(免费号新行为)或返回的 quota 结构缺失,降级为"可能有 quota",
// 不阻断主流程——真正的 quota / 出图状态由 f/conversation 的实际响应决定。
// 这个函数是“尽早跳过明显无额度账号”的优化,不是 image 能力主判据。
func (c *Client) ImageQuotaProbe(ctx context.Context) (hasQuota bool, blockedReason string, err error) {
	// 超时控制:预检不应阻塞太久
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	raw, rawErr := c.ConversationInitRaw(probeCtx, -480, []string{"picture_v2"})
	if rawErr != nil {
		// 404 在免费号上是正常的,不视为无 quota
		if ue, ok := rawErr.(*UpstreamError); ok && ue.Status == 404 {
			return true, "", nil
		}
		return true, "", fmt.Errorf("quota probe: %w", rawErr)
	}

	var resp struct {
		LimitsProgress []struct {
			FeatureName string `json:"feature_name"`
			Remaining   int    `json:"remaining"`
			ResetAfter  string `json:"reset_after"`
		} `json:"limits_progress"`
		BlockedFeatures interface{} `json:"blocked_features"`
	}
	if jsonErr := json.Unmarshal(raw, &resp); jsonErr != nil {
		return true, "", nil // 解析失败,降级放行
	}

	// 检查 blocked_features
	if resp.BlockedFeatures != nil {
		switch bf := resp.BlockedFeatures.(type) {
		case []interface{}:
			for _, v := range bf {
				if s, ok := v.(string); ok && s == "image_gen" {
					return false, "blocked_features:image_gen", nil
				}
			}
		case string:
			if bf == "image_gen" {
				return false, "blocked_features:image_gen", nil
			}
		}
	}

	// 检查 limits_progress
	for _, lp := range resp.LimitsProgress {
		if lp.FeatureName == "image_gen" {
			if lp.Remaining <= 0 {
				return false, fmt.Sprintf("quota_exhausted:remaining=0,reset_after=%s", lp.ResetAfter), nil
			}
			return true, "", nil
		}
	}

	// 没有 image_gen 的 limits_progress 条目——降级放行
	return true, "", nil
}
