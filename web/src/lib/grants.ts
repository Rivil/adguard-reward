// Pure helpers behind the active-grant panel and the tap flow: countdowns,
// duration formatting, the extend amount and overlap grouping. No DOM, no
// fetch — everything here is a function of its arguments.
import type { Grant } from './api'

/** Bounds mirror internal/grants: MinDuration = 1 min, MaxDuration = 24 h. */
export const MIN_DURATION = 60
export const MAX_DURATION = 86400

/** Whole seconds left until endsAt, rounded up, never negative. */
export function remainingSeconds(endsAt: string, nowMs: number): number {
  const left = (Date.parse(endsAt) - nowMs) / 1000
  return Math.max(0, Math.ceil(left))
}

function pad(n: number): string {
  return n < 10 ? `0${n}` : String(n)
}

/** h:mm:ss from an hour up, m:ss below. */
export function formatCountdown(seconds: number): string {
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = seconds % 60
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${m}:${pad(s)}`
}

/** "1 h", "30 min", "1 h 30 min" — the label on a button or an extend offer. */
export function formatDuration(seconds: number): string {
  const h = Math.floor(seconds / 3600)
  const m = Math.round((seconds % 3600) / 60)
  if (h === 0) return `${m} min`
  return m === 0 ? `${h} h` : `${h} h ${m} min`
}

/**
 * The amount an extend adds: the grant's own span (locked extend_amount),
 * clamped to the server's bounds so an already-extended grant still gets a
 * request the server accepts.
 */
export function ownDuration(g: Pick<Grant, 'started_at' | 'ends_at'>): number {
  const span = Math.round((Date.parse(g.ends_at) - Date.parse(g.started_at)) / 1000)
  return Math.min(MAX_DURATION, Math.max(MIN_DURATION, span))
}

export interface Overlap {
  /** Active grants for the child that already cover one of the services, id ASC. */
  overlapping: Grant[]
  /** Services no active grant covers, in input order. */
  remaining: string[]
}

/** Splits a tap into the grants it would collide with and what is left to create. */
export function overlapsFor(childId: number, services: string[], grants: Grant[]): Overlap {
  const overlapping = grants
    .filter((g) => g.child_id === childId && g.services.some((s) => services.includes(s)))
    .sort((a, b) => a.id - b.id)
  const covered = new Set(overlapping.flatMap((g) => g.services))
  const remaining = services.filter((s) => !covered.has(s))
  return { overlapping, remaining }
}

export function minutesToSeconds(minutes: number): number {
  return Math.round(minutes * 60)
}

export function secondsToMinutes(seconds: number): number {
  return Math.round(seconds / 60)
}
