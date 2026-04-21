// Package backup 数据库备份/恢复。
//
// 物理方案:
//  1. 备份:mysqldump 子进程 → stdout → gzip pipe → 目标文件
//     - 只 dump 业务库(从 DSN 解析得到的 database)
//     - 默认包含数据;支持 --no-data 只 dump 结构
//     - 默认使用 --single-transaction(InnoDB 一致性快照,不锁表)
//     - 排除 audit 表本身可选
//  2. 恢复:mysql 子进程读 gzip 解压流 → stdin
//     - 生产默认禁用(backup.allow_restore=false),需配置显式开启
//     - 执行前后都会写入审计
//
// 安全考虑:
//   - 文件名正则白名单,防路径遍历
//   - restore/upload/delete 受本地控制台和配置开关约束
//   - 大文件流式处理,避免 OOM
//   - sha256 校验完整性
package backup

import (
	"database/sql"
	"time"
)

// 状态常量。
const (
	StatusRunning = "running"
	StatusReady   = "ready"
	StatusFailed  = "failed"
)

// 触发来源。
const (
	TriggerManual = "manual"
	TriggerCron   = "cron"
	TriggerUpload = "upload"
)

// File 对应 backup_files 表。
type File struct {
	ID          uint64       `db:"id" json:"id"`
	BackupID    string       `db:"backup_id" json:"backup_id"` // bk_YYYYMMDD_HHMMSS_xxxx
	FileName    string       `db:"file_name" json:"file_name"`
	SizeBytes   int64        `db:"size_bytes" json:"size_bytes"`
	SHA256      string       `db:"sha256" json:"sha256"`
	Trigger     string       `db:"trigger" json:"trigger"`
	Status      string       `db:"status" json:"status"`
	Error       string       `db:"error" json:"error,omitempty"`
	IncludeData bool         `db:"include_data" json:"include_data"`
	CreatedBy   uint64       `db:"created_by" json:"created_by"`
	CreatedAt   time.Time    `db:"created_at" json:"created_at"`
	FinishedAt  sql.NullTime `db:"finished_at" json:"finished_at,omitempty"`
}
