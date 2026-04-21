package usage

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/432539/gpt2api/pkg/resp"
)

// MeHandler 面向本地控制台的 usage 只读接口。
type MeHandler struct{ qdao *QueryDAO }

func NewMeHandler(qdao *QueryDAO) *MeHandler { return &MeHandler{qdao: qdao} }

func filterFromMeQuery(c *gin.Context) Filter {
	f := Filter{Type: c.Query("type"), Status: c.Query("status"), Since: parseFlexTime(c.Query("since")), Until: parseFlexTime(c.Query("until"))}
	if mid, err := strconv.ParseUint(c.Query("model_id"), 10, 64); err == nil {
		f.ModelID = mid
	}
	return f
}

// Logs GET /api/me/usage/logs。
func (h *MeHandler) Logs(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit > 200 {
		limit = 200
	}
	f := filterFromMeQuery(c)
	rows, total, err := h.qdao.List(c.Request.Context(), f, offset, limit)
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, gin.H{"items": rows, "total": total, "limit": limit, "offset": offset})
}

// Stats GET /api/me/usage/stats。
func (h *MeHandler) Stats(c *gin.Context) {
	days, _ := strconv.Atoi(c.DefaultQuery("days", "14"))
	topN, _ := strconv.Atoi(c.DefaultQuery("top_n", "5"))
	f := filterFromMeQuery(c)
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
