export class APIError extends Error {
  status: number
  code?: string

  constructor(status: number, message: string, code?: string) {
    super(message)
    this.name = 'APIError'
    this.status = status
    this.code = code
  }
}

type Envelope<T> = T | { data: T }

function unwrap<T>(body: Envelope<T>): T {
  if (body && typeof body === 'object' && 'data' in body) {
    return body.data
  }
  return body
}

export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  if (init.body && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }

  const response = await fetch(path, {
    ...init,
    headers,
    credentials: 'same-origin',
    cache: init.cache ?? 'no-store',
  })

  const contentType = response.headers.get('content-type') ?? ''
  const body = contentType.includes('application/json')
    ? await response.json()
    : await response.text()

  if (!response.ok) {
    if (response.status === 401 && path !== '/api/v1/auth/login' && path !== '/api/v1/auth/session') {
      window.dispatchEvent(new Event('subpool:unauthorized'))
    }
    const message =
      typeof body === 'object' && body !== null && 'message' in body
        ? String(body.message)
        : typeof body === 'object' && body !== null && 'error' in body
          ? typeof body.error === 'object' && body.error !== null && 'message' in body.error
            ? String(body.error.message)
            : String(body.error)
          : `Request failed (${response.status})`
    const code =
      typeof body === 'object' && body !== null && 'code' in body
        ? String(body.code)
        : typeof body === 'object' && body !== null && 'error' in body && typeof body.error === 'object' && body.error !== null && 'code' in body.error
          ? String(body.error.code)
        : undefined
    throw new APIError(response.status, message, code)
  }

  if (response.status === 204 || body === '') {
    return undefined as T
  }
  return unwrap(body as Envelope<T>)
}

export function collection<T>(payload: unknown, keys: string[]): T[] {
  if (Array.isArray(payload)) return payload as T[]
  if (!payload || typeof payload !== 'object') return []
  for (const key of [...keys, 'items']) {
    const value = (payload as Record<string, unknown>)[key]
    if (Array.isArray(value)) return value as T[]
  }
  return []
}

export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : 'Something went wrong. Try again.'
}
