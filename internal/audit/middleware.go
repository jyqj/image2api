package audit

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/432539/gpt2api/internal/middleware"
	"github.com/432539/gpt2api/pkg/logger"
)

// Middleware 在 /api/admin/* 下自动记录写操作。
// 非写操作(GET/HEAD/OPTIONS)默认不落盘,避免审计表爆炸。
// 查看敏感资源如需单独记录,在 handler 里 `Record(c, ...)`。
func Middleware(dao *DAO) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isWrite(c.Request.Method) {
			c.Next()
			return
		}
		start := time.Now()
		c.Next()
		go writeLog(dao, c, start)
	}
}

// Record 供 handler 显式追加审计记录(用于带业务 meta 的场景)。
// action 形如 "accounts.update",target 为业务 id/slug,meta 为 JSON-encode 的任意上下文。
func Record(c *gin.Context, dao *DAO, action, target string, meta any) {
	if dao == nil {
		return
	}
	metaB, _ := json.Marshal(meta)
	email, _ := c.Get("actor_email")
	emailStr, _ := email.(string)
	l := &Log{
		ActorID:    middleware.ActorID(c),
		ActorEmail: emailStr,
		Action:     action,
		Method:     c.Request.Method,
		Path:       c.FullPath(),
		StatusCode: c.Writer.Status(),
		IP:         c.ClientIP(),
		UA:         c.Request.UserAgent(),
		Target:     target,
		Meta:       metaB,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := dao.Insert(ctx, l); err != nil {
		logger.L().Warn("audit record failed", zap.Error(err), zap.String("action", action))
	}
}

func writeLog(dao *DAO, c *gin.Context, start time.Time) {
	email, _ := c.Get("actor_email")
	emailStr, _ := email.(string)
	l := &Log{
		ActorID:    middleware.ActorID(c),
		ActorEmail: emailStr,
		Action:     deriveAction(c),
		Method:     c.Request.Method,
		Path:       c.FullPath(),
		StatusCode: c.Writer.Status(),
		IP:         c.ClientIP(),
		UA:         c.Request.UserAgent(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := dao.Insert(ctx, l); err != nil {
		logger.L().Warn("audit insert failed", zap.Error(err),
			zap.String("path", c.FullPath()), zap.Duration("elapsed", time.Since(start)))
	}
}

// deriveAction 从路径+方法推导一个语义化 action,形如 "accounts.create"。
func deriveAction(c *gin.Context) string {
	p := strings.TrimPrefix(c.FullPath(), "/api/admin/")
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return c.Request.Method
	}
	// 把占位符换成 *,方便在审计里聚合
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		if strings.HasPrefix(seg, ":") {
			parts[i] = "*"
		}
	}
	key := strings.ReplaceAll(strings.Join(parts, "."), "-", "_")
	switch c.Request.Method {
	case "POST":
		return key + ".create"
	case "PUT", "PATCH":
		return key + ".update"
	case "DELETE":
		return key + ".delete"
	default:
		return key + "." + strings.ToLower(c.Request.Method)
	}
}

func isWrite(m string) bool {
	switch m {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	}
	return false
}
