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
	"time"
)

// ImportTokenMode 批量 token 导入的模式。
type ImportTokenMode string

const (
	ImportModeAT          ImportTokenMode = "at"           // 每行一个 access_token
	ImportModeRT          ImportTokenMode = "rt"           // 每行一个 refresh_token,需要 client_id(APPID)
	ImportModeST          ImportTokenMode = "st"           // 每行一个 session_token
	ImportModeSessionJSON ImportTokenMode = "session_json" // chatgpt.com/api/auth/session 的完整 JSON
)

// ImportTokensOptions 批量 token 导入选项。
type ImportTokensOptions struct {
	Mode ImportTokenMode

	// ClientID: RT 模式必填,AT/ST 模式可选(不填则用 DefaultClientID)。
	// 在 RT 模式下会作为 OAuth 的 client_id 发起 auth.openai.com/oauth/token 请求。
	ClientID string

	// ProxyURL: 换 AT 时走的代理(RT/ST 必须能访问 auth.openai.com / chatgpt.com)。
	// 空字符串=直连,生产环境通常必须走代理。
	ProxyURL string

	// 下面几个直接透传给底层 ImportBatch。
	DefaultProxyID  uint64
	UpdateExisting  bool
	DefaultClientID string
	BatchSize       int
}

// ImportTokensBatch 把运维侧粘贴的「一行一个 token」的文本批量转成账号入库。
//
// 处理流程:
//
//  1. 对 AT 模式:直接解 JWT payload 拿 email,不发起任何外部请求。
//  2. 对 RT 模式:POST auth.openai.com/oauth/token(需要 client_id)换出 AT,
//     再从 AT 解 email。RT 和 newAT 都会保存到账号。
//  3. 对 ST 模式:GET chatgpt.com/api/auth/session(带 cookie)换出 AT,再从 AT 解 email。
//     ST 和 newAT 都会保存到账号。
//  4. 拿到 email + AT 的行 → 复用现有 ImportBatch 走 upsert。
//  5. 无法拿到 email 的行进 failed 明细,返回给前端。
func (s *Service) ImportTokensBatch(ctx context.Context, tokens []string, opts ImportTokensOptions) *ImportSummary {
	if opts.Mode == "" {
		opts.Mode = ImportModeAT
	}
	if opts.DefaultClientID == "" {
		opts.DefaultClientID = "app_LlGpXReQgckcGGUo2JrYvtJK"
	}

	httpc := buildImportHTTPClient(opts.ProxyURL)

	// 去重 + 归一化
	seen := map[string]struct{}{}
	cleaned := make([]string, 0, len(tokens))
	for _, raw := range tokens {
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}
		// 容错:某些 RT / ST 复制过来带前缀 "Bearer " 或 "__Secure-next-auth.session-token="
		t = strings.TrimPrefix(t, "Bearer ")
		t = strings.TrimPrefix(t, "bearer ")
		if i := strings.Index(t, "="); i > 0 && i < 60 && strings.HasPrefix(strings.ToLower(t), "__secure-next-auth.session-token") {
			t = t[i+1:]
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		cleaned = append(cleaned, t)
	}

	sum := &ImportSummary{Results: make([]ImportLineResult, 0, len(cleaned))}
	items := make([]ImportSource, 0, len(cleaned))

	// RT 模式先强制校验 client_id
	clientID := strings.TrimSpace(opts.ClientID)
	if opts.Mode == ImportModeRT && clientID == "" {
		clientID = opts.DefaultClientID
	}

	for idx, t := range cleaned {
		if err := ctx.Err(); err != nil {
			break
		}

		var src ImportSource
		var err error

		// 自动检测: 如果输入以 { 开头且含 accessToken/access_token,自动当 session_json
		mode := opts.Mode
		if strings.HasPrefix(t, "{") {
			low := strings.ToLower(t)
			if strings.Contains(low, "accesstoken") || strings.Contains(low, "access_token") {
				mode = ImportModeSessionJSON
			}
		}

		switch mode {
		case ImportModeAT:
			src, err = convertATToSource(t, clientID)
		case ImportModeRT:
			if clientID == "" {
				err = errors.New("RT 模式需要 APPID(client_id)")
				break
			}
			src, err = convertRTToSource(ctx, httpc, t, clientID)
		case ImportModeST:
			src, err = convertSTToSource(ctx, httpc, t, clientID)
		case ImportModeSessionJSON:
			src, err = convertSessionJSONToSource(t, clientID)
		default:
			err = fmt.Errorf("未知模式:%s", opts.Mode)
		}
		if err != nil {
			sum.Failed++
			sum.Total++
			sum.Results = append(sum.Results, ImportLineResult{
				Index:  idx,
				Email:  "?",
				Status: "failed",
				Reason: truncate(err.Error(), 160),
			})
			continue
		}
		items = append(items, src)
	}

	// 复用已有批量 upsert(去重、分批、UpdateExisting 等都在里面)
	batch := s.ImportBatch(ctx, items, ImportOptions{
		UpdateExisting:  opts.UpdateExisting,
		DefaultClientID: clientID,
		DefaultProxyID:  opts.DefaultProxyID,
		BatchSize:       opts.BatchSize,
	})
	sum.Total += batch.Total
	sum.Created += batch.Created
	sum.Updated += batch.Updated
	sum.Skipped += batch.Skipped
	sum.Failed += batch.Failed
	sum.Results = append(sum.Results, batch.Results...)
	return sum
}

