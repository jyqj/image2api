package model

import (
	"database/sql"
	"time"
)

const (
	TypeChat  = "chat"
	TypeImage = "image"
)

// Model 对应 models 表,只描述本地 slug 与上游 slug 的映射。
type Model struct {
	ID                uint64       `db:"id" json:"id"`
	Slug              string       `db:"slug" json:"slug"`
	Type              string       `db:"type" json:"type"`
	UpstreamModelSlug string       `db:"upstream_model_slug" json:"upstream_model_slug"`
	Description       string       `db:"description" json:"description"`
	Enabled           bool         `db:"enabled" json:"enabled"`
	CreatedAt         time.Time    `db:"created_at" json:"created_at"`
	UpdatedAt         time.Time    `db:"updated_at" json:"updated_at"`
	DeletedAt         sql.NullTime `db:"deleted_at" json:"-"`
}
