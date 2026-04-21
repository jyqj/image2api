// Package settings 系统设置(KV)。
package settings

import "strings"

// KeyDef 声明可编辑的设置项。
type KeyDef struct {
	Key      string
	Type     string // string | bool | int | float | email | url
	Category string // site | gateway | account | mail
	Default  string
	Label    string
	Desc     string
	Public   bool
}

const (
	SiteName          = "site.name"
	SiteDescription   = "site.description"
	SiteLogoURL       = "site.logo_url"
	SiteFooter        = "site.footer"
	SiteContactEmail  = "site.contact_email"
	SiteDocsURL       = "site.docs_url"
	SiteAPIBaseURL    = "site.api_base_url"
	UIDefaultPageSize = "ui.default_page_size"

	GatewayUpstreamTimeoutSec   = "gateway.upstream_timeout_sec"
	GatewaySSEReadTimeoutSec    = "gateway.sse_read_timeout_sec"
	GatewayCooldown429Sec       = "gateway.cooldown_429_sec"
	GatewayWarnedPauseHours     = "gateway.warned_pause_hours"
	GatewayDailyUsageRatio      = "gateway.daily_usage_ratio"
	GatewayRetryOnFailure       = "gateway.retry_on_failure"
	GatewayRetryMax             = "gateway.retry_max"
	GatewayDispatchQueueWaitSec = "gateway.dispatch_queue_wait_sec"
	GatewayImageExploreRatio    = "gateway.image_explore_ratio"

	ProxyProbeEnabled     = "proxy.probe_enabled"
	ProxyProbeIntervalSec = "proxy.probe_interval_sec"
	ProxyProbeTimeoutSec  = "proxy.probe_timeout_sec"
	ProxyProbeTargetURL   = "proxy.probe_target_url"
	ProxyProbeConcurrency = "proxy.probe_concurrency"

	AccountRefreshEnabled        = "account.refresh_enabled"
	AccountRefreshIntervalSec    = "account.refresh_interval_sec"
	AccountRefreshAheadSec       = "account.refresh_ahead_sec"
	AccountRefreshConcurrency    = "account.refresh_concurrency"
	AccountQuotaProbeEnabled     = "account.quota_probe_enabled"
	AccountQuotaProbeIntervalSec = "account.quota_probe_interval_sec"
	AccountDefaultClientID       = "account.default_client_id"

	APIKey = "api.v1_key"

	MailEnabledDisplay = "mail.enabled_display"
)

