import { http } from './http'

export interface Model {
  id: number
  slug: string
  type: 'chat' | 'image' | string
  upstream_model_slug: string
  description: string
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface ModelUpsert {
  slug?: string
  type: 'chat' | 'image'
  upstream_model_slug: string
  description: string
  enabled?: boolean
}

export function listModels(): Promise<{ items: Model[]; total: number }> {
  return http.get<any, { items: Model[]; total: number }>('/api/admin/models')
}
export function createModel(body: ModelUpsert): Promise<Model> {
  return http.post<any, Model>('/api/admin/models', body)
}
export function updateModel(id: number, body: ModelUpsert): Promise<Model> {
  return http.put<any, Model>(`/api/admin/models/${id}`, body)
}
export function setModelEnabled(id: number, enabled: boolean) {
  return http.patch<any, { enabled: boolean }>(`/api/admin/models/${id}/enabled`, { enabled })
}
export function deleteModel(id: number) {
  return http.delete<any, { deleted: number }>(`/api/admin/models/${id}`)
}

export interface Overall {
  requests: number
  failures: number
  chat_requests: number
  image_images: number
  input_tokens: number
  output_tokens: number
}

export interface DailyPoint {
  day: string
  requests: number
  failures: number
  input_tokens: number
  output_tokens: number
  image_count: number
}

export interface ModelStat {
  model_id: number
  model_slug: string
  type: string
  requests: number
  failures: number
  input_tokens: number
  output_tokens: number
  image_count: number
  avg_dur_ms: number
}

export interface StatsResp {
  overall: Overall
  daily: DailyPoint[]
  by_model: ModelStat[]
}

export interface UsageLogRow {
  id: number
  model_id: number
  model_slug: string
  account_id: number
  request_id: string
  type: 'chat' | 'image' | string
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  image_count: number
  duration_ms: number
  status: string
  error_code: string
  ip: string
  created_at: string
}

export function getUsageStats(params: {
  days?: number; top_n?: number; model_id?: number; type?: string; status?: string; since?: string; until?: string
} = {}): Promise<StatsResp> {
  return http.get<any, StatsResp>('/api/admin/usage/stats', { params })
}

export function listUsageLogs(params: {
  type?: string; status?: string; since?: string; until?: string; model_id?: number; limit?: number; offset?: number
} = {}): Promise<{ items: UsageLogRow[]; total: number; limit: number; offset: number }> {
  return http.get<any, { items: UsageLogRow[]; total: number; limit: number; offset: number }>('/api/admin/usage/logs', { params })
}
