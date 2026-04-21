// Package scheduler 负责 chatgpt.com 账号的并发安全调度。
//
// 核心规则(参考 RISK_AND_SAAS.md):
//  1. 一号一锁:同账号同时只允许 1 个请求占用(Redis SETNX)。
//  2. 最小间隔:同账号相邻请求 >= min_interval_sec。
//  3. 每日配额:today_used_count < daily_image_quota * daily_usage_ratio。
//  4. 状态机:healthy -> warned -> throttled -> suspicious -> dead,冷却过期自动恢复。
//  5. 选择策略:status=healthy + cooldown 到期 + last_used_at 最早的优先。
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/432539/gpt2api/internal/account"
	"github.com/432539/gpt2api/internal/config"
	"github.com/432539/gpt2api/internal/proxy"
	"github.com/432539/gpt2api/pkg/lock"
	"github.com/432539/gpt2api/pkg/logger"

	"go.uber.org/zap"
)

// ErrNoAvailable 没有任何账号可用。
var ErrNoAvailable = errors.New("scheduler: no available account")

// Lease 代表一次账号占用的租约。
type Lease struct {
	Account     *account.Account
	AuthToken   string // 已解密
	ProxyURL    string // 已带密码
	ProxyID     uint64
	DeviceID    string
	SessionID   string // oai_session_id(按账号稳定)
	Cookies     string // oai_account_cookies 解密后的 JSON 字符串,让 runner 与 probe 共用浏览器态
	lockKey     string
	lockToken   string
	releaseFunc func(context.Context) error
}

// Release 释放锁并更新账号 last_used_at / today_used。
func (l *Lease) Release(ctx context.Context) error {
	if l.releaseFunc != nil {
		return l.releaseFunc(ctx)
	}
	return nil
}

// RuntimeParams 调度器运行期可热更的参数。
//   - 由外部 settings.Service 提供回调,每次读都取最新值;
//   - 回调未注入时回退到 cfg 的静态值。
type RuntimeParams struct {
	// 为 nil 时 Scheduler 使用 cfg 里的静态值。
	Cooldown429Sec func() int
	WarnedPauseHrs func() int
	// QueueWaitSec 拿不到空闲账号时最长排队等待秒数,≤0 表示不排队(老语义)。
	QueueWaitSec func() int
	// ImageExploreRatio 控制 image 调度里留给 unknown/新号/冷号的探索比例。
	// 0=关闭探索,0.2=约 20% 探索;未注入时回退 0.2。
	ImageExploreRatio func() float64
}

// Scheduler 账号调度器。
type Scheduler struct {
	accSvc      *account.Service
	proxySvc    *proxy.Service
	lock        *lock.RedisLock
	cfg         config.SchedulerConfig
	rt          RuntimeParams
	dispatchLim int // 一次 Dispatch 扫描的候选数上限,默认 50

	// 单账号并发信号量:限制同账号同时生图数,超出排队等待
	semMu sync.Mutex
	sems  map[uint64]chan struct{} // accountID -> buffered channel(size = maxConcurrent)

	// 内存候选池 + round-robin 轮转
	poolMu      sync.Mutex
	pool        []*account.Account // 当前可调度候选(定期从 DB 刷新)
	poolIdx     int                // 轮转指针
	poolRefresh time.Time          // 上次刷新时间
}

func New(
	accSvc *account.Service,
	proxySvc *proxy.Service,
	rl *lock.RedisLock,
	cfg config.SchedulerConfig,
) *Scheduler {
	if cfg.LockTTLSec <= 0 {
		cfg.LockTTLSec = 180
	}
	if cfg.MinIntervalSec <= 0 {
		cfg.MinIntervalSec = 5
	}
	if cfg.Cooldown429Sec <= 0 {
		cfg.Cooldown429Sec = 300
	}
	if cfg.MaxConcurrentPerAccount <= 0 {
		cfg.MaxConcurrentPerAccount = 3
	}
	return &Scheduler{
		accSvc: accSvc, proxySvc: proxySvc, lock: rl, cfg: cfg,
		dispatchLim: 50,
		sems:        make(map[uint64]chan struct{}),
	}
}

// SetRuntime 注入运行期可热更的参数。建议在 main 里一次性设置:
//
//	sched.SetRuntime(scheduler.RuntimeParams{
//	    DailyUsageRatio: settingsSvc.DailyUsageRatio,
//	    Cooldown429Sec:  settingsSvc.Cooldown429Sec,
//	    WarnedPauseHrs:  settingsSvc.WarnedPauseHours,
//	})
func (s *Scheduler) SetRuntime(p RuntimeParams) { s.rt = p }

