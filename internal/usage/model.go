package usage

import "time"

const (
	TypeChat  = "chat"
	TypeImage = "image"
)

const (
	StatusSuccess = "success"
	StatusFailed  = "failed"
)

// Log 对应 usage_logs 表一行。
type Log struct {
	ModelID          uint64    `db:"model_id"`
	AccountID        uint64    `db:"account_id"`
	RequestID        string    `db:"request_id"`
	Type             string    `db:"type"`
	InputTokens      int       `db:"input_tokens"`
	OutputTokens     int       `db:"output_tokens"`
	CacheReadTokens  int       `db:"cache_read_tokens"`
	CacheWriteTokens int       `db:"cache_write_tokens"`
	ImageCount       int       `db:"image_count"`
	DurationMs       int       `db:"duration_ms"`
	Status           string    `db:"status"`
	ErrorCode        string    `db:"error_code"`
	IP               string    `db:"ip"`
	UA               string    `db:"ua"`
	CreatedAt        time.Time `db:"created_at"`
}
