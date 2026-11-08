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

/**
 * Poll once. A call while one is in flight joins it rather than overlapping —
 * unless `fresh`: a caller that has just changed server state (a toggle that
 * trimmed or ended a grant) needs an answer read after its change, so it
 * waits out the in-flight poll and then polls again.
 */
export function refresh(opts: { fresh?: boolean } = {}): Promise<void> {
  if (inFlight) return opts.fresh ? inFlight.then(() => refresh(opts)) : inFlight
  inFlight = listGrants()
    .then((grants) => {
      state.set({ grants, unreachable: false, loaded: true })
    })
    .catch((err: unknown) => {
      // A lapsed session is routed to login or the PIN pad by the api layer; it says nothing about reachability.
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
 * How long the screen may go untouched before interval polls stop. Every
 * poll is an authenticated request that slides the session, so a screen left
 * open on a PIN device would otherwise never reach its 15-minute idle lock.
 */
export const IDLE_AFTER_MS = 60 * 1000

/** What counts as someone using the screen. */
const INPUT_EVENTS = ['pointerdown', 'keydown'] as const

/**
 * Poll now and every intervalMs while someone is using the screen, plus
 * whenever the tab comes back into view so a backgrounded phone catches up.
 * Once IDLE_AFTER_MS passes without a pointerdown or keydown the interval
 * stops; the next input, or the tab coming back into view, polls at once and
 * restarts it. Returns stop; a second start while running is a no-op that
 * returns the same stop.
 */
export function start(intervalMs = 15000): () => void {
  if (stopCurrent) return stopCurrent
  let lastInput = Date.now()
  let timer: ReturnType<typeof setInterval> | null = null
  const tick = () => {
    if (Date.now() - lastInput < IDLE_AFTER_MS) {
      void refresh()
      return
    }
    if (timer !== null) clearInterval(timer)
    timer = null
  }
  /** Record use; restart the interval if idle had stopped it. True when it did. */
  const wake = (): boolean => {
    lastInput = Date.now()
    if (timer !== null) return false
    timer = setInterval(tick, intervalMs)
    return true
  }
  const onInput = () => {
    if (wake()) void refresh()
  }
  const onVisible = () => {
    if (document.visibilityState !== 'visible') return
    wake()
    void refresh()
  }
  document.addEventListener('visibilitychange', onVisible)
  // Capture, so a component that stops propagation still counts as use.
  for (const e of INPUT_EVENTS) document.addEventListener(e, onInput, { capture: true, passive: true })
  const stop = () => {
    if (timer !== null) clearInterval(timer)
    timer = null
    document.removeEventListener('visibilitychange', onVisible)
    for (const e of INPUT_EVENTS) document.removeEventListener(e, onInput, { capture: true })
    if (stopCurrent === stop) stopCurrent = null
  }
  stopCurrent = stop
  wake()
  void refresh()
  return stop
}

/** Current snapshot, for code outside a component. */
export function snapshot(): ActiveGrantsState {
  return get(state)
}
