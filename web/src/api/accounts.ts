import { http } from './http'

export interface Account {
  id: number
  email: string
  client_id: string
  chatgpt_account_id: string
  account_type: string            // codex / chatgpt
  oai_session_id: string
  oai_device_id: string
  subscription_type: string               // plus / team / free / ...
  daily_image_quota: number
  status: string                  // healthy / warned / throttled / suspicious / dead
  today_used_count: number
  notes: string
  token_expires_at?: { Time: string; Valid: boolean } | string | null
  warned_at?:        { Time: string; Valid: boolean } | string | null
  cooldown_until?:   { Time: string; Valid: boolean } | string | null
  last_used_at?:     { Time: string; Valid: boolean } | string | null
  today_used_date?:  { Time: string; Valid: boolean } | string | null

  last_refresh_at?:  { Time: string; Valid: boolean } | string | null
  last_refresh_source: string
  refresh_error: string

  image_quota_remaining: number
  image_quota_total: number
  image_quota_reset_at?:   { Time: string; Valid: boolean } | string | null
  image_quota_updated_at?: { Time: string; Valid: boolean } | string | null

  has_rt: boolean
  has_st: boolean

  created_at: string
  updated_at: string
}

export interface Page<T> {
  list: T[]; total: number; page: number; page_size: number
}

export function listAccounts(params: {
  page?: number; page_size?: number; status?: string; keyword?: string
} = {}) {
  return http.get<any, Page<Account>>('/api/admin/accounts', { params })
}

export function getAccount(id: number) {
  return http.get<any, Account>(`/api/admin/accounts/${id}`)
}

export interface AccountCreate {
  email: string
  auth_token: string
  refresh_token?: string
  session_token?: string
  token_expires_at?: string
  oai_session_id?: string
  oai_device_id?: string
  client_id?: string
  chatgpt_account_id?: string
  account_type?: string
  subscription_type?: string
  daily_image_quota?: number
  notes?: string
  cookies?: string
  proxy_id?: number
}
export interface AccountUpdate extends Partial<AccountCreate> {
  status?: string
}

export function createAccount(body: AccountCreate) {
  return http.post<any, Account>('/api/admin/accounts', body)
}
export function updateAccount(id: number, body: AccountUpdate) {
  return http.patch<any, Account>(`/api/admin/accounts/${id}`, body)
}
export function deleteAccount(id: number) {
  return http.delete<any, { deleted: number }>(`/api/admin/accounts/${id}`)
}
export function bindProxy(id: number, proxyID: number) {
  return http.post<any, { ok: boolean }>(`/api/admin/accounts/${id}/bind-proxy`, { proxy_id: proxyID })
}
export function unbindProxy(id: number) {
  return http.delete<any, { ok: boolean }>(`/api/admin/accounts/${id}/bind-proxy`)
}

// ---------- 批量导入 ----------
export interface ImportLineResult {
  index: number
  email: string
  status: 'created' | 'updated' | 'skipped' | 'failed'
  reason?: string
  id?: number
}
export interface ImportSummary {
  total: number
  created: number
  updated: number
  skipped: number
  failed: number
  results: ImportLineResult[]
}

/**
 * 批量导入账号。
 * 支持两种调用形态:
 *   1) 纯文本 text(运维侧粘贴 JSON / JSONL),走 application/json
 *   2) FormData files[](多文件上传),走 multipart/form-data
 *
 * 大量文件(>500 个)建议前端在客户端先分批合并 text 再用 json 发送,
 * 避免一次 multipart 过大。
 */
export function importAccountsJSON(body: {
  text: string
  update_existing?: boolean
  default_client_id?: string
  default_proxy_id?: number
}) {
  return http.post<any, ImportSummary>('/api/admin/accounts/import', body)
}

