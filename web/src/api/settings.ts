import { http } from './http'

// 系统设置 KV 条目(管理端用,带 schema)。
export interface SettingItem {
  key: string
  value: string
  type: 'string' | 'bool' | 'int' | 'email' | 'url' | string
  category: 'site' | 'gateway' | 'account' | 'mail' | string
  label: string
  desc: string
}

export function listSettings(): Promise<{ items: SettingItem[] }> {
  return http.get<any, { items: SettingItem[] }>('/api/admin/settings')
}

export function updateSettings(items: Record<string, string>): Promise<{ updated: number }> {
  return http.put<any, { updated: number }>('/api/admin/settings', { items })
}

export function reloadSettings(): Promise<{ reloaded: boolean }> {
  return http.post<any, { reloaded: boolean }>('/api/admin/settings/reload')
}

export function sendTestEmail(to: string): Promise<{ sent: boolean; to: string }> {
  return http.post<any, { sent: boolean; to: string }>('/api/admin/settings/test-email', { to })
}

// 公开接口:返回控制台需要的站点元信息(site.name 等)。
export function fetchSiteInfo(): Promise<Record<string, string>> {
  return http.get<any, Record<string, string>>('/api/public/site-info')
}
