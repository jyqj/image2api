// Package image 生图任务的数据模型、DAO 以及同步 Runner。
//
// 路线:
//   - /v1/images/generations 默认同步返回结果,同时落库生成 task_id。
//   - /v1/images/tasks/:id 可查询任务历史。
//
// 链路复用 Account + Proxy + Scheduler,并把结果写入 usage/image 日志。
package image

import "time"

const (
	StatusQueued     = "queued"
	StatusDispatched = "dispatched"
	StatusRunning    = "running"
	StatusSuccess    = "success"
	StatusFailed     = "failed"
)

// 错误码(短字符串,便于排查)。
const (
	ErrUnknown         = "unknown"
	ErrNoAccount       = "no_available_account"
	ErrAuthRequired    = "auth_required"
	ErrRateLimited     = "rate_limited"
	ErrPOWTimeout      = "pow_timeout"
	ErrPOWFailed       = "pow_failed"
	ErrTurnstile       = "turnstile_required"
	ErrUpstream        = "upstream_error"
	ErrPreviewOnly     = "preview_only"
	ErrPollTimeout     = "poll_timeout"
	ErrDownload        = "download_failed"
	ErrInvalidResponse = "invalid_response"
	ErrContentPolicy   = "content_policy_violation"
)

// Task 对应 image_tasks 表。
type Task struct {
	ID             uint64     `db:"id"`
	TaskID         string     `db:"task_id"`
	ModelID        uint64     `db:"model_id"`
	AccountID      uint64     `db:"account_id"`
	Prompt         string     `db:"prompt"`
	RevisedPrompt  string     `db:"revised_prompt"`
	N              int        `db:"n"`
	Size           string     `db:"size"`
	Quality        string     `db:"quality"`
	Style          string     `db:"style"`
	Status         string     `db:"status"`
	ConversationID string     `db:"conversation_id"`
	FileIDs        []byte     `db:"file_ids"`
	ResultURLs     []byte     `db:"result_urls"`
	ReferenceURLs  []byte     `db:"reference_urls"`
	LocalPaths     []byte     `db:"local_paths"`
	Error          string     `db:"error"`
	Attempts       int        `db:"attempts"`
	DurationMs     int64      `db:"duration_ms"`
	UserID         string     `db:"user_id"`
	CreatedAt      time.Time  `db:"created_at"`
	StartedAt      *time.Time `db:"started_at"`
	FinishedAt     *time.Time `db:"finished_at"`
}

// Result 是 Runner 返回给网关/客户端的生图结果。
type Result struct {
	TaskID         string        `json:"task_id"`
	Status         string        `json:"status"`
	ConversationID string        `json:"conversation_id,omitempty"`
	Images         []ResultImage `json:"images,omitempty"`
	ErrorCode      string        `json:"error_code,omitempty"`
	ErrorMessage   string        `json:"error_message,omitempty"`
}

// ResultImage 单张生图。
type ResultImage struct {
	URL         string `json:"url"`
	FileID      string `json:"file_id"`
	IsSediment  bool   `json:"is_sediment,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}
