package resp

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// 统一响应结构。HTTP status 只用于框架级错误(401/403/404/500);
// 业务错误一律走 code,HTTP 200。
type Body struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
	TraceID string      `json:"trace_id,omitempty"`
}

const (
	CodeOK           = 0
	CodeBadRequest   = 40000
	CodeUnauthorized = 40100
	CodeForbidden    = 40300
	CodeNotFound     = 40400
	CodeConflict     = 40900
	CodeRateLimited  = 42900
	CodeInternal     = 50000
	CodeUpstream     = 50200
)

func OK(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, Body{Code: CodeOK, Message: "ok", Data: data, TraceID: traceID(c)})
}

func Fail(c *gin.Context, code int, msg string) {
	httpStatus := http.StatusOK
	switch code {
	case CodeUnauthorized:
		httpStatus = http.StatusUnauthorized
	case CodeForbidden:
		httpStatus = http.StatusForbidden
	case CodeNotFound:
		httpStatus = http.StatusNotFound
	case CodeRateLimited:
		httpStatus = http.StatusTooManyRequests
	case CodeInternal, CodeUpstream:
		httpStatus = http.StatusInternalServerError
	}
	c.AbortWithStatusJSON(httpStatus, Body{Code: code, Message: msg, TraceID: traceID(c)})
}

func BadRequest(c *gin.Context, msg string)   { Fail(c, CodeBadRequest, msg) }
func Unauthorized(c *gin.Context, msg string) { Fail(c, CodeUnauthorized, msg) }
func Forbidden(c *gin.Context, msg string)    { Fail(c, CodeForbidden, msg) }
func NotFound(c *gin.Context, msg string)     { Fail(c, CodeNotFound, msg) }
func Conflict(c *gin.Context, msg string)     { Fail(c, CodeConflict, msg) }
func Internal(c *gin.Context, msg string)     { Fail(c, CodeInternal, msg) }
func RateLimited(c *gin.Context, msg string)  { Fail(c, CodeRateLimited, msg) }

func traceID(c *gin.Context) string {
	if v, ok := c.Get("request_id"); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
