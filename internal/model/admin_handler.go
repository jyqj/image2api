package model

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	mysqlDrv "github.com/go-sql-driver/mysql"

	"github.com/432539/gpt2api/internal/audit"
	"github.com/432539/gpt2api/pkg/resp"
)

var slugRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._\-]{1,63}$`)

// AdminHandler 本地控制台的模型 CRUD。
type AdminHandler struct {
	dao      *DAO
	registry *Registry
	auditDAO *audit.DAO
}

func NewAdminHandler(dao *DAO, registry *Registry, auditDAO *audit.DAO) *AdminHandler {
	return &AdminHandler{dao: dao, registry: registry, auditDAO: auditDAO}
}

type upsertReq struct {
	Slug              string `json:"slug"`
	Type              string `json:"type"`
	UpstreamModelSlug string `json:"upstream_model_slug"`
	Description       string `json:"description"`
	Enabled           *bool  `json:"enabled,omitempty"`
}

func (r *upsertReq) validate(forCreate bool) error {
	r.Slug = strings.TrimSpace(r.Slug)
	r.UpstreamModelSlug = strings.TrimSpace(r.UpstreamModelSlug)
	r.Type = strings.TrimSpace(strings.ToLower(r.Type))
	if forCreate && !slugRe.MatchString(r.Slug) {
		return errors.New("slug 非法:需字母开头,2-64 位字母/数字/点/下划线/短横")
	}
	if r.Type != TypeChat && r.Type != TypeImage {
		return errors.New("type 只能为 chat 或 image")
	}
	if r.UpstreamModelSlug == "" {
		return errors.New("upstream_model_slug 不能为空")
	}
	if len(r.Description) > 255 {
		return errors.New("description 超过 255 字")
	}
	return nil
}

func (h *AdminHandler) List(c *gin.Context) {
	rows, err := h.dao.List(c.Request.Context())
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, gin.H{"items": rows, "total": len(rows)})
}

func (h *AdminHandler) Create(c *gin.Context) {
	var req upsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.BadRequest(c, err.Error())
		return
	}
	if err := req.validate(true); err != nil {
		resp.BadRequest(c, err.Error())
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	m := &Model{Slug: req.Slug, Type: req.Type, UpstreamModelSlug: req.UpstreamModelSlug, Description: req.Description, Enabled: enabled}
	if err := h.dao.Create(c.Request.Context(), m); err != nil {
		if isDupSlug(err) {
			resp.BadRequest(c, "slug 已存在")
			return
		}
		resp.Internal(c, err.Error())
		return
	}
	h.reloadRegistry(c)
	audit.Record(c, h.auditDAO, "models.create", strconv.FormatUint(m.ID, 10), gin.H{"slug": m.Slug, "type": m.Type})
	resp.OK(c, m)
}

func (h *AdminHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		resp.BadRequest(c, "invalid id")
		return
	}
	var req upsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.BadRequest(c, err.Error())
		return
	}
	if err := req.validate(false); err != nil {
		resp.BadRequest(c, err.Error())
		return
	}
	cur, err := h.dao.GetByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			resp.NotFound(c, "model not found")
			return
		}
		resp.Internal(c, err.Error())
		return
	}
	cur.Type = req.Type
	cur.UpstreamModelSlug = req.UpstreamModelSlug
	cur.Description = req.Description
	if req.Enabled != nil {
		cur.Enabled = *req.Enabled
	}
	if err := h.dao.Update(c.Request.Context(), cur); err != nil {
		resp.Internal(c, err.Error())
		return
	}
	h.reloadRegistry(c)
	audit.Record(c, h.auditDAO, "models.update", strconv.FormatUint(id, 10), gin.H{"slug": cur.Slug})
	resp.OK(c, cur)
}

func (h *AdminHandler) SetEnabled(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		resp.BadRequest(c, "invalid id")
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		resp.BadRequest(c, err.Error())
		return
	}
	if err := h.dao.SetEnabled(c.Request.Context(), id, body.Enabled); err != nil {
		if errors.Is(err, ErrNotFound) {
			resp.NotFound(c, "model not found")
			return
		}
		resp.Internal(c, err.Error())
		return
	}
	h.reloadRegistry(c)
	audit.Record(c, h.auditDAO, "models.set_enabled", strconv.FormatUint(id, 10), gin.H{"enabled": body.Enabled})
	resp.OK(c, gin.H{"id": id, "enabled": body.Enabled})
}

func (h *AdminHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		resp.BadRequest(c, "invalid id")
		return
	}
	if err := h.dao.SoftDelete(c.Request.Context(), id); err != nil {
		if errors.Is(err, ErrNotFound) {
			resp.NotFound(c, "model not found")
			return
		}
		resp.Internal(c, err.Error())
		return
	}
	h.reloadRegistry(c)
	audit.Record(c, h.auditDAO, "models.delete", strconv.FormatUint(id, 10), nil)
	resp.OK(c, gin.H{"deleted": id})
}

// ListEnabledForMe 本地控制台视角,只返回 enabled 模型,用于在线体验下拉选择。
func (h *AdminHandler) ListEnabledForMe(c *gin.Context) {
	rows, err := h.dao.ListEnabled(c.Request.Context())
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	type simple struct {
		ID          uint64 `json:"id"`
		Slug        string `json:"slug"`
		Type        string `json:"type"`
		Description string `json:"description"`
	}
	out := make([]simple, 0, len(rows))
	for _, m := range rows {
		out = append(out, simple{ID: m.ID, Slug: m.Slug, Type: m.Type, Description: m.Description})
	}
	resp.OK(c, gin.H{"items": out, "total": len(out)})
}

func (h *AdminHandler) reloadRegistry(c *gin.Context) {
	if h.registry == nil {
		return
	}
	_ = h.registry.Reload(c.Request.Context())
}

func isDupSlug(err error) bool {
	var me *mysqlDrv.MySQLError
	return errors.As(err, &me) && me.Number == 1062
}
