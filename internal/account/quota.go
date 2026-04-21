package account

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/432539/gpt2api/internal/upstream/chatgpt"
)

// QuotaSettings 热更新参数。
type QuotaSettings interface {
	AccountQuotaProbeEnabled() bool
	AccountQuotaProbeIntervalSec() int
	AccountRefreshConcurrency() int // 复用刷新并发上限
}

// QuotaResult 探测结果。
type QuotaResult struct {
	AccountID             uint64    `json:"account_id"`
	Email                 string    `json:"email"`
	OK                    bool      `json:"ok"`
	Remaining             int       `json:"remaining"`
	Total                 int       `json:"total"`
	ResetAt               time.Time `json:"reset_at,omitempty"`
	DefaultModel          string    `json:"default_model,omitempty"`           // 优先来自 /backend-api/models
	BlockedFeatures       []string  `json:"blocked_features,omitempty"`        // conversation/init 的诊断弱参考
	ImageCapabilityStatus string    `json:"image_capability_status,omitempty"` // enabled/disabled/unknown/error
	ImageCapabilitySource string    `json:"image_capability_source,omitempty"` // models/init
	ImageCapabilityDetail string    `json:"image_capability_detail,omitempty"`
	Error                 string    `json:"error,omitempty"`
}

// QuotaProber 后台定期探测账号图片剩余额度。
type QuotaProber struct {
	svc      *Service
	settings QuotaSettings
	log      *zap.Logger

	proxyResolver AccountProxyResolver

	kick chan struct{}
}

func NewQuotaProber(svc *Service, settings QuotaSettings, logger *zap.Logger) *QuotaProber {
	return &QuotaProber{
		svc:      svc,
		settings: settings,
		log:      logger,
		kick:     make(chan struct{}, 1),
	}
}

// SetProxyResolver 注入账号代理解析器;未注入则直连。
func (q *QuotaProber) SetProxyResolver(pr AccountProxyResolver) { q.proxyResolver = pr }

func (q *QuotaProber) Kick() {
	select {
	case q.kick <- struct{}{}:
	default:
	}
}

// Run 后台循环。
func (q *QuotaProber) Run(ctx context.Context) {
	q.log.Info("account quota prober started")
	defer q.log.Info("account quota prober stopped")

	select {
	case <-ctx.Done():
		return
	case <-time.After(10 * time.Second):
	}

	for {
		// 最小扫描间隔 60s;实际复用探测最小间隔的 1/3 做节奏,至少 60s
		interval := time.Duration(q.settings.AccountQuotaProbeIntervalSec()/3) * time.Second
		if interval < 60*time.Second {
			interval = 60 * time.Second
		}

		if q.settings.AccountQuotaProbeEnabled() {
			q.scanOnce(ctx)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		case <-q.kick:
		}
	}
}

func (q *QuotaProber) scanOnce(ctx context.Context) {
	minInterval := q.settings.AccountQuotaProbeIntervalSec()
	conc := q.settings.AccountRefreshConcurrency()

	rows, err := q.svc.dao.ListNeedProbeQuota(ctx, minInterval, 256)
	if err != nil {
		q.log.Warn("list quota probe candidates failed", zap.Error(err))
		return
	}
	if len(rows) == 0 {
		return
	}

	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for _, a := range rows {
		a := a
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			_, _ = q.ProbeOne(ctx, a)
		}()
	}
	wg.Wait()
}

// ProbeByID 指定账号探测。
func (q *QuotaProber) ProbeByID(ctx context.Context, id uint64) (*QuotaResult, error) {
	a, err := q.svc.dao.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return q.ProbeOne(ctx, a)
}

