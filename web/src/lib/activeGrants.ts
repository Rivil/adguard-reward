// The active-grant list the Home panel renders: polled from the server,
// updated in place from a 201 (server truth, not optimism), and kept as it
// was when a poll fails — the revert is server-side, so a stale countdown
// is still truthful (locked unreachable_countdown).
import { get, writable } from 'svelte/store'
import { ApiError, listGrants, type Grant } from './api'

export interface ActiveGrantsState {
  grants: Grant[]
  /** The last poll failed for a reason other than a lapsed session. */
  unreachable: boolean
  /** At least one poll has succeeded. */
  loaded: boolean
}

const state = writable<ActiveGrantsState>({ grants: [], unreachable: false, loaded: false })

/** Read-only view for components. */
export const activeGrants = { subscribe: state.subscribe }

let inFlight: Promise<void> | null = null

/** Poll once. A call while one is in flight joins it rather than overlapping. */
export function refresh(): Promise<void> {
  if (inFlight) return inFlight
  inFlight = listGrants()
    .then((grants) => {
      state.set({ grants, unreachable: false, loaded: true })
    })
    .catch((err: unknown) => {
      // A lapsed session is routed to login by the api layer; it says nothing about reachability.
      if (err instanceof ApiError && err.status === 401) return
      state.update((s) => ({ ...s, unreachable: true }))
    })
    .finally(() => {
      inFlight = null
    })
  return inFlight
}

/** Upsert one grant by id — what a 201 or an extend response reports. */
export function merge(g: Grant): void {
  state.update((s) => {
    const i = s.grants.findIndex((x) => x.id === g.id)
    const grants = i < 0 ? [...s.grants, g] : s.grants.map((x, j) => (j === i ? g : x))
    return { ...s, grants }
  })
}

export function remove(id: number): void {
  state.update((s) => ({ ...s, grants: s.grants.filter((g) => g.id !== id) }))
}

let stopCurrent: (() => void) | null = null

/**
 * Poll now and every intervalMs, plus whenever the tab comes back into
 * view so a backgrounded phone catches up. Returns stop; a second start
 * while running is a no-op that returns the same stop.
 */
export function start(intervalMs = 15000): () => void {
  if (stopCurrent) return stopCurrent
  const timer = setInterval(() => void refresh(), intervalMs)
  const onVisible = () => {
    if (document.visibilityState === 'visible') void refresh()
  }
  document.addEventListener('visibilitychange', onVisible)
  const stop = () => {
    clearInterval(timer)
    document.removeEventListener('visibilitychange', onVisible)
    if (stopCurrent === stop) stopCurrent = null
  }
  stopCurrent = stop
  void refresh()
  return stop
}

/** Current snapshot, for code outside a component. */
export function snapshot(): ActiveGrantsState {
  return get(state)
}
