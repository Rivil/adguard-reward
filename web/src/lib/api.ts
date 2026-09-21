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

export type Route = 'login' | 'home' | 'children'

const PATHS: Record<Route, string> = { login: '/login', home: '/', children: '/children' }

function routeFor(pathname: string): Route {
  for (const r of Object.keys(PATHS) as Route[]) {
    if (r !== 'home' && PATHS[r] === pathname) return r
  }
  return 'home'
}

function currentPath(): string {
  return typeof location === 'undefined' ? '/' : location.pathname
}

/** The page the SPA is showing, driven by the URL. */
export const route = writable<Route>(routeFor(currentPath()))

/** Switch page and push the matching URL. */
export function navigate(r: Route): void {
  const path = PATHS[r]
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

type Method = 'GET' | 'POST' | 'PUT' | 'DELETE'

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

/**
 * Whether the parent said "not now" to the global-list migration banner.
 * Per login only: reset by login() and logout(), never persisted, so a
 * device still on the global list resurfaces on the next sign-in
 * (locked: migration_offer_ux).
 */
export const migrationDismissed = writable(false)

export async function login(username: string, password: string): Promise<void> {
  await request<void>('POST', LOGIN_PATH, { username, password })
  migrationDismissed.set(false)
}

export async function logout(): Promise<void> {
  try {
    await request<void>('POST', '/api/v1/logout')
  } finally {
    migrationDismissed.set(false)
  }
}

export function me(): Promise<Me> {
  return request<Me>('GET', '/api/v1/me')
}

// ---- children, clients, services ----------------------------------------

export interface Child {
  id: number
  name: string
  clients: string[]
}

export interface ChildRef {
  id: number
  name: string
}

export interface ClientView {
  name: string
  ids: string[]
  use_global_blocked_services: boolean
  child: ChildRef | null
}

export interface Service {
  id: string
  name: string
  /** icon_svg as AdGuard serves it: base64 SVG. */
  icon: string
}

export type ServiceStateName = 'blocked' | 'partial' | 'unblocked'

export interface ServiceState extends Service {
  state: ServiceStateName
  /** Present clients on which a partial service is NOT blocked. */
  differs: string[]
}

export interface BlockedClient {
  name: string
  missing: boolean
  uses_global: boolean
}

export interface BlockedView {
  child: ChildRef
  clients: BlockedClient[]
  services: ServiceState[]
}

export interface MigrationClient {
  name: string
  child: ChildRef
  gains: string[]
}

export interface MigrationOffer {
  global: string[]
  /** Empty means there is nothing to offer. */
  clients: MigrationClient[]
}

const CHILDREN = '/api/v1/children'

export async function listChildren(): Promise<Child[]> {
  const res = await request<{ children: Child[] }>('GET', CHILDREN)
  return res.children
}

export function createChild(name: string, clients: string[]): Promise<Child> {
  return request<Child>('POST', CHILDREN, { name, clients })
}

export function updateChild(id: number, name: string, clients: string[]): Promise<Child> {
  return request<Child>('PUT', `${CHILDREN}/${id}`, { name, clients })
}

export function deleteChild(id: number): Promise<void> {
  return request<void>('DELETE', `${CHILDREN}/${id}`)
}

export async function listClients(): Promise<ClientView[]> {
  const res = await request<{ clients: ClientView[] }>('GET', '/api/v1/clients')
  return res.clients
}

export async function listServices(): Promise<Service[]> {
  const res = await request<{ services: Service[] }>('GET', '/api/v1/services')
  return res.services
}

export function childBlocked(id: number): Promise<BlockedView> {
  return request<BlockedView>('GET', `${CHILDREN}/${id}/blocked`)
}

export function migrationOffer(): Promise<MigrationOffer> {
  return request<MigrationOffer>('GET', '/api/v1/migration')
}

export async function applyMigration(): Promise<string[]> {
  const res = await request<{ migrated: string[] }>('POST', '/api/v1/migration')
  return res.migrated
}

/** A data: URL for an <img> from a catalogue icon. */
export function iconUrl(icon: string): string {
  return 'data:image/svg+xml;base64,' + icon
}

// ---- messages ------------------------------------------------------------

/**
 * The line a page shows for an ApiError. Codes without a fixed line — a
 * 409 conflict among them — echo the server's message verbatim, which
 * already names the child or client involved.
 */
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
