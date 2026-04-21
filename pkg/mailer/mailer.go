// Package mailer 提供最小可用的 SMTP 发信能力。
//
// 设计:
//   - 使用标准库 net/smtp + crypto/tls,零外部依赖。
//   - 支持 465 隐式 TLS 与 587 STARTTLS。
//   - 异步发送(一个 worker 协程),内部有 100 大小的 buffered chan,
//     chan 满时直接丢弃并打 warn 日志,绝不阻塞业务主流程。
//   - Enabled=false 时全部 Send 变成 no-op。
package mailer

import (
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Config 与 config.SMTPConfig 对齐。
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	FromName string
	UseTLS   bool
}

// Message 是一次发送。
type Message struct {
	To      string
	Subject string
	HTML    string
}

// Mailer 可被多个业务复用。
type Mailer struct {
	cfg    Config
	ch     chan Message
	log    *zap.Logger
	wg     sync.WaitGroup
	closed chan struct{}
	once   sync.Once
}

// Disabled 表示当前 mailer 未配置。
func (m *Mailer) Disabled() bool { return m == nil || m.cfg.Host == "" }

// New 构造 Mailer 并启动后台协程。
// host 为空时返回一个 disabled 的实例(Send 成 no-op)。
func New(cfg Config, log *zap.Logger) *Mailer {
	m := &Mailer{
		cfg:    cfg,
		ch:     make(chan Message, 100),
		log:    log.With(zap.String("mod", "mailer")),
		closed: make(chan struct{}),
	}
	if !m.Disabled() {
		m.wg.Add(1)
		go m.loop()
	}
	return m
}

func (m *Mailer) loop() {
	defer m.wg.Done()
	for msg := range m.ch {
		if err := m.send(msg); err != nil {
			m.log.Warn("smtp send failed",
				zap.String("to", msg.To),
				zap.String("subject", msg.Subject),
				zap.Error(err))
		}
	}
	close(m.closed)
}

// SendSync 同步发送,直接把错误抛给调用方。
// 专供"测试发送"等需要立即反馈结果的场景;业务路径请继续用 Send。
func (m *Mailer) SendSync(msg Message) error {
	if m.Disabled() {
		return errors.New("mailer disabled: SMTP not configured")
	}
	return m.send(msg)
}

// Send 非阻塞投递。
// chan 满时打 warn 并丢弃(邮件不是主业务路径)。
func (m *Mailer) Send(msg Message) {
	if m.Disabled() {
		return
	}
	select {
	case m.ch <- msg:
	default:
		m.log.Warn("mail queue full, drop message",
			zap.String("to", msg.To), zap.String("subject", msg.Subject))
	}
}

func (m *Mailer) Close() {
	if m.Disabled() {
		return
	}
	m.once.Do(func() {
		close(m.ch)
	})
	select {
	case <-m.closed:
	case <-time.After(5 * time.Second):
		m.log.Warn("mailer close timeout")
	}
	m.wg.Wait()
}

func (m *Mailer) send(msg Message) error {
	if msg.To == "" || msg.Subject == "" {
		return errors.New("mailer: to/subject empty")
	}
	addr := net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port))

	fromHeader := m.cfg.From
	if m.cfg.FromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", m.cfg.FromName, m.cfg.From)
	}

	headers := map[string]string{
		"From":         fromHeader,
		"To":           msg.To,
		"Subject":      encodeSubject(msg.Subject),
		"MIME-Version": "1.0",
		"Content-Type": `text/html; charset="UTF-8"`,
	}
	var buf []byte
	for k, v := range headers {
		buf = append(buf, []byte(k+": "+v+"\r\n")...)
	}
	buf = append(buf, []byte("\r\n")...)
	buf = append(buf, []byte(msg.HTML)...)

	auth := smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)

	if m.cfg.UseTLS {
		// 465:隐式 TLS —— 先 TLS 再 SMTP 握手
		return sendTLS(addr, m.cfg.Host, auth, m.cfg.From, msg.To, buf)
	}
	// 587:明文起 STARTTLS
	return smtp.SendMail(addr, auth, m.cfg.From, []string{msg.To}, buf)
}

// sendTLS 实现 SMTPS(465) 隐式 TLS。
func sendTLS(addr, host string, auth smtp.Auth, from, to string, body []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
	if err != nil {
		return fmt.Errorf("tls dial: %w", err)
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()

	if ok, _ := c.Extension("AUTH"); ok {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close: %w", err)
	}
	return c.Quit()
}

// encodeSubject 把 UTF-8 标题按 RFC 2047 封装,避免中文乱码。
// 对全 ASCII 的标题保持原样,对含非 ASCII 的用 Q-encoded。
func encodeSubject(s string) string {
	return mime.QEncoding.Encode("UTF-8", s)
}
