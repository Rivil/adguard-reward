// Everything a tap does except the DOM: one POST, the 201 merged into the
// active list as server truth, a 409 turned into an extend offer, and the
// accepted offer delivered whole — extend what overlaps, create the rest
// (locked no_confirm, overlap_offer, extend_amount).
import { ApiError, messageFor, type Grant, type GrantCreated } from './api'
import { overlapsFor } from './grants'

export interface TapSpec {
  /** Dedup key for in-flight taps: a button id or the ad-hoc form. */
  key: string
  child_id: number
  services: string[]
  /** Seconds; also what an accepted offer extends by. */
  duration: number
  /** The child's clients, so the merged grant matches what the server will list. */
  clients: string[]
}

export interface TapDeps {
  createGrant(child_id: number, services: string[], duration: number): Promise<GrantCreated>
  extendGrant(id: number, duration: number): Promise<{ id: number; ends_at: string }>
  refresh(): Promise<void>
  merge(g: Grant): void
  /** The active list as currently known. */
  grants(): Grant[]
}

export interface Offer {
  /** Active grants the tap overlaps, id ASC. */
  targets: Grant[]
  /** Services none of them cover, in tap order. */
  remaining: string[]
}

export type Outcome =
  | { kind: 'applied'; grant: Grant }
  | { kind: 'partial'; grant: Grant; failed: string[] }
  | { kind: 'offer'; targets: Grant[]; remaining: string[] }
  | { kind: 'error'; code: string; message: string }
  | { kind: 'ignored' }

const NETWORK_MESSAGE = "Can't reach the server — check your connection"

// Keys with a request in flight: a second tap on the same button while the
// first is still out is dropped, never a second POST.
const inflight = new Set<string>()

/** The grant a 201 describes, shaped like the server will list it. */
function grantFrom(spec: TapSpec, services: string[], created: GrantCreated): Grant {
  const ends = Date.parse(created.ends_at)
  return {
    id: created.id,
    child_id: spec.child_id,
    services,
    clients: spec.clients,
    started_at: new Date(ends - spec.duration * 1000).toISOString(),
    ends_at: created.ends_at,
  }
}

function errorOutcome(err: unknown): Outcome {
  if (err instanceof ApiError) return { kind: 'error', code: err.code, message: messageFor(err) }
  return { kind: 'error', code: 'network', message: NETWORK_MESSAGE }
}

/** POST once; 201 is merged then the list re-synced. A 409 is returned as-is for the caller to map. */
async function create(spec: TapSpec, services: string[], deps: TapDeps): Promise<Outcome | ApiError> {
  let created: GrantCreated
  try {
    created = await deps.createGrant(spec.child_id, services, spec.duration)
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) return err
    return errorOutcome(err)
  }
  const grant = grantFrom(spec, services, created)
  deps.merge(grant)
  await deps.refresh().catch(() => {})
  if (!created.applied) return { kind: 'partial', grant, failed: created.failed ?? [] }
  return { kind: 'applied', grant }
}

async function withKey(key: string, fn: () => Promise<Outcome>): Promise<Outcome> {
  if (inflight.has(key)) return { kind: 'ignored' }
  inflight.add(key)
  try {
    return await fn()
  } finally {
    inflight.delete(key)
  }
}

/** One tap: create the grant, or turn an overlap into an offer. */
export function runTap(spec: TapSpec, deps: TapDeps): Promise<Outcome> {
  return withKey(spec.key, async () => {
    const res = await create(spec, spec.services, deps)
    if (!(res instanceof ApiError)) return res
    // 409: re-sync so the offer is computed against the server's list, then
    // fall back to the id the envelope named if the list is still stale.
    await deps.refresh().catch(() => {})
    const { overlapping, remaining } = overlapsFor(spec.child_id, spec.services, deps.grants())
    if (overlapping.length > 0) return { kind: 'offer', targets: overlapping, remaining }
    if (res.grantId === undefined) return errorOutcome(res)
    const stub: Grant = { id: res.grantId, child_id: spec.child_id, services: [], clients: [], started_at: '', ends_at: '' }
    return { kind: 'offer', targets: [stub], remaining: spec.services }
  })
}

/**
 * The accepted offer: extend every target by the tap's duration (a target
 * that expired meanwhile hands its services back to the create), then
 * create whatever is left. A 409 from that create is an error, not a
 * second offer.
 */
export function acceptOffer(offer: Offer, spec: TapSpec, deps: TapDeps): Promise<Outcome> {
  return withKey(spec.key, async () => {
    const remaining = new Set(offer.remaining)
    let last: Grant | null = null
    for (const target of [...offer.targets].sort((a, b) => a.id - b.id)) {
      try {
        const res = await deps.extendGrant(target.id, spec.duration)
        last = { ...target, ends_at: res.ends_at }
        deps.merge(last)
      } catch (err) {
        if (err instanceof ApiError && err.status === 404) {
          for (const s of target.services) if (spec.services.includes(s)) remaining.add(s)
          continue
        }
        return errorOutcome(err)
      }
    }
    const rest = spec.services.filter((s) => remaining.has(s))
    if (rest.length > 0) {
      const res = await create(spec, rest, deps)
      return res instanceof ApiError ? errorOutcome(res) : res
    }
    await deps.refresh().catch(() => {})
    if (last === null) return { kind: 'error', code: 'not_found', message: 'That grant has already ended' }
    return { kind: 'applied', grant: last }
  })
}
