import { TOKEN_KEY, SESSION_EXPIRED_EVENT, discardRejectedSession } from './session.js'

export const API_BASE = (import.meta.env.VITE_API_BASE as string | undefined) || '/api/v1'

export class ApiError extends Error {
  status: number
  code: string
  data: any
  traceId?: string
  constructor(status: number, code: string, message: string, data?: any, traceId?: string) {
    super(message)
    this.status = status
    this.code = code
    this.data = data
    this.traceId = traceId
  }
}

export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) || ''
}

export function setToken(token: string) {
  if (token) localStorage.setItem(TOKEN_KEY, token)
  else localStorage.removeItem(TOKEN_KEY)
}

type RequestOptions = {
  method?: string
  body?: unknown
  headers?: Record<string, string>
  signal?: AbortSignal
}

export async function api<T = any>(path: string, options: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = {
    Accept: 'application/json',
    ...(options.headers || {}),
  }
  const token = getToken()
  if (token) headers.Authorization = 'Bearer ' + token
  let body: BodyInit | undefined
  if (options.body !== undefined) {
    headers['Content-Type'] = 'application/json'
    body = JSON.stringify(options.body)
  }
  const response = await fetch(API_BASE + path, {
    method: options.method || 'GET',
    headers,
    body,
    signal: options.signal,
  })
  if (response.status === 204) return undefined as T
  let payload: any = null
  try {
    payload = await response.json()
  } catch {
    payload = null
  }
  if (!response.ok) {
    if (response.status === 401 && !['/auth/login', '/auth/staff-login'].includes(path)) {
      if (discardRejectedSession(localStorage, token)) {
        window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT))
      }
    }
    throw new ApiError(response.status, payload?.code || 'HTTP_' + response.status, payload?.message || '请求失败', payload?.data, payload?.traceId)
  }
  return (payload?.data ?? payload) as T
}

export function money(cents: number | undefined | null): string {
  return '¥' + (Number(cents || 0) / 100).toFixed(2)
}

export function shortTime(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return date.toLocaleString('zh-CN', { hour12: false })
}

export function newRequestId(prefix: string): string {
  const random = Math.random().toString(36).slice(2, 10)
  return `${prefix}_${Date.now()}_${random}`.slice(0, 120)
}
