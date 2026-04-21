// Package audit 控制台操作审计日志。
//
// 所有 /api/admin/* 的 POST/PUT/PATCH/DELETE 请求会被 Middleware 拦截,
// 自动把操作者、IP、路径、方法、关键响应码落入 admin_audit_logs 表。
//
// 特殊高危操作(备份恢复、删除账号等)还会携带额外的 meta JSON,
// 由 handler 自行调用 `Record(c, "meta...")` 显式追加。
package audit

import (
	"database/sql"
	"time"
)

// Log 对应 admin_audit_logs 表。
type Log struct {
	ID         uint64       `db:"id" json:"id"`
	ActorID    uint64       `db:"actor_id" json:"actor_id"`
	ActorEmail string       `db:"actor_email" json:"actor_email"`
	Action     string       `db:"action" json:"action"` // 形如 "accounts.update"
	Method     string       `db:"method" json:"method"` // HTTP method
	Path       string       `db:"path" json:"path"`
	StatusCode int          `db:"status_code" json:"status_code"`
	IP         string       `db:"ip" json:"ip"`
	UA         string       `db:"ua" json:"ua"`
	Target     string       `db:"target" json:"target"`       // 业务对象 id/slug(可选)
	Meta       []byte       `db:"meta" json:"meta,omitempty"` // JSON
	CreatedAt  time.Time    `db:"created_at" json:"created_at"`
	Finished   sql.NullTime `db:"-" json:"-"` // 预留,不落表
}
