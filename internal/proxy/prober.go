package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ProbeSettings 探测器配置提供者(从 settings.Service 注入)。
// 所有字段都支持热更新:循环每轮结束都会重新读取。
type ProbeSettings interface {
	ProbeEnabled() bool
	ProbeIntervalSec() int // 两轮探测之间的间隔(秒);<= 0 视为关闭
	ProbeTimeoutSec() int  // 单次探测超时(秒)
	ProbeTargetURL() string
	ProbeConcurrency() int // 并发 worker 数;<=0 默认 8
}

// ProbeResult 单次探测结果。
type ProbeResult struct {
	ProxyID   uint64        `json:"proxy_id"`
	OK        bool          `json:"ok"`
	LatencyMs int           `json:"latency_ms"`
	Error     string        `json:"error,omitempty"`
	TriedAt   time.Time     `json:"tried_at"`
	Duration  time.Duration `json:"-"`
}

// Prober 周期性对启用的代理发起连通性探测,刷新 health_score/last_probe_at/last_error。
//
// 评分策略:
//   - 成功  → score = min(100, score + 10),清空 last_error
//   - 失败  → score = max(0,   score - 20),记录简短 error
type Prober struct {
	svc      *Service
	settings ProbeSettings
	log      *zap.Logger
	reassign ReassignFunc // 代理淘汰时重分配账号

	// 手动触发通道:发送 <id> 探测单个(0 表示全部)
	kickCh chan uint64
}

func NewProber(svc *Service, settings ProbeSettings, log *zap.Logger) *Prober {
	return &Prober{
		svc:      svc,
		settings: settings,
		log:      log,
		kickCh:   make(chan uint64, 32),
	}
}

// Run 后台循环探测,受 ctx 控制。建议作为独立 goroutine 启动。
func (p *Prober) Run(ctx context.Context) {
	// 启动后先睡 5 秒,避开启动峰值。
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
	}

	lastCleanup := time.Now()

	for {
		interval := time.Duration(p.settings.ProbeIntervalSec()) * time.Second
		if !p.settings.ProbeEnabled() || interval <= 0 {
			select {
			case <-ctx.Done():
				return
			case id := <-p.kickCh:
				p.runOnce(ctx, id)
			case <-time.After(30 * time.Second):
			}
			continue
		}

		p.runOnce(ctx, 0)

		// 每小时清理一次:禁用超过 7 天的代理自动软删除
		if time.Since(lastCleanup) > time.Hour {
			before := time.Now().Add(-7 * 24 * time.Hour)
			if n, err := p.svc.dao.CleanupDisabledBefore(ctx, before); err == nil && n > 0 {
				p.log.Info("prober: cleaned up stale disabled proxies", zap.Int64("count", n))
			}
			lastCleanup = time.Now()
		}

		select {
		case <-ctx.Done():
			return
		case id := <-p.kickCh:
			p.runOnce(ctx, id)
		case <-time.After(interval):
		}
	}
}

// Kick 触发一次立即探测。id=0 表示全部启用的代理;否则只探一条。
// 非阻塞(通道满时直接丢弃,避免调用者卡住)。
func (p *Prober) Kick(id uint64) {
	select {
	case p.kickCh <- id:
	default:
	}
}

// ProbeOne 对单条代理做一次同步探测(不写库)。对外暴露用于手动测试。
func (p *Prober) ProbeOne(ctx context.Context, pr *Proxy) ProbeResult {
	return p.probe(ctx, pr)
}

// ProbeByID 手动触发单条探测并写库,返回结果。
func (p *Prober) ProbeByID(ctx context.Context, id uint64) (ProbeResult, error) {
	pr, err := p.svc.Get(ctx, id)
	if err != nil {
		return ProbeResult{}, err
	}
	res := p.probe(ctx, pr)
	p.applyResult(ctx, pr, res)
	return res, nil
}

