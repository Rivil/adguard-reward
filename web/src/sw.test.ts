// Stryker disable all: test sources are not mutation targets
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cacheName } from './lib/sw-routing'

type Listener = (e: unknown) => unknown

const ORIGIN = 'http://localhost'

function pathOf(key: unknown): string {
  if (typeof key === 'string') return key
  return new URL((key as { url: string }).url).pathname
}

/** A fake worker scope: listeners captured, caches and fetch stubbed. */
async function boot(precache: string[], cached: Record<string, Response> = {}, cacheNames: string[] = []) {
  const listeners: Record<string, Listener[]> = {}
  const cache = {
    addAll: vi.fn(async (_: string[]) => {}),
    match: vi.fn(async (key: unknown) => cached[pathOf(key)]),
    put: vi.fn(async () => {}),
  }
  const caches = {
    open: vi.fn(async (_: string) => cache),
    match: vi.fn(async (key: unknown) => cached[pathOf(key)]),
    keys: vi.fn(async () => cacheNames),
    delete: vi.fn(async (_: string) => true),
  }
  const scope = {
    addEventListener: (type: string, l: Listener) => {
      ;(listeners[type] ??= []).push(l)
    },
    skipWaiting: vi.fn(async () => {}),
    clients: { claim: vi.fn(async () => {}) },
    location: { origin: ORIGIN },
    __PRECACHE__: precache,
  }
  const fetchFn = vi.fn(async (_req: unknown) => new Response('net', { status: 200 }))
  vi.stubGlobal('self', scope)
  vi.stubGlobal('caches', caches)
  vi.stubGlobal('fetch', fetchFn)
  vi.resetModules()
  await import('./sw')
  const dispatch = (type: string, event: Record<string, unknown>) => {
    for (const l of listeners[type] ?? []) l(event)
  }
  return { listeners, cache, caches, scope, fetch: fetchFn, dispatch }
}

function fetchEvent(method: string, path: string, mode = 'no-cors') {
  const url = path.startsWith('http') ? path : ORIGIN + path
  const respondWith = vi.fn((_: Promise<Response> | Response) => {})
  return { request: { url, method, mode }, respondWith, waitUntil: vi.fn() }
}

async function responded(ev: ReturnType<typeof fetchEvent>): Promise<Response> {
  expect(ev.respondWith).toHaveBeenCalledTimes(1)
  return ev.respondWith.mock.calls[0][0]
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('sw', () => {
  it('api requests bypass the worker', async () => {
    const w = await boot(['/', '/assets/app-abc.js'], { '/': new Response('shell') })
    for (const ev of [
      fetchEvent('GET', '/api/v1/grants'),
      fetchEvent('POST', '/api/v1/grants'),
      fetchEvent('GET', '/healthz'),
      fetchEvent('GET', 'https://other.example/x'),
      fetchEvent('GET', '/api/v1/grants', 'navigate'),
    ]) {
      w.dispatch('fetch', ev)
      expect(ev.respondWith, ev.request.url).not.toHaveBeenCalled()
    }
    expect(w.caches.open).not.toHaveBeenCalled()
    expect(w.caches.match).not.toHaveBeenCalled()
    expect(w.cache.match).not.toHaveBeenCalled()
    expect(w.fetch).not.toHaveBeenCalled()
  })

  it('offline navigation serves the shell', async () => {
    const shell = new Response('<html>shell</html>')
    const w = await boot(['/'], { '/': shell })
    const online = new Response('fresh', { status: 200 })
    w.fetch.mockResolvedValueOnce(online)
    const ev = fetchEvent('GET', '/children', 'navigate')
    w.dispatch('fetch', ev)
    expect(await responded(ev)).toBe(online)
    expect(w.caches.match).not.toHaveBeenCalled()

    w.fetch.mockRejectedValueOnce(new TypeError('Failed to fetch'))
    const offline = fetchEvent('GET', '/children', 'navigate')
    w.dispatch('fetch', offline)
    expect(await responded(offline)).toBe(shell)
    expect(w.caches.match).toHaveBeenCalledWith('/')
    expect(w.cache.put).not.toHaveBeenCalled()
  })

  it('assets come from the precache, never put at runtime', async () => {
    const js = new Response('console.log(1)')
    const w = await boot(['/', '/assets/app-abc.js', '/assets/gone.js'], { '/assets/app-abc.js': js })
    const hit = fetchEvent('GET', '/assets/app-abc.js')
    w.dispatch('fetch', hit)
    expect(await responded(hit)).toBe(js)
    expect(w.fetch).not.toHaveBeenCalled()

    const miss = fetchEvent('GET', '/assets/gone.js')
    w.dispatch('fetch', miss)
    const net = await responded(miss)
    expect(w.fetch).toHaveBeenCalledTimes(1)
    expect(await net.text()).toBe('net')
    expect(w.cache.put).not.toHaveBeenCalled()

    const unknown = fetchEvent('GET', '/assets/other.js')
    w.dispatch('fetch', unknown)
    expect(unknown.respondWith).not.toHaveBeenCalled()
  })

  it('install precaches and activate prunes', async () => {
    const list = ['/', '/assets/a.js']
    const current = cacheName(list)
    const w = await boot(list, {}, ['shell-old', current])

    const install = { waitUntil: vi.fn((_: Promise<unknown>) => {}) }
    w.dispatch('install', install)
    expect(install.waitUntil).toHaveBeenCalledTimes(1)
    await install.waitUntil.mock.calls[0][0]
    expect(w.caches.open).toHaveBeenCalledWith(current)
    expect(w.cache.addAll).toHaveBeenCalledWith(list)
    expect(w.scope.skipWaiting).toHaveBeenCalledTimes(1)

    const activate = { waitUntil: vi.fn((_: Promise<unknown>) => {}) }
    w.dispatch('activate', activate)
    await activate.waitUntil.mock.calls[0][0]
    expect(w.caches.delete).toHaveBeenCalledTimes(1)
    expect(w.caches.delete).toHaveBeenCalledWith('shell-old')
    expect(w.scope.clients.claim).toHaveBeenCalledTimes(1)
  })
})