// ProbeOne 执行一次探测。
// models-first 判断 image 入口资格,conversation/init 只作为 quota/reset 诊断弱参考。
func (q *QuotaProber) ProbeOne(ctx context.Context, a *Account) (*QuotaResult, error) {
	res := &QuotaResult{AccountID: a.ID, Email: a.Email}
	at, err := q.svc.cipher.DecryptString(a.AuthTokenEnc)
	if err != nil || at == "" {
		res.Error = "AT 解密失败"
		_ = q.svc.dao.ApplyQuotaResult(ctx, a.ID, -1, -1, nil)
		return res, errors.New(res.Error)
	}

	probe, probeErr := q.doProbe(ctx, a, at)
	if probe.capabilitySource != "" {
		_ = q.svc.dao.ApplyImageCapabilityResult(ctx, a.ID, probe.capabilityStatus,
			probe.defaultModel, probe.capabilitySource, probe.capabilityDetail, probe.blockedFeatures)
	}
	res.DefaultModel = probe.defaultModel
	res.BlockedFeatures = probe.blockedFeatures
	res.ImageCapabilityStatus = probe.capabilityStatus
	res.ImageCapabilitySource = probe.capabilitySource
	res.ImageCapabilityDetail = probe.capabilityDetail
	if probeErr != nil {
		res.Error = friendlyProbeErr(probeErr)
		_ = q.svc.dao.ApplyQuotaResult(ctx, a.ID, -1, -1, nil)
		return res, probeErr
	}

	if probe.quotaProbed {
		var resetPtr *time.Time
		if !probe.resetAt.IsZero() {
			resetPtr = &probe.resetAt
		}
		if err := q.svc.dao.ApplyQuotaResult(ctx, a.ID, probe.remaining, probe.total, resetPtr); err != nil {
			res.Error = "写库失败:" + err.Error()
			return res, err
		}
	}
	res.OK = true
	res.Remaining = probe.remaining
	res.Total = probe.total
	res.ResetAt = probe.resetAt
	return res, nil
}

type probeOutcome struct {
	remaining        int
	total            int
	resetAt          time.Time
	quotaProbed      bool
	defaultModel     string
	blockedFeatures  []string
	capabilityStatus string
	capabilitySource string
	capabilityDetail string
}

type initProbeOutcome struct {
	remaining       int
	total           int
	resetAt         time.Time
	defaultModel    string
	blockedFeatures []string
}

type modelsProbeOutcome struct {
	defaultModel       string
	modelPickerVersion string
	imageEnabled       bool
	imageModels        []string
	fConversationEPs   []string
}

const defaultProbeTimezoneOffsetMin = -480

// upstreamClientFor 构造与真实 f/conversation 链路一致的 ChatGPT client。
// /backend-api/models 已经是 image 能力主探针,因此探针不能再使用弱化的
// 标准 net/http 指纹;这里统一复用 uTLS transport、commonHeaders、Oai-*、
// 稳定 device/session、代理与 cookie jar。
func (q *QuotaProber) upstreamClientFor(ctx context.Context, a *Account, accessToken string) (*chatgpt.Client, error) {
	deviceID := a.OAIDeviceID
	if deviceID == "" {
		gen := uuid.NewString()
		if fixed, err := q.svc.dao.EnsureDeviceID(ctx, a.ID, gen); err == nil && fixed != "" {
			deviceID = fixed
			a.OAIDeviceID = fixed
		} else {
			deviceID = gen
		}
	}

	sessionID := a.OAISessionID
	if sessionID == "" {
		gen := uuid.NewString()
		if fixed, err := q.svc.dao.EnsureSessionID(ctx, a.ID, gen); err == nil && fixed != "" {
			sessionID = fixed
			a.OAISessionID = fixed
		} else {
			sessionID = gen
		}
	}

	var proxyURL string
	if q.proxyResolver != nil {
		proxyURL = q.proxyResolver.ProxyURLForAccount(ctx, a.ID)
	}

	var cookies string
	if enc, err := q.svc.dao.GetCookies(ctx, a.ID); err == nil && enc != "" {
		if dec, derr := q.svc.cipher.DecryptString(enc); derr == nil {
			cookies = dec
		} else if q.log != nil {
			q.log.Warn("decrypt account cookies for quota probe failed",
				zap.Uint64("account_id", a.ID), zap.Error(derr))
		}
	}

	return chatgpt.New(chatgpt.Options{
		AuthToken: accessToken,
		DeviceID:  deviceID,
		SessionID: sessionID,
		ProxyURL:  proxyURL,
		Cookies:   cookies,
		Timeout:   30 * time.Second,
	})
}