// ProbeAll 手动触发对所有启用代理的并发探测并写库,返回结果列表。
func (p *Prober) ProbeAll(ctx context.Context) ([]ProbeResult, error) {
	list, err := p.svc.dao.ListAllEnabled(ctx)
	if err != nil {
		return nil, err
	}
	return p.probeBatch(ctx, list), nil
}

// ---------- 内部实现 ----------

func (p *Prober) runOnce(ctx context.Context, only uint64) {
	var list []*Proxy
	var err error
	if only == 0 {
		list, err = p.svc.dao.ListAllEnabled(ctx)
	} else {
		pr, gerr := p.svc.Get(ctx, only)
		if gerr == nil {
			list = []*Proxy{pr}
		}
		err = gerr
	}
	if err != nil {
		p.log.Warn("prober: list failed", zap.Error(err))
		return
	}
	if len(list) == 0 {
		return
	}
	results := p.probeBatch(ctx, list)
	ok, bad := 0, 0
	for _, r := range results {
		if r.OK {
			ok++
		} else {
			bad++
		}
	}
	p.log.Info("prober: round finished",
		zap.Int("total", len(results)), zap.Int("ok", ok), zap.Int("bad", bad))
}

func (p *Prober) probeBatch(ctx context.Context, list []*Proxy) []ProbeResult {
	conc := p.settings.ProbeConcurrency()
	if conc <= 0 {
		conc = 8
	}
	if conc > len(list) {
		conc = len(list)
	}

	results := make([]ProbeResult, len(list))
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup

	for i, pr := range list {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, pr *Proxy) {
			defer wg.Done()
			defer func() { <-sem }()
			r := p.probe(ctx, pr)
			p.applyResult(ctx, pr, r)
			results[i] = r
		}(i, pr)
	}
	wg.Wait()
	return results
}

// probe 做一次真实 HTTP(S) 请求。只组装结果,不写库。
func (p *Prober) probe(ctx context.Context, pr *Proxy) ProbeResult {
	out := ProbeResult{ProxyID: pr.ID, TriedAt: time.Now()}

	proxyURL, err := p.svc.BuildURL(pr)
	if err != nil {
		out.Error = "密码解密失败:" + err.Error()
		return out
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		out.Error = "代理 URL 格式错误:" + err.Error()
		return out
	}

	timeout := time.Duration(p.settings.ProbeTimeoutSec()) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	target := strings.TrimSpace(p.settings.ProbeTargetURL())
	if target == "" {
		target = "https://chatgpt.com/cdn-cgi/trace"
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyURL(u),
		DialContext:           (&net.Dialer{Timeout: timeout}).DialContext,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: 1 * time.Second,
		DisableKeepAlives:     true,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
	if err != nil {
		out.Error = "构造探测请求失败:" + err.Error()
		return out
	}
	req.Header.Set("User-Agent", "gpt2api-proxy-prober/1.0")

	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start)
	out.Duration = elapsed
	out.LatencyMs = int(elapsed / time.Millisecond)

	if err != nil {
		out.Error = shortenErr(err)
		return out
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		out.OK = true
		return out
	}
	out.Error = fmt.Sprintf("目标站返回异常状态码 %d", resp.StatusCode)
	return out
}

// ReassignFunc 当代理被淘汰时,将其绑定的账号重新分配到其他代理。
// 由 main.go 注入(避免 proxy 包直接依赖 account 包)。
type ReassignFunc func(ctx context.Context, deadProxyID uint64)

// SetReassignFunc 注入账号重分配回调。
func (p *Prober) SetReassignFunc(fn ReassignFunc) { p.reassign = fn }

