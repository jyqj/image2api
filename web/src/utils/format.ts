export function formatDateTime(v: string | { Time: string; Valid: boolean } | null | undefined): string {
  if (!v) return '-'
  let s = ''
  if (typeof v === 'string') s = v
  else if (v.Valid && v.Time) s = v.Time
  if (!s) return '-'
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return s
  return d.toLocaleString()
}

export function formatDateShort(v: string | { Time: string; Valid: boolean } | null | undefined): string {
  if (!v) return '-'
  let s = ''
  if (typeof v === 'string') s = v
  else if (v.Valid && v.Time) s = v.Time
  if (!s) return '-'
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return s
  return d.toLocaleDateString()
}

export function formatBytes(n: number | null | undefined): string {
  if (!n || n <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

export function formatDuration(ms: number | null | undefined): string {
  if (!ms || ms <= 0) return '-'
  if (ms < 1000) return `${ms} ms`
  return `${(ms / 1000).toFixed(2)} s`
}

export function formatErrorCode(code?: string | null): string {
  if (!code) return '-'
  const m: Record<string, string> = {
    upstream_init_error: '上游客户端初始化失败',
    upstream_error: '上游返回错误',
    invalid_request_error: '请求参数错误',
    model_not_found: '模型未开放',
    account_dispatch_timeout: '等待可用账号超时',
    proxy_error: '代理异常',
    timeout: '请求超时',
  }
  return m[code] || code
}
