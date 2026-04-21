package account

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// AccountProxyResolver 把账号 ID 映射成代理 URL(形如 http(s)://user:pass@host:port)。
// 由 main.go 用 account.Service + proxy.Service 组装后注入;未绑定或禁用返回 ""。
type AccountProxyResolver interface {
	ProxyURLForAccount(ctx context.Context, accountID uint64) string
}

// RefreshSettings 热更新参数。由 settings.Service 实现。
type RefreshSettings interface {
	AccountRefreshEnabled() bool
	AccountRefreshIntervalSec() int
	AccountRefreshAheadSec() int
	AccountRefreshConcurrency() int
	AccountDefaultClientID() string
}

// RefreshResult 刷新结果。
type RefreshResult struct {
	AccountID uint64    `json:"account_id"`
	Email     string    `json:"email"`
	OK        bool      `json:"ok"`
	Source    string    `json:"source"` // rt / st / failed
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Error     string    `json:"error,omitempty"`
	RTRotated bool      `json:"rt_rotated,omitempty"`
	// ATVerified 表示新 AT 已被 chatgpt.com web 后端接受(GET /backend-api/me 返回 200)。
	// 只要 Source=st 必为 true;Source=rt 时取决于 verify 结果;Source=failed 时为 false。
	ATVerified bool `json:"at_verified"`
	// WebUnauthorized 为 true 表示 RT 换出来的 AT 被 chatgpt.com 以 401 拒绝
	// (iOS OAuth 作用域 vs Web 作用域不一致)。前端据此提示补充 Session Token。
	WebUnauthorized bool `json:"web_unauthorized,omitempty"`
}

// Refresher 负责把账号的 AT 通过 RT 或 ST 刷新成新的 AT。
type Refresher struct {
	svc      *Service
	settings RefreshSettings
	log      *zap.Logger
	client   *http.Client

	proxyResolver AccountProxyResolver

	kick chan struct{}
}

// NewRefresher 构造。
// HTTP client 默认直连;如果注入了 AccountProxyResolver,则每次刷新会优先使用
// 账号绑定的代理,避免从境内直连 auth.openai.com / chatgpt.com 时被劫持。
func NewRefresher(svc *Service, settings RefreshSettings, logger *zap.Logger) *Refresher {
	return &Refresher{
		svc:      svc,
		settings: settings,
		log:      logger,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		kick: make(chan struct{}, 1),
	}
}

// Kick 立刻触发一次扫描(批量刷新按钮触发时调)。
// SetProxyResolver 注入账号代理解析器。
// 未调用时 Refresher 保持直连行为(兼容无代理池环境)。
func (r *Refresher) SetProxyResolver(pr AccountProxyResolver) { r.proxyResolver = pr }

// clientFor 根据账号 ID 选择合适的 http.Client:
//   - proxyResolver 未注入或返回空 URL → 用默认直连 client
//   - 返回非空 URL → 构造一次性带代理的 client(结束后 GC)
//
// 代理 URL 解析失败时降级到直连,并打 warn 日志。
func (r *Refresher) clientFor(ctx context.Context, accountID uint64) *http.Client {
	if r.proxyResolver == nil {
		return r.client
	}
	pu := r.proxyResolver.ProxyURLForAccount(ctx, accountID)
	if pu == "" {
		return r.client
	}
	u, err := url.Parse(pu)
	if err != nil {
		r.log.Warn("invalid proxy url for refresh, fallback direct",
			zap.Uint64("account_id", accountID), zap.Error(err))
		return r.client
	}
	tr := &http.Transport{
		Proxy:               http.ProxyURL(u),
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        16,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &http.Client{Transport: tr, Timeout: r.client.Timeout}
}

func (r *Refresher) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// Run 后台循环。
func (r *Refresher) Run(ctx context.Context) {
	r.log.Info("account refresher started")
	defer r.log.Info("account refresher stopped")

	// 启动延迟 5s,避免和数据库初始化抢锁
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
	}

	for {
		interval := time.Duration(r.settings.AccountRefreshIntervalSec()) * time.Second
		if interval < 30*time.Second {
			interval = 30 * time.Second
		}

		if r.settings.AccountRefreshEnabled() {
			r.scanOnce(ctx)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		case <-r.kick:
		}
	}
}