func (p *Prober) applyResult(ctx context.Context, pr *Proxy, r ProbeResult) {
	score := pr.HealthScore
	lastErr := ""
	if r.OK {
		score += 10
		if score > 100 {
			score = 100
		}
	} else {
		score -= 20
		if score < 0 {
			score = 0
		}
		lastErr = r.Error
		if len(lastErr) > 200 {
			lastErr = lastErr[:200]
		}
	}
	if err := p.svc.dao.UpdateHealth(ctx, pr.ID, score, lastErr); err != nil {
		p.log.Warn("prober: update health failed",
			zap.Uint64("proxy_id", pr.ID), zap.Error(err))
	}

	// 健康分归零 → 自动禁用代理 + 重新分配账号
	if score <= 0 && pr.Enabled {
		p.log.Warn("prober: proxy health exhausted, auto-disabling",
			zap.Uint64("proxy_id", pr.ID),
			zap.String("last_error", lastErr))
		_ = p.svc.dao.SetEnabled(ctx, pr.ID, false)
		if p.reassign != nil {
			p.reassign(ctx, pr.ID)
		}
	}
}

// shortenErr 把网络错误压成一行、对前端友好的中文字符串。
// 兜底会带上简短的英文原文,便于排障。
func shortenErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	low := strings.ToLower(s)

	switch {
	// 超时 / 主动取消
	case errors.Is(err, context.DeadlineExceeded),
		strings.Contains(low, "deadline exceeded"),
		strings.Contains(low, "i/o timeout"),
		strings.Contains(low, "timeout awaiting"),
		strings.Contains(low, "request canceled") && strings.Contains(low, "timeout"):
		return "连接超时(探测超时)"

	// 407 代理鉴权失败 —— 用户名/密码错误最常见
	case strings.Contains(s, "Proxy Authentication Required"),
		strings.Contains(low, "407"):
		return "代理鉴权失败(407,请核对用户名/密码)"

	// DNS 解析失败 —— 细分代理自身域名 vs 目标站域名
	case strings.Contains(low, "proxyconnect") && strings.Contains(low, "no such host"):
		return "DNS 解析失败:代理域名无法解析(宿主梯子/DNS 污染,可在 docker-compose 里给 server 指定公共 DNS 如 8.8.8.8)"
	case strings.Contains(low, "no such host"),
		strings.Contains(low, "lookup ") && strings.Contains(low, "no such"):
		return "DNS 解析失败(域名不存在或 DNS 被污染)"

	// 各类拒绝 / 不可达
	case strings.Contains(low, "connection refused"):
		return "目标拒绝连接(connection refused)"
	case strings.Contains(low, "network is unreachable"):
		return "网络不可达"
	case strings.Contains(low, "no route to host"):
		return "无法路由到目标主机"
	case strings.Contains(low, "host is down"):
		return "目标主机不可达"

	// 连接在握手/发送中被对端断开
	case strings.Contains(low, "connection reset by peer"):
		return "对端重置连接(代理可能限流/拒绝)"
	case strings.Contains(low, "broken pipe"):
		return "连接已断开(broken pipe)"
	case strings.Contains(low, "unexpected eof"),
		low == "eof",
		strings.HasSuffix(low, ": eof"):
		return "代理握手被关闭(鉴权或协议不匹配)"

	// 代理协议问题
	case strings.Contains(low, "proxyconnect tcp"):
		return "代理握手失败(请检查 host:port/scheme)"
	case strings.Contains(low, "malformed http response"):
		return "代理响应非 HTTP(scheme 可能写错)"
	case strings.Contains(low, "socks"):
		return "SOCKS 代理握手失败"

	// TLS
	case strings.Contains(low, "tls:"),
		strings.Contains(low, "x509:"),
		strings.Contains(low, "certificate"):
		return "TLS/证书错误"

	// 其它 —— 给中文前缀 + 简短原文,方便排障
	default:
		// 截断 "Get \"...\": " 等 net/http 前缀
		if i := strings.Index(s, "\": "); i > 0 && i < len(s)-3 {
			s = s[i+3:]
		} else if i := strings.Index(s, ": "); i > 0 && i < len(s)-2 {
			s = s[i+2:]
		}
		if len(s) > 140 {
			s = s[:140] + "…"
		}
		return "探测失败:" + s
	}
}
