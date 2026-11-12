// Retrying a load the network refused. Pure apart from the timer and the two
// wake-up events it listens for, so tests drive it with fake timers.
import { ApiError } from './api'

/** First retry after 1s, doubling, never more than 30s apart. */
export function backoff(attempt: number): number {
  return Math.min(1000 * 2 ** Math.max(0, attempt), 30_000)
}

/**
 * True when err says the server is unreachable rather than that the parent
 * is signed out: a network failure (fetch threw, so no ApiError) or a 5xx —
 * tailscale serve answers 502 when the app is down. A 401 or any other 4xx
 * is a real answer and never retried.
 */
export function unreachable(err: unknown): boolean {
  return !(err instanceof ApiError) || err.status >= 500
}

export interface RetryTarget {
  addEventListener(type: string, fn: () => void): void
  removeEventListener(type: string, fn: () => void): void
}

/**
 * Call attempt() until it resolves true, waiting backoff(n) between tries
 * and trying at once when the browser comes back online or the page becomes
 * visible again. attempt() reports failure by resolving false; it never
 * runs twice at the same time. Returns a stop function; after stop no
 * further attempt starts.
 */
export function retryUntil(
  attempt: () => Promise<boolean>,
  wake: { window: RetryTarget; document: RetryTarget & { visibilityState: string } } = { window, document },
): () => void {
  let stopped = false
  let running = false
  let tries = 0
  let timer: ReturnType<typeof setTimeout> | null = null

  const schedule = () => {
    if (stopped) return
    timer = setTimeout(run, backoff(tries))
  }

  async function run() {
    if (stopped || running) return
    if (timer !== null) clearTimeout(timer)
    timer = null
    running = true
    let ok = false
    try {
      ok = await attempt()
    } catch {
      ok = false
    }
    running = false
    if (ok) {
      stop()
      return
    }
    tries++
    schedule()
  }

  const onOnline = () => void run()
  const onVisible = () => {
    if (wake.document.visibilityState === 'visible') void run()
  }

  function stop() {
    stopped = true
    if (timer !== null) clearTimeout(timer)
    timer = null
    wake.window.removeEventListener('online', onOnline)
    wake.document.removeEventListener('visibilitychange', onVisible)
  }

  wake.window.addEventListener('online', onOnline)
  wake.document.addEventListener('visibilitychange', onVisible)
  schedule()
  return stop
}
