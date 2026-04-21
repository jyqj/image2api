package account

import (
	"database/sql"
	"time"
)

// 账号状态常量。
const (
	StatusHealthy    = "healthy"
	StatusWarned     = "warned"
	StatusThrottled  = "throttled"
	StatusSuspicious = "suspicious"
	StatusDead       = "dead"
)

// 刷新来源。
const (
	RefreshSourceRT     = "rt"
	RefreshSourceST     = "st"
	RefreshSourceManual = "manual"
)

// Account 对应 oai_accounts 表。
type Account struct {
	ID               uint64         `db:"id" json:"id"`
	Email            string         `db:"email" json:"email"`
	AuthTokenEnc     string         `db:"auth_token_enc" json:"-"`
	RefreshTokenEnc  sql.NullString `db:"refresh_token_enc" json:"-"`
	SessionTokenEnc  sql.NullString `db:"session_token_enc" json:"-"`
	TokenExpiresAt   sql.NullTime   `db:"token_expires_at" json:"token_expires_at,omitempty"`
	OAISessionID     string         `db:"oai_session_id" json:"oai_session_id"`
	OAIDeviceID      string         `db:"oai_device_id" json:"oai_device_id"`
	ClientID         string         `db:"client_id" json:"client_id"`
	ChatGPTAccountID string         `db:"chatgpt_account_id" json:"chatgpt_account_id"`
	AccountType      string         `db:"account_type" json:"account_type"`
	SubscriptionType string         `db:"subscription_type" json:"subscription_type"`
	DailyImageQuota  int            `db:"daily_image_quota" json:"daily_image_quota"`
	Status           string         `db:"status" json:"status"`
	WarnedAt         sql.NullTime   `db:"warned_at" json:"warned_at,omitempty"`
	CooldownUntil    sql.NullTime   `db:"cooldown_until" json:"cooldown_until,omitempty"`
	LastUsedAt       sql.NullTime   `db:"last_used_at" json:"last_used_at,omitempty"`
	TodayUsedCount   int            `db:"today_used_count" json:"today_used_count"`
	TodayUsedDate    sql.NullTime   `db:"today_used_date" json:"today_used_date,omitempty"`

	LastRefreshAt     sql.NullTime `db:"last_refresh_at" json:"last_refresh_at,omitempty"`
	LastRefreshSource string       `db:"last_refresh_source" json:"last_refresh_source"`
	RefreshError      string       `db:"refresh_error" json:"refresh_error"`

	ImageQuotaRemaining int          `db:"image_quota_remaining" json:"image_quota_remaining"`
	ImageQuotaTotal     int          `db:"image_quota_total"     json:"image_quota_total"`
	ImageQuotaResetAt   sql.NullTime `db:"image_quota_reset_at"   json:"image_quota_reset_at,omitempty"`
	ImageQuotaUpdatedAt sql.NullTime `db:"image_quota_updated_at" json:"image_quota_updated_at,omitempty"`

	// image2 能力探测:主依据来自 /backend-api/models;conversation/init 只保留为 quota/诊断弱参考。
	ImageCapabilityStatus    string         `db:"image_capability_status" json:"image_capability_status"` // unknown/enabled/disabled/error
	ImageCapabilityModel     string         `db:"image_capability_model" json:"image_capability_model"`
	ImageCapabilitySource    string         `db:"image_capability_source" json:"image_capability_source"` // models/init/manual
	ImageCapabilityDetail    sql.NullString `db:"image_capability_detail" json:"image_capability_detail,omitempty"`
	ImageCapabilityUpdatedAt sql.NullTime   `db:"image_capability_updated_at" json:"image_capability_updated_at,omitempty"`
	ImageInitBlockedFeatures sql.NullString `db:"image_init_blocked_features" json:"image_init_blocked_features,omitempty"`

	// image2 命中画像:IMG2 是「账号资格 + 请求/会话抽卡」,不是简单布尔开关。
	IMG2HitCount         int          `db:"img2_hit_count" json:"img2_hit_count"`
	IMG2PreviewOnlyCount int          `db:"img2_preview_only_count" json:"img2_preview_only_count"`
	IMG2MissCount        int          `db:"img2_miss_count" json:"img2_miss_count"`
	IMG2ConsecutiveMiss  int          `db:"img2_consecutive_miss" json:"img2_consecutive_miss"`
	IMG2LastStatus       string       `db:"img2_last_status" json:"img2_last_status"`
	IMG2LastHitAt        sql.NullTime `db:"img2_last_hit_at" json:"img2_last_hit_at,omitempty"`
	IMG2LastAttemptAt    sql.NullTime `db:"img2_last_attempt_at" json:"img2_last_attempt_at,omitempty"`

	// IMG2 交付画像:和协议命中分开统计,避免「抽中但下载失败」污染账号优先级。
	IMG2DeliverySuccessCount int          `db:"img2_delivery_success_count" json:"img2_delivery_success_count"`
	IMG2DeliveryFailCount    int          `db:"img2_delivery_fail_count" json:"img2_delivery_fail_count"`
	IMG2DeliveryPartialCount int          `db:"img2_delivery_partial_count" json:"img2_delivery_partial_count"`
	IMG2LastDeliveryStatus   string       `db:"img2_last_delivery_status" json:"img2_last_delivery_status"`
	IMG2LastDeliveryAt       sql.NullTime `db:"img2_last_delivery_at" json:"img2_last_delivery_at,omitempty"`

	Notes     string       `db:"notes" json:"notes"`
	CreatedAt time.Time    `db:"created_at" json:"created_at"`
	UpdatedAt time.Time    `db:"updated_at" json:"updated_at"`
	DeletedAt sql.NullTime `db:"deleted_at" json:"-"`

	// 辅助字段(非数据库列):前端展示用标志位。
	HasRT bool `db:"-" json:"has_rt"`
	HasST bool `db:"-" json:"has_st"`
}

// Binding 对应 account_proxy_bindings 表。
type Binding struct {
	AccountID uint64    `db:"account_id" json:"account_id"`
	ProxyID   uint64    `db:"proxy_id" json:"proxy_id"`
	BoundAt   time.Time `db:"bound_at" json:"bound_at"`
}
