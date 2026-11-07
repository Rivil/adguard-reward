// Pure helpers behind a child's blocked view and its permanent toggles: which
// rows to list, where each is open and why, which controls a row offers, and
// what the "Block a service…" picker lists. No DOM, no fetch — everything
// here is a function of its arguments.
import type { BlockedView, Service, ServiceState } from './api'

/** A row control: a permanent unblock or a permanent block. */
export type Control = 'unblock' | 'block'

/**
 * The rows the view lists: everything not plainly unblocked, plus an
 * unblocked service this child toggled (so the toggle can be reversed) or
 * that another child's grant or permanent unblock holds open on a shared
 * client (so the parent can see why). An unblocked service the app never
 * touched stays hidden, as before toggles existed.
 */
export function shown(v: BlockedView): ServiceState[] {
  return v.services.filter((s) => s.state !== 'unblocked' || s.toggle != null || (s.unblocked_by ?? []).length > 0)
}

/**
 * Where the service is open: plain client names for a partial service, then
 * "<client> by <child>'s grant" for another child's grant and
 * "<client> by <child> (permanent)" for another child's permanent unblock.
 * [] for a plainly blocked service.
 */
export function openOn(s: ServiceState): string[] {
  const by = s.unblocked_by ?? []
  const attributed = new Set(by.map((u) => u.client))
  const plain = s.state === 'partial' ? s.differs.filter((c) => !attributed.has(c)) : []
  const others = by.map((u) => {
    if (u.permanent) return `${u.client} by ${u.child.name === '' ? 'a removed child' : u.child.name} (permanent)`
    return `${u.client} by ${u.child.name === '' ? "a removed child's grant" : `${u.child.name}'s grant`}`
  })
  return [...plain, ...others]
}

/**
 * The controls a row offers: what its state calls for (blocked -> unblock,
 * partial -> both, unblocked -> none) plus the reverse of this child's own
 * toggle ('unblocked' -> block, 'blocked' -> unblock), so a toggle the state
 * has not caught up with yet can still be undone. Unblock always comes first.
 */
export function controlFor(s: ServiceState): Control[] {
  const unblock = s.state !== 'unblocked' || s.toggle === 'blocked'
  const block = s.state === 'partial' || s.toggle === 'unblocked'
  const out: Control[] = []
  if (unblock) out.push('unblock')
  if (block) out.push('block')
  return out
}

/**
 * The picker's list: the catalogue in its own order, minus every service the
 * view shows as blocked, narrowed to names containing query (trimmed, case
 * ignored). A blank query lists them all.
 */
export function pickerOptions(catalogue: Service[], view: BlockedView, query: string): Service[] {
  const blocked = new Set(view.services.filter((s) => s.state === 'blocked').map((s) => s.id))
  const q = query.trim().toLowerCase()
  return catalogue.filter((s) => !blocked.has(s.id) && s.name.toLowerCase().includes(q))
}
