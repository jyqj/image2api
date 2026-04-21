package server

import (
	"github.com/gin-gonic/gin"

	"github.com/432539/gpt2api/internal/account"
	"github.com/432539/gpt2api/internal/audit"
	"github.com/432539/gpt2api/internal/backup"
	"github.com/432539/gpt2api/internal/config"
	"github.com/432539/gpt2api/internal/gateway"
	"github.com/432539/gpt2api/internal/image"
	"github.com/432539/gpt2api/internal/middleware"
	"github.com/432539/gpt2api/internal/model"
	"github.com/432539/gpt2api/internal/proxy"
	"github.com/432539/gpt2api/internal/settings"
	"github.com/432539/gpt2api/internal/usage"
	"github.com/432539/gpt2api/pkg/resp"
)

// Deps 是本地 2API 控制台需要的处理器集合。
type Deps struct {
	Config *config.Config

	ProxyH   *proxy.Handler
	AccountH *account.Handler

	GatewayH *gateway.Handler
	ImagesH  *gateway.ImagesHandler

	BackupH  *backup.Handler
	AuditH   *audit.Handler
	AuditDAO *audit.DAO

	AdminModelH *model.AdminHandler
	AdminUsageH *usage.AdminHandler

	MeUsageH *usage.MeHandler
	MeImageH *image.MeHandler

	SettingsH   *settings.Handler
	SettingsSvc *settings.Service
}

// New 构建 gin.Engine 并挂载本地控制台与 OpenAI 兼容路由。
func New(d *Deps) *gin.Engine {
	if d.Config.App.Env == "prod" {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(
		middleware.RequestID(),
		middleware.Recover(),
		middleware.AccessLog(),
		middleware.CORS(d.Config.Security.CORSOrigins),
	)

	r.GET("/healthz", func(c *gin.Context) { resp.OK(c, gin.H{"status": "ok"}) })
	r.GET("/readyz", func(c *gin.Context) { resp.OK(c, gin.H{"status": "ok"}) })

	cfg := config.Get()

	// POST /api/admin/login — 管理员登录,无需 token
	r.POST("/api/admin/login", func(c *gin.Context) {
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			resp.BadRequest(c, "参数错误")
			return
		}
		if cfg == nil || req.Username != cfg.Admin.Username || req.Password != cfg.Admin.Password {
			c.JSON(401, gin.H{"code": 401, "message": "用户名或密码错误"})
			return
		}
		token := middleware.GenerateToken(req.Username)
		resp.OK(c, gin.H{"token": token, "username": req.Username})
	})

	// 公开 API（不需要登录）
	pub := r.Group("/api/public")
	if d.SettingsH != nil {
		pub.GET("/site-info", d.SettingsH.Public)
	}

	api := r.Group("/api", middleware.AdminAuth())
	{

		api.GET("/me", localMe)
		api.GET("/me/menu", localMenu)
		if d.AdminModelH != nil {
			api.GET("/me/models", d.AdminModelH.ListEnabledForMe)
		}
		if d.MeUsageH != nil {
			ug := api.Group("/me/usage")
			ug.GET("/logs", d.MeUsageH.Logs)
			ug.GET("/stats", d.MeUsageH.Stats)
		}
		if d.MeImageH != nil {
			ig := api.Group("/me/images")
			ig.GET("/tasks", d.MeImageH.List)
			ig.GET("/tasks/:id", d.MeImageH.Get)
		}
		if d.GatewayH != nil {
			pg := api.Group("/me/playground")
			pg.POST("/chat", d.GatewayH.ChatCompletions)
			if d.ImagesH != nil {
				pg.POST("/image", d.ImagesH.ImageGenerations)
				pg.POST("/image-edit", d.ImagesH.ImageEdits)
			}
		}

		adminMW := []gin.HandlerFunc{}
		if d.AuditDAO != nil {
			adminMW = append(adminMW, audit.Middleware(d.AuditDAO))
		}
		admin := api.Group("/admin", adminMW...)
		admin.GET("/ping", func(c *gin.Context) { resp.OK(c, gin.H{"msg": "admin pong"}) })

		if d.ProxyH != nil {
			pg := admin.Group("/proxies")
			pg.POST("", d.ProxyH.Create)
			pg.POST("/import", d.ProxyH.Import)
			pg.POST("/probe-all", d.ProxyH.ProbeAll)
			pg.GET("", d.ProxyH.List)
			pg.GET("/:id", d.ProxyH.Get)
			pg.POST("/:id/probe", d.ProxyH.Probe)
			pg.PATCH("/:id", d.ProxyH.Update)
			pg.DELETE("/:id", d.ProxyH.Delete)
		}

		if d.AccountH != nil {
			ag := admin.Group("/accounts")
			ag.POST("", d.AccountH.Create)
			ag.POST("/import", d.AccountH.Import)
			ag.POST("/import-tokens", d.AccountH.ImportTokens)
			ag.POST("/refresh-all", d.AccountH.RefreshAll)
			ag.POST("/probe-quota-all", d.AccountH.ProbeQuotaAll)
			ag.POST("/bulk-delete", d.AccountH.BulkDelete)
			ag.POST("/purge-deleted", d.AccountH.PurgeDeleted)
			ag.GET("/auto-refresh", d.AccountH.GetAutoRefresh)
			ag.PUT("/auto-refresh", d.AccountH.SetAutoRefresh)
			ag.GET("", d.AccountH.List)
			ag.GET("/:id", d.AccountH.Get)
			ag.GET("/:id/secrets", d.AccountH.GetSecrets)
			ag.PATCH("/:id", d.AccountH.Update)
			ag.DELETE("/:id", d.AccountH.Delete)
			ag.POST("/:id/refresh", d.AccountH.Refresh)
			ag.POST("/:id/probe-quota", d.AccountH.ProbeQuota)
			ag.POST("/:id/bind-proxy", d.AccountH.BindProxy)
			ag.DELETE("/:id/bind-proxy", d.AccountH.UnbindProxy)
		}

		if d.AuditH != nil {
			auditG := admin.Group("/audit")
			auditG.GET("/logs", d.AuditH.List)
		}

		if d.AdminModelH != nil {
			mg := admin.Group("/models")
			mg.GET("", d.AdminModelH.List)
			mg.POST("", d.AdminModelH.Create)
			mg.PUT("/:id", d.AdminModelH.Update)
			mg.PATCH("/:id/enabled", d.AdminModelH.SetEnabled)
			mg.DELETE("/:id", d.AdminModelH.Delete)
		}

		if d.AdminUsageH != nil {
			ug := admin.Group("/usage")
			ug.GET("/stats", d.AdminUsageH.Stats)
			ug.GET("/logs", d.AdminUsageH.Logs)
		}

		if d.SettingsH != nil {
			sg := admin.Group("/settings")
			sg.GET("", d.SettingsH.List)
			sg.PUT("", d.SettingsH.Update)
			sg.POST("/reload", d.SettingsH.Reload)
			sg.POST("/test-email", d.SettingsH.TestMail)
		}

		if d.BackupH != nil {
			bg := admin.Group("/system/backup")
			bg.GET("", d.BackupH.List)
			bg.POST("", d.BackupH.Create)
			bg.GET("/:id/download", d.BackupH.Download)
			bg.DELETE("/:id", d.BackupH.Delete)
			bg.POST("/:id/restore", d.BackupH.Restore)
			bg.POST("/upload", d.BackupH.Upload)
		}
	}

	var v1MW []gin.HandlerFunc
	if d.SettingsSvc != nil {
		v1MW = append(v1MW, middleware.V1APIKeyAuth(d.SettingsSvc))
	} else {
		v1MW = append(v1MW, middleware.LocalActor())
	}
	v1 := r.Group("/v1", v1MW...)
	{
		v1.GET("/models", d.GatewayH.ListModels)
		v1.POST("/chat/completions", d.GatewayH.ChatCompletions)
		if d.ImagesH != nil {
			v1.POST("/images/generations", d.ImagesH.ImageGenerations)
			v1.POST("/images/edits", d.ImagesH.ImageEdits)
			v1.GET("/images/tasks/:id", d.ImagesH.ImageTask)
		}
	}

	if d.ImagesH != nil {
		r.GET("/p/img/:task_id/:idx", d.ImagesH.ImageProxy)
	}

	mountSPA(r)
	return r
}