// SetDispatchLimit 设置每次 Dispatch 扫描的候选账号上限。
// 默认 50;账号池特别大时可适当提高。
func (s *Scheduler) SetDispatchLimit(n int) {
	if n > 0 {
		s.dispatchLim = n
	}
}

func (s *Scheduler) cooldown429() time.Duration {
	if s.rt.Cooldown429Sec != nil {
		if v := s.rt.Cooldown429Sec(); v > 0 {
			return time.Duration(v) * time.Second
		}
	}
	return time.Duration(s.cfg.Cooldown429Sec) * time.Second
}
func (s *Scheduler) warnedPause() time.Duration {
	if s.rt.WarnedPauseHrs != nil {
		if v := s.rt.WarnedPauseHrs(); v > 0 {
			return time.Duration(v) * time.Hour
		}
	}
	return time.Duration(s.cfg.WarnedPauseHours) * time.Hour
}

// queueWait 拿不到账号时的最长排队等待时间。
// 返回 0 表示关闭排队(立即返回 ErrNoAvailable)。
func (s *Scheduler) queueWait() time.Duration {
	if s.rt.QueueWaitSec != nil {
		if v := s.rt.QueueWaitSec(); v >= 0 {
			return time.Duration(v) * time.Second
		}
	}
	return 120 * time.Second
}

func (s *Scheduler) imageExploreRatio() float64 {
	if s.rt.ImageExploreRatio != nil {
		v := s.rt.ImageExploreRatio()
		if v < 0 {
			return 0
		}
		if v > 0.8 {
			return 0.8
		}
		return v
	}
	return 0.2
}

// Dispatch 为本次请求挑选一个账号并加锁。调用方必须 defer lease.Release(ctx)。
//
// 语义(一号一任务 + 排队):
//   - 同账号同时只允许 1 个请求持有 Redis 锁(acct:lock:{id},SETNX+TTL)。
//   - 扫一遍所有 candidate 都被锁住 / 不满足 min_interval / 日配额时,
//     不立即返回失败,而是按指数退避轮询重试,直到拿到锁或超过 queueWait。
//   - queueWait=0 时退化为老语义(扫一次,失败即返回 ErrNoAvailable)。
func (s *Scheduler) Dispatch(ctx context.Context, modelType string) (*Lease, error) {
	return s.DispatchWithExclude(ctx, modelType, nil)
}

