package account

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

var ErrNotFound = errors.New("账号不存在")

type DAO struct{ db *sqlx.DB }

func NewDAO(db *sqlx.DB) *DAO { return &DAO{db: db} }

// DB 暴露底层 handle 给刷新器 / 探测器用于直接原子更新(少量场景)。
func (d *DAO) DB() *sqlx.DB { return d.db }

// fill 填充非 db 列的辅助字段。
func fill(a *Account) {
	if a == nil {
		return
	}
	a.HasRT = a.RefreshTokenEnc.Valid && a.RefreshTokenEnc.String != ""
	a.HasST = a.SessionTokenEnc.Valid && a.SessionTokenEnc.String != ""
}

func fillAll(rows []*Account) {
	for _, r := range rows {
		fill(r)
	}
}

func (d *DAO) Create(ctx context.Context, a *Account) (uint64, error) {
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO oai_accounts
         (email, auth_token_enc, refresh_token_enc, session_token_enc, token_expires_at,
          oai_session_id, oai_device_id, client_id, chatgpt_account_id, account_type,
          subscription_type, status, notes)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Email, a.AuthTokenEnc, a.RefreshTokenEnc, a.SessionTokenEnc, a.TokenExpiresAt,
		a.OAISessionID, a.OAIDeviceID, a.ClientID, a.ChatGPTAccountID, a.AccountType,
		a.SubscriptionType, a.Status, a.Notes,
	)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return uint64(id), nil
}

