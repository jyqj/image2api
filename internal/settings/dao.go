package settings

import (
	"context"
	"strings"

	"github.com/jmoiron/sqlx"
)

// DAO 访问 system_settings 表。
type DAO struct {
	db *sqlx.DB
}

func NewDAO(db *sqlx.DB) *DAO { return &DAO{db: db} }

type row struct {
	K string `db:"k"`
	V string `db:"v"`
}

// LoadAll 全量读。启动时以及 Set 后无需调用(Set 内部维护)。
func (d *DAO) LoadAll(ctx context.Context) (map[string]string, error) {
	var rows []row
	if err := d.db.SelectContext(ctx, &rows, "SELECT `k`, COALESCE(`v`, '') AS `v` FROM `system_settings`"); err != nil {
		return nil, err
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		m[r.K] = r.V
	}
	return m, nil
}

// SetMany 用事务批量 upsert,未列出的 key 不动。
// 调用方负责白名单校验。
func (d *DAO) SetMany(ctx context.Context, kv map[string]string) error {
	if len(kv) == 0 {
		return nil
	}
	tx, err := d.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	const q = "INSERT INTO `system_settings` (`k`, `v`) VALUES (?, ?) " +
		"ON DUPLICATE KEY UPDATE `v` = VALUES(`v`)"
	for k, v := range kv {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, q, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}
