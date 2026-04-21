package usage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

type QueryDAO struct{ db *sqlx.DB }

func NewQueryDAO(db *sqlx.DB) *QueryDAO { return &QueryDAO{db: db} }

type Filter struct {
	ModelID   uint64
	AccountID uint64
	Type      string
	Status    string
	Since     time.Time
	Until     time.Time
}

type ItemRow struct {
	ID               uint64    `db:"id" json:"id"`
	ModelID          uint64    `db:"model_id" json:"model_id"`
	ModelSlug        string    `db:"model_slug" json:"model_slug"`
	AccountID        uint64    `db:"account_id" json:"account_id"`
	RequestID        string    `db:"request_id" json:"request_id"`
	Type             string    `db:"type" json:"type"`
	InputTokens      int       `db:"input_tokens" json:"input_tokens"`
	OutputTokens     int       `db:"output_tokens" json:"output_tokens"`
	CacheReadTokens  int       `db:"cache_read_tokens" json:"cache_read_tokens"`
	CacheWriteTokens int       `db:"cache_write_tokens" json:"cache_write_tokens"`
	ImageCount       int       `db:"image_count" json:"image_count"`
	DurationMs       int       `db:"duration_ms" json:"duration_ms"`
	Status           string    `db:"status" json:"status"`
	ErrorCode        string    `db:"error_code" json:"error_code"`
	IP               string    `db:"ip" json:"ip"`
	CreatedAt        time.Time `db:"created_at" json:"created_at"`
}

type ModelStat struct {
	ModelID      uint64 `db:"model_id" json:"model_id"`
	ModelSlug    string `db:"model_slug" json:"model_slug"`
	Type         string `db:"type" json:"type"`
	Requests     int64  `db:"requests" json:"requests"`
	Failures     int64  `db:"failures" json:"failures"`
	InputTokens  int64  `db:"input_tokens" json:"input_tokens"`
	OutputTokens int64  `db:"output_tokens" json:"output_tokens"`
	ImageCount   int64  `db:"image_count" json:"image_count"`
	AvgDurMs     int64  `db:"avg_dur_ms" json:"avg_dur_ms"`
}

type DailyPoint struct {
	Day          string `db:"day" json:"day"`
	Requests     int64  `db:"requests" json:"requests"`
	Failures     int64  `db:"failures" json:"failures"`
	InputTokens  int64  `db:"input_tokens" json:"input_tokens"`
	OutputTokens int64  `db:"output_tokens" json:"output_tokens"`
	ImageCount   int64  `db:"image_count" json:"image_count"`
}

type Overall struct {
	Requests     int64 `db:"requests" json:"requests"`
	Failures     int64 `db:"failures" json:"failures"`
	ChatRequests int64 `db:"chat_requests" json:"chat_requests"`
	ImageImages  int64 `db:"image_images" json:"image_images"`
	InputTokens  int64 `db:"input_tokens" json:"input_tokens"`
	OutputTokens int64 `db:"output_tokens" json:"output_tokens"`
}

func (d *QueryDAO) buildWhere(f Filter) (string, []any) {
	b := strings.Builder{}
	b.WriteString("WHERE 1=1")
	args := make([]any, 0, 6)
	if f.ModelID > 0 {
		b.WriteString(" AND u.model_id = ?")
		args = append(args, f.ModelID)
	}
	if f.AccountID > 0 {
		b.WriteString(" AND u.account_id = ?")
		args = append(args, f.AccountID)
	}
	if f.Type != "" {
		b.WriteString(" AND u.type = ?")
		args = append(args, f.Type)
	}
	if f.Status != "" {
		b.WriteString(" AND u.status = ?")
		args = append(args, f.Status)
	}
	if !f.Since.IsZero() {
		b.WriteString(" AND u.created_at >= ?")
		args = append(args, f.Since)
	}
	if !f.Until.IsZero() {
		b.WriteString(" AND u.created_at < ?")
		args = append(args, f.Until)
	}
	return b.String(), args
}