func (d *DAO) GetByID(ctx context.Context, id uint64) (*Account, error) {
	var a Account
	err := d.db.GetContext(ctx, &a,
		`SELECT * FROM oai_accounts WHERE id = ? AND deleted_at IS NULL`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	fill(&a)
	return &a, err
}

// GetByEmail 精确找;未命中返回 nil, nil(方便 importer 判 upsert)。
func (d *DAO) GetByEmail(ctx context.Context, email string) (*Account, error) {
	var a Account
	err := d.db.GetContext(ctx, &a,
		`SELECT * FROM oai_accounts WHERE email = ? AND deleted_at IS NULL LIMIT 1`, email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	fill(&a)
	return &a, nil
}

func (d *DAO) List(ctx context.Context, status string, keyword string, offset, limit int) ([]*Account, int64, error) {
	var total int64
	var err error
	var rows []*Account

	where := "deleted_at IS NULL"
	args := []interface{}{}
	if status != "" {
		where += " AND status = ?"
		args = append(args, status)
	}
	if keyword != "" {
		where += " AND (email LIKE ? OR notes LIKE ?)"
		like := "%" + keyword + "%"
		args = append(args, like, like)
	}

	if err = d.db.GetContext(ctx, &total, "SELECT COUNT(*) FROM oai_accounts WHERE "+where, args...); err != nil {
		return nil, 0, err
	}
	argsPage := append([]interface{}{}, args...)
	argsPage = append(argsPage, limit, offset)
	err = d.db.SelectContext(ctx, &rows,
		"SELECT * FROM oai_accounts WHERE "+where+" ORDER BY id DESC LIMIT ? OFFSET ?", argsPage...)
	fillAll(rows)
	return rows, total, err
}

// DispatchOptions 控制账号候选排序。普通 chat 仍按 last_used_at；image 会额外使用
// image2 能力探测与 IMG2 命中画像做「利用 + 探索」排序。
type DispatchOptions struct {
	ModelType         string
	ImageExploreRatio float64       // 0=关闭探索,0.2=约 20% 探索,最大建议 0.8
	ImageExploreStale time.Duration // 多久没尝试的账号进入探索池;<=0 默认 12h
}

// ListDispatchable 调度器专用:返回 status=healthy 且 cooldown 到期、AT 未过期的候选。
// 保留旧签名给非 image 调用使用;image 默认使用 20% 探索比例。
func (d *DAO) ListDispatchable(ctx context.Context, limit int, modelType ...string) ([]*Account, error) {
	opt := DispatchOptions{}
	if len(modelType) > 0 {
		opt.ModelType = modelType[0]
	}
	if opt.ModelType == "image" {
		opt.ImageExploreRatio = 0.2
	}
	return d.ListDispatchableWithOptions(ctx, limit, opt)
}

// ListDispatchableWithOptions 是带调度策略参数的候选查询入口。
//
// image 请求会额外参考 image_capability_status、IMG2 协议命中画像与交付画像排序:
//   - /backend-api/models 探测为 enabled 的账号优先;
//   - 连续 miss 少的账号优先;
//   - 签名 URL 交付成功率高的账号优先;
//   - 历史 IMG2 协议命中率高的账号优先;
//   - 按 ImageExploreRatio 给 unknown/新号/长时间未尝试号留探索位。
//
// 注意这里不把 image_capability_status != enabled 的账号过滤掉,只做排序。原因是
// /backend-api/models 也可能短时失败/缓存缺失,真实 f/conversation 执行结果才是最终判据。
func (d *DAO) ListDispatchableWithOptions(ctx context.Context, limit int, opt DispatchOptions) ([]*Account, error) {
	if opt.ModelType == "image" {
		return d.listDispatchableImage(ctx, limit, opt)
	}

	rows := make([]*Account, 0, limit)
	now := time.Now()
	query := `SELECT * FROM oai_accounts
         WHERE deleted_at IS NULL AND status = 'healthy'
           AND (cooldown_until IS NULL OR cooldown_until <= ?)
           AND (token_expires_at IS NULL OR token_expires_at > ?)
         ORDER BY CASE WHEN last_used_at IS NULL THEN 0 ELSE 1 END, last_used_at ASC
         LIMIT ?`
	err := d.db.SelectContext(ctx, &rows, query, now, now, limit)
	fillAll(rows)
	return rows, err
}

// listDispatchableImage 使用「利用 + 探索」混合排序。
//
// 利用(exploitation):优先 models 探测 enabled、连续 miss 少、交付成功率高、IMG2 协议命中率高的账号。
// 探索(exploration):给 unknown/新号/长时间未尝试号保留可配置比例,避免头部账号被打爆、
// 尾部账号永远没有重新学习机会。
func (d *DAO) listDispatchableImage(ctx context.Context, limit int, opt DispatchOptions) ([]*Account, error) {
	if limit <= 0 {
		return nil, nil
	}
	exploreRatio := opt.ImageExploreRatio
	if exploreRatio < 0 {
		exploreRatio = 0
	}
	if exploreRatio > 0.8 {
		exploreRatio = 0.8
	}
	exploreStale := opt.ImageExploreStale
	if exploreStale <= 0 {
		exploreStale = 12 * time.Hour
	}

	now := time.Now()
	exploit := make([]*Account, 0, limit)
	// 利用池排序:
	//   1) enabled > unknown > error (能力状态)
	//   2) consecutive_miss = 0 的优先(没连续失败的)
	//   3) last_used_at 最早的优先(轮转均衡,核心!)
	// 这样所有健康账号会均匀轮转,而不是永远选同一个"最优"号
	exploitQuery := `SELECT * FROM oai_accounts
         WHERE deleted_at IS NULL AND status = 'healthy'
           AND (cooldown_until IS NULL OR cooldown_until <= ?)
           AND (token_expires_at IS NULL OR token_expires_at > ?)
         ORDER BY
           CASE image_capability_status
             WHEN 'enabled' THEN 0
             WHEN 'unknown' THEN 1
             WHEN 'error'   THEN 2
             ELSE 3
           END ASC,
           CASE WHEN img2_consecutive_miss > 3 THEN 1 ELSE 0 END ASC,
           CASE WHEN last_used_at IS NULL THEN 0 ELSE 1 END,
           last_used_at ASC
         LIMIT ?`
	if err := d.db.SelectContext(ctx, &exploit, exploitQuery, now, now, limit); err != nil {
		return nil, err
	}

	exploreLimit := int(float64(limit)*exploreRatio + 0.999)
	if exploreRatio > 0 && exploreLimit < 1 {
		exploreLimit = 1
	}
	if exploreLimit > limit {
		exploreLimit = limit
	}
	explore := make([]*Account, 0, exploreLimit)
	if exploreLimit > 0 {
		exploreThreshold := now.Add(-exploreStale)
		exploreQuery := `SELECT * FROM oai_accounts
         WHERE deleted_at IS NULL AND status = 'healthy'
           AND (cooldown_until IS NULL OR cooldown_until <= ?)
           AND (token_expires_at IS NULL OR token_expires_at > ?)
           AND (image_capability_status <> 'enabled'
                OR img2_last_attempt_at IS NULL
                OR img2_last_attempt_at <= ?)
         ORDER BY
           CASE WHEN img2_last_attempt_at IS NULL THEN 0 ELSE 1 END ASC,
           img2_last_attempt_at ASC,
           CASE image_capability_status
             WHEN 'unknown' THEN 0
             WHEN 'error'   THEN 1
             WHEN 'enabled' THEN 2
             ELSE 3
           END ASC,
           CASE WHEN last_used_at IS NULL THEN 0 ELSE 1 END,
           last_used_at ASC
         LIMIT ?`
		if err := d.db.SelectContext(ctx, &explore, exploreQuery, now, now, exploreThreshold, exploreLimit); err != nil {
			return nil, err
		}
	}

	rows := mergeDispatchCandidates(exploit, explore, limit, exploreRatio)
	fillAll(rows)
	return rows, nil
}

func mergeDispatchCandidates(exploit, explore []*Account, limit int, exploreRatio float64) []*Account {
	out := make([]*Account, 0, limit)
	seen := make(map[uint64]struct{}, limit)
	add := func(a *Account) bool {
		if a == nil {
			return false
		}
		if _, ok := seen[a.ID]; ok {
			return false
		}
		seen[a.ID] = struct{}{}
		out = append(out, a)
		return true
	}

	ei, xi := 0, 0
	addExplore := func() bool {
		for xi < len(explore) && len(out) < limit {
			a := explore[xi]
			xi++
			if add(a) {
				return true
			}
		}
		return false
	}
	addExploit := func() bool {
		for ei < len(exploit) && len(out) < limit {
			a := exploit[ei]
			ei++
			if add(a) {
				return true
			}
		}
		return false
	}

	if exploreRatio < 0 {
		exploreRatio = 0
	}
	if exploreRatio > 0.8 {
		exploreRatio = 0.8
	}
	exploreDebt := 0.0

	for len(out) < limit && (ei < len(exploit) || xi < len(explore)) {
		if exploreRatio > 0 {
			exploreDebt += exploreRatio
		}
		if exploreDebt >= 1 && addExplore() {
			exploreDebt -= 1
			continue
		}
		if addExploit() {
			continue
		}
		if addExplore() {
			if exploreDebt >= 1 {
				exploreDebt -= 1
			}
			continue
		}
		break
	}
	return out
}

// ListNeedRefresh 返回需要预刷新的账号(AT 将在 aheadSec 秒内过期)。
// 按 token_expires_at 升序,最快过期的先刷。
func (d *DAO) ListNeedRefresh(ctx context.Context, aheadSec int, limit int) ([]*Account, error) {
	rows := make([]*Account, 0, limit)
	threshold := time.Now().Add(time.Duration(aheadSec) * time.Second)
	err := d.db.SelectContext(ctx, &rows,
		`SELECT * FROM oai_accounts
         WHERE deleted_at IS NULL
           AND status <> 'dead'
           AND (refresh_token_enc IS NOT NULL OR session_token_enc IS NOT NULL)
           AND token_expires_at IS NOT NULL
           AND token_expires_at <= ?
         ORDER BY token_expires_at ASC
         LIMIT ?`, threshold, limit)
	fillAll(rows)
	return rows, err
}

// ListNeedProbeQuota 返回需要探测图片额度的账号(上次探测超过 minIntervalSec 秒,或从未探测过)。
func (d *DAO) ListNeedProbeQuota(ctx context.Context, minIntervalSec int, limit int) ([]*Account, error) {
	rows := make([]*Account, 0, limit)
	threshold := time.Now().Add(-time.Duration(minIntervalSec) * time.Second)
	err := d.db.SelectContext(ctx, &rows,
		`SELECT * FROM oai_accounts
         WHERE deleted_at IS NULL
           AND status = 'healthy'
           AND (token_expires_at IS NULL OR token_expires_at > NOW())
           AND (image_quota_updated_at IS NULL OR image_quota_updated_at <= ?)
         ORDER BY CASE WHEN image_quota_updated_at IS NULL THEN 0 ELSE 1 END,
                  image_quota_updated_at ASC
         LIMIT ?`, threshold, limit)
	fillAll(rows)
	return rows, err
}

// ListAllActiveIDs 用于批量刷新 / 批量探测:返回未软删的所有 id。
func (d *DAO) ListAllActiveIDs(ctx context.Context) ([]uint64, error) {
	ids := make([]uint64, 0, 128)
	err := d.db.SelectContext(ctx, &ids,
		`SELECT id FROM oai_accounts WHERE deleted_at IS NULL ORDER BY id ASC`)
	return ids, err
}

func (d *DAO) Update(ctx context.Context, a *Account) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE oai_accounts
         SET email=?, auth_token_enc=?, refresh_token_enc=?, session_token_enc=?, token_expires_at=?,
             oai_session_id=?, oai_device_id=?, client_id=?, chatgpt_account_id=?, account_type=?,
             subscription_type=?, status=?, notes=?
         WHERE id = ? AND deleted_at IS NULL`,
		a.Email, a.AuthTokenEnc, a.RefreshTokenEnc, a.SessionTokenEnc, a.TokenExpiresAt,
		a.OAISessionID, a.OAIDeviceID, a.ClientID, a.ChatGPTAccountID, a.AccountType,
		a.SubscriptionType, a.Status, a.Notes, a.ID,
	)
	return err
}

// SetSubscriptionType 更新账号的订阅类型（pro/plus/free/team 等）。
func (d *DAO) SetSubscriptionType(ctx context.Context, id uint64, subType string) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE oai_accounts SET subscription_type = ? WHERE id = ? AND deleted_at IS NULL`, subType, id)
	return err
}

