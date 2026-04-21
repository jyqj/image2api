package mailer

import "html"

func htmlEscape(s string) string { return html.EscapeString(s) }

// RenderTest 生成 SMTP 测试邮件。
func RenderTest(siteName string) (subject, html string) {
	if siteName == "" {
		siteName = "GPT2API Local"
	}
	subject = siteName + " SMTP 测试"
	html = `<html><body><h1>` + htmlEscape(siteName) + `</h1><p>这是一封本地控制台测试邮件。</p></body></html>`
	return
}
