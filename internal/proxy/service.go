package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/432539/gpt2api/pkg/crypto"
)

// Service 封装代理 CRUD + 密码加解密。
type Service struct {
	dao    *DAO
	cipher *crypto.AESGCM
}

func NewService(dao *DAO, cipher *crypto.AESGCM) *Service {
	return &Service{dao: dao, cipher: cipher}
}

// DAO 暴露给外部使用。
func (s *Service) DAO() *DAO { return s.dao }

// CreateInput 是 Create 的入参(明文 password)。
type CreateInput struct {
	Scheme   string `json:"scheme"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Country  string `json:"country"`
	ISP      string `json:"isp"`
	Enabled  bool   `json:"enabled"`
	Remark   string `json:"remark"`
}

// UpdateInput 是 Update 的入参;Password 为空时不改密。
type UpdateInput struct {
	Scheme   string `json:"scheme"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"` // 空串表示不改
	Country  string `json:"country"`
	ISP      string `json:"isp"`
	Enabled  bool   `json:"enabled"`
	Remark   string `json:"remark"`
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*Proxy, error) {
	if in.Scheme == "" {
		in.Scheme = "http"
	}
	if in.Host == "" || in.Port == 0 {
		return nil, errors.New("host 和 port 不能为空")
	}
	var enc string
	if in.Password != "" {
		v, err := s.cipher.EncryptString(in.Password)
		if err != nil {
			return nil, err
		}
		enc = v
	}
	p := &Proxy{
		Scheme: in.Scheme, Host: in.Host, Port: in.Port,
		Username: in.Username, PasswordEnc: enc,
		Country: in.Country, ISP: in.ISP,
		HealthScore: 100, Enabled: in.Enabled, Remark: in.Remark,
	}
	id, err := s.dao.Create(ctx, p)
	if err != nil {
		return nil, err
	}
	return s.dao.GetByID(ctx, id)
}