func (d *DAO) SoftDelete(ctx context.Context, id uint64) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE oai_accounts SET deleted_at = ? WHERE id = ?`, time.Now(), id)
	return err
}

// PurgeSoftDeleted 真删除所有已软删除的账号及其关联数据。返回删除行数。
func (d *DAO) PurgeSoftDeleted(ctx context.Context) (int64, error) {
	// 先删关联表
	d.db.ExecContext(ctx, `DELETE FROM account_proxy_bindings WHERE account_id IN (SELECT id FROM oai_accounts WHERE deleted_at IS NOT NULL)`)
	d.db.ExecContext(ctx, `DELETE FROM oai_account_cookies WHERE account_id IN (SELECT id FROM oai_accounts WHERE deleted_at IS NOT NULL)`)
	res, err := d.db.ExecContext(ctx, `DELETE FROM oai_accounts WHERE deleted_at IS NOT NULL`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SoftDeleteByStatus 按状态批量软删。status 为空时删除全部(调用方需二次确认)。
// 返回删除行数。
func (d *DAO) SoftDeleteByStatus(ctx context.Context, status string) (int64, error) {
	now := time.Now()
	if status == "" {
		res, err := d.db.ExecContext(ctx,
			`UPDATE oai_accounts SET deleted_at = ? WHERE deleted_at IS NULL`, now)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		return n, nil
	}
	res, err := d.db.ExecContext(ctx,
		`UPDATE oai_accounts SET deleted_at = ? WHERE deleted_at IS NULL AND status = ?`,
		now, status)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// EnsureDeviceID 确保账号有 oai_device_id。
// 如果当前为空,原子写入给定的 deviceID;返回最终实际的 device_id(已有则原值)。
func (d *DAO) EnsureDeviceID(ctx context.Context, id uint64, deviceID string) (string, error) {
	_, err := d.db.ExecContext(ctx,
		`UPDATE oai_accounts SET oai_device_id = ?
         WHERE id = ? AND deleted_at IS NULL AND (oai_device_id = '' OR oai_device_id IS NULL)`,
		deviceID, id)
	if err != nil {
		return "", err
	}
	// 回读,兼容其他协程并发填写的情形
	var cur string
	if err := d.db.GetContext(ctx, &cur,
		`SELECT oai_device_id FROM oai_accounts WHERE id = ?`, id); err != nil {
		return "", err
	}
	return cur, nil
}

// EnsureSessionID 确保账号有 oai_session_id(按账号稳定复用)。
// 逻辑与 EnsureDeviceID 完全一致,单独一个函数是为了日志/审计区分用途。
func (d *DAO) EnsureSessionID(ctx context.Context, id uint64, sessionID string) (string, error) {
	_, err := d.db.ExecContext(ctx,
		`UPDATE oai_accounts SET oai_session_id = ?
         WHERE id = ? AND deleted_at IS NULL AND (oai_session_id = '' OR oai_session_id IS NULL)`,
		sessionID, id)
	if err != nil {
		return "", err
	}
	var cur string
	if err := d.db.GetContext(ctx, &cur,
		`SELECT oai_session_id FROM oai_accounts WHERE id = ?`, id); err != nil {
		return "", err
	}
	return cur, nil
}

// MarkUsed 更新 last_used_at + 今日计数。today 是当日零点(用于 today_used_date 比较)。
func (d *DAO) MarkUsed(ctx context.Context, id uint64, today time.Time) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE oai_accounts
         SET last_used_at = ?,
             today_used_count = CASE WHEN today_used_date = ? THEN today_used_count + 1 ELSE 1 END,
             today_used_date  = ?
         WHERE id = ?`,
		time.Now(), today, today, id)
	return err
}