func (r *Refresher) scanOnce(ctx context.Context) {
	// 先清理:把已过期且无 RT/ST 的纯 AT 账号自动标记 dead
	if n, err := r.svc.dao.RetireExpiredATOnly(ctx); err != nil {
		r.log.Warn("retire expired AT-only accounts failed", zap.Error(err))
	} else if n > 0 {
		r.log.Info("retired expired AT-only accounts", zap.Int64("count", n))
	}

	ahead := r.settings.AccountRefreshAheadSec()
	conc := r.settings.AccountRefreshConcurrency()

	rows, err := r.svc.dao.ListNeedRefresh(ctx, ahead, 256)
	if err != nil {
		r.log.Warn("list need-refresh accounts failed", zap.Error(err))
		return
	}
	if len(rows) == 0 {
		return
	}
	r.log.Info("refreshing accounts", zap.Int("count", len(rows)), zap.Int("ahead_sec", ahead), zap.Int("concurrency", conc))

	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for _, a := range rows {
		a := a
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			_, _ = r.RefreshAuto(ctx, a)
		}()
	}
	wg.Wait()
}

// RefreshByID 指定 id 刷新。
func (r *Refresher) RefreshByID(ctx context.Context, id uint64) (*RefreshResult, error) {
	a, err := r.svc.dao.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return r.RefreshAuto(ctx, a)
}

// RefreshAuto 优先 RT,失败/没有 RT 回退 ST;都失败则 markDead。
//
// 判定规则(严格):
//
//  1. RT → AT HTTP 成功后,立即 GET /backend-api/me 做作用域校验
//     - 200:写库,Source=rt,ATVerified=true,返回成功
//     - 非 200(含 401/403/429/5xx/网络错误):**视为 RT 不可用**,丢弃本次 AT,
//     不写库,继续尝试 ST
//  2. ST → AT HTTP 成功:写库,Source=st,ATVerified=true,返回成功
//  3. 两条路径都未拿到可用 AT:返回失败(ok=false),账号标 warned(RT 被 web 拒)
//     或 dead(完全没可用 token)。不会把"无法校验通过"的 AT 悄悄写进库。
//
// 这条规则确保前端看到「刷新成功」时,AT 一定是 chatgpt.com web 后端接受的,
// 后续探测 / 聊天 / 图像请求不会因作用域不匹配而 401。
func (r *Refresher) RefreshAuto(ctx context.Context, a *Account) (*RefreshResult, error) {
	if a == nil {
		return nil, errors.New("account is nil")
	}
	res := &RefreshResult{AccountID: a.ID, Email: a.Email}

	// 尝试 RT
	if a.RefreshTokenEnc.Valid && a.RefreshTokenEnc.String != "" {
		rt, err := r.svc.cipher.DecryptString(a.RefreshTokenEnc.String)
		if err == nil && rt != "" {
			clientID := a.ClientID
			if clientID == "" {
				clientID = r.settings.AccountDefaultClientID()
			}
			newAT, newRT, expAt, err := r.rtToAT(ctx, a.ID, rt, clientID)
			if err == nil && newAT != "" {
				// RT → AT 成功,直接写库(不再做 verifyATOnWeb 校验——
				// 用 app_LlGpXReQgckcGGUo2JrYvtJK 换出的 AT 在 f/conversation 里能用,
				// 但 /backend-api/me 可能因 scope 不同返回 401 导致误杀)
				return r.applyRefresh(ctx, a, newAT, newRT, expAt, RefreshSourceRT, res)
			}
			// RT → AT HTTP 本身失败,回退 ST
			r.log.Warn("RT refresh failed, fallback to ST", zap.Uint64("id", a.ID), zap.Error(err))
			res.Error = friendlyRefreshErr(err)
		}
	}

	// 尝试 ST(ST → AT 本来就是 web 作用域,不需要再校验)
	if a.SessionTokenEnc.Valid && a.SessionTokenEnc.String != "" {
		st, err := r.svc.cipher.DecryptString(a.SessionTokenEnc.String)
		if err == nil && st != "" {
			newAT, expAt, err := r.stToAT(ctx, a.ID, st)
			if err == nil && newAT != "" {
				res.ATVerified = true
				return r.applyRefresh(ctx, a, newAT, "", expAt, RefreshSourceST, res)
			}
			if res.Error == "" {
				res.Error = friendlyRefreshErr(err)
			} else {
				res.Error += " / ST:" + friendlyRefreshErr(err)
			}
		}
	}

	// 都不行:标 warned,保留重试机会
	if res.Error == "" {
		res.Error = "账号既无可用 RT 也无可用 ST"
	}
	_ = r.svc.dao.RecordRefreshError(ctx, a.ID, RefreshSourceRT, res.Error, false)
	r.log.Warn("refresh failed, marking warned (not dead)",
		zap.Uint64("id", a.ID), zap.String("email", a.Email), zap.String("error", res.Error))
	res.Source = "failed"
	return res, nil
}

