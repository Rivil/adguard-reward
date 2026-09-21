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
  /** The active grant a 409 on grant creation names, when the envelope carries a numeric grant_id. */
  readonly grantId?: number

  constructor(status: number, code: string, message: string, retryAfter?: number, grantId?: number) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.retryAfter = retryAfter
    this.grantId = grantId
  }
}

// ---- routing -------------------------------------------------------------

export type Route = 'login' | 'home' | 'children' | 'buttons'

const PATHS: Record<Route, string> = { login: '/login', home: '/', children: '/children', buttons: '/buttons' }

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
  grant_id?: unknown
}

async function readEnvelope(res: Response): Promise<{ code: string; message: string; grantId?: number }> {
  let code = 'unknown'
  let message = res.statusText || `HTTP ${res.status}`
  let grantId: number | undefined
  try {
    const body = (await res.json()) as ErrorEnvelope
    if (typeof body?.error === 'string') code = body.error
    if (typeof body?.message === 'string') message = body.message
    if (typeof body?.grant_id === 'number') grantId = body.grant_id
  } catch {
    // Not JSON: keep the status text.
  }
  return { code, message, grantId }
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
  const { code, message, grantId } = await readEnvelope(res)
  const err = new ApiError(res.status, code, message, retryAfterOf(res), grantId)
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

// ---- grants and buttons --------------------------------------------------

export interface Grant {
  id: number
  child_id: number
  services: string[]
  clients: string[]
  /** RFC 3339. */
  started_at: string
  ends_at: string
}

export interface GrantCreated {
  id: number
  ends_at: string
  /** False when at least one client could not be unblocked; failed names them. */
  applied: boolean
  failed: string[]
}

export interface ButtonInput {
  label: string
  child_id: number
  services: string[]
  /** Seconds. */
  duration: number
}

export interface Button extends ButtonInput {
  id: number
}

const GRANTS = '/api/v1/grants'
const BUTTONS = '/api/v1/buttons'

export async function listGrants(): Promise<Grant[]> {
  const res = await request<{ grants: Grant[] }>('GET', GRANTS)
  return res.grants
}

/** duration is seconds. A 409 rejects with grantId naming the overlapping grant. */
export function createGrant(child_id: number, services: string[], duration: number): Promise<GrantCreated> {
  return request<GrantCreated>('POST', GRANTS, { child_id, services, duration })
}

export function extendGrant(id: number, duration: number): Promise<{ id: number; ends_at: string }> {
  return request<{ id: number; ends_at: string }>('POST', `${GRANTS}/${id}/extend`, { duration })
}

export function endGrant(id: number): Promise<void> {
  return request<void>('POST', `${GRANTS}/${id}/end`)
}

export async function listButtons(): Promise<Button[]> {
  const res = await request<{ buttons: Button[] }>('GET', BUTTONS)
  return res.buttons
}

/** Replaces the whole list; the server assigns fresh ids and returns the stored list. */
export async function saveButtons(buttons: ButtonInput[]): Promise<Button[]> {
  const res = await request<{ buttons: Button[] }>('PUT', BUTTONS, { buttons })
  return res.buttons
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
