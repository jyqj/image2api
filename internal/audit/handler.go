package audit

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/432539/gpt2api/pkg/resp"
)

// Handler 暴露审计日志只读查询接口。
type Handler struct {
	dao *DAO
}

// NewHandler 构造。
func NewHandler(dao *DAO) *Handler { return &Handler{dao: dao} }

// List GET /api/admin/audit/logs?actor_id=&action=&limit=&offset=
func (h *Handler) List(c *gin.Context) {
	actorID, _ := strconv.ParseUint(c.Query("actor_id"), 10, 64)
	action := c.Query("action")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit > 500 {
		limit = 500
	}

	items, err := h.dao.List(c.Request.Context(), actorID, action, limit, offset)
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	total, _ := h.dao.Count(c.Request.Context(), actorID, action)
	resp.OK(c, gin.H{"items": items, "total": total, "limit": limit, "offset": offset})
}