func (d *QueryDAO) List(ctx context.Context, f Filter, offset, limit int) ([]ItemRow, int64, error) {
	where, args := d.buildWhere(f)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	q := fmt.Sprintf(`
SELECT u.id, u.model_id,
       COALESCE(m.slug, '') AS model_slug,
       u.account_id, u.request_id, u.type,
       u.input_tokens, u.output_tokens, u.cache_read_tokens, u.cache_write_tokens,
       u.image_count, u.duration_ms, u.status, u.error_code, u.ip, u.created_at
FROM usage_logs u
LEFT JOIN models m ON m.id = u.model_id
%s
ORDER BY u.id DESC
LIMIT ? OFFSET ?`, where)
	rows := make([]ItemRow, 0, limit)
	err := d.db.SelectContext(ctx, &rows, q, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	countQ := fmt.Sprintf(`SELECT COUNT(*) FROM usage_logs u %s`, where)
	var total int64
	if err := d.db.GetContext(ctx, &total, countQ, args...); err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (d *QueryDAO) Overall(ctx context.Context, f Filter) (Overall, error) {
	where, args := d.buildWhere(f)
	q := fmt.Sprintf(`
SELECT COUNT(*) AS requests,
       COALESCE(SUM(CASE WHEN u.status = 'failed' THEN 1 ELSE 0 END), 0) AS failures,
       COALESCE(SUM(CASE WHEN u.type = 'chat' THEN 1 ELSE 0 END), 0) AS chat_requests,
       COALESCE(SUM(CASE WHEN u.type = 'image' THEN u.image_count ELSE 0 END), 0) AS image_images,
       COALESCE(SUM(u.input_tokens), 0) AS input_tokens,
       COALESCE(SUM(u.output_tokens), 0) AS output_tokens
FROM usage_logs u %s`, where)
	var out Overall
	if err := d.db.GetContext(ctx, &out, q, args...); err != nil {
		return out, err
	}
	return out, nil
}

func (d *QueryDAO) ByModel(ctx context.Context, f Filter, limit int) ([]ModelStat, error) {
	where, args := d.buildWhere(f)
	if limit <= 0 {
		limit = 50
	}
	q := fmt.Sprintf(`
SELECT u.model_id,
       COALESCE(m.slug, '') AS model_slug,
       COALESCE(MAX(u.type), '') AS type,
       COUNT(*) AS requests,
       COALESCE(SUM(CASE WHEN u.status='failed' THEN 1 ELSE 0 END), 0) AS failures,
       COALESCE(SUM(u.input_tokens), 0) AS input_tokens,
       COALESCE(SUM(u.output_tokens), 0) AS output_tokens,
       COALESCE(SUM(u.image_count), 0) AS image_count,
       COALESCE(CAST(AVG(u.duration_ms) AS SIGNED), 0) AS avg_dur_ms
FROM usage_logs u
LEFT JOIN models m ON m.id = u.model_id
%s
GROUP BY u.model_id, m.slug
ORDER BY requests DESC
LIMIT ?`, where)
	rows := make([]ModelStat, 0, limit)
	err := d.db.SelectContext(ctx, &rows, q, append(args, limit)...)
	return rows, err
}

func (d *QueryDAO) Daily(ctx context.Context, f Filter, days int) ([]DailyPoint, error) {
	if days <= 0 || days > 180 {
		days = 14
	}
	since := time.Now().AddDate(0, 0, -days+1).Truncate(24 * time.Hour)
	if f.Since.IsZero() || f.Since.Before(since) {
		f.Since = since
	}
	where, args := d.buildWhere(f)
	q := fmt.Sprintf(`
SELECT DATE_FORMAT(u.created_at, '%%Y-%%m-%%d') AS day,
       COUNT(*) AS requests,
       COALESCE(SUM(CASE WHEN u.status='failed' THEN 1 ELSE 0 END), 0) AS failures,
       COALESCE(SUM(u.input_tokens), 0) AS input_tokens,
       COALESCE(SUM(u.output_tokens), 0) AS output_tokens,
       COALESCE(SUM(u.image_count), 0) AS image_count
FROM usage_logs u
%s
GROUP BY day
ORDER BY day ASC`, where)
	rows := make([]DailyPoint, 0, days)
	err := d.db.SelectContext(ctx, &rows, q, args...)
	return rows, err
}