// SetStatus 迁移状态,可选 cooldownUntil。
func (d *DAO) SetStatus(ctx context.Context, id uint64, status string, cooldownUntil *time.Time) error {
	if cooldownUntil != nil {
		_, err := d.db.ExecContext(ctx,
			`UPDATE oai_accounts SET status=?, cooldown_until=? WHERE id=?`,
			status, *cooldownUntil, id)
		return err
	}
	_, err := d.db.ExecContext(ctx,
		`UPDATE oai_accounts SET status=?, cooldown_until=NULL WHERE id=?`,
		status, id)
	return err
}

// ApplyRefreshResult 原子更新 AT / RT + 过期时间 + 最近刷新信息。
// newRTEnc 为空字符串表示 RT 没有轮转,保持不变。
func (d *DAO) ApplyRefreshResult(
	ctx context.Context,
	id uint64,
	newATEnc string,
	newRTEnc string,
	expiresAt time.Time,
	source string,
) error {
	var err error
	if newRTEnc != "" {
		_, err = d.db.ExecContext(ctx,
			`UPDATE oai_accounts
             SET auth_token_enc = ?,
                 refresh_token_enc = ?,
                 token_expires_at = ?,
                 last_refresh_at = ?,
                 last_refresh_source = ?,
                 refresh_error = '',
                 status = CASE WHEN status IN ('dead','suspicious') THEN 'healthy' ELSE status END
             WHERE id = ? AND deleted_at IS NULL`,
			newATEnc, newRTEnc, expiresAt, time.Now(), source, id)
	} else {
		_, err = d.db.ExecContext(ctx,
			`UPDATE oai_accounts
             SET auth_token_enc = ?,
                 token_expires_at = ?,
                 last_refresh_at = ?,
                 last_refresh_source = ?,
                 refresh_error = '',
                 status = CASE WHEN status IN ('dead','suspicious') THEN 'healthy' ELSE status END
             WHERE id = ? AND deleted_at IS NULL`,
			newATEnc, expiresAt, time.Now(), source, id)
	}
	return err
}

