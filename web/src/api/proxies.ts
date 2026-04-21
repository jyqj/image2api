import { http } from './http'

export interface Proxy {
  id: number
  scheme: string          // http / https / socks5
  host: string
  port: number
  username: string
  country: string
  isp: string
  health_score: number
  last_probe_at?: { Time: string; Valid: boolean } | string | null
  last_error?: string
  enabled: boolean
  remark: string
  created_at: string
  updated_at?: string
}

export interface Page<T> {
  list: T[]; total: number; page: number; page_size: number
}

export interface ProxyCreate {
  scheme: string
  host: string
  port: number
  username?: string
  password?: string       // 明文,后端会加密
  country?: string
  isp?: string
  enabled?: boolean
  remark?: string
}
export type ProxyUpdate = ProxyCreate

export function listProxies(params: { page?: number; page_size?: number } = {}) {
  return http.get<any, Page<Proxy>>('/api/admin/proxies', { params })
}
export function createProxy(body: ProxyCreate) {
  return http.post<any, Proxy>('/api/admin/proxies', body)
}
export function updateProxy(id: number, body: ProxyUpdate) {
  return http.patch<any, Proxy>(`/api/admin/proxies/${id}`, body)
}
export function deleteProxy(id: number) {
  return http.delete<any, { deleted: number }>(`/api/admin/proxies/${id}`)
}

export interface ProxyImportLine {
  line: number
  raw: string
  status: 'created' | 'updated' | 'skipped' | 'invalid'
  id?: number
  error?: string
}
export interface ProxyImportResp {
  items: ProxyImportLine[]
  created: number
  updated: number
  skipped: number
  invalid: number
  total: number
  probe?: ProbeAllResp
  probe_queued?: boolean
}
export function importProxies(body: {
  text: string
  enabled?: boolean
  country?: string
  isp?: string
  remark?: string
  overwrite?: boolean
  probe_after_import?: boolean
}) {
  return http.post<any, ProxyImportResp>('/api/admin/proxies/import', body)
}

// 单条探测结果
export interface ProbeOneResp {
  ok: boolean
  latency_ms: number
  error?: string
  tried_at: string
  health_score: number
}
export function probeProxy(id: number) {
  return http.post<any, ProbeOneResp>(`/api/admin/proxies/${id}/probe`)
}

// 全量探测
export interface ProbeItem {
  proxy_id: number
  ok: boolean
  latency_ms: number
  error?: string
  tried_at: string
}
export interface ProbeAllResp {
  total: number
  ok: number
  bad: number
  items: ProbeItem[]
}
export function probeAllProxies() {
  return http.post<any, ProbeAllResp>('/api/admin/proxies/probe-all')
}
