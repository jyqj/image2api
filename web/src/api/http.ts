import axios, { AxiosError, type AxiosInstance, type AxiosRequestConfig } from 'axios'
import { ElMessage } from 'element-plus'

export interface ApiEnvelope<T = any> {
  code: number
  message: string
  data: T
}

const baseURL = import.meta.env.VITE_API_BASE || ''
const TOKEN_KEY = 'admin_token'

export const http: AxiosInstance = axios.create({ baseURL, timeout: 30_000 })

// 请求拦截器：自动注入 token
http.interceptors.request.use((config) => {
  const token = localStorage.getItem(TOKEN_KEY)
  if (token) {
    config.headers = config.headers || {}
    config.headers['Authorization'] = `Bearer ${token}`
  }
  return config
})

// 响应拦截器
http.interceptors.response.use(
  (response) => {
    const contentType = response.headers?.['content-type'] || ''
    if (response.config.responseType === 'blob' || contentType.startsWith('application/gzip')) return response
    const payload = response.data as ApiEnvelope
    if (payload && typeof payload === 'object' && 'code' in payload) {
      if (payload.code === 0) return payload.data as any
      const msg = payload.message || `请求失败 (code=${payload.code})`
      ElMessage.error(msg)
      return Promise.reject(new Error(msg))
    }
    return response.data
  },
  (error: AxiosError<ApiEnvelope>) => {
    if (error.response?.status === 401) {
      localStorage.removeItem(TOKEN_KEY)
      // 避免在登录页重复跳转
      if (!window.location.pathname.startsWith('/login')) {
        window.location.href = '/login'
      }
      return Promise.reject(error)
    }
    const msg = error.response?.data?.message || error.message || '网络错误'
    ElMessage.error(msg)
    return Promise.reject(error)
  },
)

export function request<T = any>(cfg: AxiosRequestConfig): Promise<T> {
  return http.request(cfg) as unknown as Promise<T>
}
