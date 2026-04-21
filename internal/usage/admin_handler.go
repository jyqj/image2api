package usage

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/432539/gpt2api/pkg/resp"
)

type AdminHandler struct{ qdao *QueryDAO }

func NewAdminHandler(qdao *QueryDAO) *AdminHandler { return &AdminHandler{qdao: qdao} }

func filterFromQuery(c *gin.Context) Filter {
	mid64, _ := strconv.ParseUint(c.Query("model_id"), 10, 64)
	aid64, _ := strconv.ParseUint(c.Query("account_id"), 10, 64)
	return Filter{
		ModelID:   mid64,
		AccountID: aid64,
		Type:      c.Query("type"),
		Status:    c.Query("status"),
		Since:     parseFlexTime(c.Query("since")),
		Until:     parseFlexTime(c.Query("until")),
	}
}

func parseFlexTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t
	}
	return time.Time{}
}

// Stats GET /api/admin/usage/stats。
func (h *AdminHandler) Stats(c *gin.Context) {
	f := filterFromQuery(c)
	days, _ := strconv.Atoi(c.DefaultQuery("days", "14"))
	topN, _ := strconv.Atoi(c.DefaultQuery("top_n", "10"))
	overall, err := h.qdao.Overall(c.Request.Context(), f)
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	daily, err := h.qdao.Daily(c.Request.Context(), f, days)
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	byModel, err := h.qdao.ByModel(c.Request.Context(), f, topN)
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, gin.H{"overall": overall, "daily": daily, "by_model": byModel})
}

// Logs GET /api/admin/usage/logs。
func (h *AdminHandler) Logs(c *gin.Context) {
	f := filterFromQuery(c)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	rows, total, err := h.qdao.List(c.Request.Context(), f, offset, limit)
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, gin.H{"items": rows, "total": total, "limit": limit, "offset": offset})
}