// doProbe 组合探测账号 image 状态。
//
// 实测结论: /backend-api/conversation/init 已经不能再作为 image 能力主判据。
// 同一个 Pro/image 灰度号可能在 /backend-api/models 暴露 image_gen_tool_enabled,
// 但 init 仍返回 blocked_features=image_gen / default_model_slug=auto。
//
// 因此这里分层处理:
//  1. /backend-api/models:主能力探针,判断账号是否具备 image 入口资格;
//  2. /backend-api/conversation/init:仅保留 quota / reset / blocked_features 诊断弱参考;
//  3. 真实 IMG2 是否命中仍由 Runner 的 SSE/poll/tool payload 画像决定。
func (q *QuotaProber) doProbe(ctx context.Context, a *Account, accessToken string) (out probeOutcome, err error) {
	out.remaining = -1
	out.total = -1
	out.capabilityStatus = "unknown"

	cli, clientErr := q.upstreamClientFor(ctx, a, accessToken)
	if clientErr != nil {
		out.capabilityStatus = "error"
		out.capabilitySource = "models"
		out.capabilityDetail = truncate(clientErr.Error(), 500)
		return out, clientErr
	}

	models, modelsErr := q.doModelsProbe(ctx, cli)
	if modelsErr == nil {
		out.defaultModel = models.defaultModel
		out.capabilitySource = "models"
		if models.imageEnabled {
			out.capabilityStatus = "enabled"
		} else {
			out.capabilityStatus = "disabled"
		}
		out.capabilityDetail = models.detailJSON()
	} else {
		out.capabilitySource = "models"
		out.capabilityStatus = "error"
		out.capabilityDetail = truncate(modelsErr.Error(), 500)
	}

	initOut, initErr := q.doInitProbe(ctx, cli)
	if initErr == nil {
		out.remaining = initOut.remaining
		out.total = initOut.total
		out.resetAt = initOut.resetAt
		out.quotaProbed = true
		out.blockedFeatures = initOut.blockedFeatures
		if out.defaultModel == "" {
			out.defaultModel = initOut.defaultModel
		}
	} else if modelsErr != nil {
		// 两个探针都失败才把这轮标为失败;否则以 models 为主继续写入能力画像。
		return out, fmt.Errorf("models probe: %v; init probe: %v", modelsErr, initErr)
	}

	return out, nil
}

func (m modelsProbeOutcome) detailJSON() string {
	payload := map[string]interface{}{
		"default_model_slug":       m.defaultModel,
		"model_picker_version":     m.modelPickerVersion,
		"image_enabled":            m.imageEnabled,
		"image_models":             m.imageModels,
		"f_conversation_endpoints": m.fConversationEPs,
	}
	buf, _ := json.Marshal(payload)
	return string(buf)
}

// doModelsProbe 调 /backend-api/models,这是当前 image 能力主探针。
// 只要某个模型 enabledTools 含 image_gen_tool_enabled,或 supportedFeatures 含 image,
// 就认为账号具有 image 入口资格。是否抽中 IMG2 终稿由 Runner 另行画像。
func (q *QuotaProber) doModelsProbe(ctx context.Context, cli *chatgpt.Client) (out modelsProbeOutcome, err error) {
	data, err := cli.ModelsRaw(ctx)
	if err != nil {
		return out, err
	}

	var payload map[string]interface{}
	if err = json.Unmarshal(data, &payload); err != nil {
		return out, err
	}
	out.defaultModel = firstString(payload, "default_model_slug", "defaultModelSlug", "default_model", "defaultModel")
	out.modelPickerVersion = firstString(payload, "model_picker_version", "modelPickerVersion")

	modelsRaw := modelListFromAny(payload["models"])
	seenModel := map[string]struct{}{}
	seenEP := map[string]struct{}{}
	for _, mm := range modelsRaw {
		if mm == nil {
			continue
		}
		slug := firstString(mm, "slug", "id", "model", "model_slug", "modelSlug")
		enabledTools := stringSliceFromAny(firstAny(mm, "enabledTools", "enabled_tools", "enabled_tools_override"))
		supportedFeatures := stringSliceFromAny(firstAny(mm, "supportedFeatures", "supported_features", "features"))
		fep := firstString(mm, "fConversationEndpoint", "f_conversation_endpoint")
		if fep != "" {
			if _, ok := seenEP[fep]; !ok {
				seenEP[fep] = struct{}{}
				out.fConversationEPs = append(out.fConversationEPs, fep)
			}
		}
		if hasImageToolOrFeature(enabledTools, supportedFeatures) {
			out.imageEnabled = true
			if slug != "" {
				if _, ok := seenModel[slug]; !ok {
					seenModel[slug] = struct{}{}
					out.imageModels = append(out.imageModels, slug)
				}
			}
		}
	}
	return out, nil
}

