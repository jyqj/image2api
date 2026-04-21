package settings

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
)

// Service 带内存缓存的只读/可写访问层。
// 所有读走本地 map,写走 DB + 原子替换缓存。
type Service struct {
	dao   *DAO
	mu    sync.RWMutex
	cache map[string]string // 最新快照;不直接暴露,通过 GetXxx 读
}

var ErrUnknownKey = errors.New("settings: unknown key")

func NewService(dao *DAO) *Service {
	return &Service{dao: dao, cache: map[string]string{}}
}

// Reload 启动时 / 手动触发时调用。
func (s *Service) Reload(ctx context.Context) error {
	m, err := s.dao.LoadAll(ctx)
	if err != nil {
		return err
	}
	// 补齐 Defs 默认值(DB 里缺某个 key 时)
	for _, d := range Defs {
		if _, ok := m[d.Key]; !ok {
			m[d.Key] = d.Default
		}
	}
	s.mu.Lock()
	s.cache = m
	s.mu.Unlock()
	return nil
}

// Snapshot 拷贝当前所有设置(不含未登记 key)。
func (s *Service) Snapshot() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.cache))
	for _, d := range Defs {
		if v, ok := s.cache[d.Key]; ok {
			out[d.Key] = v
		} else {
			out[d.Key] = d.Default
		}
	}
	return out
}

// PublicSnapshot 只返回 Defs 中 Public=true 的条目,面向匿名访问。
func (s *Service) PublicSnapshot() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string)
	for _, d := range Defs {
		if !d.Public {
			continue
		}
		if v, ok := s.cache[d.Key]; ok {
			out[d.Key] = v
		} else {
			out[d.Key] = d.Default
		}
	}
	return out
}

// Set 批量写入并刷新缓存;未在 Defs 白名单中的 key 会被过滤。
func (s *Service) Set(ctx context.Context, in map[string]string) error {
	filtered := make(map[string]string, len(in))
	for k, v := range in {
		if !IsAllowedKey(k) {
			continue
		}
		filtered[k] = strings.TrimSpace(v)
	}
	if len(filtered) == 0 {
		return nil
	}
	if err := s.dao.SetMany(ctx, filtered); err != nil {
		return err
	}
	s.mu.Lock()
	for k, v := range filtered {
		s.cache[k] = v
	}
	s.mu.Unlock()
	return nil
}

// --- typed getters ---

func (s *Service) GetString(key string) string {
	s.mu.RLock()
	v, ok := s.cache[key]
	s.mu.RUnlock()
	if ok {
		return v
	}
	if d, ok := DefByKey(key); ok {
		return d.Default
	}
	return ""
}

func (s *Service) GetBool(key string) bool {
	v := strings.ToLower(strings.TrimSpace(s.GetString(key)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func (s *Service) GetInt(key string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s.GetString(key)), 10, 64)
	return n
}

func (s *Service) GetFloat(key string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s.GetString(key)), 64)
	return f
}

// --- convenience helpers (业务语义) ---
// 所有 helper 都保证返回"安全"的业务默认(例如不让 0/负值落到业务路径)。

// -- site --
func (s *Service) SiteName() string { return firstNonEmpty(s.GetString(SiteName), "GPT2API") }

// -- gateway --
func (s *Service) GatewayUpstreamTimeoutSec() int {
	n := int(s.GetInt(GatewayUpstreamTimeoutSec))
	if n <= 0 {
		return 60
	}
	return n
}
func (s *Service) GatewaySSEReadTimeoutSec() int {
	n := int(s.GetInt(GatewaySSEReadTimeoutSec))
	if n <= 0 {
		return 120
	}
	return n
}
func (s *Service) Cooldown429Sec() int {
	n := int(s.GetInt(GatewayCooldown429Sec))
	if n <= 0 {
		return 300
	}
	return n
}
func (s *Service) WarnedPauseHours() int {
	n := int(s.GetInt(GatewayWarnedPauseHours))
	if n <= 0 {
		return 24
	}
	return n
}
func (s *Service) DailyUsageRatio() float64 {
	f := s.GetFloat(GatewayDailyUsageRatio)
	if f <= 0 || f > 1 {
		return 0.8
	}
	return f
}
func (s *Service) RetryOnFailure() bool { return s.GetBool(GatewayRetryOnFailure) }
func (s *Service) RetryMax() int {
	n := int(s.GetInt(GatewayRetryMax))
	if n < 0 {
		return 0
	}
	if n > 3 {
		return 3
	}
	return n
}

// DispatchQueueWaitSec 账号池忙时请求的最长排队时间。
//   - 0   ⇒ 不排队(立即返回 no_available_account)
//   - 负数 / 未设置 ⇒ 回退默认 120
func (s *Service) DispatchQueueWaitSec() int {
	n := int(s.GetInt(GatewayDispatchQueueWaitSec))
	if n < 0 {
		return 120
	}
	return n
}

func (s *Service) ImageExploreRatio() float64 {
	f := s.GetFloat(GatewayImageExploreRatio)
	if f < 0 {
		return 0
	}
	if f > 0.8 {
		return 0.8
	}
	return f
}

// -- proxy probe --
func (s *Service) ProbeEnabled() bool { return s.GetBool(ProxyProbeEnabled) }
func (s *Service) ProbeIntervalSec() int {
	n := int(s.GetInt(ProxyProbeIntervalSec))
	if n <= 0 {
		return 300
	}
	return n
}
func (s *Service) ProbeTimeoutSec() int {
	n := int(s.GetInt(ProxyProbeTimeoutSec))
	if n <= 0 {
		return 10
	}
	return n
}
func (s *Service) ProbeTargetURL() string {
	return firstNonEmpty(s.GetString(ProxyProbeTargetURL), "https://chatgpt.com/cdn-cgi/trace")
}
func (s *Service) ProbeConcurrency() int {
	n := int(s.GetInt(ProxyProbeConcurrency))
	if n <= 0 {
		return 8
	}
	if n > 64 {
		return 64
	}
	return n
}

// -- account refresh / quota probe --
func (s *Service) AccountRefreshEnabled() bool { return s.GetBool(AccountRefreshEnabled) }
func (s *Service) AccountRefreshIntervalSec() int {
	n := int(s.GetInt(AccountRefreshIntervalSec))
	if n <= 0 {
		return 120
	}
	return n
}
func (s *Service) AccountRefreshAheadSec() int {
	n := int(s.GetInt(AccountRefreshAheadSec))
	if n <= 0 {
		return 120
	}
	return n
}
func (s *Service) AccountRefreshConcurrency() int {
	n := int(s.GetInt(AccountRefreshConcurrency))
	if n <= 0 {
		return 4
	}
	if n > 32 {
		return 32
	}
	return n
}
func (s *Service) AccountQuotaProbeEnabled() bool { return s.GetBool(AccountQuotaProbeEnabled) }
func (s *Service) AccountQuotaProbeIntervalSec() int {
	n := int(s.GetInt(AccountQuotaProbeIntervalSec))
	if n <= 0 {
		return 900
	}
	return n
}
func (s *Service) AccountDefaultClientID() string {
	return firstNonEmpty(s.GetString(AccountDefaultClientID), "app_LlGpXReQgckcGGUo2JrYvtJK")
}

// -- api --
func (s *Service) V1APIKey() string { return s.GetString(APIKey) }

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