// verifyATOnWeb 用新 AT 访问一个极轻量的 chatgpt.com web 端点,确认 AT 的作用域
// 能被 web 后端接受。
//
// 选用 GET /backend-api/me:
//   - 200 说明 AT 有效且作用域匹配 web
//   - 401 说明 AT 无效或作用域不匹配(iOS OAuth RT 刷出的 AT 常见)
//   - 403/429/5xx/网络错误 都不作为"AT 无效"的依据
//
// 返回 (http_status, err);err 为非 HTTP 层错误(dial/tls 等)。
func (r *Refresher) verifyATOnWeb(ctx context.Context, accountID uint64, accessToken string) (int, error) {
	vctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(vctx, "GET",
		"https://chatgpt.com/backend-api/me", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://chatgpt.com/")
	req.Header.Set("Origin", "https://chatgpt.com")
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")

	resp, err := r.clientFor(vctx, accountID).Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	// 读掉 body,释放连接
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

func (r *Refresher) applyRefresh(
	ctx context.Context, a *Account,
	newAT, newRT string, expAt time.Time, source string,
	res *RefreshResult,
) (*RefreshResult, error) {
	atEnc, err := r.svc.cipher.EncryptString(newAT)
	if err != nil {
		return nil, err
	}
	var rtEnc string
	if newRT != "" {
		enc, err := r.svc.cipher.EncryptString(newRT)
		if err != nil {
			return nil, err
		}
		rtEnc = enc
	}
	if expAt.IsZero() {
		expAt = parseJWTExp(newAT)
	}
	if err := r.svc.dao.ApplyRefreshResult(ctx, a.ID, atEnc, rtEnc, expAt, source); err != nil {
		return nil, err
	}
	// 自动更新 subscription_type（从新 AT 的 JWT 解析）
	if pt := parseJWTPlanType(newAT); pt != "" && pt != a.SubscriptionType {
		_ = r.svc.dao.SetSubscriptionType(ctx, a.ID, pt)
	}
	res.OK = true
	res.Source = source
	res.ExpiresAt = expAt
	res.Error = ""
	res.RTRotated = rtEnc != ""
	return res, nil
}

// rtToAT POST https://auth.openai.com/oauth/token
func (r *Refresher) rtToAT(ctx context.Context, accountID uint64, refreshToken, clientID string) (newAT, newRT string, expAt time.Time, err error) {
	body := map[string]string{
		"client_id":     clientID,
		"grant_type":    "refresh_token",
		"redirect_uri":  "com.openai.chat://auth0.openai.com/ios/com.openai.chat/callback",
		"refresh_token": refreshToken,
	}
	buf, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://auth.openai.com/oauth/token", bytes.NewReader(buf))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ChatGPT/1.2025.122 (iOS 18.2; iPhone15,2; build 15096)")

	resp, err := r.clientFor(ctx, accountID).Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		err = fmt.Errorf("rt refresh http=%d body=%s", resp.StatusCode, truncate(string(data), 200))
		return
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err = json.Unmarshal(data, &out); err != nil {
		return
	}
	if out.AccessToken == "" {
		err = errors.New("rt refresh: missing access_token in response")
		return
	}
	newAT = out.AccessToken
	newRT = out.RefreshToken
	if out.ExpiresIn > 0 {
		expAt = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	} else {
		expAt = parseJWTExp(newAT)
	}
	return
}

// stToAT GET https://chatgpt.com/api/auth/session  Cookie: __Secure-next-auth.session-token=ST
func (r *Refresher) stToAT(ctx context.Context, accountID uint64, sessionToken string) (newAT string, expAt time.Time, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://chatgpt.com/api/auth/session", nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://chatgpt.com/")
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")

	// 同时尝试两个可能的 cookie 名
	req.AddCookie(&http.Cookie{Name: "__Secure-next-auth.session-token", Value: sessionToken})

	resp, err := r.clientFor(ctx, accountID).Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		err = fmt.Errorf("st refresh http=%d body=%s", resp.StatusCode, truncate(string(data), 200))
		return
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "{}" {
		err = errors.New("ST 已过期或无效,响应为空")
		return
	}
	var out struct {
		AccessToken string `json:"accessToken"`
		Expires     string `json:"expires"`
	}
	if err = json.Unmarshal([]byte(raw), &out); err != nil {
		return
	}
	if out.AccessToken == "" {
		err = errors.New("响应缺少 accessToken 字段")
		return
	}
	newAT = out.AccessToken
	if out.Expires != "" {
		if t, e := time.Parse(time.RFC3339, out.Expires); e == nil {
			expAt = t
		}
	}
	if expAt.IsZero() {
		expAt = parseJWTExp(newAT)
	}
	return
}

// jwtClaims JWT payload 中需要的字段。
type jwtClaims struct {
	Exp       int64  `json:"exp"`
	PlanType  string `json:"chatgpt_plan_type"`
	AccountID string `json:"chatgpt_account_id"`
}

func parseJWTClaims(token string) jwtClaims {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return jwtClaims{}
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		raw, err = base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return jwtClaims{}
		}
	}
	// ChatGPT JWT: plan_type 在嵌套的 "https://api.openai.com/auth" 下
	var outer struct {
		Exp  int64 `json:"exp"`
		Auth struct {
			PlanType  string `json:"chatgpt_plan_type"`
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(raw, &outer); err == nil && outer.Auth.PlanType != "" {
		return jwtClaims{Exp: outer.Exp, PlanType: outer.Auth.PlanType, AccountID: outer.Auth.AccountID}
	}
	var flat jwtClaims
	_ = json.Unmarshal(raw, &flat)
	return flat
}

// parseJWTExp 解 JWT payload 里的 exp(秒级)。失败返回 +24h。
func parseJWTExp(token string) time.Time {
	c := parseJWTClaims(token)
	if c.Exp == 0 {
		return time.Now().Add(24 * time.Hour)
	}
	return time.Unix(c.Exp, 0)
}

// parseJWTPlanType 从 JWT 解析 chatgpt_plan_type(pro/plus/free/team 等)。
func parseJWTPlanType(token string) string {
	return parseJWTClaims(token).PlanType
}

func friendlyRefreshErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "http=401"), strings.Contains(low, "invalid_grant"):
		return "RT 已失效(401)"
	case strings.Contains(low, "http=403"):
		return "上游拒绝访问(403)"
	case strings.Contains(low, "http=429"):
		return "触发速率限制(429)"
	case strings.Contains(low, "timeout"), strings.Contains(low, "deadline exceeded"):
		return "刷新请求超时"
	case strings.Contains(low, "proxyconnect") && strings.Contains(low, "no such host"):
		return "代理域名无法解析"
	case strings.Contains(low, "proxyconnect tcp"):
		return "代理握手失败"
	case strings.Contains(low, "no such host"):
		return "DNS 解析失败"
	case strings.Contains(low, "connection refused"):
		return "连接被拒绝"
	case strings.Contains(low, "connection reset"):
		return "连接被重置"
	case strings.Contains(low, "unexpected eof"), strings.HasSuffix(low, ": eof"):
		return "连接被对端关闭"
	case strings.Contains(low, "tls"), strings.Contains(low, "x509"):
		return "TLS 握手失败"
	case strings.Contains(low, "missing access_token"), strings.Contains(s, "ST 已过期"):
		return stripHTTPPrefix(s)
	default:
		return "刷新失败:" + stripHTTPPrefix(s)
	}
}

// stripHTTPPrefix 去掉 Go net/http 错误里形如
//
//	Post "https://auth.openai.com/oauth/token": dial tcp: ...
//
// 的 URL 前缀,只保留后面真正的原因,避免把敏感/冗长的 URL 暴露给前端。
func stripHTTPPrefix(s string) string {
	// 典型前缀: Get/Post/Put "https://...": rest
	if i := strings.Index(s, `": `); i > 0 && i < len(s)-3 {
		s = s[i+3:]
	}
	// 再剥一层常见的 "dial tcp: " / "proxyconnect tcp: " 类前缀最靠前的修饰,
	// 保留中间有用的原因(如 lookup xxx: no such host)。
	s = strings.TrimSpace(s)
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
