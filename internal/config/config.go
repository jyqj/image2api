package config

import (
	"fmt"
	"strings"
	"sync"

	"github.com/spf13/viper"
)

type Config struct {
	App       AppConfig       `mapstructure:"app"`
	Admin     AdminConfig     `mapstructure:"admin"`
	Log       LogConfig       `mapstructure:"log"`
	MySQL     MySQLConfig     `mapstructure:"mysql"`
	Redis     RedisConfig     `mapstructure:"redis"`
	Crypto    CryptoConfig    `mapstructure:"crypto"`
	Security  SecurityConfig  `mapstructure:"security"`
	Scheduler SchedulerConfig `mapstructure:"scheduler"`
	Upstream  UpstreamConfig  `mapstructure:"upstream"`
	Backup    BackupConfig    `mapstructure:"backup"`
	SMTP      SMTPConfig      `mapstructure:"smtp"`
}

type AppConfig struct {
	Name      string `mapstructure:"name"`
	Env       string `mapstructure:"env"`
	Listen    string `mapstructure:"listen"`
	BaseURL   string `mapstructure:"base_url"`
	LocalMode bool   `mapstructure:"local_mode"`
}

type AdminConfig struct {
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
}

type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
	Output string `mapstructure:"output"`
}

type MySQLConfig struct {
	DSN                string `mapstructure:"dsn"`
	MaxOpenConns       int    `mapstructure:"max_open_conns"`
	MaxIdleConns       int    `mapstructure:"max_idle_conns"`
	ConnMaxLifetimeSec int    `mapstructure:"conn_max_lifetime_sec"`
}

type RedisConfig struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
	PoolSize int    `mapstructure:"pool_size"`
}

type CryptoConfig struct {
	AESKey string `mapstructure:"aes_key"`
}

type SecurityConfig struct {
	CORSOrigins []string `mapstructure:"cors_origins"`
}

type SchedulerConfig struct {
	MinIntervalSec          int `mapstructure:"min_interval_sec"`
	LockTTLSec              int `mapstructure:"lock_ttl_sec"`
	Cooldown429Sec          int `mapstructure:"cooldown_429_sec"`
	WarnedPauseHours        int `mapstructure:"warned_pause_hours"`
	MaxConcurrentPerAccount int `mapstructure:"max_concurrent_per_account"` // 单账号最大并发,默认 3
}

type UpstreamConfig struct {
	BaseURL           string `mapstructure:"base_url"`
	RequestTimeoutSec int    `mapstructure:"request_timeout_sec"`
	SSEReadTimeoutSec int    `mapstructure:"sse_read_timeout_sec"`
}

// BackupConfig 数据库备份配置。
type BackupConfig struct {
	Dir          string `mapstructure:"dir"`           // 备份落盘目录,默认 /app/data/backups
	Retention    int    `mapstructure:"retention"`     // 保留最近 N 个(>0),0 表示不自动清理
	MysqldumpBin string `mapstructure:"mysqldump_bin"` // 默认 mysqldump
	MysqlBin     string `mapstructure:"mysql_bin"`     // 恢复用,默认 mysql
	MaxUploadMB  int    `mapstructure:"max_upload_mb"` // 上传 .sql.gz 上限,默认 512
	AllowRestore bool   `mapstructure:"allow_restore"` // 是否允许 /restore 端点(生产强烈建议 false 手动切)
}

// SMTPConfig 用于测试邮件和系统通知。
// Host 为空时邮件通道整体关闭,不影响主流程。
type SMTPConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
	From     string `mapstructure:"from"`      // 显示的 From 地址
	FromName string `mapstructure:"from_name"` // 显示名
	UseTLS   bool   `mapstructure:"use_tls"`   // true 隐式 TLS(465),false STARTTLS(587)
}

var (
	global *Config
	once   sync.Once
)

func Load(path string) (*Config, error) {
	var loadErr error
	once.Do(func() {
		v := viper.New()
		v.SetConfigFile(path)
		v.SetEnvPrefix("GPT2API")
		v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
		v.SetDefault("app.local_mode", true)
		v.SetDefault("admin.username", "admin")
		v.SetDefault("admin.password", "admin123")
		v.AutomaticEnv()
		if err := v.ReadInConfig(); err != nil {
			loadErr = fmt.Errorf("read config: %w", err)
			return
		}
		var c Config
		if err := v.Unmarshal(&c); err != nil {
			loadErr = fmt.Errorf("unmarshal config: %w", err)
			return
		}
		global = &c
		// 校验必填字段,拒绝明显未配置的默认值
		loadErr = validate(&c)
	})
	return global, loadErr
}

// Get 返回全局配置,仅在 Load 之后调用。
func Get() *Config {
	if global == nil {
		panic("config not loaded; call config.Load first")
	}
	return global
}

// validate 校验配置必填字段,拒绝未配置的占位默认值。
func validate(c *Config) error {
	var errs []string
	if c.Crypto.AESKey == "" || c.Crypto.AESKey == "CHANGE_ME_TO_RANDOM_32_BYTES_SECRET" {
		errs = append(errs, "crypto.aes_key is required and must not be the default placeholder")
	}
	if c.MySQL.DSN == "" {
		errs = append(errs, "mysql.dsn is required")
	}
	if c.Redis.Addr == "" {
		errs = append(errs, "redis.addr is required")
	}
	if len(errs) > 0 {
		return fmt.Errorf("config validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}
