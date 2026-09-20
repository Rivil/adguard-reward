// The SPA's only door to /api/v1: every request carries the CSRF header and
// the session cookie, every error arrives as an ApiError keyed by the
// server's error code, and a 401 anywhere but the login route sends the
// user back to the login page.
import { writable } from 'svelte/store'

export const CSRF_HEADER = 'X-Requested-With'
export const CSRF_VALUE = 'adguard-reward'
export const LOGIN_PATH = '/api/v1/login'

export class ApiError extends Error {
  readonly status: number
  readonly code: string
  /** Seconds from the Retry-After header, present on 429 only. */
  readonly retryAfter?: number

  constructor(status: number, code: string, message: string, retryAfter?: number) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.retryAfter = retryAfter
  }
}

// ---- routing -------------------------------------------------------------

export type Route = 'login' | 'home'

function routeFor(pathname: string): Route {
  return pathname === '/login' ? 'login' : 'home'
}

function currentPath(): string {
  return typeof location === 'undefined' ? '/' : location.pathname
}

/** The page the SPA is showing, driven by the URL. */
export const route = writable<Route>(routeFor(currentPath()))

/** Switch page and push the matching URL. */
export function navigate(r: Route): void {
  const path = r === 'login' ? '/login' : '/'
  if (typeof history !== 'undefined' && currentPath() !== path) {
    history.pushState(null, '', path)
  }
  route.set(r)
}

if (typeof window !== 'undefined') {
  window.addEventListener('popstate', () => route.set(routeFor(currentPath())))
}

let onUnauthorized: () => void = () => navigate('login')

/** Replace the 401 handler; tests inject a spy, the app keeps the default. */
export function setOnUnauthorized(fn: () => void): void {
  onUnauthorized = fn
}

// ---- transport -----------------------------------------------------------

type Method = 'GET' | 'POST' | 'DELETE'

interface ErrorEnvelope {
  error?: unknown
  message?: unknown
}

async function readEnvelope(res: Response): Promise<{ code: string; message: string }> {
  let code = 'unknown'
  let message = res.statusText || `HTTP ${res.status}`
  try {
    const body = (await res.json()) as ErrorEnvelope
    if (typeof body?.error === 'string') code = body.error
    if (typeof body?.message === 'string') message = body.message
  } catch {
    // Not JSON: keep the status text.
  }
  return { code, message }
}

function retryAfterOf(res: Response): number | undefined {
  if (res.status !== 429) return undefined
  const raw = res.headers.get('Retry-After')
  if (raw === null) return undefined
  const n = Number(raw)
  return Number.isFinite(n) ? n : undefined
}

/**
 * Perform one API call. Resolves with the JSON body (or undefined on 204)
 * and rejects with an ApiError for any non-2xx status.
 */
export async function request<T>(method: Method, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { [CSRF_HEADER]: CSRF_VALUE }
  const init: RequestInit = { method, headers, credentials: 'same-origin' }
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    init.body = JSON.stringify(body)
  }
  const res = await fetch(path, init)
  if (res.ok) {
    if (res.status === 204) return undefined as T
    return (await res.json()) as T
  }
  const { code, message } = await readEnvelope(res)
  const err = new ApiError(res.status, code, message, retryAfterOf(res))
  if (res.status === 401 && path !== LOGIN_PATH) onUnauthorized()
  throw err
}

// ---- endpoints -----------------------------------------------------------

export interface Me {
  username: string
  expires_at: string
}

export function login(username: string, password: string): Promise<void> {
  return request<void>('POST', LOGIN_PATH, { username, password })
}

export function logout(): Promise<void> {
  return request<void>('POST', '/api/v1/logout')
}

export function me(): Promise<Me> {
  return request<Me>('GET', '/api/v1/me')
}

// ---- messages ------------------------------------------------------------

/** The line the login page shows for an ApiError. */
export function messageFor(e: ApiError): string {
  switch (e.code) {
    case 'bad_credentials':
      return 'Wrong username or password'
    case 'adguard_unavailable':
      return "Can't reach AdGuard Home — try again in a moment"
    case 'rate_limited':
      return `Too many attempts — wait ${e.retryAfter ?? 60}s`
    default:
      return e.message
  }
}
