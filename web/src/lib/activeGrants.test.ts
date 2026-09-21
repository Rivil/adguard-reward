// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { get } from 'svelte/store'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { activeGrants, merge, refresh, remove, snapshot, start } from './activeGrants'
import { setOnUnauthorized, type Grant } from './api'

function grant(id: number, endsAt = '2026-09-21T11:00:00Z'): Grant {
  return {
    id,
    child_id: 1,
    services: ['tiktok'],
    clients: ['Kid phone'],
    started_at: '2026-09-21T10:00:00Z',
    ends_at: endsAt,
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

    for (let i = 0; i < 3; i++) {
      await vi.advanceTimersByTimeAsync(15000)
    }
    expect(server.fetch).toHaveBeenCalledTimes(4)

    // A poll that does not come back: the interval must not start a second one.
    let release!: (r: Response) => void
    server.push(() => new Promise<Response>((r) => (release = r)))
    await vi.advanceTimersByTimeAsync(15000)
    expect(server.fetch).toHaveBeenCalledTimes(5)
    await vi.advanceTimersByTimeAsync(30000)
    expect(server.fetch).toHaveBeenCalledTimes(5)

    const again = start(15000)
    expect(again).toBe(stop)
    await vi.advanceTimersByTimeAsync(15000)
    expect(server.fetch).toHaveBeenCalledTimes(5)

    release(json(200, { grants: [] }))
    await settle()
    stop()
    stop = null
    server.push(ok([]))
    await vi.advanceTimersByTimeAsync(60000)
    expect(server.fetch).toHaveBeenCalledTimes(5)
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
