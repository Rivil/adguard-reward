// Pure helpers for the grant-history screen: the label each end status
// shows, when a grant actually ended, how a removed service is marked and how
// a further page joins the list already on screen.
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
export function appendPage(shown: HistoryGrant[], page: HistoryGrant[]): HistoryGrant[] {
  const seen = new Set(shown.map((g) => g.id))
  const out = [...shown]
  for (const g of page) {
    if (seen.has(g.id)) continue
    seen.add(g.id)
    out.push(g)
  }
  return out
}
