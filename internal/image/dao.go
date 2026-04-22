package image

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"
)

var ErrNotFound = errors.New("image: task not found")

type DAO struct{ db *sqlx.DB }

func NewDAO(db *sqlx.DB) *DAO { return &DAO{db: db} }

func (d *DAO) Create(ctx context.Context, t *Task) error {
	res, err := d.db.ExecContext(ctx, `
INSERT INTO image_tasks
  (task_id, model_id, account_id, prompt, revised_prompt, n, size, quality, style, status,
   conversation_id, file_ids, result_urls, reference_urls, error, user_id, created_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, NOW())`,
		t.TaskID, t.ModelID, t.AccountID, t.Prompt, t.RevisedPrompt,
		t.N, t.Size, t.Quality, t.Style, nullEmpty(t.Status, StatusQueued),
		t.ConversationID, nullJSON(t.FileIDs), nullJSON(t.ResultURLs), nullJSON(t.ReferenceURLs),
		t.Error, t.UserID,
	)
	if err != nil {
		return fmt.Errorf("image dao create: %w", err)
	}
	id, _ := res.LastInsertId()
	t.ID = uint64(id)
	return nil
}

func (d *DAO) MarkRunning(ctx context.Context, taskID string, accountID uint64) error {
	_, err := d.db.ExecContext(ctx, `
UPDATE image_tasks
   SET status='running', account_id=?, started_at=NOW()
 WHERE task_id=? AND status IN ('queued','dispatched')`, accountID, taskID)
	return err
}

func (d *DAO) SetAccount(ctx context.Context, taskID string, accountID uint64) error {
	_, err := d.db.ExecContext(ctx, `UPDATE image_tasks SET account_id = ? WHERE task_id = ?`, accountID, taskID)
	return err
}

func (d *DAO) MarkSuccess(ctx context.Context, taskID, convID string, fileIDs, resultURLs []string, extra SuccessExtra) error {
	fidB, _ := json.Marshal(fileIDs)
	urlB, _ := json.Marshal(resultURLs)
	var refB []byte
	if len(extra.ReferenceFileIDs) > 0 {
		refB, _ = json.Marshal(extra.ReferenceFileIDs)
	}
	_, err := d.db.ExecContext(ctx, `
UPDATE image_tasks
   SET status='success', conversation_id=?, file_ids=?, result_urls=?,
       reference_urls=COALESCE(?, reference_urls), revised_prompt=?,
       attempts=?, duration_ms=?, finished_at=NOW()
 WHERE task_id=?`, convID, fidB, urlB, nullJSON(refB), extra.RevisedPrompt,
		extra.Attempts, extra.DurationMs, taskID)
	return err
}

// SuccessExtra 携带落库时的额外统计信息。
type SuccessExtra struct {
	RevisedPrompt    string
	ReferenceFileIDs []string // GPT 侧的 file-service ID
	Attempts         int
	DurationMs       int64
}

func (d *DAO) MarkFailed(ctx context.Context, taskID, errorCode string) error {
	_, err := d.db.ExecContext(ctx, `
UPDATE image_tasks
   SET status='failed', error=?, finished_at=NOW()
 WHERE task_id=?`, truncate(errorCode, 500), taskID)
	return err
}

func (d *DAO) Get(ctx context.Context, taskID string) (*Task, error) {
	var t Task
	err := d.db.GetContext(ctx, &t, `
SELECT id, task_id, model_id, account_id, prompt, revised_prompt, n, size, quality, style, status,
       conversation_id, file_ids, result_urls, reference_urls, error,
       attempts, duration_ms, user_id, created_at, started_at, finished_at
  FROM image_tasks
 WHERE task_id = ?`, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (d *DAO) ListAll(ctx context.Context, limit, offset int) ([]Task, error) {
	if limit <= 0 {
		limit = 20
	}
	var out []Task
	err := d.db.SelectContext(ctx, &out, `
SELECT id, task_id, model_id, account_id, prompt, revised_prompt, n, size, quality, style, status,
       conversation_id, file_ids, result_urls, reference_urls, error,
       attempts, duration_ms, user_id, created_at, started_at, finished_at
  FROM image_tasks
 ORDER BY id DESC
 LIMIT ? OFFSET ?`, limit, offset)
	return out, err
}

func (t *Task) DecodeFileIDs() []string {
	var out []string
	if len(t.FileIDs) > 0 {
		_ = json.Unmarshal(t.FileIDs, &out)
	}
	return out
}

func (t *Task) DecodeResultURLs() []string {
	var out []string
	if len(t.ResultURLs) > 0 {
		_ = json.Unmarshal(t.ResultURLs, &out)
	}
	return out
}

func nullEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
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
