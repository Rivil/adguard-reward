// The SPA's only door to /api/v1: every request carries the CSRF header and
// the session cookie, every error arrives as an ApiError keyed by the
// server's error code, and a 401 outside the login and unlock routes either
// locks the screen behind the PIN pad (pin_required) or sends the user back
// to the login page (any other code).
import { writable } from 'svelte/store'

export const CSRF_HEADER = 'X-Requested-With'
export const CSRF_VALUE = 'adguard-reward'
export const LOGIN_PATH = '/api/v1/login'
export const UNLOCK_PATH = '/api/v1/unlock'

export class ApiError extends Error {
  readonly status: number
  readonly code: string
  /** Seconds from the Retry-After header, present on 429 only. */
  readonly retryAfter?: number
  /** The active grant a 409 names (grant creation, custom-service delete), when the envelope carries a numeric grant_id. */
  readonly grantId?: number
  /** Wrong PINs left before the device is revoked, from a bad_pin envelope's whole-number attempts_remaining. */
  readonly attemptsRemaining?: number

  constructor(
    status: number,
    code: string,
    message: string,
    retryAfter?: number,
    grantId?: number,
    attemptsRemaining?: number,
  ) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.retryAfter = retryAfter
    this.grantId = grantId
    this.attemptsRemaining = attemptsRemaining
  }
}

// ---- routing -------------------------------------------------------------

export type Route = 'login' | 'home' | 'children' | 'buttons' | 'services' | 'history' | 'settings'

const PATHS: Record<Route, string> = {
  login: '/login',
  home: '/',
  children: '/children',
  buttons: '/buttons',
  services: '/services',
  history: '/history',
  settings: '/settings',
}

/** Query parameters a route carries; only 'history' reads child. */
export interface RouteParams {
  child?: number | null
}

/** The route a pathname names; anything unknown is home. */
export function routeFor(pathname: string): Route {
  for (const r of Object.keys(PATHS) as Route[]) {
    if (r !== 'home' && PATHS[r] === pathname) return r
  }
  return 'home'
}

/** A child id is a positive whole number written plainly; anything else is null, never NaN, 0 or negative. */
function childIdOf(raw: string | null): number | null {
  if (raw === null || !/^[1-9][0-9]*$/.test(raw)) return null
  const n = Number(raw)
  return Number.isSafeInteger(n) ? n : null
}

function validChild(child: number | null | undefined): number | null {
  return typeof child === 'number' && Number.isSafeInteger(child) && child > 0 ? child : null
}

function currentPath(): string {
  return typeof location === 'undefined' ? '/' : location.pathname
}

function currentSearch(): string {
  return typeof location === 'undefined' ? '' : location.search
}

/** The history child the URL names; null off the history route. */
function childFor(r: Route, search: string): number | null {
  return r === 'history' ? childIdOf(new URLSearchParams(search).get('child')) : null
}

const initialRoute = routeFor(currentPath())

/** The page the SPA is showing, driven by the URL. */
export const route = writable<Route>(initialRoute)

/**
 * The child the history page shows, from ?child= on /history. null when the
 * parameter is missing or not a positive whole number, and on every other route.
 */
export const historyChild = writable<number | null>(childFor(initialRoute, currentSearch()))

/**
 * Switch page and push the matching URL. Only 'history' carries a query
 * (?child=N, when params.child is a valid id); every other route pushes its
 * bare path, so no query outlives the page that set it.
 */
export function navigate(r: Route, params: RouteParams = {}): void {
  const child = r === 'history' ? validChild(params.child) : null
  const url = child === null ? PATHS[r] : `${PATHS[r]}?child=${child}`
  if (typeof history !== 'undefined' && currentPath() + currentSearch() !== url) {
    history.pushState(null, '', url)
  }
  historyChild.set(child)
  route.set(r)
}

if (typeof window !== 'undefined') {
  window.addEventListener('popstate', () => {
    const r = routeFor(currentPath())
    historyChild.set(childFor(r, currentSearch()))
    route.set(r)
  })
}

/**
 * True while the session has lapsed on a device with a PIN: the server
 * answered 401 pin_required, so the app shows the PIN pad over the current
 * page instead of the login page. The URL is kept; whoever unlocks clears it.
 */