export interface ImportTokensBody {
  /** at=每行 AT;rt=每行 RT 需要 client_id;st=每行 ST */
  mode: 'at' | 'rt' | 'st' | 'session_json'
  /** 字符串或字符串数组,后端都兼容 */
  tokens: string | string[]
  /** RT 模式必填;AT/ST 模式可选,传了也会记到账号上 */
  client_id?: string
  update_existing?: boolean
  /** RT/ST 换 AT 时走的代理(chatgpt.com / auth.openai.com),强烈推荐 */
  default_proxy_id?: number
}

export function importAccountsTokens(body: ImportTokensBody) {
  return http.post<any, ImportSummary>('/api/admin/accounts/import-tokens', body)
}

export function importAccountsFiles(
  files: File[],
  opt: { update_existing?: boolean; default_client_id?: string; default_proxy_id?: number } = {}
) {
  const fd = new FormData()
  for (const f of files) fd.append('files', f, f.name)
  if (opt.update_existing !== undefined) fd.append('update_existing', String(opt.update_existing))
  if (opt.default_client_id) fd.append('default_client_id', opt.default_client_id)
  if (opt.default_proxy_id) fd.append('default_proxy_id', String(opt.default_proxy_id))
  return http.post<any, ImportSummary>('/api/admin/accounts/import', fd, {
    headers: { 'Content-Type': 'multipart/form-data' },
  })
}

// ---------- 刷新 / 探测 ----------
export interface RefreshResult {
  account_id: number
  email: string
  ok: boolean
  source: string            // rt / st / failed
  expires_at?: string
  error?: string
  rt_rotated?: boolean
  /** 新 AT 是否通过 chatgpt.com web 后端校验(/backend-api/me 返回 200) */
  at_verified?: boolean
  /** true 表示 RT 换出的 AT 被 chatgpt.com 以 401 拒绝(iOS 作用域),需要 Session Token */
  web_unauthorized?: boolean
}
export interface RefreshAllResult {
  total: number
  success: number
  failed: number
  results: RefreshResult[]
}

export function refreshAccount(id: number) {
  return http.post<any, RefreshResult>(`/api/admin/accounts/${id}/refresh`)
}
export function refreshAllAccounts() {
  return http.post<any, RefreshAllResult>('/api/admin/accounts/refresh-all')
}

export interface QuotaResult {
  account_id: number
  email: string
  ok: boolean
  remaining: number
  total: number
  reset_at?: string
  default_model?: string
  blocked_features?: string[]
  error?: string
}
export interface QuotaAllResult {
  total: number
  success: number
  failed: number
  results: QuotaResult[]
}

export function probeAccountQuota(id: number) {
  return http.post<any, QuotaResult>(`/api/admin/accounts/${id}/probe-quota`)
}
export function probeAllAccountsQuota() {
  return http.post<any, QuotaAllResult>('/api/admin/accounts/probe-quota-all')
}

// ---------- 自动刷新开关 ----------
export interface AutoRefreshConfig {
  enabled: boolean
  ahead_sec: number
  threshold?: string
}
export function getAutoRefresh() {
  return http.get<any, AutoRefreshConfig>('/api/admin/accounts/auto-refresh')
}
export function setAutoRefresh(enabled: boolean) {
  return http.put<any, AutoRefreshConfig>('/api/admin/accounts/auto-refresh', { enabled })
}

// ---------- 批量删除 ----------
export type BulkDeleteScope = 'dead' | 'suspicious' | 'warned' | 'throttled' | 'all'
export function bulkDeleteAccounts(scope: BulkDeleteScope) {
  return http.post<any, { deleted: number; scope: string }>(
    '/api/admin/accounts/bulk-delete', { scope },
  )
}

// ---------- 获取 AT / RT / ST 明文(编辑弹窗回显) ----------
export interface AccountSecrets {
  auth_token: string
  refresh_token: string
  session_token: string
}
export function getAccountSecrets(id: number) {
  return http.get<any, AccountSecrets>(`/api/admin/accounts/${id}/secrets`)
}
