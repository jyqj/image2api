package proxy

import (
	"database/sql"
	"time"
)

// Proxy 对应 proxies 表。
// password 在表里是加密存储的(AES-256-GCM),这里只暴露密文字段,
// 取明文时通过 Service.Decrypt。
type Proxy struct {
	ID          uint64       `db:"id" json:"id"`
	Scheme      string       `db:"scheme" json:"scheme"`
	Host        string       `db:"host" json:"host"`
	Port        int          `db:"port" json:"port"`
	Username    string       `db:"username" json:"username"`
	PasswordEnc string       `db:"password_enc" json:"-"`
	Country     string       `db:"country" json:"country"`
	ISP         string       `db:"isp" json:"isp"`
	HealthScore int          `db:"health_score" json:"health_score"`
	LastProbeAt sql.NullTime `db:"last_probe_at" json:"last_probe_at,omitempty"`
	LastError   string       `db:"last_error" json:"last_error,omitempty"`
	Enabled     bool         `db:"enabled" json:"enabled"`
	Remark      string       `db:"remark" json:"remark"`
	CreatedAt   time.Time    `db:"created_at" json:"created_at"`
	UpdatedAt   time.Time    `db:"updated_at" json:"updated_at"`
	DeletedAt   sql.NullTime `db:"deleted_at" json:"-"`
}

// URL 返回代理 URL(解密后由 Service 组装)。
func (p *Proxy) URLWithPassword(password string) string {
	if p.Username == "" {
		return p.Scheme + "://" + host(p.Host, p.Port)
	}
	if password == "" {
		return p.Scheme + "://" + p.Username + "@" + host(p.Host, p.Port)
	}
	return p.Scheme + "://" + p.Username + ":" + password + "@" + host(p.Host, p.Port)
}

func host(h string, port int) string {
	return h + ":" + itoa(port)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	bp := len(buf)
	for i > 0 {
		bp--
		buf[bp] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		bp--
		buf[bp] = '-'
	}
	return string(buf[bp:])
}
