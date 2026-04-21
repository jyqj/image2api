package settings

import (
	"github.com/gin-gonic/gin"

	"github.com/432539/gpt2api/internal/audit"
	"github.com/432539/gpt2api/internal/middleware"
	"github.com/432539/gpt2api/pkg/mailer"
	"github.com/432539/gpt2api/pkg/resp"
)

// Handler 系统设置 HTTP 接口。
//   - List    GET  /api/admin/settings          本地控制台读取所有 key
//   - Update  PUT  /api/admin/settings          本地控制台批量更新
//   - Reload  POST /api/admin/settings/reload   从 DB 强制重载缓存(应急)
//   - TestMail POST /api/admin/settings/test-email 本地控制台给任意地址发一封测试邮件
//   - Public  GET  /api/public/site-info        匿名可访问,返回 Public=true 的子集
type Handler struct {
	svc      *Service
	mail     *mailer.Mailer
	auditDAO *audit.DAO
}

func NewHandler(svc *Service, mail *mailer.Mailer, adao *audit.DAO) *Handler {
	return &Handler{svc: svc, mail: mail, auditDAO: adao}
}

// itemView 给前端使用的完整条目(带 schema,便于统一渲染)。
type itemView struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Type     string `json:"type"`
	Category string `json:"category"`
	Label    string `json:"label"`
	Desc     string `json:"desc"`
}

// List GET /api/admin/settings
func (h *Handler) List(c *gin.Context) {
	snap := h.svc.Snapshot()
	items := make([]itemView, 0, len(Defs))
	for _, d := range Defs {
		items = append(items, itemView{
			Key: d.Key, Value: snap[d.Key], Type: d.Type,
			Category: d.Category, Label: d.Label, Desc: d.Desc,
		})
	}
	resp.OK(c, gin.H{"items": items})
}

// Update PUT /api/admin/settings
// body: { "items": { "site.name": "...", "gateway.upstream_timeout_sec": "60", ... } }
type updateReq struct {
	Items map[string]string `json:"items"`
}

func (h *Handler) Update(c *gin.Context) {
	var req updateReq
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Items) == 0 {
		resp.BadRequest(c, "items required")
		return
	}
	// 白名单过滤 + 类型轻校验(严重错误直接拒,warning 放行由前端提示)
	for k, v := range req.Items {
		if !IsAllowedKey(k) {
			resp.BadRequest(c, "unknown key: "+k)
			return
		}
		if def, _ := DefByKey(k); def.Type == "int" {
			if v == "" {
				req.Items[k] = "0"
				continue
			}
			if _, err := parseInt64(v); err != nil {
				resp.BadRequest(c, k+" must be integer")
				return
			}
		}
	}
	if err := h.svc.Set(c.Request.Context(), req.Items); err != nil {
		resp.Internal(c, err.Error())
		return
	}
	if h.auditDAO != nil {
		actor := middleware.UserID(c)
		if actor > 0 {
			_ = h.auditDAO.Insert(c.Request.Context(), &audit.Log{
				ActorID: actor,
				Action:  "settings.update",
				Method:  c.Request.Method,
				Path:    c.FullPath(),
				Target:  sprintKeys(req.Items),
				IP:      c.ClientIP(),
				UA:      c.Request.UserAgent(),
			})
		}
	}
	resp.OK(c, gin.H{"updated": len(req.Items)})
}

// Reload POST /api/admin/settings/reload
func (h *Handler) Reload(c *gin.Context) {
	if err := h.svc.Reload(c.Request.Context()); err != nil {
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, gin.H{"reloaded": true})
}

// TestMail POST /api/admin/settings/test-email
// body: { "to": "foo@bar.com" }
type testMailReq struct {
	To string `json:"to" binding:"required,email"`
}

func (h *Handler) TestMail(c *gin.Context) {
	var req testMailReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.BadRequest(c, "invalid email: "+err.Error())
		return
	}
	if h.mail == nil || h.mail.Disabled() {
		resp.Fail(c, resp.CodeBadRequest, "SMTP not configured: fill host/user/pass in config and restart")
		return
	}
	subject := "[" + h.svc.SiteName() + "] SMTP test email"
	html := `<p>This is a <b>test email</b> sent from ` + h.svc.SiteName() + ` admin console.</p>` +
		`<p>If you see this, your SMTP configuration works.</p>`
	if err := h.mail.SendSync(mailer.Message{To: req.To, Subject: subject, HTML: html}); err != nil {
		resp.Fail(c, resp.CodeInternal, "send failed: "+err.Error())
		return
	}
	resp.OK(c, gin.H{"sent": true, "to": req.To})
}

// Public GET /api/public/site-info
func (h *Handler) Public(c *gin.Context) {
	resp.OK(c, h.svc.PublicSnapshot())
}
