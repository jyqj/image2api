package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	CtxActorID    = "actor_id"
	CtxActorEmail = "actor_email"
)

// tokenSecret 从 aes_key 派生,进程启动后不变。
var tokenSecret []byte

// InitTokenSecret 从 aes_key 派生 admin token 签名密钥。由 main.go 调用。
func InitTokenSecret(aesKeyHex string) {
	h := sha256.Sum256([]byte("admin-token-secret:" + aesKeyHex))
	tokenSecret = h[:]
}

// GenerateToken 签发 admin token。格式: timestamp_hex.hmac_hex
// 有效期 7 天。
func GenerateToken(username string) string {
	exp := time.Now().Add(7 * 24 * time.Hour).Unix()
	payload := fmt.Sprintf("%s|%d", username, exp)
	mac := hmac.New(sha256.New, tokenSecret)
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("%s.%s", payload, sig)
}

// ValidateToken 验证 token,返回 username 或空串。
func ValidateToken(token string) string {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return ""
	}
	payload, sig := parts[0], parts[1]

	mac := hmac.New(sha256.New, tokenSecret)
	mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return ""
	}

	fields := strings.SplitN(payload, "|", 2)
	if len(fields) != 2 {
		return ""
	}
	exp, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return ""
	}
	return fields[0]
}

// AdminAuth 验证 admin token 的中间件。
// 从 Authorization: Bearer <token> 或 query 参数 token 中提取。
func AdminAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := ""
		if auth := c.GetHeader("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			token = strings.TrimPrefix(auth, "Bearer ")
		}
		if token == "" {
			token = c.Query("token")
		}
		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "未登录"})
			return
		}
		username := ValidateToken(token)
		if username == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "登录已过期，请重新登录"})
			return
		}
		c.Set(CtxActorID, uint64(0))
		c.Set(CtxActorEmail, username)
		c.Next()
	}
}

// V1APIKeyGetter 获取当前配置的 API Key。
type V1APIKeyGetter interface {
	V1APIKey() string
}

// V1APIKeyAuth 验证 /v1/* 请求的 API Key。
// key 为空时不验证(开放访问)。
func V1APIKeyAuth(getter V1APIKeyGetter) gin.HandlerFunc {
	return func(c *gin.Context) {
		configuredKey := getter.V1APIKey()
		if configuredKey == "" {
			c.Set(CtxActorID, uint64(0))
			c.Set(CtxActorEmail, "anonymous")
			c.Next()
			return
		}
		auth := c.GetHeader("Authorization")
		providedKey := ""
		if strings.HasPrefix(auth, "Bearer ") {
			providedKey = strings.TrimPrefix(auth, "Bearer ")
		}
		if providedKey == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"message": "Missing API key. Set Authorization: Bearer <key>", "type": "invalid_request_error"},
			})
			return
		}
		if !hmac.Equal([]byte(providedKey), []byte(configuredKey)) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"message": "Invalid API key", "type": "invalid_request_error"},
			})
			return
		}
		c.Set(CtxActorID, uint64(0))
		c.Set(CtxActorEmail, "api-client")
		c.Next()
	}
}

// LocalActor 为单人本地控制台注入固定操作者信息。
func LocalActor() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(CtxActorID, uint64(0))
		c.Set(CtxActorEmail, "local-console")
		c.Next()
	}
}

// UserID 在本地模式中始终返回 0,仅用于审计/设置等兼容字段。
func UserID(c *gin.Context) uint64 {
	if c == nil {
		return 0
	}
	if v, ok := c.Get(CtxActorID); ok {
		switch x := v.(type) {
		case uint64:
			return x
		case int:
			if x >= 0 {
				return uint64(x)
			}
		}
	}
	return 0
}

// ActorID 返回本地操作者标识。
func ActorID(c *gin.Context) uint64 { return UserID(c) }

// Role 返回本地控制台角色。
func Role(c *gin.Context) string { return "local" }
