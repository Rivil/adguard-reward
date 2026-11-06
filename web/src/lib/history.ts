// Pure helpers for the grant-history screen: the label each end status
// shows, when a grant actually ended, how a removed service is marked, how
// a further page joins the list already on screen, and how the filters read
// from and write to the URL.
import type { HistoryGrant, HistoryService, HistoryStatus } from './api'

const LABELS: Record<HistoryStatus, string> = {
  ongoing: 'Ongoing',
  expired: 'Expired',
  ended_early: 'Ended early',
  permanent_block: 'Ended by permanent block',
  // A pre-upgrade end: the old rows cannot tell a parent End from a
  // permanent-block trim, so history never claims either (locked legacy_end_reason).
  ended_unknown: 'Ended (reason unknown)',
}

/** The line a grant's end status shows. */
export function statusLabel(status: HistoryStatus): string {
  return LABELS[status]
}

/**
 * When the grant actually ended: ended_at, never the scheduled ends_at, and
 * null while it is ongoing — even when its scheduled end is already past.
 */
export function endTimeOf(g: Pick<HistoryGrant, 'status' | 'ended_at'>): string | null {
  return g.status === 'ongoing' ? null : g.ended_at
}

/** A history timestamp as the parent's locale writes a date and time. */
export function formatTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}

const REMOVED_REASONS: Record<NonNullable<HistoryService['removed_reason']>, string> = {
  permanent_block: 'permanent block',
}

/** The marker a removed service carries; null while it stayed on the grant. */
export function removedText(s: HistoryService): string | null {
  if (s.removed_at === null) return null
  const reason = s.removed_reason === null ? null : REMOVED_REASONS[s.removed_reason]
  return reason ? `removed ${formatTime(s.removed_at)} · ${reason}` : `removed ${formatTime(s.removed_at)}`
}

/** Appends an older page, dropping any grant already shown; the shown page keeps its order. */
export function appendPage<T extends { id: number }>(shown: T[], page: T[]): T[] {
  const seen = new Set(shown.map((g) => g.id))
  const out = [...shown]
  for (const g of page) {
    if (seen.has(g.id)) continue
    seen.add(g.id)
    out.push(g)
  }
  return out
}

/**
 * The overview's filters as the URL carries them, one value each (locked
 * filter_values). from and to are local calendar days, YYYY-MM-DD, both
 * included; null means no filter.
 */
export interface HistoryFilters {
  service: string | null
  status: HistoryStatus | null
  from: string | null
  to: string | null
}

const DAY = /^(\d{4})-(\d{2})-(\d{2})$/

// A day outside these years is a half-typed date or one the server cannot
// read: the day after 9999-12-31 serialises as +010000, which Go rejects.
const MIN_YEAR = 1000
const MAX_YEAR = 9998

/** v when it is a real calendar day in range, else null. UTC, so no zone can move it. */
function parseDay(v: string | null): string | null {
  const m = v === null ? null : DAY.exec(v)
  if (!m) return null
  const [year, month, day] = [Number(m[1]), Number(m[2]), Number(m[3])]
  if (year < MIN_YEAR || year > MAX_YEAR) return null
  const d = new Date(Date.UTC(year, month - 1, day))
  return d.getUTCMonth() === month - 1 && d.getUTCDate() === day ? v : null
}

function parseStatus(v: string | null): HistoryStatus | null {
  return v !== null && Object.hasOwn(LABELS, v) ? (v as HistoryStatus) : null
}

/** Reads the filters from a query string; a value that is not valid is no filter. */
export function parseFilters(search: string): HistoryFilters {
  const q = new URLSearchParams(search)
  return {
    service: q.get('service') || null,
    status: parseStatus(q.get('status')),
    from: parseDay(q.get('from')),
    to: parseDay(q.get('to')),
  }
}

/** The set filters as query params, always in this order. */
export function filterParams(f: HistoryFilters): URLSearchParams {
  const q = new URLSearchParams()
  if (f.service !== null) q.set('service', f.service)
  if (f.status !== null) q.set('status', f.status)
  if (f.from !== null) q.set('from', f.from)
  if (f.to !== null) q.set('to', f.to)
  return q
}

/** Whether any filter is set. */
export function hasFilters(f: HistoryFilters): boolean {
  return f.service !== null || f.status !== null || f.from !== null || f.to !== null
}

/**
 * The instant a local calendar day starts, offset by addDays. new Date(y, m, d)
 * is local time: a midnight the clocks skip becomes the day's first instant and
 * a midnight they repeat takes the earlier one. Never add 24 hours instead,
 * because a day can be 23 or 25 hours long.
 */
function dayStart(day: string, addDays: number): string {
  const [year, month, date] = day.split('-').map(Number)
  return new Date(year, month - 1, date + addDays).toISOString()
}

/**
 * The API's date bounds for two inclusive local days: from is the instant the
 * first day starts and until the instant after the last day ends. The server
 * knows no time zone, so the parent's days become instants here (locked
 * date_range). An unset day leaves its bound out.
 */
export function dayBounds(from: string | null, to: string | null): { from?: string; until?: string } {
  return {
    ...(from !== null && { from: dayStart(from, 0) }),
    ...(to !== null && { until: dayStart(to, 1) }),
  }
}
