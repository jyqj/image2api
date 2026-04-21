package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

// mountSPA 把前端 Vite 产物(web/dist)挂到 `/` 上,并实现 SPA 回退(deep link 刷新)。
//
// 路径选择优先级:
//  1. 环境变量 GPT2API_WEB_DIR
//  2. 容器默认:/app/web/dist
//  3. 源码工作目录:./web/dist
//  4. 都不存在则什么都不挂(退化为纯 API)
//
// 注意:
//   - 只有 GET/HEAD 的 NoRoute 请求才会被 fallback 到 index.html。其它方法保持 404。
//   - 明确排除 /api/、/v1/、/healthz、/readyz 等 API 前缀,避免打包问题把接口 404 掩盖成 index.html。
func mountSPA(r *gin.Engine) bool {
	dir := resolveWebDir()
	if dir == "" {
		return false
	}
	indexPath := filepath.Join(dir, "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		return false
	}

	// 静态资源(/assets/** 等)
	r.Static("/assets", filepath.Join(dir, "assets"))

	// 常见的"在根下"的单文件
	registerRootFile := func(name string) {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			r.StaticFile("/"+name, p)
		}
	}
	registerRootFile("favicon.svg")
	registerRootFile("favicon.ico")
	registerRootFile("robots.txt")

	// 根路径直接返回 index.html,而不是 404。
	r.GET("/", func(c *gin.Context) { c.File(indexPath) })

	// NoRoute 兜底:仅对 GET/HEAD 且不在 API 前缀下的请求返回 index.html,
	// 让前端 vue-router 接管 deep link。
	r.NoRoute(func(c *gin.Context) {
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.Status(http.StatusNotFound)
			return
		}
		p := c.Request.URL.Path
		for _, prefix := range apiPrefixes {
			if strings.HasPrefix(p, prefix) {
				c.Status(http.StatusNotFound)
				return
			}
		}
		c.File(indexPath)
	})
	return true
}

// API 前缀白名单:凡是命中这里的请求不做 SPA fallback。
var apiPrefixes = []string{
	"/api/",
	"/v1/",
	"/healthz",
	"/readyz",
	"/assets/",
}

func resolveWebDir() string {
	if d := os.Getenv("GPT2API_WEB_DIR"); d != "" {
		if isDir(d) {
			return d
		}
	}
	candidates := []string{
		"/app/web/dist",
		"./web/dist",
	}
	for _, d := range candidates {
		if isDir(d) {
			abs, _ := filepath.Abs(d)
			return abs
		}
	}
	return ""
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	if err != nil {
		return false
	}
	return st.IsDir()
}