// Defs 所有合法 key 的 schema。前端设置页按 category 展示。
var Defs = []KeyDef{
	{Key: SiteName, Type: "string", Category: "site", Default: "GPT2API Local", Label: "站点名称", Desc: "展示在顶栏和控制台标题", Public: true},
	{Key: SiteDescription, Type: "string", Category: "site", Default: "自用 OpenAI 兼容 2API 中转", Label: "副标题", Desc: "控制台说明文案", Public: true},
	{Key: SiteLogoURL, Type: "url", Category: "site", Default: "", Label: "Logo URL", Desc: "空则使用默认图标", Public: true},
	{Key: SiteFooter, Type: "string", Category: "site", Default: "", Label: "页脚文案", Desc: "版权/备案号等纯文本", Public: true},
	{Key: SiteContactEmail, Type: "email", Category: "site", Default: "", Label: "联系邮箱", Desc: "本地部署备注邮箱", Public: true},
	{Key: SiteDocsURL, Type: "url", Category: "site", Default: "", Label: "文档链接", Desc: "留空则使用内置文档", Public: true},
	{Key: SiteAPIBaseURL, Type: "url", Category: "site", Default: "", Label: "API Base URL", Desc: "展示给客户端的 /v1 入口;留空=当前站点地址", Public: true},
	{Key: UIDefaultPageSize, Type: "int", Category: "site", Default: "20", Label: "默认每页条数", Desc: "控制台表格默认分页(5~100)"},

	{Key: GatewayUpstreamTimeoutSec, Type: "int", Category: "gateway", Default: "60", Label: "上游请求超时(秒)", Desc: "非流式请求上游响应超时"},
	{Key: GatewaySSEReadTimeoutSec, Type: "int", Category: "gateway", Default: "120", Label: "SSE 读超时(秒)", Desc: "流式响应无数据时的中断阈值"},
	{Key: GatewayCooldown429Sec, Type: "int", Category: "gateway", Default: "300", Label: "429 冷却(秒)", Desc: "账号遇 429 后暂停调度"},
	{Key: GatewayWarnedPauseHours, Type: "int", Category: "gateway", Default: "24", Label: "风险暂停(小时)", Desc: "账号被识别为 warned 时的暂停时长"},
	{Key: GatewayDailyUsageRatio, Type: "float", Category: "gateway", Default: "0.8", Label: "日用比例阈值", Desc: "0.0~1.0;超过后降低调度优先级"},
	{Key: GatewayRetryOnFailure, Type: "bool", Category: "gateway", Default: "true", Label: "失败自动重试", Desc: "遇到可恢复错误时切换账号重试"},
	{Key: GatewayRetryMax, Type: "int", Category: "gateway", Default: "1", Label: "最大重试次数", Desc: "0~3"},
	{Key: GatewayDispatchQueueWaitSec, Type: "int", Category: "gateway", Default: "120", Label: "账号排队等待上限(秒)", Desc: "并发大于账号数时等待空闲账号的最长秒数"},
	{Key: GatewayImageExploreRatio, Type: "float", Category: "gateway", Default: "0.2", Label: "IMG2 账号探索比例", Desc: "image 调度中留给 unknown/新号/长时间未尝试号的比例"},

	{Key: ProxyProbeEnabled, Type: "bool", Category: "gateway", Default: "true", Label: "代理探测开关", Desc: "后台定时对启用的代理做连通性探测"},
	{Key: ProxyProbeIntervalSec, Type: "int", Category: "gateway", Default: "300", Label: "代理探测间隔(秒)", Desc: "两轮探测之间的间隔"},
	{Key: ProxyProbeTimeoutSec, Type: "int", Category: "gateway", Default: "10", Label: "代理探测超时(秒)", Desc: "单条代理一次探测的超时时间"},
	{Key: ProxyProbeTargetURL, Type: "url", Category: "gateway", Default: "https://chatgpt.com/cdn-cgi/trace", Label: "代理探测目标 URL", Desc: "返回 2xx/3xx 视为成功"},
	{Key: ProxyProbeConcurrency, Type: "int", Category: "gateway", Default: "8", Label: "代理探测并发", Desc: "同时探测的代理数(1~64)"},

	{Key: AccountRefreshEnabled, Type: "bool", Category: "account", Default: "true", Label: "账号 AT 自动刷新", Desc: "后台定时刷新即将过期的 AT"},
	{Key: AccountRefreshIntervalSec, Type: "int", Category: "account", Default: "120", Label: "账号刷新扫描间隔(秒)", Desc: "多久扫一次"},
	{Key: AccountRefreshAheadSec, Type: "int", Category: "account", Default: "120", Label: "账号预刷新提前量(秒)", Desc: "距离过期多少秒内触发刷新(不宜过大,避免频繁刷新触发上游风控)"},
	{Key: AccountRefreshConcurrency, Type: "int", Category: "account", Default: "4", Label: "账号刷新并发", Desc: "同时刷新的账号数(1~32)"},
	{Key: AccountQuotaProbeEnabled, Type: "bool", Category: "account", Default: "true", Label: "账号额度自动探测", Desc: "后台定期查询账号的图片剩余量"},
	{Key: AccountQuotaProbeIntervalSec, Type: "int", Category: "account", Default: "900", Label: "剩余量探测最小间隔(秒)", Desc: "同一账号两次探测之间的最小间隔"},
	{Key: AccountDefaultClientID, Type: "string", Category: "account", Default: "app_LlGpXReQgckcGGUo2JrYvtJK", Label: "导入账号默认 client_id", Desc: "JSON 未指定时使用的 OAuth client_id"},

	{Key: APIKey, Type: "string", Category: "api", Default: "", Label: "API Key", Desc: "用于 /v1/* 接口认证;留空则不验证密钥(开放访问)"},

	{Key: MailEnabledDisplay, Type: "string", Category: "mail", Default: "auto", Label: "邮件开关展示", Desc: "auto/true/false;实际由 SMTP 配置决定"},
}

func DefByKey(k string) (KeyDef, bool) {
	for _, d := range Defs {
		if d.Key == k {
			return d, true
		}
	}
	return KeyDef{}, false
}

func IsAllowedKey(k string) bool {
	k = strings.TrimSpace(k)
	if k == "" {
		return false
	}
	_, ok := DefByKey(k)
	return ok
}