// DispatchWithExclude 与 Dispatch 相同,但排除指定账号 ID(用于跨账号重试时
// 跳过已尝试失败的账号)。
func (s *Scheduler) DispatchWithExclude(ctx context.Context, modelType string, excludeIDs map[uint64]struct{}) (*Lease, error) {
	deadline := time.Now().Add(s.queueWait())

	const (
		minBackoff = 200 * time.Millisecond
		maxBackoff = 2 * time.Second
	)
	backoff := minBackoff

	attempt := 0
	start := time.Now()

	for {
		attempt++
		lease, err := s.tryDispatchOnce(ctx, modelType, excludeIDs)
		if err == nil {
			if attempt > 1 {
				logger.L().Info("scheduler queued dispatch ok",
					zap.Int("attempt", attempt),
					zap.Duration("waited", time.Since(start)),
					zap.Uint64("account_id", lease.Account.ID))
			}
			return lease, nil
		}
		if !errors.Is(err, ErrNoAvailable) {
			return nil, err
		}

		// 所有候选都忙或不就绪:排队等待。
		if !time.Now().Before(deadline) {
			return nil, ErrNoAvailable
		}
		wait := backoff
		if remain := time.Until(deadline); remain < wait {
			wait = remain
		}
		if wait <= 0 {
			return nil, ErrNoAvailable
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		// 指数退避(×1.5)
		backoff += backoff / 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// poolTTL 候选池缓存有效期。在此期间不查 DB,直接用内存轮转。
const poolTTL = 10 * time.Second

// refreshPool 从 DB 刷新候选池(有缓存)。
func (s *Scheduler) refreshPool(ctx context.Context, modelType string, force bool) {
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	if !force && time.Since(s.poolRefresh) < poolTTL && len(s.pool) > 0 {
		return
	}
	dao := s.accSvc.DAO()
	candidates, err := dao.ListDispatchableWithOptions(ctx, s.dispatchLim, account.DispatchOptions{
		ModelType:         modelType,
		ImageExploreRatio: s.imageExploreRatio(),
	})
	if err != nil {
		logger.L().Warn("scheduler refresh pool failed", zap.Error(err))
		return
	}
	s.pool = candidates
	if s.poolIdx >= len(candidates) {
		s.poolIdx = 0
	}
	s.poolRefresh = time.Now()
}

// tryDispatchOnce 从内存候选池 round-robin 选号。
// 遍历一圈,每个候选尝试 acquireSem;全部忙/排除时返回 ErrNoAvailable。
func (s *Scheduler) tryDispatchOnce(ctx context.Context, modelType string, excludeIDs map[uint64]struct{}) (*Lease, error) {
	s.refreshPool(ctx, modelType, false)

	s.poolMu.Lock()
	pool := s.pool
	startIdx := s.poolIdx
	s.poolMu.Unlock()

	if len(pool) == 0 {
		return nil, ErrNoAvailable
	}

	// 从 poolIdx 开始遍历一圈
	for i := 0; i < len(pool); i++ {
		idx := (startIdx + i) % len(pool)
		acc := pool[idx]

		if _, excluded := excludeIDs[acc.ID]; excluded {
			continue
		}

		lease, err := s.tryLock(ctx, acc)
		if err == nil {
			// 成功:推进指针到下一个
			s.poolMu.Lock()
			s.poolIdx = (idx + 1) % len(pool)
			s.poolMu.Unlock()
			return lease, nil
		}
		if errors.Is(err, lock.ErrNotAcquired) {
			continue
		}
		logger.L().Warn("scheduler tryLock error",
			zap.Uint64("account_id", acc.ID), zap.Error(err))
	}

	// 遍历一圈都没选到,强制刷新池再试一次
	s.refreshPool(ctx, modelType, true)
	return nil, ErrNoAvailable
}

// acquireSem 获取账号并发信号量,超出 maxConcurrent 时阻塞等待。
// 返回的 release 函数必须在请求完成后调用。
func (s *Scheduler) acquireSem(ctx context.Context, accountID uint64) (release func(), err error) {
	s.semMu.Lock()
	sem, ok := s.sems[accountID]
	if !ok {
		sem = make(chan struct{}, s.cfg.MaxConcurrentPerAccount)
		s.sems[accountID] = sem
	}
	s.semMu.Unlock()

	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Scheduler) tryLock(ctx context.Context, acc *account.Account) (*Lease, error) {
	key := fmt.Sprintf("acct:lock:%d", acc.ID)
	token := uuid.NewString()

	// 获取并发信号量(限制同账号同时生图数)
	semRelease, err := s.acquireSem(ctx, acc.ID)
	if err != nil {
		return nil, err
	}

	authToken, err := s.accSvc.DecryptAuthToken(acc)
	if err != nil {
		semRelease()
		return nil, fmt.Errorf("decrypt auth_token: %w", err)
	}

	// 首次使用时为账号补发一个持久化的 oai_device_id(导入时常为空)。
	// chatgpt.com 要求请求头带 oai-device-id,等同于浏览器首访拿到的 oai-did cookie;
	// 一次生成后持久化,账号绑定的"设备身份"保持稳定,避免每次换 id 触发风控。
	deviceID := acc.OAIDeviceID
	if deviceID == "" {
		gen := uuid.NewString()
		if fixed, err := s.accSvc.DAO().EnsureDeviceID(ctx, acc.ID, gen); err == nil && fixed != "" {
			deviceID = fixed
			acc.OAIDeviceID = fixed
		} else {
			deviceID = gen
		}
	}

	// oai_session_id:真实浏览器是"每打开页面生成一次"。为了保持账号行为稳定
	// (风控倾向于把频繁变换 session_id 的账号识别为脚本),我们按账号持久化,
	// 与 device_id 同策略。
	sessionID := acc.OAISessionID
	if sessionID == "" {
		gen := uuid.NewString()
		if fixed, err := s.accSvc.DAO().EnsureSessionID(ctx, acc.ID, gen); err == nil && fixed != "" {
			sessionID = fixed
			acc.OAISessionID = fixed
		} else {
			sessionID = gen
		}
	}

	var proxyURL string
	var proxyID uint64
	if b, _ := s.accSvc.GetBinding(ctx, acc.ID); b != nil {
		p, err := s.proxySvc.Get(ctx, b.ProxyID)
		if err == nil && p != nil && p.Enabled {
			if u, err := s.proxySvc.BuildURL(p); err == nil {
				proxyURL = u
				proxyID = p.ID
			}
		}
	}

	var cookies string
	if v, err := s.accSvc.DecryptCookies(ctx, acc.ID); err == nil {
		cookies = v
	} else {
		logger.L().Warn("scheduler decrypt account cookies failed",
			zap.Uint64("account_id", acc.ID), zap.Error(err))
	}

	accCopy := acc

	// 立即更新 last_used_at，让并发请求看到最新排序，避免多个请求选同一个号
	today := truncateDay(time.Now())
	_ = s.accSvc.DAO().MarkUsed(ctx, accCopy.ID, today)

	lease := &Lease{
		Account:   accCopy,
		AuthToken: authToken,
		ProxyURL:  proxyURL,
		ProxyID:   proxyID,
		DeviceID:  deviceID,
		SessionID: sessionID,
		Cookies:   cookies,
		lockKey:   key,
		lockToken: token,
	}
	lease.releaseFunc = func(c context.Context) error {
		semRelease() // 释放并发信号量
		return nil
	}
	return lease, nil
}

// MarkRateLimited 上游 429:标记账号冷却并降级状态。
func (s *Scheduler) MarkRateLimited(ctx context.Context, accountID uint64) {
	cooldown := time.Now().Add(s.cooldown429())
	_ = s.accSvc.DAO().SetStatus(ctx, accountID, account.StatusThrottled, &cooldown)
}

// MarkWarned 上游返回 suspicious 横幅时降级。
func (s *Scheduler) MarkWarned(ctx context.Context, accountID uint64) {
	pause := time.Now().Add(s.warnedPause())
	_ = s.accSvc.DAO().SetStatus(ctx, accountID, account.StatusWarned, &pause)
}

// MarkDead 账号彻底不可用(403/token 失效)。
func (s *Scheduler) MarkDead(ctx context.Context, accountID uint64) {
	_ = s.accSvc.DAO().SetStatus(ctx, accountID, account.StatusDead, nil)
}

// RestoreHealthy 调度成功后回归健康(仅对 throttled 且冷却到期有效,
// 简单起见此处不强检查,由运维侧按需恢复)。
func (s *Scheduler) RestoreHealthy(ctx context.Context, accountID uint64) {
	_ = s.accSvc.DAO().SetStatus(ctx, accountID, account.StatusHealthy, nil)
}

// RecordIMG2Outcome 记录真实图像请求后的 IMG2 协议命中画像,供后续 image 调度排序。
func (s *Scheduler) RecordIMG2Outcome(ctx context.Context, accountID uint64, outcome string) {
	_ = s.accSvc.DAO().RecordIMG2Outcome(ctx, accountID, outcome)
}

// RecordIMG2Delivery 记录 IMG2 协议命中后的交付结果。
func (s *Scheduler) RecordIMG2Delivery(ctx context.Context, accountID uint64, status string) {
	_ = s.accSvc.DAO().RecordIMG2Delivery(ctx, accountID, status)
}

// AccountBinding 查询账号当前绑定的代理。
func (s *Scheduler) AccountBinding(ctx context.Context, accountID uint64) (*account.Binding, error) {
	return s.accSvc.GetBinding(ctx, accountID)
}

// SwitchProxy 将账号切换到另一个代理(排除当前失效代理)。
// 返回新代理的完整 URL。代理池无可用代理时返回空串(不阻断请求)。
func (s *Scheduler) SwitchProxy(ctx context.Context, accountID, currentProxyID uint64) (newProxyURL string, newProxyID uint64) {
	newID, err := s.accSvc.DAO().SwitchProxy(ctx, accountID, currentProxyID)
	if err != nil {
		logger.L().Warn("scheduler switch proxy failed",
			zap.Uint64("account_id", accountID),
			zap.Uint64("old_proxy_id", currentProxyID),
			zap.Error(err))
		return "", 0
	}
	p, err := s.proxySvc.Get(ctx, newID)
	if err != nil || p == nil || !p.Enabled {
		return "", 0
	}
	u, err := s.proxySvc.BuildURL(p)
	if err != nil {
		return "", 0
	}
	logger.L().Info("scheduler switched proxy",
		zap.Uint64("account_id", accountID),
		zap.Uint64("old_proxy_id", currentProxyID),
		zap.Uint64("new_proxy_id", newID))
	return u, newID
}

// ------ helpers ------

func truncateDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day()
}
