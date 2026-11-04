// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { get } from 'svelte/store'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { IDLE_AFTER_MS, activeGrants, merge, refresh, remove, snapshot, start } from './activeGrants'
import { setOnUnauthorized, type Grant } from './api'

function grant(id: number, endsAt = '2026-09-21T11:00:00Z'): Grant {
  return {
    id,
    child_id: 1,
    services: ['tiktok'],
    clients: ['Kid phone'],
    started_at: '2026-09-21T10:00:00Z',
    ends_at: endsAt,
    shared: [],
  }
}

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

type Answer = () => Promise<Response>

/** A GET /api/v1/grants stub whose answers are supplied one poll at a time. */
function grantsServer(...answers: Answer[]) {
  const queue = [...answers]
  const fetchFn = vi.fn(async (url: string) => {
    if (url !== '/api/v1/grants') throw new Error('unexpected fetch ' + url)
    const next = queue.shift()
    if (!next) throw new Error('no answer queued for poll ' + (fetchFn.mock.calls.length + 1))
    return next()
  })
  vi.stubGlobal('fetch', fetchFn)
  return { fetch: fetchFn, push: (a: Answer) => queue.push(a) }
}

/** A GET /api/v1/grants stub that always answers an empty list. */
function grantsForever() {
  const fetchFn = vi.fn(async (url: string) => {
    if (url !== '/api/v1/grants') throw new Error('unexpected fetch ' + url)
    return json(200, { grants: [] })
  })
  vi.stubGlobal('fetch', fetchFn)
  return fetchFn
}

/** Someone touching the screen. */
function input(type: 'pointerdown' | 'keydown' = 'pointerdown') {
  document.body.dispatchEvent(new Event(type, { bubbles: true }))
}

const ok = (grants: Grant[]) => () => Promise.resolve(json(200, { grants }))
const netFail = () => Promise.reject(new TypeError('Failed to fetch'))
const unauthorized = () => Promise.resolve(json(401, { error: 'unauthorized', message: 'no session' }))

let onUnauthorized: ReturnType<typeof vi.fn<() => void>>
let stop: (() => void) | null = null

beforeEach(() => {
  onUnauthorized = vi.fn<() => void>()
  setOnUnauthorized(onUnauthorized)
})

