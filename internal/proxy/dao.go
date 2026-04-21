package proxy

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
)

var ErrNotFound = errors.New("proxy: not found")

type DAO struct{ db *sqlx.DB }

func NewDAO(db *sqlx.DB) *DAO { return &DAO{db: db} }

func (d *DAO) Create(ctx context.Context, p *Proxy) (uint64, error) {
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO proxies (scheme, host, port, username, password_enc, country, isp, health_score, enabled, remark)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Scheme, p.Host, p.Port, p.Username, p.PasswordEnc,
		p.Country, p.ISP, p.HealthScore, p.Enabled, p.Remark,
	)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return uint64(id), nil
}

func (d *DAO) GetByID(ctx context.Context, id uint64) (*Proxy, error) {
	var p Proxy
	err := d.db.GetContext(ctx, &p,
		`SELECT * FROM proxies WHERE id = ? AND deleted_at IS NULL`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &p, err
}

func (d *DAO) List(ctx context.Context, offset, limit int) ([]*Proxy, int64, error) {
	var total int64
	if err := d.db.GetContext(ctx, &total,
		`SELECT COUNT(*) FROM proxies WHERE deleted_at IS NULL`); err != nil {
		return nil, 0, err
	}
	rows := make([]*Proxy, 0, limit)
	err := d.db.SelectContext(ctx, &rows,
		`SELECT * FROM proxies WHERE deleted_at IS NULL ORDER BY id DESC LIMIT ? OFFSET ?`,
		limit, offset)
	return rows, total, err
}

// ListAllEnabled 返回所有启用且未删除的代理,用于后台健康探测。
func (d *DAO) ListAllEnabled(ctx context.Context) ([]*Proxy, error) {
	rows := make([]*Proxy, 0, 64)
	err := d.db.SelectContext(ctx, &rows,
		`SELECT * FROM proxies WHERE deleted_at IS NULL AND enabled = 1 ORDER BY id ASC`)
	return rows, err
}

func (d *DAO) Update(ctx context.Context, p *Proxy) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE proxies
         SET scheme=?, host=?, port=?, username=?, password_enc=?, country=?, isp=?,
             enabled=?, remark=?
         WHERE id = ? AND deleted_at IS NULL`,
		p.Scheme, p.Host, p.Port, p.Username, p.PasswordEnc, p.Country, p.ISP,
		p.Enabled, p.Remark, p.ID,
	)
	return err
}

func (d *DAO) SoftDelete(ctx context.Context, id uint64) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE proxies SET deleted_at = ? WHERE id = ?`, time.Now(), id)
	return err
}

// SetEnabled 启用/禁用代理。
func (d *DAO) SetEnabled(ctx context.Context, id uint64, enabled bool) error {
	v := 0
	if enabled {
		v = 1
	}
	_, err := d.db.ExecContext(ctx,
		`UPDATE proxies SET enabled = ? WHERE id = ?`, v, id)
	return err
}

// CleanupDisabledBefore 软删除在 before 之前已禁用的代理(自动清理过期代理)。
func (d *DAO) CleanupDisabledBefore(ctx context.Context, before time.Time) (int64, error) {
	res, err := d.db.ExecContext(ctx, `
		UPDATE proxies SET deleted_at = NOW()
		WHERE enabled = 0 AND deleted_at IS NULL AND updated_at < ?`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ListBoundAccountIDs 返回绑定了指定代理的所有账号 ID。
func (d *DAO) ListBoundAccountIDs(ctx context.Context, proxyID uint64) ([]uint64, error) {
	var ids []uint64
	err := d.db.SelectContext(ctx, &ids,
		`SELECT account_id FROM account_proxy_bindings WHERE proxy_id = ?`, proxyID)
	return ids, err
}

// FindByEndpoint 按 scheme+host+port+username 精确查存活记录,用于批量导入去重。
func (d *DAO) FindByEndpoint(ctx context.Context, scheme, host string, port int, username string) (*Proxy, error) {
	var p Proxy
	err := d.db.GetContext(ctx, &p, `
SELECT * FROM proxies
 WHERE scheme = ? AND host = ? AND port = ? AND username = ?
   AND deleted_at IS NULL
 LIMIT 1`, scheme, host, port, username)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &p, err
}

func (d *DAO) UpdateHealth(ctx context.Context, id uint64, score int, lastErr string) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE proxies SET health_score=?, last_probe_at=?, last_error=? WHERE id=?`,
		score, time.Now(), lastErr, id)
	return err
}