// ---------- 四种模式的 token → ImportSource 转换 ----------

// convertSessionJSONToSource 解析 chatgpt.com/api/auth/session 返回的完整 JSON。
// 格式: {"user":{"id":"...","name":"...","email":"..."},"expires":"...","accessToken":"eyJ..."}
func convertSessionJSONToSource(raw, clientID string) (ImportSource, error) {
	var sess struct {
		User *struct {
			ID    string `json:"id"`
			Email string `json:"email"`
			Name  string `json:"name"`
		} `json:"user"`
		Expires          string `json:"expires"`
		AccessToken      string `json:"accessToken"`
		AccessTokenSnake string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(raw), &sess); err != nil {
		return ImportSource{}, fmt.Errorf("JSON 解析失败:%w", err)
	}
	if sess.AccessToken == "" {
		sess.AccessToken = sess.AccessTokenSnake
	}
	if sess.AccessToken == "" {
		return ImportSource{}, errors.New("JSON 中缺少 accessToken/access_token 字段")
	}

	email := ""
	if sess.User != nil {
		email = sess.User.Email
	}

	// 从 AT JWT 补充信息
	jwtEmail, subAccID, expAt, _ := decodeATClaims(sess.AccessToken)
	if email == "" {
		email = jwtEmail
	}
	if email == "" {
		return ImportSource{}, errors.New("无法获取 email")
	}

	// 优先用 JSON 里的 expires
	if sess.Expires != "" {
		if t, err := time.Parse(time.RFC3339, sess.Expires); err == nil {
			expAt = t
		}
	}

	return ImportSource{
		AccessToken:      sess.AccessToken,
		Email:            email,
		ChatGPTAccountID: subAccID,
		ExpiredAt:        expAt,
		ClientID:         clientID,
		AccountType:      "chatgpt",
	}, nil
}

// convertATToSource 仅凭 access_token 的 JWT payload 解 email / sub / 过期时间。
func convertATToSource(at, clientID string) (ImportSource, error) {
	email, subAccountID, expAt, err := decodeATClaims(at)
	if err != nil {
		return ImportSource{}, fmt.Errorf("解析 AT 失败:%w", err)
	}
	if email == "" {
		return ImportSource{}, errors.New("无法从 AT 解出 email,请改用 JSON 或带 email 的导入")
	}
	return ImportSource{
		AccessToken:      at,
		Email:            email,
		ChatGPTAccountID: subAccountID,
		ExpiredAt:        expAt,
		ClientID:         clientID,
		AccountType:      "chatgpt",
	}, nil
}

// convertRTToSource 用 refresh_token + client_id 调 auth.openai.com/oauth/token 换出 AT,
// 再解 AT claims 拿 email。AT + RT 一起存进账号(后续仍可 RT 续签)。
func convertRTToSource(ctx context.Context, httpc *http.Client, rt, clientID string) (ImportSource, error) {
	at, newRT, expAt, err := rtExchange(ctx, httpc, rt, clientID)
	if err != nil {
		return ImportSource{}, fmt.Errorf("RT 换 AT 失败:%s", friendlyImportErr(err))
	}
	email, subAccID, expFromJWT, jerr := decodeATClaims(at)
	if jerr != nil || email == "" {
		return ImportSource{}, errors.New("RT 换出的 AT 无法解析出 email")
	}
	if expAt.IsZero() {
		expAt = expFromJWT
	}
	// 如果服务端下发新 RT,用新的;否则用本次输入的
	usedRT := rt
	if newRT != "" {
		usedRT = newRT
	}
	return ImportSource{
		AccessToken:      at,
		RefreshToken:     usedRT,
		Email:            email,
		ChatGPTAccountID: subAccID,
		ExpiredAt:        expAt,
		ClientID:         clientID,
		AccountType:      "chatgpt",
	}, nil
}

// convertSTToSource 用 session_token 调 chatgpt.com/api/auth/session 换出 AT,再解 email。
// AT + ST 一起存进账号(后续可由 ST 定时续签)。
func convertSTToSource(ctx context.Context, httpc *http.Client, st, clientID string) (ImportSource, error) {
	at, expAt, err := stExchange(ctx, httpc, st)
	if err != nil {
		return ImportSource{}, fmt.Errorf("ST 换 AT 失败:%s", friendlyImportErr(err))
	}
	email, subAccID, expFromJWT, jerr := decodeATClaims(at)
	if jerr != nil || email == "" {
		return ImportSource{}, errors.New("ST 换出的 AT 无法解析出 email")
	}
	if expAt.IsZero() {
		expAt = expFromJWT
	}
	return ImportSource{
		AccessToken:      at,
		SessionToken:     st,
		Email:            email,
		ChatGPTAccountID: subAccID,
		ExpiredAt:        expAt,
		ClientID:         clientID,
		AccountType:      "chatgpt",
	}, nil
}