// doInitProbe 调 /backend-api/conversation/init。
//
// 这个接口只用于 quota/reset 诊断;不要再用 blocked_features 决定 image2 能力。
func (q *QuotaProber) doInitProbe(ctx context.Context, cli *chatgpt.Client) (out initProbeOutcome, err error) {
	out.remaining = -1
	out.total = -1

	// timezone_offset_min 只对齐前端抓包值;不要把它解释成服务端时区语义。
	// 后续如果要做区域画像,可以把这个值提升到配置或从浏览器登录态同步。
	data, err := cli.ConversationInitRaw(ctx, defaultProbeTimezoneOffsetMin, []string{"picture_v2"})
	if err != nil {
		return out, err
	}

	var payload struct {
		Type             string   `json:"type"`
		BlockedFeatures  []string `json:"blocked_features"`
		DefaultModelSlug string   `json:"default_model_slug"`
		LimitsProgress   []struct {
			FeatureName string `json:"feature_name"`
			Remaining   *int   `json:"remaining"`
			ResetAfter  string `json:"reset_after"`
		} `json:"limits_progress"`
	}
	if err = json.Unmarshal(data, &payload); err != nil {
		return out, err
	}
	out.defaultModel = payload.DefaultModelSlug
	out.blockedFeatures = payload.BlockedFeatures

	for _, item := range payload.LimitsProgress {
		if !isImageFeature(item.FeatureName) {
			continue
		}
		if item.Remaining != nil {
			if out.remaining < 0 || *item.Remaining < out.remaining {
				out.remaining = *item.Remaining
			}
		}
		if item.ResetAfter != "" {
			if t, e := time.Parse(time.RFC3339, item.ResetAfter); e == nil {
				if out.resetAt.IsZero() || t.Before(out.resetAt) {
					out.resetAt = t
				}
			}
		}
	}
	return out, nil
}

func modelListFromAny(v interface{}) []map[string]interface{} {
	switch xs := v.(type) {
	case []interface{}:
		out := make([]map[string]interface{}, 0, len(xs))
		for _, x := range xs {
			if m, ok := x.(map[string]interface{}); ok {
				out = append(out, m)
			}
		}
		return out
	case map[string]interface{}:
		out := make([]map[string]interface{}, 0, len(xs))
		for slug, raw := range xs {
			if m, ok := raw.(map[string]interface{}); ok {
				if _, exists := m["slug"]; !exists {
					m["slug"] = slug
				}
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

func firstAny(m map[string]interface{}, keys ...string) interface{} {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return nil
}

func firstString(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch x := v.(type) {
			case string:
				return x
			case float64:
				return fmt.Sprintf("%.0f", x)
			}
		}
	}
	return ""
}

func stringSliceFromAny(v interface{}) []string {
	switch xs := v.(type) {
	case []string:
		return xs
	case []interface{}:
		out := make([]string, 0, len(xs))
		for _, x := range xs {
			switch y := x.(type) {
			case string:
				if y != "" {
					out = append(out, y)
				}
			case map[string]interface{}:
				if s := firstString(y, "name", "slug", "id", "feature", "tool"); s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	case map[string]interface{}:
		out := make([]string, 0, len(xs))
		for k, v := range xs {
			if b, ok := v.(bool); ok && b {
				out = append(out, k)
			}
		}
		return out
	default:
		return nil
	}
}

func hasImageToolOrFeature(enabledTools, supportedFeatures []string) bool {
	for _, s := range enabledTools {
		low := strings.ToLower(s)
		if low == "image_gen_tool_enabled" || strings.Contains(low, "image_gen") || strings.Contains(low, "image") {
			return true
		}
	}
	for _, s := range supportedFeatures {
		low := strings.ToLower(s)
		if low == "image" || strings.Contains(low, "image") {
			return true
		}
	}
	return false
}

func isImageFeature(name string) bool {
	n := strings.ToLower(name)
	switch n {
	case "image_gen", "image_generation", "image_edit", "img_gen":
		return true
	}
	return strings.Contains(n, "image_gen") || strings.Contains(n, "img_gen")
}

func friendlyProbeErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "http=401"):
		return "AT 已过期,无法探测额度"
	case strings.Contains(low, "http=403"):
		return "上游拒绝访问(403)"
	case strings.Contains(low, "http=429"):
		return "上游速率限制(429)"
	case strings.Contains(low, "timeout"), strings.Contains(low, "deadline exceeded"):
		return "探测超时"
	case strings.Contains(low, "connection refused"), strings.Contains(low, "no such host"):
		return "网络不通"
	default:
		if len(s) > 160 {
			s = s[:160] + "…"
		}
		return "探测失败:" + s
	}
}