// RetireExpiredATOnly 把已过期且无 RT/ST 的纯 AT 账号自动标记 dead。
// 返回受影响的行数。
func (d *DAO) RetireExpiredATOnly(ctx context.Context) (int64, error) {
	res, err := d.db.ExecContext(ctx, `
		UPDATE oai_accounts SET status = 'dead', refresh_error = 'AT 已过期且无 RT/ST,自动丢弃'
		WHERE deleted_at IS NULL
		  AND status <> 'dead'
		  AND token_expires_at IS NOT NULL
		  AND token_expires_at < NOW()
		  AND (refresh_token_enc IS NULL OR refresh_token_enc = '')
		  AND (session_token_enc IS NULL OR session_token_enc = '')`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RecordRefreshError 写入刷新失败原因,同时推进 last_refresh_at(避免 pressed-out 重试)。
// markDead=true 标 dead(仅用于确定性不可恢复的情况);false 标 warned(保留重试机会)。
func (d *DAO) RecordRefreshError(ctx context.Context, id uint64, source string, reason string, markDead bool) error {
	status := "warned"
	if markDead {
		status = "dead"
	}
	_, err := d.db.ExecContext(ctx,
		`UPDATE oai_accounts
         SET last_refresh_at = ?, last_refresh_source = ?, refresh_error = ?,
             status = CASE WHEN status = 'healthy' THEN ? ELSE status END
         WHERE id = ? AND deleted_at IS NULL`,
		time.Now(), source, reason, status, id)
	return err
}

// ApplyQuotaResult 更新图片额度探测结果;remaining/total = -1 表示保持原值。
func (d *DAO) ApplyQuotaResult(ctx context.Context, id uint64, remaining, total int, resetAt *time.Time) error {
	q := `UPDATE oai_accounts
          SET image_quota_remaining = CASE WHEN ? < 0 THEN image_quota_remaining ELSE ? END,
              image_quota_total     = CASE WHEN ? < 0 THEN image_quota_total     ELSE ? END,
              image_quota_reset_at  = ?,
              image_quota_updated_at = ?
          WHERE id = ? AND deleted_at IS NULL`
	var reset interface{}
	if resetAt != nil {
		reset = *resetAt
	} else {
		reset = nil
	}
	_, err := d.db.ExecContext(ctx, q, remaining, remaining, total, total, reset, time.Now(), id)
	return err
}

// ApplyImageCapabilityResult 写入 image 能力探测结果。
// status 建议取 unknown/enabled/disabled/error;source 目前主要是 models。
func (d *DAO) ApplyImageCapabilityResult(ctx context.Context, id uint64, status, model, source, detail string, blockedFeatures []string) error {
	if status == "" {
		status = "unknown"
	}
	blockedJSON, _ := json.Marshal(blockedFeatures)
	_, err := d.db.ExecContext(ctx,
		`UPDATE oai_accounts
          SET image_capability_status = ?,
              image_capability_model = ?,
              image_capability_source = ?,
              image_capability_detail = ?,
              image_capability_updated_at = ?,
              image_init_blocked_features = ?
          WHERE id = ? AND deleted_at IS NULL`,
		status, model, source, detail, time.Now(), string(blockedJSON), id)
	return err
}

// RecordIMG2Outcome 记录一次真实 f/conversation 图像请求的 IMG2 命中画像。
// outcome:
//   - hit:          出现 file-service 或 sediment+gen_size_v2 等 IMG2 指纹
//   - preview_only:只拿到 IMG1 preview/sediment 兜底
//   - miss:         真实请求没拿到有效图片结构
func (d *DAO) RecordIMG2Outcome(ctx context.Context, id uint64, outcome string) error {
	now := time.Now()
	switch outcome {
	case "hit":
		_, err := d.db.ExecContext(ctx,
			`UPDATE oai_accounts
              SET img2_hit_count = img2_hit_count + 1,
                  img2_consecutive_miss = 0,
                  img2_last_status = ?,
                  img2_last_hit_at = ?,
                  img2_last_attempt_at = ?
              WHERE id = ? AND deleted_at IS NULL`,
			outcome, now, now, id)
		return err
	case "preview_only":
		_, err := d.db.ExecContext(ctx,
			`UPDATE oai_accounts
              SET img2_preview_only_count = img2_preview_only_count + 1,
                  img2_consecutive_miss = img2_consecutive_miss + 1,
                  img2_last_status = ?,
                  img2_last_attempt_at = ?
              WHERE id = ? AND deleted_at IS NULL`,
			outcome, now, id)
		return err
	case "miss":
		_, err := d.db.ExecContext(ctx,
			`UPDATE oai_accounts
              SET img2_miss_count = img2_miss_count + 1,
                  img2_consecutive_miss = img2_consecutive_miss + 1,
                  img2_last_status = ?,
                  img2_last_attempt_at = ?
              WHERE id = ? AND deleted_at IS NULL`,
			outcome, now, id)
		return err
	default:
		return nil
	}
}

// RecordIMG2Delivery 记录 IMG2 协议命中后的交付结果。
//
// 注意:img2_hit_count 表示「协议/灰度抽中」,这里表示「签名 URL 交付成功」。
// 两者拆开后,调度器可以识别「经常抽中但下载失败」的账号/代理。
func (d *DAO) RecordIMG2Delivery(ctx context.Context, id uint64, status string) error {
	now := time.Now()
	switch status {
	case "success":
		_, err := d.db.ExecContext(ctx,
			`UPDATE oai_accounts
              SET img2_delivery_success_count = img2_delivery_success_count + 1,
                  img2_last_delivery_status = ?,
                  img2_last_delivery_at = ?
              WHERE id = ? AND deleted_at IS NULL`,
			status, now, id)
		return err
	case "partial":
		_, err := d.db.ExecContext(ctx,
			`UPDATE oai_accounts
              SET img2_delivery_partial_count = img2_delivery_partial_count + 1,
                  img2_last_delivery_status = ?,
                  img2_last_delivery_at = ?
              WHERE id = ? AND deleted_at IS NULL`,
			status, now, id)
		return err
	case "fail":
		_, err := d.db.ExecContext(ctx,
			`UPDATE oai_accounts
              SET img2_delivery_fail_count = img2_delivery_fail_count + 1,
                  img2_last_delivery_status = ?,
                  img2_last_delivery_at = ?
              WHERE id = ? AND deleted_at IS NULL`,
			status, now, id)
		return err
	default:
		return nil
	}
}

// ---- cookies ----

func (d *DAO) UpsertCookies(ctx context.Context, accountID uint64, cookieEnc string) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO oai_account_cookies (account_id, cookie_json_enc)
         VALUES (?, ?)
         ON DUPLICATE KEY UPDATE cookie_json_enc = VALUES(cookie_json_enc)`,
		accountID, cookieEnc)
	return err
}

func (d *DAO) GetCookies(ctx context.Context, accountID uint64) (string, error) {
	var enc string
	err := d.db.GetContext(ctx, &enc,
		`SELECT cookie_json_enc FROM oai_account_cookies WHERE account_id = ?`,
		accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return enc, err
}

// ---- bindings ----

func (d *DAO) SetBinding(ctx context.Context, accountID, proxyID uint64) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO account_proxy_bindings (account_id, proxy_id)
         VALUES (?, ?)
         ON DUPLICATE KEY UPDATE proxy_id = VALUES(proxy_id), bound_at = CURRENT_TIMESTAMP`,
		accountID, proxyID)
	return err
}