export const locked = writable(false)

let onUnauthorized: () => void = () => {
  locked.set(false)
  navigate('login')
}

let onPinRequired: () => void = () => locked.set(true)

/** Replace the signed-out 401 handler; tests inject a spy, the app keeps the default. */
export function setOnUnauthorized(fn: () => void): void {
  onUnauthorized = fn
}

/** Replace the pin_required 401 handler; tests inject a spy, the app keeps the default. */
export function setOnPinRequired(fn: () => void): void {
  onPinRequired = fn
}

// ---- transport -----------------------------------------------------------

type Method = 'GET' | 'POST' | 'PUT' | 'DELETE'

interface ErrorEnvelope {
  error?: unknown
  message?: unknown
  grant_id?: unknown
  attempts_remaining?: unknown
}

interface Envelope {
  code: string
  message: string
  grantId?: number
  attemptsRemaining?: number
}

async function readEnvelope(res: Response): Promise<Envelope> {
  let code = 'unknown'
  let message = res.statusText || `HTTP ${res.status}`
  let grantId: number | undefined
  let attemptsRemaining: number | undefined
  try {
    const body = (await res.json()) as ErrorEnvelope
    if (typeof body?.error === 'string') code = body.error
    if (typeof body?.message === 'string') message = body.message
    if (typeof body?.grant_id === 'number') grantId = body.grant_id
    const left = body?.attempts_remaining
    if (typeof left === 'number' && Number.isSafeInteger(left) && left >= 0) attemptsRemaining = left
  } catch {
    // Not JSON: keep the status text.
  }
  return { code, message, grantId, attemptsRemaining }
}

/**
 * Routes whose 401 is the caller's to show, not a lapsed session: a wrong
 * password on login, a wrong PIN or a revoked device on unlock.
 */
const OWN_401 = new Set([LOGIN_PATH, UNLOCK_PATH])

function retryAfterOf(res: Response): number | undefined {
  if (res.status !== 429) return undefined
  const raw = res.headers.get('Retry-After')
  if (raw === null) return undefined
  const n = Number(raw)
  return Number.isFinite(n) ? n : undefined
}

/**
 * Put one call on the wire with body as its exact text. Resolves with the
 * JSON body (or undefined on 204) and rejects with an ApiError for any
 * non-2xx status.
 */
async function send<T>(method: Method, path: string, body?: string): Promise<T> {
  const headers: Record<string, string> = { [CSRF_HEADER]: CSRF_VALUE }
  const init: RequestInit = { method, headers, credentials: 'same-origin' }
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    init.body = body
  }
  const res = await fetch(path, init)
  if (res.ok) {
    if (res.status === 204) return undefined as T
    return (await res.json()) as T
  }
  const { code, message, grantId, attemptsRemaining } = await readEnvelope(res)
  const err = new ApiError(res.status, code, message, retryAfterOf(res), grantId, attemptsRemaining)
  if (res.status === 401 && !OWN_401.has(path)) {
    if (code === 'pin_required') onPinRequired()
    else onUnauthorized()
  }
  throw err
}

/**
 * Perform one API call. Resolves with the JSON body (or undefined on 204)
 * and rejects with an ApiError for any non-2xx status. Not async: it hands
 * back send()'s own promise, so a caller settles no later than it would on
 * send() itself.
 */
