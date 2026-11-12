// Stryker disable all: test sources are not mutation targets
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from './api'
import { backoff, retryUntil, unreachable, type RetryTarget } from './retry'

/** A minimal EventTarget stand-in that records its listeners. */
function target<T extends object>(extra: T): RetryTarget & T & { fire(type: string): void; count(): number } {
  const fns = new Map<string, Set<() => void>>()
  return Object.assign(extra, {
    addEventListener(type: string, fn: () => void) {
      if (!fns.has(type)) fns.set(type, new Set())
      fns.get(type)!.add(fn)
    },
    removeEventListener(type: string, fn: () => void) {
      fns.get(type)?.delete(fn)
    },
    fire(type: string) {
      for (const fn of fns.get(type) ?? []) fn()
    },
    count() {
      let n = 0
      for (const s of fns.values()) n += s.size
      return n
    },
  })
}

describe('backoff', () => {
  it('starts at 1s, doubles, and caps at 30s', () => {
    expect([0, 1, 2, 3, 4, 5, 6, 20].map(backoff)).toEqual([1000, 2000, 4000, 8000, 16000, 30000, 30000, 30000])
  })
  it('treats a negative attempt as the first', () => {
    expect(backoff(-3)).toBe(1000)
  })
})

describe('unreachable', () => {
  it('a thrown network error is unreachable', () => {
    expect(unreachable(new TypeError('Failed to fetch'))).toBe(true)
  })
  it('a 5xx is unreachable, the 500 boundary included', () => {
    expect(unreachable(new ApiError(502, 'bad_gateway', ''))).toBe(true)
    expect(unreachable(new ApiError(500, 'internal', ''))).toBe(true)
  })
  it('a 401 or other 4xx is a real answer', () => {
    expect(unreachable(new ApiError(401, 'unauthorized', ''))).toBe(false)
    expect(unreachable(new ApiError(499, 'x', ''))).toBe(false)
  })
})

describe('retryUntil', () => {
  let win: ReturnType<typeof target<object>>
  let doc: ReturnType<typeof target<{ visibilityState: string }>>
  beforeEach(() => {
    vi.useFakeTimers()
    win = target({})
    doc = target({ visibilityState: 'visible' })
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('waits backoff between tries and stops once attempt succeeds', async () => {
    const results = [false, false, true]
    const attempt = vi.fn(async () => results.shift() ?? true)
    retryUntil(attempt, { window: win, document: doc })

    expect(attempt).toHaveBeenCalledTimes(0)
    await vi.advanceTimersByTimeAsync(999)
    expect(attempt).toHaveBeenCalledTimes(0)
    await vi.advanceTimersByTimeAsync(1)
    expect(attempt).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(2000)
    expect(attempt).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(4000)
    expect(attempt).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(attempt).toHaveBeenCalledTimes(3)
    expect(win.count() + doc.count()).toBe(0)
  })

  it('a rejected attempt counts as a failure, not a crash', async () => {
    const attempt = vi.fn().mockRejectedValueOnce(new Error('boom')).mockResolvedValue(true)
    retryUntil(attempt, { window: win, document: doc })
    await vi.advanceTimersByTimeAsync(1000)
    await vi.advanceTimersByTimeAsync(2000)
    expect(attempt).toHaveBeenCalledTimes(2)
  })

  it('tries at once when the browser comes back online', async () => {
    const attempt = vi.fn(async () => true)
    retryUntil(attempt, { window: win, document: doc })
    win.fire('online')
    await vi.advanceTimersByTimeAsync(0)
    expect(attempt).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(attempt).toHaveBeenCalledTimes(1)
  })

  it('tries at once when the page becomes visible, not when it hides', async () => {
    const attempt = vi.fn(async () => true)
    retryUntil(attempt, { window: win, document: doc })
    doc.visibilityState = 'hidden'
    doc.fire('visibilitychange')
    await vi.advanceTimersByTimeAsync(0)
    expect(attempt).toHaveBeenCalledTimes(0)
    doc.visibilityState = 'visible'
    doc.fire('visibilitychange')
    await vi.advanceTimersByTimeAsync(0)
    expect(attempt).toHaveBeenCalledTimes(1)
  })

  it('never overlaps two attempts', async () => {
    let release: (ok: boolean) => void = () => {}
    const attempt = vi.fn(() => new Promise<boolean>((r) => (release = r)))
    retryUntil(attempt, { window: win, document: doc })
    await vi.advanceTimersByTimeAsync(1000)
    win.fire('online')
    win.fire('online')
    await vi.advanceTimersByTimeAsync(0)
    expect(attempt).toHaveBeenCalledTimes(1)
    release(true)
  })

  it('stop cancels the pending try and drops the listeners', async () => {
    const attempt = vi.fn(async () => false)
    const stop = retryUntil(attempt, { window: win, document: doc })
    stop()
    win.fire('online')
    await vi.advanceTimersByTimeAsync(60_000)
    expect(attempt).toHaveBeenCalledTimes(0)
    expect(win.count() + doc.count()).toBe(0)
  })
})
