package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// ErrNotFound 备份记录不存在。
var ErrNotFound = errors.New("backup: not found")

// DAO backup_files 表访问对象。
type DAO struct{ db *sqlx.DB }

// NewDAO 构造。
func NewDAO(db *sqlx.DB) *DAO { return &DAO{db: db} }

// 注意:`trigger` 和 `error` 是 MySQL 保留字,必须加反引号。
// 出于一致性,所有列都用反引号包裹,免得下次又被某个保留字坑。
const colsSelect = "`id`,`backup_id`,`file_name`,`size_bytes`,`sha256`," +
	"`trigger`,`status`,`error`,`include_data`,`created_by`,`created_at`,`finished_at`"

// Create 插入 running 记录,返回自增 id。
func (d *DAO) Create(ctx context.Context, f *File) error {
	res, err := d.db.ExecContext(ctx, `
INSERT INTO `+"`backup_files`"+`
  (`+"`backup_id`,`file_name`,`size_bytes`,`sha256`,`trigger`,`status`,`error`,`include_data`,`created_by`,`created_at`"+`)
VALUES (?,?,?,?,?,?,?,?,?, NOW())`,
		f.BackupID, f.FileName, f.SizeBytes, f.SHA256, f.Trigger,
		nonEmpty(f.Status, StatusRunning), f.Error, f.IncludeData, f.CreatedBy,
	)
	if err != nil {
		return fmt.Errorf("backup dao create: %w", err)
	}
	id, _ := res.LastInsertId()
	f.ID = uint64(id)
	return nil
}

// MarkReady 更新成功状态。
func (d *DAO) MarkReady(ctx context.Context, backupID string, size int64, sha string) error {
	_, err := d.db.ExecContext(ctx, `
UPDATE `+"`backup_files`"+`
   SET `+"`status`"+`='ready', `+"`size_bytes`"+`=?, `+"`sha256`"+`=?, `+"`finished_at`"+`=NOW()
 WHERE `+"`backup_id`"+`=?`, size, sha, backupID)
	return err
}

// MarkFailed 更新失败状态。
func (d *DAO) MarkFailed(ctx context.Context, backupID, errMsg string) error {
	if len(errMsg) > 500 {
		errMsg = errMsg[:500]
	}
	_, err := d.db.ExecContext(ctx, `
UPDATE `+"`backup_files`"+`
   SET `+"`status`"+`='failed', `+"`error`"+`=?, `+"`finished_at`"+`=NOW()
 WHERE `+"`backup_id`"+`=?`, errMsg, backupID)
	return err
}

// Get 按 backup_id 查询。
func (d *DAO) Get(ctx context.Context, backupID string) (*File, error) {
	var f File
	err := d.db.GetContext(ctx, &f,
		"SELECT "+colsSelect+" FROM `backup_files` WHERE `backup_id`=?", backupID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &f, err
}

// Delete 物理删除记录(文件删除由 Service 层做)。
func (d *DAO) Delete(ctx context.Context, backupID string) error {
	_, err := d.db.ExecContext(ctx,
		"DELETE FROM `backup_files` WHERE `backup_id`=?", backupID)
	return err
}

// List 分页列出。
func (d *DAO) List(ctx context.Context, limit, offset int) ([]File, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []File
	err := d.db.SelectContext(ctx, &out,
		"SELECT "+colsSelect+" FROM `backup_files` ORDER BY `id` DESC LIMIT ? OFFSET ?",
		limit, offset)
	return out, err
}

// Count 总数。
func (d *DAO) Count(ctx context.Context) (int64, error) {
	var n int64
	err := d.db.GetContext(ctx, &n, "SELECT COUNT(*) FROM `backup_files`")
	return n, err
}

// ListReadyOldest 拿最老的 N 个 ready 备份,用于 retention 清理。
func (d *DAO) ListReadyOldest(ctx context.Context, keep int) ([]File, error) {
	if keep < 0 {
		keep = 0
	}
	var out []File
	err := d.db.SelectContext(ctx, &out,
		"SELECT "+colsSelect+" FROM `backup_files` WHERE `status`='ready' "+
			"ORDER BY `id` DESC LIMIT 1000 OFFSET ?", keep)
	return out, err
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