func (d *DAO) RemoveBinding(ctx context.Context, accountID uint64) error {
	_, err := d.db.ExecContext(ctx,
		`DELETE FROM account_proxy_bindings WHERE account_id = ?`, accountID)
	return err
}

func (d *DAO) GetBinding(ctx context.Context, accountID uint64) (*Binding, error) {
	var b Binding
	err := d.db.GetContext(ctx, &b,
		`SELECT * FROM account_proxy_bindings WHERE account_id = ?`, accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &b, err
}

// LeastBoundProxyID 返回绑定账号数最少的可用代理 ID(用于自动分配)。
// excludeIDs 可以排除指定代理(如刚失效的)。
func (d *DAO) LeastBoundProxyID(ctx context.Context, excludeIDs []uint64) (uint64, error) {
	query := `
		SELECT p.id
		FROM proxies p
		LEFT JOIN account_proxy_bindings b ON b.proxy_id = p.id
		WHERE p.enabled = 1 AND p.deleted_at IS NULL
		  AND p.health_score > 0`
	var args []interface{}
	if len(excludeIDs) > 0 {
		placeholders := make([]string, len(excludeIDs))
		for i, id := range excludeIDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		query += " AND p.id NOT IN (" + strings.Join(placeholders, ",") + ")"
	}
	query += `
		GROUP BY p.id
		ORDER BY COUNT(b.account_id) ASC, p.health_score DESC
		LIMIT 1`
	var proxyID uint64
	err := d.db.GetContext(ctx, &proxyID, query, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errors.New("无可用代理")
	}
	return proxyID, err
}

// SwitchProxy 将账号切换到另一个代理(排除当前代理),返回新代理 ID。
func (d *DAO) SwitchProxy(ctx context.Context, accountID, currentProxyID uint64) (uint64, error) {
	newID, err := d.LeastBoundProxyID(ctx, []uint64{currentProxyID})
	if err != nil {
		return 0, err
	}
	if err := d.SetBinding(ctx, accountID, newID); err != nil {
		return 0, err
	}
	return newID, nil
}