export function request<T>(method: Method, path: string, body?: unknown): Promise<T> {
  return send<T>(method, path, body === undefined ? undefined : JSON.stringify(body))
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

// ---- device PIN ----------------------------------------------------------

const PIN = '/api/v1/pin'

export interface PinStatus {
  /** Whether this device has a PIN, read from the session, never the cookie. */
  has_pin: boolean
}

/**
 * Trade the device cookie and a PIN for a fresh session. Its 401s (bad_pin
 * with attemptsRemaining, device_revoked, unauthorized) are the caller's to
 * show: they never reach the global handlers.
 */
export function unlock(pin: string): Promise<void> {
  return request<void>('POST', UNLOCK_PATH, { pin })
}

export function pinStatus(): Promise<PinStatus> {
  return request<PinStatus>('GET', PIN)
}

/** The first PIN on this device; the session alone is enough. */
export function setPin(pin: string): Promise<void> {
  return request<void>('POST', PIN, { pin })
}

/** Replace this device's PIN. A wrong or missing password is a 403 the caller shows. */
export function changePin(pin: string, password: string): Promise<void> {
  return request<void>('POST', PIN, { pin, password })
}

/** Drop this device's PIN; the session carries on unlocked. A wrong or missing password is a 403. */
export function removePin(password: string): Promise<void> {
  return request<void>('DELETE', PIN, { password })
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

// The shared-device fields below (children, shared, shared_with,
// unblocked_by) are optional in the types only so object literals built
// before the server sent them still type-check; every fetcher in this module
// fills an absent one with [], so a value read from the API is never
// undefined. The permanent-toggle fields (ServiceState.toggle,
// Unblock.permanent) follow the same rule, filled with null and false.

/** A client a child shares with other children: the other owners, id ASC. */
export interface SharedRef {
  client: string
  others: ChildRef[]
}

export interface ClientView {
  name: string
  ids: string[]
  use_global_blocked_services: boolean
  /** Every owner, id ASC; [] when unassigned. Always filled by the fetchers below. */
  children?: ChildRef[]
}

export interface Service {
  /** A built-in AdGuard id, or "custom:<n>" for a parent-defined service. */
  id: string
  name: string
  /** icon_svg as AdGuard serves it: base64 SVG; "" for a custom service. */
  icon: string
  /** True for a custom service. Always filled by the fetchers below. */
  custom?: boolean
}

export type ServiceStateName = 'blocked' | 'partial' | 'unblocked'

/** A parent's permanent intent for one (child, service). */
export type ToggleState = 'blocked' | 'unblocked'

export interface ServiceState extends Service {
  state: ServiceStateName
  /** Present clients on which a partial service is NOT blocked. */
  differs: string[]
  /** Another child's grant or permanent unblock holding the service open on one of this child's clients. */
  unblocked_by?: Unblock[]
  /** This child's own permanent toggle; null when never toggled. Always filled by childBlocked. */
  toggle?: ToggleState | null
}

export interface Unblock {
  client: string
  /** 0 for a permanent unblock. */
  grant_id: number
  /** name is "" when the granting child has since been deleted. */
  child: ChildRef
  /** True when the other child's permanent unblock, not a grant, holds it open. Always filled by childBlocked. */
  permanent?: boolean
}

export interface BlockedClient {
  name: string
  missing: boolean
  uses_global: boolean
  /** The client's other owners, id ASC. */
  shared_with?: ChildRef[]
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

const CLIENTS = '/api/v1/clients'

/** Fills `children` on a client row from a server that omitted it. */
function clientView(c: ClientView): ClientView {
  return { ...c, children: c.children ?? [] }
}

export async function listClients(): Promise<ClientView[]> {
  const res = await request<{ clients: ClientView[] }>('GET', CLIENTS)
  return res.clients.map(clientView)
}

/** A device AdGuard has seen that no persistent client covers yet. */
export interface UnlabelledDevice {
  ip: string
  /** Blank when AdGuard knows the device by address only. */
  hostname: string
  /** AdGuard's label for where it learnt the device: ARP, rDNS, DHCP, WHOIS… */
  source: string
  /** [ip], or [ip, mac] when AdGuard's DHCP server leased that ip. */
  suggested_ids: string[]
}

export interface Devices {
  clients: ClientView[]
  unlabelled: UnlabelledDevice[]
}

/** Persistent clients plus the unlabelled devices, from one GET. */
export async function listDevices(): Promise<Devices> {
  const res = await request<Partial<Devices>>('GET', CLIENTS)
  return { clients: (res.clients ?? []).map(clientView), unlabelled: res.unlabelled ?? [] }
}

/**
 * Create an AdGuard persistent client, optionally assigned to a child in the
 * same call. childId null sends child_id: null — no child.
 */
export async function createClient(name: string, ids: string[], childId: number | null): Promise<ClientView> {
  return clientView(await request<ClientView>('POST', CLIENTS, { name, ids, child_id: childId }))
}

/** Rename a persistent client. Both names travel in the body, never the path. */
export async function renameClient(from: string, to: string): Promise<ClientView> {
  return clientView(await request<ClientView>('POST', `${CLIENTS}/rename`, { from, to }))
}

export async function listServices(): Promise<Service[]> {
  const res = await request<{ services: Service[] }>('GET', '/api/v1/services')
  return res.services.map((s) => ({ ...s, custom: s.custom ?? false }))
}

export async function childBlocked(id: number): Promise<BlockedView> {
  const v = await request<BlockedView>('GET', `${CHILDREN}/${id}/blocked`)
  return {
    ...v,
    clients: v.clients.map((c) => ({ ...c, shared_with: c.shared_with ?? [] })),
    services: v.services.map((s) => ({
      ...s,
      custom: s.custom ?? false,
      unblocked_by: (s.unblocked_by ?? []).map((u) => ({ ...u, permanent: u.permanent ?? false })),
      toggle: s.toggle ?? null,
    })),
  }
}

// ---- permanent toggles ---------------------------------------------------

/** What a toggle did. applied=false names the clients AdGuard refused in failed; the reconciler catches up. */
export interface ToggleResult {
  service_id: string
  state: ToggleState
  applied: boolean
  failed: string[]
  /** Shared clients the toggle reached and their other owners; client ASC. */
  shared: SharedRef[]
  /** The active grant a block trimmed the service from; ended when that left it empty. null when no grant was touched. */
  grant: { id: number; ended: boolean } | null
}

/** Permanently block or unblock one service (built-in or "custom:<n>") on every client of the child. */
export async function toggleService(childId: number, serviceId: string, state: ToggleState): Promise<ToggleResult> {
  const r = await request<ToggleResult>('POST', `${CHILDREN}/${childId}/toggles`, { service_id: serviceId, state })
  return { ...r, failed: r.failed ?? [], shared: r.shared ?? [], grant: r.grant ?? null }
}

// ---- custom services -----------------------------------------------------

/** A parent-defined service: a name and the plain hostnames it blocks for the children. */
export interface CustomService {
  id: number
  /** The catalogue id grants and buttons use: "custom:<id>". */
  service_id: string
  name: string
  domains: string[]
  /** False when it was stored but AdGuard's rules were not updated yet; the reconciler catches up. */
  applied: boolean
}

const CUSTOM_SERVICES = '/api/v1/custom-services'

export async function listCustomServices(): Promise<CustomService[]> {
  const res = await request<{ custom_services: CustomService[] }>('GET', CUSTOM_SERVICES)
  return res.custom_services
}

/** A 400 names the offending domain in its message. */
export function createCustomService(name: string, domains: string[]): Promise<CustomService> {
  return request<CustomService>('POST', CUSTOM_SERVICES, { name, domains })
}

export function updateCustomService(id: number, name: string, domains: string[]): Promise<CustomService> {
  return request<CustomService>('PUT', `${CUSTOM_SERVICES}/${id}`, { name, domains })
}

/** A 409 while an active grant covers it rejects with grantId naming that grant. */
export function deleteCustomService(id: number): Promise<void> {
  return request<void>('DELETE', `${CUSTOM_SERVICES}/${id}`)
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
  /** Shared clients the grant touches, from the current mapping; client ASC. */
  shared?: SharedRef[]
}

export interface GrantCreated {
  id: number
  ends_at: string
  /** False when at least one client could not be unblocked; failed names them. */
  applied: boolean
  failed: string[]
  /** Shared clients the grant touches and their other owners; client ASC. */
  shared?: SharedRef[]
}

export interface ButtonInput {
  label: string
  child_id: number
  services: string[]
  /** Seconds. */
  duration: number
  /**
   * Catalogue service id whose icon Home shows; "" (or absent) is the first
   * service's icon. The server always sends it; optional in the type only so
   * literals built before it existed still type-check. Read it as `icon ?? ''`.
   */
  icon?: string
}

export interface Button extends ButtonInput {
  id: number
}

const GRANTS = '/api/v1/grants'
const BUTTONS = '/api/v1/buttons'

export async function listGrants(): Promise<Grant[]> {
  const res = await request<{ grants: Grant[] }>('GET', GRANTS)
  return res.grants.map((g) => ({ ...g, shared: g.shared ?? [] }))
}

/** duration is seconds. A 409 rejects with grantId naming the overlapping grant. */
export async function createGrant(child_id: number, services: string[], duration: number): Promise<GrantCreated> {
  const g = await request<GrantCreated>('POST', GRANTS, { child_id, services, duration })
  return { ...g, shared: g.shared ?? [] }
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

// ---- grant history -------------------------------------------------------

/** How a finished grant ended; 'ended_unknown' is a pre-upgrade end whose reason was never recorded. */
export type HistoryStatus = 'ongoing' | 'expired' | 'ended_early' | 'permanent_block' | 'ended_unknown'

export interface HistoryService {
  id: string
  /** The name snapshotted at grant start, else the current name, else the bare id. */
  name: string
  /** RFC 3339; null while the service stayed on the grant. */
  removed_at: string | null
  removed_reason: 'permanent_block' | null
}

export interface HistoryExtension {
  /** RFC 3339: when the extension happened. */
  at: string
  old_ends_at: string
  new_ends_at: string
}

export interface HistoryGrant {
  id: number
  status: HistoryStatus
  /** RFC 3339. */
  started_at: string
  /** The scheduled end, not when it actually ended. */
  ends_at: string
  /** When it actually ended; null while ongoing. */
  ended_at: string | null
  services: HistoryService[]
  /** Oldest first. */
  extensions: HistoryExtension[]
}

export interface HistoryPage {
  child: ChildRef
  /** Newest first. */
  grants: HistoryGrant[]
  /** The cursor for the next (older) page; null on the last page. */
  next_before: number | null
}

/**
 * One page of a child's grant history, newest first. before is the previous
 * page's next_before; omitted for the first page. A 404 means no such child.
 */
export async function fetchHistory(childId: number, before?: number): Promise<HistoryPage> {
  const query = before === undefined ? '' : `?before=${before}`
  const p = await request<HistoryPage>('GET', `${CHILDREN}/${childId}/history${query}`)
  return {
    ...p,
    grants: (p.grants ?? []).map((g) => ({ ...g, services: g.services ?? [], extensions: g.extensions ?? [] })),
    next_before: p.next_before ?? null,
  }
}

/** A data: URL for an <img> from a catalogue icon. */
export function iconUrl(icon: string): string {
  return 'data:image/svg+xml;base64,' + icon
}

// ---- setup export and import ---------------------------------------------

/** What an import did. */
export interface ImportResult {
  /** Clients in the file this AdGuard does not have; their mappings are kept. */
  missing_clients: string[]
  /** False when the setup was stored but AdGuard's rules were not updated yet; the reconciler catches up. */
  rules_applied: boolean
}

/** The export file, unknown here because the SPA only saves it: the format is the server's. */
export function exportSetup(): Promise<unknown> {
  return request<unknown>('GET', '/api/v1/export')
}

/**
 * Restore an export file into an empty install. text is sent as read, never
 * parsed or re-encoded, so the server names every problem with the file. A
 * 409 means the install is not empty; a 502 means nothing was stored.
 */
export async function importSetup(text: string): Promise<ImportResult> {
  const r = await send<ImportResult>('POST', '/api/v1/import', text)
  return { ...r, missing_clients: r.missing_clients ?? [] }
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
      // A 403 is the password re-check on a PIN change or remove: no username was typed.
      return e.status === 403 ? 'Wrong password' : 'Wrong username or password'
    case 'reauth_required':
      return 'Enter your AdGuard password'
    case 'bad_pin':
      if (e.attemptsRemaining === undefined) return 'Wrong PIN'
      return `Wrong PIN — ${e.attemptsRemaining} ${e.attemptsRemaining === 1 ? 'attempt' : 'attempts'} left`
    case 'device_revoked':
      return 'This device was signed out — sign in with your password'
    case 'adguard_unavailable':
      return "Can't reach AdGuard Home — try again in a moment"
    case 'rate_limited':
      return `Too many attempts — wait ${e.retryAfter ?? 60}s`
    default:
      return e.message
  }
}
