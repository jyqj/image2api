import { http } from './http'

export interface SimpleModel {
  id: number
  slug: string
  type: 'chat' | 'image' | string
  description: string
}

export function listMyModels(): Promise<{ items: SimpleModel[]; total: number }> {
  return http.get<any, { items: SimpleModel[]; total: number }>('/api/me/models')
}

export interface UsageItem {
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

export interface UsageOverall {
  requests: number
  failures: number
  chat_requests: number
  image_images: number
  input_tokens: number
  output_tokens: number
}

export interface UsageDaily {
  day: string
  requests: number
  failures: number
  input_tokens: number
  output_tokens: number
  image_count: number
}

export interface UsageModelStat {
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

export interface MyStatsResp {
  overall: UsageOverall
  daily: UsageDaily[]
  by_model: UsageModelStat[]
}

export function listMyUsageLogs(params: {
  type?: 'chat' | 'image' | ''
  status?: string
  since?: string
  until?: string
  limit?: number
  offset?: number
} = {}): Promise<{ items: UsageItem[]; total: number; limit: number; offset: number }> {
  return http.get<any, { items: UsageItem[]; total: number; limit: number; offset: number }>('/api/me/usage/logs', { params })
}

export function getMyUsageStats(params: {
  days?: number
  top_n?: number
  type?: 'chat' | 'image' | ''
  since?: string
  until?: string
} = {}): Promise<MyStatsResp> {
  return http.get<any, MyStatsResp>('/api/me/usage/stats', { params })
}

export interface ImageTask {
  id: number
  task_id: string
  model_id: number
  account_id: number
  prompt: string
  n: number
  size: string
  status: 'queued' | 'dispatched' | 'running' | 'success' | 'failed' | string
  conversation_id?: string
  error?: string
  image_urls: string[]
  file_ids?: string[]
  created_at: string
  started_at?: string | null
  finished_at?: string | null
}

export function listMyImageTasks(params: { limit?: number; offset?: number } = {}): Promise<{ items: ImageTask[]; limit: number; offset: number }> {
  return http.get<any, { items: ImageTask[]; limit: number; offset: number }>('/api/me/images/tasks', { params })
}

export function getMyImageTask(taskID: string): Promise<ImageTask> {
  return http.get<any, ImageTask>(`/api/me/images/tasks/${taskID}`)
}

export interface ChatStreamDelta { role?: string; content?: string }
export interface ChatStreamChunk {
  id?: string
  model?: string
  choices?: Array<{ index?: number; delta?: ChatStreamDelta; finish_reason?: string | null }>
}
export interface PlayChatMessage { role: 'system' | 'user' | 'assistant'; content: string }

export async function streamPlayChat(
  req: { model: string; messages: PlayChatMessage[]; temperature?: number },
  onDelta: (text: string) => void,
  signal?: AbortSignal,
): Promise<void> {
  const resp = await fetch('/api/me/playground/chat', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ...req, stream: true }),
    signal,
  })
  if (!resp.ok || !resp.body) {
    const text = await resp.text().catch(() => '')
    throw new Error(`chat ${resp.status}: ${text || resp.statusText}`)
  }
  const reader = resp.body.getReader()
  const decoder = new TextDecoder('utf-8')
  let buffer = ''
  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    const blocks = buffer.split('\n\n')
    buffer = blocks.pop() || ''
    for (const block of blocks) {
      for (const line of block.split('\n')) {
        if (!line.startsWith('data:')) continue
        const data = line.slice(5).trim()
        if (!data || data === '[DONE]') continue
        try {
          const chunk: ChatStreamChunk = JSON.parse(data)
          const delta = chunk.choices?.[0]?.delta?.content
          if (delta) onDelta(delta)
        } catch { /* ignore heartbeat */ }
      }
    }
  }
}

export interface PlayImageRequest {
  model: string
  prompt: string
  n?: number
  size?: string
  reference_images?: string[]
}
export interface PlayImageData { url: string; file_id?: string; revised_prompt?: string }
export interface PlayImageResponse { created: number; task_id?: string; data: PlayImageData[]; is_preview?: boolean }

export async function playGenerateImage(req: PlayImageRequest, signal?: AbortSignal): Promise<PlayImageResponse> {
  const resp = await fetch('/api/me/playground/image', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
    signal,
  })
  if (!resp.ok) {
    let detail = ''
    try { const body = await resp.json(); detail = body?.error?.message || body?.message || '' } catch { /* ignore */ }
    throw new Error(detail || `image ${resp.status}: ${resp.statusText}`)
  }
  return (await resp.json()) as PlayImageResponse
}

export async function playEditImage(
  model: string,
  prompt: string,
  files: File[],
  opts?: { n?: number; size?: string; signal?: AbortSignal },
): Promise<PlayImageResponse> {
  if (!files.length) throw new Error('至少需要选择一张参考图')
  const fd = new FormData()
  fd.append('model', model)
  fd.append('prompt', prompt)
  if (opts?.n) fd.append('n', String(opts.n))
  if (opts?.size) fd.append('size', opts.size)
  files.forEach((f, idx) => fd.append(idx === 0 ? 'image' : 'image[]', f, f.name))
  const resp = await fetch('/api/me/playground/image-edit', { method: 'POST', body: fd, signal: opts?.signal })
  if (!resp.ok) {
    let detail = ''
    try { const body = await resp.json(); detail = body?.error?.message || body?.message || '' } catch { /* ignore */ }
    throw new Error(detail || `image-edit ${resp.status}: ${resp.statusText}`)
  }
  return (await resp.json()) as PlayImageResponse
}