afterEach(() => {
  stop?.()
  stop = null
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

/** Let an in-flight poll finish under fake timers (Response.json() needs a few turns). */
async function settle() {
  for (let i = 0; i < 5; i++) await vi.advanceTimersByTimeAsync(0)
}

describe('activeGrants', () => {
  it('polls on the interval and stops', async () => {
    vi.useFakeTimers()
    const server = grantsServer(ok([]), ok([]), ok([]), ok([]))
    stop = start(15000)
    expect(server.fetch).toHaveBeenCalledTimes(1)
    await settle()

    // Input before every tick keeps the screen in use, so idle never pauses it here.
    for (let i = 0; i < 3; i++) {
      input()
      await vi.advanceTimersByTimeAsync(15000)
    }
    expect(server.fetch).toHaveBeenCalledTimes(4)

    // A poll that does not come back: the interval must not start a second one.
    let release!: (r: Response) => void
    server.push(() => new Promise<Response>((r) => (release = r)))
    input()
    await vi.advanceTimersByTimeAsync(15000)
    expect(server.fetch).toHaveBeenCalledTimes(5)
    input()
    await vi.advanceTimersByTimeAsync(30000)
    expect(server.fetch).toHaveBeenCalledTimes(5)

    const again = start(15000)
    expect(again).toBe(stop)
    input()
    await vi.advanceTimersByTimeAsync(15000)
    expect(server.fetch).toHaveBeenCalledTimes(5)

    release(json(200, { grants: [] }))
    await settle()
    stop()
    stop = null
    server.push(ok([]))
    input()
    await vi.advanceTimersByTimeAsync(60000)
    expect(server.fetch).toHaveBeenCalledTimes(5)
  })

  it('an untouched screen stops polling until input', async () => {
    // Every poll slides the session; an open screen nobody touches must let
    // the 15-minute PIN lock arrive.
    vi.useFakeTimers()
    const fetchFn = grantsForever()
    stop = start(15000)
    await settle()
    expect(fetchFn).toHaveBeenCalledTimes(1)

    // Ticks at 15s, 30s, 45s poll; the 60s tick finds a minute without input.
    await vi.advanceTimersByTimeAsync(IDLE_AFTER_MS)
    expect(fetchFn).toHaveBeenCalledTimes(4)

    for (let i = 0; i < (20 * 60 * 1000) / 15000; i++) await vi.advanceTimersByTimeAsync(15000)
    expect(fetchFn).toHaveBeenCalledTimes(4)

    // The next pointerdown polls at once and the 15s cadence resumes from it.
    input('pointerdown')
    expect(fetchFn).toHaveBeenCalledTimes(5)
    await settle()
    await vi.advanceTimersByTimeAsync(14999)
    expect(fetchFn).toHaveBeenCalledTimes(5)
    await vi.advanceTimersByTimeAsync(1)
    expect(fetchFn).toHaveBeenCalledTimes(6)

    // Input while in use only keeps it in use: no extra poll per tap.
    input('pointerdown')
    input('keydown')
    expect(fetchFn).toHaveBeenCalledTimes(6)
    await vi.advanceTimersByTimeAsync(15000)
    expect(fetchFn).toHaveBeenCalledTimes(7)

    // The last input was at the +15s tick: +45s and +60s still poll, +75s is a minute on and stops.
    await vi.advanceTimersByTimeAsync(5 * 60 * 1000)
    expect(fetchFn).toHaveBeenCalledTimes(9)

    // A keydown wakes it the same way.
    input('keydown')
    expect(fetchFn).toHaveBeenCalledTimes(10)
    await settle()
    await vi.advanceTimersByTimeAsync(15000)
    expect(fetchFn).toHaveBeenCalledTimes(11)
  })

  it('coming back into view wakes an idle screen', async () => {
    vi.useFakeTimers()
    const fetchFn = grantsForever()
    const visibility = vi.spyOn(document, 'visibilityState', 'get')
    try {
      stop = start(15000)
      await settle()
      await vi.advanceTimersByTimeAsync(10 * 60 * 1000)
      expect(fetchFn).toHaveBeenCalledTimes(4)

      visibility.mockReturnValue('hidden')
      document.dispatchEvent(new Event('visibilitychange'))
      expect(fetchFn).toHaveBeenCalledTimes(4)

      visibility.mockReturnValue('visible')
      document.dispatchEvent(new Event('visibilitychange'))
      expect(fetchFn).toHaveBeenCalledTimes(5)
      await settle()
      await vi.advanceTimersByTimeAsync(15000)
      expect(fetchFn).toHaveBeenCalledTimes(6)
    } finally {
      visibility.mockRestore()
    }
  })

  it('stop removes the input listeners', async () => {
    vi.useFakeTimers()
    const fetchFn = grantsForever()
    stop = start(15000)
    await settle()
    await vi.advanceTimersByTimeAsync(IDLE_AFTER_MS)
    expect(fetchFn).toHaveBeenCalledTimes(4)

    stop()
    stop = null
    input('pointerdown')
    input('keydown')
    await vi.advanceTimersByTimeAsync(60000)
    expect(fetchFn).toHaveBeenCalledTimes(4)
  })

  it('failure keeps grants and sets unreachable', async () => {
    const g1 = grant(1)
    grantsServer(ok([g1]), netFail, ok([grant(2)]))
    await refresh()
    expect(get(activeGrants)).toEqual({ grants: [g1], unreachable: false, loaded: true })

    await refresh()
    expect(get(activeGrants)).toEqual({ grants: [g1], unreachable: true, loaded: true })

    await refresh()
    expect(get(activeGrants)).toEqual({ grants: [grant(2)], unreachable: false, loaded: true })
    expect(onUnauthorized).not.toHaveBeenCalled()
  })

  it('a server error is unreachable too', async () => {
    // Only a lapsed session is exempt: any other ApiError is as unreachable
    // as a dropped connection, and the list stays as it was.
    const g1 = grant(1)
    grantsServer(ok([g1]), () => Promise.resolve(json(502, { error: 'adguard_unavailable', message: 'down' })))
    await refresh()
    await refresh()
    expect(get(activeGrants)).toEqual({ grants: [g1], unreachable: true, loaded: true })
    expect(onUnauthorized).not.toHaveBeenCalled()
  })

  it('401 is not unreachable', async () => {
    const g1 = grant(1)
    grantsServer(ok([g1]), unauthorized)
    await refresh()
    await refresh()
    expect(get(activeGrants)).toEqual({ grants: [g1], unreachable: false, loaded: true })
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })

  it('visibilitychange refreshes', async () => {
    vi.useFakeTimers()
    const server = grantsServer(ok([]), ok([]), ok([]))
    stop = start(60 * 60 * 1000)
    await settle()
    expect(server.fetch).toHaveBeenCalledTimes(1)

    const visibility = vi.spyOn(document, 'visibilityState', 'get')
    visibility.mockReturnValue('visible')
    document.dispatchEvent(new Event('visibilitychange'))
    expect(server.fetch).toHaveBeenCalledTimes(2)
    await settle()

    visibility.mockReturnValue('hidden')
    document.dispatchEvent(new Event('visibilitychange'))
    expect(server.fetch).toHaveBeenCalledTimes(2)

    stop()
    stop = null
    visibility.mockReturnValue('visible')
    document.dispatchEvent(new Event('visibilitychange'))
    expect(server.fetch).toHaveBeenCalledTimes(2)
    visibility.mockRestore()
  })

  it('refresh in flight is shared', async () => {
    let release!: (r: Response) => void
    const server = grantsServer(() => new Promise<Response>((r) => (release = r)))
    const a = refresh()
    const b = refresh()
    expect(b).toBe(a)
    expect(server.fetch).toHaveBeenCalledTimes(1)
    release(json(200, { grants: [] }))
    await a
    expect(get(activeGrants).loaded).toBe(true)
  })

  it('fresh refresh waits out a poll in flight, then polls again', async () => {
    // A poll that left before a toggle trimmed grant 1 answers with it; the
    // toggle's fresh refresh must not settle for that answer.
    let release!: (r: Response) => void
    const server = grantsServer(() => new Promise<Response>((r) => (release = r)), ok([]))
    const stale = refresh()
    const fresh = refresh({ fresh: true })
    expect(fresh).not.toBe(stale)
    expect(server.fetch).toHaveBeenCalledTimes(1)

    release(json(200, { grants: [grant(1)] }))
    await stale
    await fresh
    expect(server.fetch).toHaveBeenCalledTimes(2)
    expect(get(activeGrants)).toEqual({ grants: [], unreachable: false, loaded: true })

    // With nothing in flight, fresh is a single poll.
    server.push(ok([grant(2)]))
    await refresh({ fresh: true })
    expect(server.fetch).toHaveBeenCalledTimes(3)
    expect(get(activeGrants).grants).toEqual([grant(2)])
  })

  it('merge upserts by id', async () => {
    const g1 = grant(1)
    const g2 = grant(2)
    grantsServer(ok([g1]), ok([grant(9)]))
    await refresh()

    merge(g2)
    expect(get(activeGrants).grants).toEqual([g1, g2])

    const g1b = grant(1, '2026-09-21T12:00:00Z')
    merge(g1b)
    expect(get(activeGrants).grants).toEqual([g1b, g2])
    expect(get(activeGrants).grants.filter((g) => g.id === 1)).toHaveLength(1)

    remove(2)
    expect(get(activeGrants).grants).toEqual([g1b])
    expect(snapshot().grants).toEqual([g1b])

    await refresh()
    expect(get(activeGrants).grants).toEqual([grant(9)])
  })
})
