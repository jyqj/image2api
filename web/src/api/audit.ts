import { http } from './http'

export interface AuditLog {
  id: number
  actor_id: number
  actor_email: string
  action: string
  method: string
  path: string
  status_code: number
  ip: string
  ua: string
  target: string
  meta: any
  created_at: string
}

export interface AuditFilter {
  action?: string
  actor_id?: number
  limit?: number
  offset?: number
}

export function listAudit(params: AuditFilter = {}): Promise<{ items: AuditLog[]; total: number; limit: number; offset: number }> {
  return http.get<any, { items: AuditLog[]; total: number; limit: number; offset: number }>('/api/admin/audit/logs', { params })
}
