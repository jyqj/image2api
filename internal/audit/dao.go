package audit

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// DAO 审计日志表访问。
type DAO struct{ db *sqlx.DB }

func NewDAO(db *sqlx.DB) *DAO { return &DAO{db: db} }

// Insert 写入一条日志。
func (d *DAO) Insert(ctx context.Context, l *Log) error {
	_, err := d.db.ExecContext(ctx, `
INSERT INTO admin_audit_logs
  (actor_id, actor_email, action, method, path, status_code, ip, ua, target, meta, created_at)
VALUES (?,?,?,?,?,?,?,?,?,?, NOW())`,
		l.ActorID, l.ActorEmail, l.Action, l.Method, l.Path, l.StatusCode,
		l.IP, truncate(l.UA, 255), l.Target, nullJSON(l.Meta))
	if err != nil {
		return fmt.Errorf("audit insert: %w", err)
	}
	return nil
}

// List 分页查询。支持按 actor_id / action 过滤。
func (d *DAO) List(ctx context.Context, actorID uint64, action string, limit, offset int) ([]Log, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, actor_id, actor_email, action, method, path, status_code, ip, ua, target, meta, created_at
	        FROM admin_audit_logs WHERE 1=1`
	args := []interface{}{}
	if actorID > 0 {
		q += " AND actor_id = ?"
		args = append(args, actorID)
	}
	if action != "" {
		q += " AND action = ?"
		args = append(args, action)
	}
	q += " ORDER BY id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	var out []Log
	err := d.db.SelectContext(ctx, &out, q, args...)
	return out, err
}

// Count 对应 List 的计数。
func (d *DAO) Count(ctx context.Context, actorID uint64, action string) (int64, error) {
	q := `SELECT COUNT(*) FROM admin_audit_logs WHERE 1=1`
	args := []interface{}{}
	if actorID > 0 {
		q += " AND actor_id = ?"
		args = append(args, actorID)
	}
	if action != "" {
		q += " AND action = ?"
		args = append(args, action)
	}
	var n int64
	err := d.db.GetContext(ctx, &n, q, args...)
	return n, err
}

func nullJSON(b []byte) interface{} {
	if len(b) == 0 {
		return nil
	}
	return b
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