func (s *Service) Update(ctx context.Context, id uint64, in UpdateInput) (*Proxy, error) {
	p, err := s.dao.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Scheme != "" {
		p.Scheme = in.Scheme
	}
	if in.Host != "" {
		p.Host = in.Host
	}
	if in.Port != 0 {
		p.Port = in.Port
	}
	p.Username = in.Username
	p.Country = in.Country
	p.ISP = in.ISP
	p.Enabled = in.Enabled
	p.Remark = in.Remark
	if in.Password != "" {
		enc, err := s.cipher.EncryptString(in.Password)
		if err != nil {
			return nil, err
		}
		p.PasswordEnc = enc
	}
	if err := s.dao.Update(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Service) Delete(ctx context.Context, id uint64) error {
	return s.dao.SoftDelete(ctx, id)
}

func (s *Service) Get(ctx context.Context, id uint64) (*Proxy, error) {
	return s.dao.GetByID(ctx, id)
}

func (s *Service) List(ctx context.Context, offset, limit int) ([]*Proxy, int64, error) {
	return s.dao.List(ctx, offset, limit)
}

// ---------- 批量导入 ----------

// ImportDefaults 批量导入时的公共默认值。
type ImportDefaults struct {
	Enabled   bool
	Country   string
	ISP       string
	Remark    string
	Overwrite bool // true 时遇到已存在记录更新密码/备注;false 直接 skip
}

// ImportLineResult 单行结果。Status ∈ "created" | "updated" | "skipped" | "invalid"。
type ImportLineResult struct {
	Line   int    `json:"line"`
	Raw    string `json:"raw"` // 原始输入(密码会被 *** 掉)
	Status string `json:"status"`
	ID     uint64 `json:"id,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ImportBatch 解析多行代理 URL,逐条入库。
// 支持格式:
//
//	scheme://user:pass@host:port
//	scheme://host:port               (无鉴权)
//	user:pass@host:port              (无 scheme,默认 http)
//	host:port                        (无 scheme 无鉴权,默认 http)
//	host:port:user:pass              (常见代理商格式)
//	user:pass:host:port              (另一种代理商格式)
//
// 以 # 或 // 开头的行视为注释跳过。空行跳过。
func (s *Service) ImportBatch(ctx context.Context, text string, defaults ImportDefaults) ([]ImportLineResult, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	out := make([]ImportLineResult, 0, len(lines))

	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		r := ImportLineResult{Line: i + 1, Raw: maskPassword(line)}

		in, err := parseProxyLine(line)
		if err != nil {
			r.Status = "invalid"
			r.Error = err.Error()
			out = append(out, r)
			continue
		}
		// 合并公共默认值
		in.Enabled = defaults.Enabled
		if in.Country == "" {
			in.Country = defaults.Country
		}
		if in.ISP == "" {
			in.ISP = defaults.ISP
		}
		if in.Remark == "" {
			in.Remark = defaults.Remark
		}

		// 查重:同 scheme+host+port+username 若已存在 → update / skip
		existing, err := s.dao.FindByEndpoint(ctx, in.Scheme, in.Host, in.Port, in.Username)
		if err != nil && !errors.Is(err, ErrNotFound) {
			r.Status = "invalid"
			r.Error = err.Error()
			out = append(out, r)
			continue
		}
		if existing != nil {
			if !defaults.Overwrite {
				r.Status = "skipped"
				r.ID = existing.ID
				r.Error = "已存在同 scheme+host:port+用户名 的代理,跳过"
				out = append(out, r)
				continue
			}
			// 覆盖模式:保留 id/health_score,更新其他字段
			existing.Scheme = in.Scheme
			existing.Host = in.Host
			existing.Port = in.Port
			existing.Username = in.Username
			existing.Country = in.Country
			existing.ISP = in.ISP
			existing.Enabled = in.Enabled
			existing.Remark = in.Remark
			if in.Password != "" {
				enc, encErr := s.cipher.EncryptString(in.Password)
				if encErr != nil {
					r.Status = "invalid"
					r.Error = encErr.Error()
					out = append(out, r)
					continue
				}
				existing.PasswordEnc = enc
			}
			if err := s.dao.Update(ctx, existing); err != nil {
				r.Status = "invalid"
				r.Error = err.Error()
				out = append(out, r)
				continue
			}
			r.Status = "updated"
			r.ID = existing.ID
			out = append(out, r)
			continue
		}

		// 新建
		p, err := s.Create(ctx, in)
		if err != nil {
			r.Status = "invalid"
			r.Error = err.Error()
			out = append(out, r)
			continue
		}
		r.Status = "created"
		r.ID = p.ID
		out = append(out, r)
	}
	return out, nil
}

// parseProxyLine 解析一行代理串为 CreateInput(不含 Enabled/Remark 默认值)。
func parseProxyLine(line string) (CreateInput, error) {
	var out CreateInput
	trim := strings.TrimSpace(line)
	if trim == "" {
		return out, errors.New("空行")
	}

	scheme := "http"
	rest := trim
	if i := strings.Index(rest, "://"); i > 0 {
		scheme = strings.ToLower(strings.TrimSpace(rest[:i]))
		rest = rest[i+3:]
	}
	if !isSupportedProxyScheme(scheme) {
		return out, fmt.Errorf("不支持的协议 %q(仅支持 http/https/socks5)", scheme)
	}
	if parsed, ok, err := parseColonProxyLine(scheme, rest); ok || err != nil {
		return parsed, err
	}

	// 补全 scheme,让 net/url 能正确解析 user/pass。
	if !strings.Contains(trim, "://") {
		trim = scheme + "://" + trim
	}
	u, err := url.Parse(trim)
	if err != nil {
		return out, fmt.Errorf("URL 格式错误:%w", err)
	}
	scheme = strings.ToLower(u.Scheme)
	if !isSupportedProxyScheme(scheme) {
		return out, fmt.Errorf("不支持的协议 %q(仅支持 http/https/socks5)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return out, errors.New("缺少 host")
	}
	portStr := u.Port()
	if portStr == "" {
		return out, errors.New("缺少 port")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return out, fmt.Errorf("端口 %q 非法(需 1~65535)", portStr)
	}
	out.Scheme = scheme
	out.Host = host
	out.Port = port
	if u.User != nil {
		out.Username = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			out.Password = pw
		}
	}
	return out, nil
}

func parseColonProxyLine(scheme, rest string) (CreateInput, bool, error) {
	var out CreateInput
	if strings.Contains(rest, "@") || strings.Contains(rest, "[") || strings.Contains(rest, "]") {
		return out, false, nil
	}
	parts := strings.Split(rest, ":")
	if len(parts) != 4 {
		return out, false, nil
	}
	if p, ok := parseProxyPort(parts[1]); ok {
		return CreateInput{Scheme: scheme, Host: strings.TrimSpace(parts[0]), Port: p, Username: strings.TrimSpace(parts[2]), Password: strings.TrimSpace(parts[3])}, true, validateColonProxy(parts[0], p)
	}
	if p, ok := parseProxyPort(parts[3]); ok {
		return CreateInput{Scheme: scheme, Host: strings.TrimSpace(parts[2]), Port: p, Username: strings.TrimSpace(parts[0]), Password: strings.TrimSpace(parts[1])}, true, validateColonProxy(parts[2], p)
	}
	return out, false, nil
}

func parseProxyPort(s string) (int, bool) {
	p, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || p < 1 || p > 65535 {
		return 0, false
	}
	return p, true
}

func validateColonProxy(host string, port int) error {
	if strings.TrimSpace(host) == "" {
		return errors.New("缺少 host")
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("端口 %d 非法(需 1~65535)", port)
	}
	return nil
}

func isSupportedProxyScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "http", "https", "socks5", "socks5h":
		return true
	default:
		return false
	}
}

// maskPassword 把 user:pass@ 里的 pass 替换成 ***,用于 result.raw 回显。
func maskPassword(line string) string {
	trim := strings.TrimSpace(line)
	scheme := ""
	rest := trim
	if i := strings.Index(rest, "://"); i >= 0 {
		scheme = rest[:i+3]
		rest = rest[i+3:]
	}
	if !strings.Contains(rest, "@") {
		parts := strings.Split(rest, ":")
		if len(parts) == 4 {
			if _, ok := parseProxyPort(parts[1]); ok {
				return scheme + parts[0] + ":" + parts[1] + ":" + parts[2] + ":***"
			}
			if _, ok := parseProxyPort(parts[3]); ok {
				return scheme + parts[0] + ":***:" + parts[2] + ":" + parts[3]
			}
		}
		return line
	}
	at := strings.Index(rest, "@")
	cred := rest[:at]
	tail := rest[at:]
	colon := strings.Index(cred, ":")
	if colon < 0 {
		return line
	}
	return scheme + cred[:colon] + ":***" + tail
}

// DecryptPassword 解密代理密码。
func (s *Service) DecryptPassword(p *Proxy) (string, error) {
	if p.PasswordEnc == "" {
		return "", nil
	}
	return s.cipher.DecryptString(p.PasswordEnc)
}

// BuildURL 返回完整代理 URL(含明文密码)。
func (s *Service) BuildURL(p *Proxy) (string, error) {
	pw, err := s.DecryptPassword(p)
	if err != nil {
		return "", err
	}
	return p.URLWithPassword(pw), nil
}
