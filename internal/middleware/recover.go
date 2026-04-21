package middleware

import (
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/432539/gpt2api/pkg/logger"
	"github.com/432539/gpt2api/pkg/resp"
)

// Recover 捕获 panic,写入日志并返回 500。
func Recover() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				logger.L().Error("panic recovered",
					zap.Any("err", r),
					zap.ByteString("stack", debug.Stack()),
					zap.String("path", c.Request.URL.Path),
					zap.String("request_id", getString(c, "request_id")),
				)
				resp.Internal(c, "internal server error")
			}
		}()
		c.Next()
	}
}
