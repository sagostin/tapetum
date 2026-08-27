const API_BASE = '/api/v1'

export class ApiError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

interface ErrorEnvelope {
  error?: {
    code?: string
    message?: string
  }
}

interface RequestOptions {
  /** Don't redirect to /login on 401 (used by auth probing). */
  skipAuthRedirect?: boolean
}

function readCsrfCookie(): string | null {
  const match = document.cookie.match(/(?:^|;\s*)tapetum_csrf=([^;]*)/)
  return match?.[1] ? decodeURIComponent(match[1]) : null
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  options: RequestOptions = {},
): Promise<T> {
  const headers: Record<string, string> = {}

  if (method !== 'GET') {
    headers['Content-Type'] = 'application/json'
    const csrf = readCsrfCookie()
    if (csrf) {
      headers['X-CSRF-Token'] = csrf
    }
  }

  const res = await fetch(`${API_BASE}${path}`, {
    method,
    headers,
    credentials: 'same-origin',
    body: body === undefined ? undefined : JSON.stringify(body),
  })

  if (res.status === 401 && !options.skipAuthRedirect) {
    if (!window.location.pathname.startsWith('/login')) {
      window.location.assign('/login')
    }
  }

  if (!res.ok) {
    let code = 'unknown_error'
    let message = `Request failed with status ${res.status}`
    try {
      const envelope = (await res.json()) as ErrorEnvelope
      if (envelope.error?.code) code = envelope.error.code
      if (envelope.error?.message) message = envelope.error.message
    } catch {
      // Body wasn't JSON — keep defaults.
    }
    throw new ApiError(res.status, code, message)
  }

  if (res.status === 204) {
    return undefined as T
  }

  return (await res.json()) as T
}

export function get<T>(path: string, options?: RequestOptions): Promise<T> {
  return request<T>('GET', path, undefined, options)
}

export function post<T>(path: string, body?: unknown, options?: RequestOptions): Promise<T> {
  return request<T>('POST', path, body, options)
}

export function patch<T>(path: string, body?: unknown, options?: RequestOptions): Promise<T> {
  return request<T>('PATCH', path, body, options)
}

export function del<T>(path: string, options?: RequestOptions): Promise<T> {
  return request<T>('DELETE', path, undefined, options)
}
