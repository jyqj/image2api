package model

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"
)

var ErrNotFound = errors.New("model: not found")

type DAO struct{ db *sqlx.DB }

func NewDAO(db *sqlx.DB) *DAO { return &DAO{db: db} }

func (d *DAO) GetBySlug(ctx context.Context, slug string) (*Model, error) {
	var m Model
	err := d.db.GetContext(ctx, &m,
		`SELECT * FROM models WHERE slug = ? AND deleted_at IS NULL`, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &m, err
}

func (d *DAO) ListEnabled(ctx context.Context) ([]*Model, error) {
	rows := make([]*Model, 0, 16)
	err := d.db.SelectContext(ctx, &rows,
		`SELECT * FROM models WHERE enabled = 1 AND deleted_at IS NULL ORDER BY id ASC`)
	return rows, err
}

func (d *DAO) List(ctx context.Context) ([]*Model, error) {
	rows := make([]*Model, 0, 16)
	err := d.db.SelectContext(ctx, &rows,
		`SELECT * FROM models WHERE deleted_at IS NULL ORDER BY id ASC`)
	return rows, err
}

// GetByID 按主键查。
func (d *DAO) GetByID(ctx context.Context, id uint64) (*Model, error) {
	var m Model
	err := d.db.GetContext(ctx, &m,
		`SELECT * FROM models WHERE id = ? AND deleted_at IS NULL`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &m, err
}

// Create 插入一条新模型。slug 唯一冲突由上层判断(返回 MySQL 1062)。
func (d *DAO) Create(ctx context.Context, m *Model) error {
	res, err := d.db.ExecContext(ctx, `
INSERT INTO models
  (slug, type, upstream_model_slug, description, enabled)
VALUES (?,?,?,?,?)`,
		m.Slug, m.Type, m.UpstreamModelSlug, m.Description, m.Enabled,
	)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	m.ID = uint64(id)
	return nil
}

// Update 按 id 全量更新(不改 slug;改 slug 走单独接口更安全)。
func (d *DAO) Update(ctx context.Context, m *Model) error {
	res, err := d.db.ExecContext(ctx, `
UPDATE models SET
  type = ?, upstream_model_slug = ?, description = ?, enabled = ?
WHERE id = ? AND deleted_at IS NULL`,
		m.Type, m.UpstreamModelSlug, m.Description, m.Enabled, m.ID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetEnabled 开关。
func (d *DAO) SetEnabled(ctx context.Context, id uint64, enabled bool) error {
	res, err := d.db.ExecContext(ctx,
		`UPDATE models SET enabled = ? WHERE id = ? AND deleted_at IS NULL`,
		enabled, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SoftDelete 软删除:打 deleted_at,释放 slug 供后续复用。
// 由于 uk_slug 是 UNIQUE,复用 slug 需要把已删除行的 slug 改名。
func (d *DAO) SoftDelete(ctx context.Context, id uint64) error {
	res, err := d.db.ExecContext(ctx, `
UPDATE models
   SET deleted_at = NOW(),
       enabled    = 0,
       slug       = CONCAT(slug, '#del', id)
 WHERE id = ? AND deleted_at IS NULL`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
