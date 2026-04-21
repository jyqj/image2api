// cmd/utls-probe 是一个独立小工具,用于验证 utls transport 是否能穿透 Cloudflare。
// 用法(在容器里):  /app/utls-probe   或者直接 go run ./cmd/utls-probe  (本机也行)
//
// 它做 2 件事:
//  1. GET https://chatgpt.com/  →  打印 status / cookie / header
//  2. POST https://chatgpt.com/backend-api/sentinel/chat-requirements (no bearer)
//     →  打印 status + body 前 400 字节
//
// 目的是快速判断:我们的 JA3/JA4 指纹是否被 CF 放行、响应里 Set-Cookie 了哪些
// 关键 cookie、chat-requirements 返回的 body 结构是什么。

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/432539/gpt2api/internal/upstream/chatgpt"
)

func main() {
	proxyURL := os.Getenv("UTLS_PROBE_PROXY")
	tr, err := chatgpt.NewUTLSTransport(proxyURL, 30*time.Second)
	if err != nil {
		fmt.Println("transport init:", err)
		os.Exit(1)
	}
	hc := &http.Client{Transport: tr, Timeout: 30 * time.Second}

	// 1. GET /
	fmt.Println("== GET https://chatgpt.com/ ==")
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://chatgpt.com/", nil)
	req.Header.Set("User-Agent", chatgpt.DefaultUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Sec-Ch-Ua", `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	res, err := hc.Do(req)
	if err != nil {
		fmt.Println("  do:", err)
		os.Exit(1)
	}
	buf, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	fmt.Printf("  status=%d  proto=%s  body_len=%d\n", res.StatusCode, res.Proto, len(buf))
	fmt.Println("  set-cookie:")
	for _, sc := range res.Header.Values("Set-Cookie") {
		if i := strings.Index(sc, ";"); i > 0 {
			sc = sc[:i]
		}
		fmt.Println("    ", sc)
	}
	fmt.Println("  body head:")
	fmt.Println("   ", preview(buf, 300))

	// 2. POST chat-requirements (no bearer → 应该返回 401,证明至少通过了 CF)
	fmt.Println()
	fmt.Println("== POST /backend-api/sentinel/chat-requirements (no auth) ==")
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"https://chatgpt.com/backend-api/sentinel/chat-requirements",
		strings.NewReader(`{"p":"gAAAAACxxx"}`))
	req2.Header.Set("User-Agent", chatgpt.DefaultUserAgent)
	req2.Header.Set("Accept", "*/*")
	req2.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Origin", "https://chatgpt.com")
	req2.Header.Set("Referer", "https://chatgpt.com/")
	req2.Header.Set("Sec-Ch-Ua", `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`)
	req2.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req2.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	req2.Header.Set("Sec-Fetch-Dest", "empty")
	req2.Header.Set("Sec-Fetch-Mode", "cors")
	req2.Header.Set("Sec-Fetch-Site", "same-origin")
	res2, err := hc.Do(req2)
	if err != nil {
		fmt.Println("  do:", err)
		os.Exit(1)
	}
	buf2, _ := io.ReadAll(res2.Body)
	_ = res2.Body.Close()
	fmt.Printf("  status=%d  proto=%s  body_len=%d\n", res2.StatusCode, res2.Proto, len(buf2))
	fmt.Println("  body head:")
	fmt.Println("   ", preview(buf2, 300))
}

func preview(b []byte, n int) string {
	s := string(b)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