// ---------- 底层 HTTP ----------

func buildImportHTTPClient(proxyURL string) *http.Client {
	httpc := &http.Client{Timeout: 30 * time.Second}
	if proxyURL == "" {
		return httpc
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return httpc
	}
	httpc.Transport = &http.Transport{
		Proxy:               http.ProxyURL(u),
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        8,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return httpc
}

// rtExchange 与 refresher.rtToAT 等价的包级实现(不需要 account_id)。
func rtExchange(ctx context.Context, httpc *http.Client, rt, clientID string) (newAT, newRT string, expAt time.Time, err error) {
	body := map[string]string{
		"client_id":     clientID,
		"grant_type":    "refresh_token",
		"redirect_uri":  "com.openai.chat://auth0.openai.com/ios/com.openai.chat/callback",
		"refresh_token": rt,
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
	resp, err := httpc.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		err = fmt.Errorf("rt exchange http=%d body=%s", resp.StatusCode, truncate(string(data), 200))
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
		err = errors.New("rt exchange: missing access_token in response")
		return
	}
	newAT = out.AccessToken
	newRT = out.RefreshToken
	if out.ExpiresIn > 0 {
		expAt = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	return
}

// stExchange 与 refresher.stToAT 等价的包级实现。
func stExchange(ctx context.Context, httpc *http.Client, st string) (newAT string, expAt time.Time, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://chatgpt.com/api/auth/session", nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://chatgpt.com/")
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.AddCookie(&http.Cookie{Name: "__Secure-next-auth.session-token", Value: st})

	resp, err := httpc.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		err = fmt.Errorf("st exchange http=%d body=%s", resp.StatusCode, truncate(string(data), 200))
		return
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "{}" {
		err = errors.New("ST 已过期或无效(响应为空)")
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
		err = errors.New("响应缺少 accessToken 字段,ST 已失效")
		return
	}
	newAT = out.AccessToken
	if out.Expires != "" {
		if t, e := time.Parse(time.RFC3339, out.Expires); e == nil {
			expAt = t
		}
	}
	return
}

// ---------- JWT claims 解析 ----------

// decodeATClaims 从 access_token(JWT)里取出 email / chatgpt_account_id / exp。
// 兼容 iOS scope(Codex)和 Web scope(ChatGPT)两种 claim 结构。
func decodeATClaims(at string) (email, accountID string, expAt time.Time, err error) {
	parts := strings.Split(at, ".")
	if len(parts) < 2 {
		err = errors.New("非法 JWT(段数不足)")
		return
	}
	raw, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		raw, e = base64.StdEncoding.DecodeString(parts[1])
		if e != nil {
			err = fmt.Errorf("base64 解码失败:%w", e)
			return
		}
	}
	// 尽可能宽松地解析,不同 scope 的 claims 字段名不一样。
	var claims map[string]interface{}
	if e := json.Unmarshal(raw, &claims); e != nil {
		err = fmt.Errorf("claims JSON 解码失败:%w", e)
		return
	}

	// 1) 直接字段
	if v, ok := claims["email"].(string); ok && v != "" {
		email = v
	}
	if v, ok := claims["chatgpt_account_id"].(string); ok && v != "" {
		accountID = v
	}

	// 2) iOS/Web scope 里常见的 namespaced claims
	for _, ns := range []string{
		"https://api.openai.com/profile",
		"https://api.openai.com/auth",
	} {
		if m, ok := claims[ns].(map[string]interface{}); ok {
			if email == "" {
				if v, ok := m["email"].(string); ok && v != "" {
					email = v
				}
			}
			if accountID == "" {
				if v, ok := m["chatgpt_account_id"].(string); ok && v != "" {
					accountID = v
				}
				claimKey := "user" + "_id"
				if v, ok := m[claimKey].(string); ok && accountID == "" && v != "" {
					accountID = v
				}
			}
		}
	}

	// 3) 从 exp
	if v, ok := claims["exp"].(float64); ok && v > 0 {
		expAt = time.Unix(int64(v), 0)
	}
	return
}

// friendlyImportErr 把底层 http 错误压成简短中文,避免把 URL / stacktrace 泄露到前端。
func friendlyImportErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "http=401"), strings.Contains(low, "invalid_grant"):
		return "token 已失效(401)"
	case strings.Contains(low, "http=403"):
		return "上游拒绝访问(403)"
	case strings.Contains(low, "http=429"):
		return "触发速率限制(429),稍后再试"
	case strings.Contains(low, "timeout"), strings.Contains(low, "deadline exceeded"):
		return "请求超时,建议配代理"
	case strings.Contains(low, "no such host"), strings.Contains(low, "dial tcp"):
		return "无法连接 openai(建议选默认代理)"
	case strings.Contains(low, "tls"), strings.Contains(low, "x509"):
		return "TLS 握手失败"
	}
	// 兜底:去掉 URL
	if i := strings.Index(s, `": `); i > 0 && i < len(s)-3 {
		s = s[i+3:]
	}
	return truncate(strings.TrimSpace(s), 120)
}