func localMe(c *gin.Context) {
	resp.OK(c, gin.H{
		"user":        gin.H{"email": "local-console", "nickname": "本地控制台"},
		"role":        "local",
		"permissions": []string{"local:*"},
	})
}

func localMenu(c *gin.Context) {
	resp.OK(c, gin.H{
		"role":        "local",
		"permissions": []string{"local:*"},
		"menu": []gin.H{
			{"key": "personal", "title": "本地面板", "icon": "Monitor", "children": []gin.H{
				{"key": "dashboard", "title": "运行概览", "icon": "DataLine", "path": "/personal/dashboard"},
				{"key": "usage", "title": "用量观察", "icon": "TrendCharts", "path": "/personal/usage"},
				{"key": "play", "title": "在线体验", "icon": "ChatDotRound", "path": "/personal/play"},
				{"key": "docs", "title": "接口文档", "icon": "Document", "path": "/personal/docs"},
			}},
			{"key": "admin", "title": "运维管理", "icon": "Setting", "children": []gin.H{
				{"key": "accounts", "title": "账号池", "icon": "User", "path": "/admin/accounts"},
				{"key": "proxies", "title": "代理池", "icon": "Connection", "path": "/admin/proxies"},
				{"key": "models", "title": "模型映射", "icon": "Grid", "path": "/admin/models"},
				{"key": "usage-admin", "title": "全局用量", "icon": "Histogram", "path": "/admin/usage"},
				{"key": "audit", "title": "审计日志", "icon": "Tickets", "path": "/admin/audit"},
				{"key": "backup", "title": "数据备份", "icon": "Folder", "path": "/admin/backup"},
				{"key": "settings", "title": "系统设置", "icon": "Tools", "path": "/admin/settings"},
			}},
		},
	})
}
