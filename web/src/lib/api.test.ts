// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { get } from 'svelte/store'
import {
  ApiError,
  CSRF_HEADER,
  CSRF_VALUE,
  applyMigration,
  changePin,
  childBlocked,
  createChild,
  createClient,
  createCustomService,
  createGrant,
  deleteChild,
  deleteCustomService,
  listCustomServices,
  updateCustomService,
  endGrant,
  exportSetup,
  extendGrant,
  fetchHistory,
  fetchHistoryPage,
  fetchHistoryServices,
  historyChild,
  historyFilters,
  iconUrl,
  importSetup,
  listButtons,
  listChildren,
  listClients,
  listDevices,
  listGrants,
  listServices,
  locked,
  login,
  logout,
  me,
  messageFor,
  migrationDismissed,
  migrationOffer,
  navigate,
  pinStatus,
  removePin,
  renameClient,
  request,
  route,
  routeFor,
  saveButtons,
  setOnPinRequired,
  setOnUnauthorized,
  setPin,
  toggleService,
  unlock,
  updateChild,
  type HistoryStatus,
} from './api'
import type { HistoryFilters } from './history'

type Call = { url: string; init: RequestInit }

function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  })
}

function mockFetch(...responses: Response[]): Call[] {
  const calls: Call[] = []
  const queue = [...responses]
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit) => {
      calls.push({ url, init })
      const next = queue.shift()
      if (!next) throw new Error('unexpected fetch ' + url)
      return next
    }),
  )
  return calls
}

function header(init: RequestInit, name: string): string | null {
  return (init.headers as Record<string, string>)[name] ?? null
}

async function fails(p: Promise<unknown>): Promise<ApiError> {
  try {
    await p
  } catch (e) {
    if (e instanceof ApiError) return e
    throw e
  }
  throw new Error('promise resolved, expected ApiError')
}

let unauthorized: ReturnType<typeof vi.fn<() => void>>
let pinRequired: ReturnType<typeof vi.fn<() => void>>

beforeEach(() => {
  unauthorized = vi.fn<() => void>()
  setOnUnauthorized(unauthorized)
  pinRequired = vi.fn<() => void>()
  setOnPinRequired(pinRequired)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('request', () => {
  it('sends X-Requested-With', async () => {
    const calls = mockFetch(new Response(null, { status: 204 }), json(200, { username: 'mum', expires_at: 'x' }))
    await login('mum', 'pw')
    await me()
    expect(calls).toHaveLength(2)
    for (const c of calls) {
      expect(header(c.init, CSRF_HEADER)).toBe(CSRF_VALUE)
      expect(c.init.credentials).toBe('same-origin')
    }
    expect(calls[0].init.method).toBe('POST')
    expect(calls[1].init.method).toBe('GET')
  })

  it('login posts credentials', async () => {
    const calls = mockFetch(new Response(null, { status: 204 }))
    await login('a', 'b')
    expect(calls[0].url).toBe('/api/v1/login')
    expect(calls[0].init.method).toBe('POST')
    expect(header(calls[0].init, 'Content-Type')).toBe('application/json')
    expect(calls[0].init.body).toBe('{"username":"a","password":"b"}')
  })

  it('logout posts', async () => {
    const calls = mockFetch(new Response(null, { status: 204 }))
    await logout()
    expect(calls[0].url).toBe('/api/v1/logout')
    expect(calls[0].init.method).toBe('POST')
  })

  it('401 on /login does not redirect', async () => {
    mockFetch(json(401, { error: 'bad_credentials', message: 'invalid username or password' }))
    const err = await fails(login('a', 'wrong'))
    expect(err.status).toBe(401)
    expect(err.code).toBe('bad_credentials')
    expect(err.message).toBe('invalid username or password')
    expect(unauthorized).not.toHaveBeenCalled()

    mockFetch(json(401, { error: 'unauthorized', message: 'authentication required' }))
    const err2 = await fails(me())
    expect(err2.code).toBe('unauthorized')
    expect(unauthorized).toHaveBeenCalledTimes(1)
  })

  it('502 on /me does not redirect', async () => {
    mockFetch(json(502, { error: 'adguard_unavailable', message: 'upstream down' }))
    const err = await fails(me())
    expect(err.status).toBe(502)
    expect(err.code).toBe('adguard_unavailable')
    expect(err.message).toBe('upstream down')
    expect(unauthorized).not.toHaveBeenCalled()
  })

  it('distinguishes adguard_unavailable', async () => {
    mockFetch(json(502, { error: 'adguard_unavailable', message: 'AdGuard Home did not accept the login attempt' }))
    const down = await fails(login('a', 'b'))
    mockFetch(json(401, { error: 'bad_credentials', message: 'invalid username or password' }))
    const bad = await fails(login('a', 'b'))
    expect(down.code).toBe('adguard_unavailable')
    expect(bad.code).toBe('bad_credentials')
    expect(down.message).not.toBe(bad.message)
    expect(messageFor(down)).not.toBe(messageFor(bad))
  })

  it('429 carries retryAfter', async () => {
    mockFetch(json(429, { error: 'rate_limited', message: 'too many attempts' }, { 'Retry-After': '120' }))
    const err = await fails(login('a', 'b'))
    expect(err.status).toBe(429)
    expect(err.code).toBe('rate_limited')
    expect(err.retryAfter).toBe(120)

    mockFetch(json(502, { error: 'adguard_unavailable', message: 'm' }, { 'Retry-After': '5' }))
    const notLimited = await fails(login('a', 'b'))
    expect(notLimited.retryAfter).toBeUndefined()

    mockFetch(json(429, { error: 'rate_limited', message: 'too many attempts' }))
    const noHeader = await fails(login('a', 'b'))
    expect(noHeader.code).toBe('rate_limited')
    expect(noHeader.retryAfter).toBeUndefined()
  })

  it('returns the JSON body on 200 and undefined on 204', async () => {
    mockFetch(json(200, { username: 'mum', expires_at: '2026-10-20T12:00:00Z' }))
    expect(await me()).toEqual({ username: 'mum', expires_at: '2026-10-20T12:00:00Z' })
    mockFetch(new Response(null, { status: 204 }))
    expect(await request<void>('POST', '/api/v1/logout')).toBeUndefined()
  })

  it('tolerates a non-JSON error body', async () => {
    mockFetch(new Response('gateway timeout', { status: 504, statusText: 'Gateway Timeout' }))
    const err = await fails(me())
    expect(err.status).toBe(504)
    expect(err.code).toBe('unknown')
    expect(err.message).toBe('Gateway Timeout')

    // HTTP/2 carries no reason phrase: fall back to the status number.
    mockFetch(new Response('gateway timeout', { status: 504 }))
    const bare = await fails(me())
    expect(bare.message).toBe('HTTP 504')
  })

  it('ignores non-string envelope fields', async () => {
    mockFetch(json(500, { error: 5, message: { nested: true } }, {}))
    const err = await fails(me())
    expect(err.code).toBe('unknown')
    expect(err.message).toBe('HTTP 500')
  })
})

describe('messageFor', () => {
  it('gives the fixed line per code', () => {
    expect(messageFor(new ApiError(401, 'bad_credentials', 'x'))).toBe('Wrong username or password')
    expect(messageFor(new ApiError(502, 'adguard_unavailable', 'x'))).toBe(
      "Can't reach AdGuard Home — try again in a moment",
    )
    expect(messageFor(new ApiError(429, 'rate_limited', 'x', 90))).toBe('Too many attempts — wait 90s')
    expect(messageFor(new ApiError(429, 'rate_limited', 'x'))).toBe('Too many attempts — wait 60s')
  })

  it('falls back to the server message for other codes', () => {
    expect(messageFor(new ApiError(400, 'bad_request', 'malformed body'))).toBe('malformed body')
  })
})

describe('children endpoints', () => {
  it('hit the exact URL and method with the CSRF header', async () => {
    const child = { id: 3, name: 'Ada', clients: ['Kid phone'] }
    const calls = mockFetch(
      json(200, { children: [child] }),
      json(201, child),
      json(200, { ...child, name: 'A', clients: ['x'] }),
      new Response(null, { status: 204 }),
      json(200, { clients: [] }),
      json(200, { services: [] }),
      json(200, { child: { id: 3, name: 'Ada' }, clients: [], services: [] }),
      json(200, { global: [], clients: [] }),
      json(200, { migrated: ['Kid tablet'] }),
    )

    expect(await listChildren()).toEqual([child])
    expect(await createChild('Ada', ['Kid phone'])).toEqual(child)
    expect(await updateChild(3, 'A', ['x'])).toEqual({ ...child, name: 'A', clients: ['x'] })
    expect(await deleteChild(3)).toBeUndefined()
    expect(await listClients()).toEqual([])
    expect(await listServices()).toEqual([])
    expect(await childBlocked(3)).toEqual({ child: { id: 3, name: 'Ada' }, clients: [], services: [] })
    expect(await migrationOffer()).toEqual({ global: [], clients: [] })
    expect(await applyMigration()).toEqual(['Kid tablet'])

    const seen = calls.map((c) => [c.init.method, c.url, c.init.body ?? null])
    expect(seen).toEqual([
      ['GET', '/api/v1/children', null],
      ['POST', '/api/v1/children', '{"name":"Ada","clients":["Kid phone"]}'],
      ['PUT', '/api/v1/children/3', '{"name":"A","clients":["x"]}'],
      ['DELETE', '/api/v1/children/3', null],
      ['GET', '/api/v1/clients', null],
      ['GET', '/api/v1/services', null],
      ['GET', '/api/v1/children/3/blocked', null],
      ['GET', '/api/v1/migration', null],
      ['POST', '/api/v1/migration', null],
    ])
    for (const c of calls) {
      expect(header(c.init, CSRF_HEADER)).toBe(CSRF_VALUE)
    }
  })

  it('401 calls onUnauthorized', async () => {
    mockFetch(json(401, { error: 'unauthorized', message: 'authentication required' }))
    const err = await fails(childBlocked(1))
    expect(err.code).toBe('unauthorized')
    expect(unauthorized).toHaveBeenCalledTimes(1)
  })
})

describe('grants and buttons endpoints', () => {
  it('hit the exact URL, method and body', async () => {
    const grant = {
      id: 4,
      child_id: 1,
      services: ['tiktok'],
      clients: ['Kid phone'],
      started_at: '2026-09-21T10:00:00Z',
      ends_at: '2026-09-21T10:30:00Z',
      shared: [],
    }
    const button = { id: 9, label: 'TikTok 30', child_id: 1, services: ['tiktok'], duration: 1800 }
    const input = { label: 'TikTok 30', child_id: 1, services: ['tiktok'], duration: 1800 }
    const calls = mockFetch(
      json(201, { id: 4, ends_at: grant.ends_at, applied: true, failed: [], shared: [] }),
      json(200, { id: 4, ends_at: '2026-09-21T10:40:00Z' }),
      new Response(null, { status: 204 }),
      json(200, { grants: [grant] }),
      json(200, { buttons: [button] }),
      json(200, { buttons: [button] }),
    )

    expect(await createGrant(1, ['tiktok'], 1800)).toEqual({
      id: 4,
      ends_at: grant.ends_at,
      applied: true,
      failed: [],
      shared: [],
    })
    expect(await extendGrant(4, 600)).toEqual({ id: 4, ends_at: '2026-09-21T10:40:00Z' })
    expect(await endGrant(4)).toBeUndefined()
    expect(await listGrants()).toEqual([grant])
    expect(await listButtons()).toEqual([button])
    expect(await saveButtons([input])).toEqual([button])

    const seen = calls.map((c) => [c.init.method, c.url, c.init.body ?? null])
    expect(seen).toEqual([
      ['POST', '/api/v1/grants', '{"child_id":1,"services":["tiktok"],"duration":1800}'],
      ['POST', '/api/v1/grants/4/extend', '{"duration":600}'],
      ['POST', '/api/v1/grants/4/end', null],
      ['GET', '/api/v1/grants', null],
      ['GET', '/api/v1/buttons', null],
      ['PUT', '/api/v1/buttons', JSON.stringify({ buttons: [input] })],
    ])
    for (const c of calls) {
      expect(header(c.init, CSRF_HEADER)).toBe(CSRF_VALUE)
    }
  })

  it('409 carries grantId', async () => {
    mockFetch(json(409, { error: 'conflict', message: 'tiktok is already granted by grant 7', grant_id: 7 }))
    const err = await fails(createGrant(1, ['tiktok'], 1800))
    expect(err.status).toBe(409)
    expect(err.code).toBe('conflict')
    expect(err.grantId).toBe(7)
    expect(err.message).toBe('tiktok is already granted by grant 7')

    mockFetch(json(409, { error: 'conflict', message: 'a child named Ada already exists' }))
    const plain = await fails(createChild('Ada', []))
    expect(plain.code).toBe('conflict')
    expect(plain.grantId).toBeUndefined()

    mockFetch(json(409, { error: 'conflict', message: 'm', grant_id: '7' }))
    const stringy = await fails(createGrant(1, ['tiktok'], 1800))
    expect(stringy.grantId).toBeUndefined()
  })
})

describe('routing', () => {
  afterEach(() => {
    history.replaceState(null, '', '/')
    route.set('home')
  })

  it('maps the buttons route both ways', () => {
    navigate('buttons')
    expect(location.pathname).toBe('/buttons')
    expect(get(route)).toBe('buttons')

    history.replaceState(null, '', '/buttons')
    window.dispatchEvent(new PopStateEvent('popstate'))
    expect(get(route)).toBe('buttons')

    history.replaceState(null, '', '/x')
    window.dispatchEvent(new PopStateEvent('popstate'))
    expect(get(route)).toBe('home')

    history.replaceState(null, '', '/')
    window.dispatchEvent(new PopStateEvent('popstate'))
    expect(get(route)).toBe('home')
  })

  it('maps the login route both ways', () => {
    navigate('login')
    expect(location.pathname).toBe('/login')
    expect(get(route)).toBe('login')

    history.replaceState(null, '', '/login')
    window.dispatchEvent(new PopStateEvent('popstate'))
    expect(get(route)).toBe('login')

    navigate('home')
    expect(location.pathname).toBe('/')
    expect(get(route)).toBe('home')
  })

  it('navigate to the current path pushes nothing', () => {
    history.replaceState(null, '', '/')
    const push = vi.spyOn(history, 'pushState')
    try {
      navigate('home')
      expect(push).toHaveBeenCalledTimes(0)

      navigate('buttons')
      expect(push).toHaveBeenCalledTimes(1)
      expect(push.mock.calls[0][2]).toBe('/buttons')
      expect(location.pathname).toBe('/buttons')

      navigate('buttons')
      expect(push).toHaveBeenCalledTimes(1)
    } finally {
      push.mockRestore()
    }
  })

  it('route reflects the URL at load', async () => {
    const cases: Array<[string, string]> = [
      ['/', 'home'],
      ['/login', 'login'],
      ['/children', 'children'],
      ['/buttons', 'buttons'],
      ['/nope', 'home'],
    ]
    for (const [path, want] of cases) {
      history.replaceState(null, '', path)
      vi.resetModules()
      const m = await import('./api')
      expect(get(m.route), path).toBe(want)
    }
  })

  it('maps the children route both ways', () => {
    navigate('children')
    expect(location.pathname).toBe('/children')
    expect(get(route)).toBe('children')

    navigate('home')
    expect(location.pathname).toBe('/')
    expect(get(route)).toBe('home')

    history.replaceState(null, '', '/children')
    window.dispatchEvent(new PopStateEvent('popstate'))
    expect(get(route)).toBe('children')

    history.replaceState(null, '', '/x')
    window.dispatchEvent(new PopStateEvent('popstate'))
    expect(get(route)).toBe('home')
  })
})

describe('iconUrl', () => {
  it('prefixes the base64 SVG', () => {
    expect(iconUrl('PHN2Zy8+')).toBe('data:image/svg+xml;base64,PHN2Zy8+')
  })
})

describe('messageFor conflict', () => {
  it('echoes the server message, which names the child or client', () => {
    expect(messageFor(new ApiError(409, 'conflict', 'a child named Ada already exists'))).toBe(
      'a child named Ada already exists',
    )
    expect(messageFor(new ApiError(409, 'conflict', 'client Kid phone is already assigned to Ben'))).toBe(
      'client Kid phone is already assigned to Ben',
    )
  })
})

describe('migrationDismissed', () => {
  afterEach(() => migrationDismissed.set(false))

  it('login resets migrationDismissed', async () => {
    migrationDismissed.set(true)
    mockFetch(new Response(null, { status: 204 }))
    await login('mum', 'pw')
    expect(get(migrationDismissed)).toBe(false)
  })

  it('a failed login leaves it alone', async () => {
    migrationDismissed.set(true)
    mockFetch(json(401, { error: 'bad_credentials', message: 'nope' }))
    await fails(login('mum', 'pw'))
    expect(get(migrationDismissed)).toBe(true)
  })

  it('logout resets migrationDismissed', async () => {
    migrationDismissed.set(true)
    mockFetch(new Response(null, { status: 204 }))
    await logout()
    expect(get(migrationDismissed)).toBe(false)
  })
})

describe('device endpoints', () => {
  const view = { name: 'TV', ids: ['192.168.1.9'], use_global_blocked_services: false, children: [] }

  it('createClient sends name, ids and child_id null with the CSRF header', async () => {
    const calls = mockFetch(json(201, view), json(201, { ...view, children: [{ id: 1, name: 'Ada' }] }))
    expect(await createClient('TV', ['192.168.1.9'], null)).toEqual(view)
    expect(calls[0].url).toBe('/api/v1/clients')
    expect(calls[0].init.method).toBe('POST')
    expect(header(calls[0].init, CSRF_HEADER)).toBe(CSRF_VALUE)
    expect(header(calls[0].init, 'Content-Type')).toBe('application/json')
    expect(calls[0].init.body).toBe('{"name":"TV","ids":["192.168.1.9"],"child_id":null}')

    await createClient('TV', ['192.168.1.9'], 1)
    expect(JSON.parse(calls[1].init.body as string)).toEqual({ name: 'TV', ids: ['192.168.1.9'], child_id: 1 })
  })

  it('createClient 409 surfaces the server message', async () => {
    mockFetch(json(409, { error: 'conflict', message: 'a client named "TV" already exists' }))
    const e = await fails(createClient('TV', ['192.168.1.9'], null))
    expect(e.status).toBe(409)
    expect(messageFor(e)).toBe('a client named "TV" already exists')
  })

  it('listDevices returns clients and unlabelled from one GET', async () => {
    const dev = { ip: '192.168.1.70', hostname: 'printer', source: 'ARP', suggested_ids: ['192.168.1.70'] }
    const calls = mockFetch(json(200, { clients: [view], unlabelled: [dev] }))
    expect(await listDevices()).toEqual({ clients: [view], unlabelled: [dev] })
    expect(calls).toHaveLength(1)
    expect(calls[0].url).toBe('/api/v1/clients')
    expect(calls[0].init.method).toBe('GET')
    expect(header(calls[0].init, CSRF_HEADER)).toBe(CSRF_VALUE)
  })

  it('listDevices defaults a missing key to []', async () => {
    mockFetch(json(200, { clients: [view] }), json(200, {}))
    expect(await listDevices()).toEqual({ clients: [view], unlabelled: [] })
    expect(await listDevices()).toEqual({ clients: [], unlabelled: [] })
  })

  it('listClients still returns the array', async () => {
    mockFetch(json(200, { clients: [view], unlabelled: [] }))
    expect(await listClients()).toEqual([view])
  })

  it('renameClient sends from and to in the body to the fixed path', async () => {
    const calls = mockFetch(json(200, { ...view, name: 'x' }))
    expect(await renameClient('a/b%c', 'x')).toEqual({ ...view, name: 'x' })
    expect(calls[0].url).toBe('/api/v1/clients/rename')
    expect(calls[0].init.method).toBe('POST')
    expect(header(calls[0].init, CSRF_HEADER)).toBe(CSRF_VALUE)
    expect(JSON.parse(calls[0].init.body as string)).toEqual({ from: 'a/b%c', to: 'x' })
  })
})

describe('shared-device fields default to []', () => {
  const kidPhone = { client: 'Kid phone', others: [{ id: 2, name: 'Ben' }] }
  const row = {
    id: 4,
    child_id: 1,
    services: ['youtube'],
    clients: ['Kid phone'],
    started_at: '2026-09-21T10:00:00Z',
    ends_at: '2026-09-21T10:30:00Z',
  }

  it('listGrants fills a missing shared and keeps a present one', async () => {
    mockFetch(json(200, { grants: [row, { ...row, id: 5, shared: [kidPhone] }] }))
    const got = await listGrants()
    expect(got[0].shared).toEqual([])
    expect(got[1].shared).toEqual([kidPhone])
  })

  it('createGrant fills a missing shared and keeps a present one', async () => {
    mockFetch(
      json(201, { id: 4, ends_at: row.ends_at, applied: true, failed: [] }),
      json(201, { id: 5, ends_at: row.ends_at, applied: false, failed: ['Kid tablet'], shared: [kidPhone] }),
    )
    expect((await createGrant(1, ['youtube'], 1800)).shared).toEqual([])
    const g = await createGrant(1, ['youtube'], 1800)
    expect(g.shared).toEqual([kidPhone])
    expect(g.failed).toEqual(['Kid tablet'])
  })

  it('childBlocked fills shared_with and unblocked_by', async () => {
    const svc = { id: 'youtube', name: 'YouTube', icon: '', state: 'unblocked', differs: [] }
    const by = { client: 'Kid phone', grant_id: 7, child: { id: 1, name: 'Ada' } }
    mockFetch(
      json(200, {
        child: { id: 2, name: 'Ben' },
        clients: [
          { name: 'Kid phone', missing: false, uses_global: false },
          { name: 'TV', missing: false, uses_global: false, shared_with: [{ id: 1, name: 'Ada' }] },
        ],
        services: [svc, { ...svc, id: 'tiktok', unblocked_by: [by] }],
      }),
    )
    const v = await childBlocked(2)
    expect(v.child).toEqual({ id: 2, name: 'Ben' })
    expect(v.clients.map((c) => c.shared_with)).toEqual([[], [{ id: 1, name: 'Ada' }]])
    expect(v.services.map((s) => s.unblocked_by)).toEqual([[], [{ ...by, permanent: false }]])
    expect(v.services[0].state).toBe('unblocked')
  })

  it('listDevices fills a missing children and keeps a present one', async () => {
    const bare = { name: 'TV', ids: [], use_global_blocked_services: false }
    const owned = { ...bare, name: 'Kid phone', children: [{ id: 1, name: 'Ada' }, { id: 2, name: 'Ben' }] }
    mockFetch(json(200, { clients: [bare, owned], unlabelled: [] }))
    const got = await listDevices()
    expect(got.clients[0]).toStrictEqual({ ...bare, children: [] })
    expect(got.clients[1]).toStrictEqual(owned)
    for (const c of got.clients) expect(c).not.toHaveProperty('child')
  })

  it('listClients, createClient and renameClient fill a missing children', async () => {
    const bare = { name: 'TV', ids: [], use_global_blocked_services: false }
    mockFetch(json(200, { clients: [bare] }), json(201, bare), json(200, bare))
    expect((await listClients())[0].children).toEqual([])
    expect((await createClient('TV', [], null)).children).toEqual([])
    expect((await renameClient('A', 'TV')).children).toEqual([])
  })
})

describe('custom services', () => {
  const portal = { id: 3, service_id: 'custom:3', name: 'Portal', domains: ['a.com'], applied: true }

  it('hit the exact URL, method and body with the CSRF header', async () => {
    const calls = mockFetch(
      json(200, { custom_services: [portal] }),
      json(201, portal),
      json(200, { ...portal, name: 'Portal 2', domains: ['b.com'] }),
      new Response(null, { status: 204 }),
    )
    expect(await listCustomServices()).toEqual([portal])
    expect(await createCustomService('Portal', ['a.com'])).toEqual(portal)
    expect((await updateCustomService(3, 'Portal 2', ['b.com'])).domains).toEqual(['b.com'])
    expect(await deleteCustomService(3)).toBeUndefined()

    expect(calls.map((c) => [c.init.method, c.url, c.init.body ?? null])).toEqual([
      ['GET', '/api/v1/custom-services', null],
      ['POST', '/api/v1/custom-services', JSON.stringify({ name: 'Portal', domains: ['a.com'] })],
      ['PUT', '/api/v1/custom-services/3', JSON.stringify({ name: 'Portal 2', domains: ['b.com'] })],
      ['DELETE', '/api/v1/custom-services/3', null],
    ])
    for (const c of calls) {
      expect(header(c.init, CSRF_HEADER)).toBe(CSRF_VALUE)
    }
  })

  it('custom service errors', async () => {
    mockFetch(json(400, { error: 'invalid_request', message: 'domains[1]: domain "a.com/x": not a plain hostname' }))
    const bad = await fails(createCustomService('Portal', ['ok.com', 'a.com/x']))
    expect(bad.status).toBe(400)
    expect(messageFor(bad)).toContain('a.com/x')

    mockFetch(json(409, { error: 'conflict', message: 'Portal is in use by active grant 7', grant_id: 7 }))
    const inUse = await fails(deleteCustomService(3))
    expect(inUse.status).toBe(409)
    expect(inUse.grantId).toBe(7)
    expect(messageFor(inUse)).toBe('Portal is in use by active grant 7')
  })

  it('listServices and childBlocked fill a missing custom and keep a present one', async () => {
    mockFetch(
      json(200, {
        services: [
          { id: 'youtube', name: 'YouTube', icon: 'x' },
          { id: 'custom:1', name: 'Portal', icon: '', custom: true },
        ],
      }),
      json(200, {
        child: { id: 2, name: 'Ada' },
        clients: [],
        services: [
          { id: 'youtube', name: 'YouTube', icon: 'x', state: 'blocked', differs: [] },
          { id: 'custom:1', name: 'Portal', icon: '', custom: true, state: 'partial', differs: ['tv'] },
        ],
      }),
    )
    expect((await listServices()).map((s) => s.custom)).toEqual([false, true])
    expect((await childBlocked(2)).services.map((s) => s.custom)).toEqual([false, true])
  })
})

describe('services route', () => {
  afterEach(() => {
    history.replaceState(null, '', '/')
    route.set('home')
  })

  it('maps the services route both ways', () => {
    navigate('services')
    expect(location.pathname).toBe('/services')
    expect(get(route)).toBe('services')

    history.replaceState(null, '', '/')
    window.dispatchEvent(new PopStateEvent('popstate'))
    expect(get(route)).toBe('home')

    history.replaceState(null, '', '/services')
    window.dispatchEvent(new PopStateEvent('popstate'))
    expect(get(route)).toBe('services')
  })
})

describe('permanent toggles', () => {
  const kidPhone = { client: 'Kid phone', others: [{ id: 2, name: 'Ben' }] }

  it('toggle service', async () => {
    const full = {
      service_id: 'custom:1',
      state: 'blocked',
      applied: false,
      failed: ['Kid tablet'],
      shared: [kidPhone],
      grant: { id: 7, ended: true },
    }
    const calls = mockFetch(json(200, full))
    expect(await toggleService(3, 'custom:1', 'blocked')).toEqual(full)
    expect(calls).toHaveLength(1)
    expect(calls[0].url).toBe('/api/v1/children/3/toggles')
    expect(calls[0].init.method).toBe('POST')
    expect(header(calls[0].init, CSRF_HEADER)).toBe(CSRF_VALUE)
    expect(header(calls[0].init, 'Content-Type')).toBe('application/json')
    expect(JSON.parse(calls[0].init.body as string)).toStrictEqual({ service_id: 'custom:1', state: 'blocked' })
  })

  it('toggle result defaults', async () => {
    mockFetch(
      json(200, { service_id: 'youtube', state: 'unblocked', applied: true }),
      json(200, {
        child: { id: 3, name: 'Ada' },
        clients: [],
        services: [
          {
            id: 'youtube',
            name: 'YouTube',
            icon: 'x',
            state: 'unblocked',
            differs: [],
            unblocked_by: [{ client: 'TV', grant_id: 5, child: { id: 2, name: 'Ben' } }],
          },
          {
            id: 'tiktok',
            name: 'TikTok',
            icon: 'x',
            state: 'unblocked',
            differs: [],
            toggle: 'unblocked',
            unblocked_by: [{ client: 'TV', grant_id: 0, child: { id: 2, name: 'Ben' }, permanent: true }],
          },
        ],
      }),
    )
    expect(await toggleService(3, 'youtube', 'unblocked')).toStrictEqual({
      service_id: 'youtube',
      state: 'unblocked',
      applied: true,
      failed: [],
      shared: [],
      grant: null,
    })
    const v = await childBlocked(3)
    expect(v.services.map((s) => s.toggle)).toStrictEqual([null, 'unblocked'])
    expect(v.services.map((s) => s.unblocked_by?.map((u) => u.permanent))).toStrictEqual([[false], [true]])
  })

  it('toggle errors', async () => {
    mockFetch(json(422, { error: 'unprocessable', message: 'unknown service "nope"' }))
    const bad = await fails(toggleService(3, 'nope', 'blocked'))
    expect(bad.status).toBe(422)
    expect(bad.code).toBe('unprocessable')
    expect(messageFor(bad)).toBe('unknown service "nope"')

    mockFetch(json(404, { error: 'not_found', message: 'child 99 not found' }))
    const gone = await fails(toggleService(99, 'youtube', 'unblocked'))
    expect(gone.status).toBe(404)
    expect(gone.code).toBe('not_found')

    mockFetch(json(502, { error: 'adguard_unavailable', message: 'upstream down' }))
    const down = await fails(toggleService(3, 'youtube', 'blocked'))
    expect(down.code).toBe('adguard_unavailable')
    expect(unauthorized).not.toHaveBeenCalled()
  })
})

describe('history route', () => {
  const noFilters: HistoryFilters = { service: null, status: null, from: null, to: null }

  afterEach(() => {
    history.replaceState(null, '', '/')
    route.set('home')
    historyChild.set(null)
    historyFilters.set({ ...noFilters, badChild: false })
  })

  it('navigate pushes the child and drops it on the next route', () => {
    history.replaceState(null, '', '/')
    const push = vi.spyOn(history, 'pushState')
    try {
      navigate('history', { child: 3 })
      expect(push).toHaveBeenCalledTimes(1)
      expect(push.mock.calls[0][2]).toBe('/history?child=3')
      expect(location.pathname + location.search).toBe('/history?child=3')
      expect(get(route)).toBe('history')
      expect(get(historyChild)).toBe(3)

      // Same page, same child: nothing pushed.
      navigate('history', { child: 3 })
      expect(push).toHaveBeenCalledTimes(1)

      navigate('history', { child: 5 })
      expect(push).toHaveBeenCalledTimes(2)
      expect(push.mock.calls[1][2]).toBe('/history?child=5')
      expect(get(historyChild)).toBe(5)

      navigate('home')
      expect(push).toHaveBeenCalledTimes(3)
      expect(push.mock.calls[2][2]).toBe('/')
      expect(location.pathname).toBe('/')
      expect(location.search).toBe('')
      expect(get(route)).toBe('home')
      expect(get(historyChild)).toBeNull()
    } finally {
      push.mockRestore()
    }
  })

  it('navigate ignores an invalid or foreign child param', () => {
    for (const child of [0, -1, 3.5, Number.NaN, null, undefined]) {
      navigate('history', { child })
      expect(location.pathname + location.search, String(child)).toBe('/history')
      expect(get(route)).toBe('history')
      expect(get(historyChild), String(child)).toBeNull()
      expect(get(historyFilters).badChild, String(child)).toBe(false)
    }

    navigate('children', { child: 3 })
    expect(location.pathname + location.search).toBe('/children')
    expect(get(historyChild)).toBeNull()
  })

  it('navigate pushes every filter once, in the fixed order', () => {
    const filters: HistoryFilters = { service: 'youtube', status: 'expired', from: '2026-10-01', to: '2026-10-31' }
    const url = '/history?child=3&service=youtube&status=expired&from=2026-10-01&to=2026-10-31'
    history.replaceState(null, '', '/')
    const push = vi.spyOn(history, 'pushState')
    try {
      navigate('history', { child: 3, filters })
      expect(push).toHaveBeenCalledTimes(1)
      expect(push.mock.calls[0][2]).toBe(url)
      expect(location.pathname + location.search).toBe(url)
      expect(get(route)).toBe('history')
      expect(get(historyChild)).toBe(3)
      expect(get(historyFilters)).toStrictEqual({ ...filters, badChild: false })

      // Same page, same filters: nothing pushed, and no store wakes the screen.
      const heard = vi.fn()
      const stop = [route.subscribe(heard), historyChild.subscribe(heard), historyFilters.subscribe(heard)]
      heard.mockClear()
      navigate('history', { child: 3, filters: { ...filters } })
      expect(push).toHaveBeenCalledTimes(1)
      expect(heard).not.toHaveBeenCalled()
      stop.forEach((unsubscribe) => unsubscribe())

      navigate('home')
      expect(push).toHaveBeenCalledTimes(2)
      expect(push.mock.calls[1][2]).toBe('/')
      expect(location.pathname + location.search).toBe('/')
      expect(get(historyChild)).toBeNull()
      expect(get(historyFilters)).toStrictEqual({ ...noFilters, badChild: false })

      navigate('history', { child: 3, filters })
      navigate('history', { child: 2 })
      expect(push).toHaveBeenCalledTimes(4)
      expect(push.mock.calls[3][2]).toBe('/history?child=2')
      expect(get(historyFilters)).toStrictEqual({ ...noFilters, badChild: false })

      navigate('children', { filters: { service: 'x', status: null, from: null, to: null } })
      expect(push).toHaveBeenCalledTimes(5)
      expect(push.mock.calls[4][2]).toBe('/children')
      expect(location.pathname + location.search).toBe('/children')
      expect(get(historyFilters)).toStrictEqual({ ...noFilters, badChild: false })
    } finally {
      push.mockRestore()
    }
  })

  it('navigate drops the filters a reload of the URL would drop', async () => {
    const bad: HistoryFilters = { service: null, status: 'bogus' as HistoryStatus, from: null, to: '9999-12-31' }
    navigate('history', { filters: bad })
    expect(location.pathname + location.search).toBe('/history')
    expect(get(historyFilters)).toStrictEqual({ ...noFilters, badChild: false })

    history.replaceState(null, '', '/history?status=bogus&to=9999-12-31')
    vi.resetModules()
    const reloaded = await import('./api')
    expect(get(reloaded.historyFilters)).toStrictEqual({ ...noFilters, badChild: false })

    // One bad value never costs the good ones beside it, and an empty service is no filter.
    navigate('history', { filters: { service: '', status: 'expired', from: '2026-02-31', to: '2026-03-01' } })
    expect(location.pathname + location.search).toBe('/history?status=expired&to=2026-03-01')
    expect(get(historyFilters)).toStrictEqual({ ...noFilters, status: 'expired', to: '2026-03-01', badChild: false })
  })

  it('navigate leaves a bad child behind', () => {
    history.replaceState(null, '', '/history?child=abc')
    window.dispatchEvent(new PopStateEvent('popstate'))
    expect(get(historyFilters).badChild).toBe(true)

    navigate('history')
    expect(location.pathname + location.search).toBe('/history')
    expect(get(historyFilters).badChild).toBe(false)
  })

  it('a filter value survives the URL it is pushed to', async () => {
    const filters: HistoryFilters = { ...noFilters, service: 'a&b=c +' }
    navigate('history', { filters })
    expect(location.search).toBe('?service=a%26b%3Dc+%2B')
    vi.resetModules()
    const reloaded = await import('./api')
    expect(get(reloaded.historyFilters)).toStrictEqual({ ...filters, badChild: false })

    navigate('history', { filters: { ...noFilters, service: 'custom:5' } })
    expect(location.search).toBe('?service=custom%3A5')
  })

  // The first entry names no child at all; the rest name one that cannot be.
  const garbage = [
    '/history',
    '/history?child=',
    '/history?child=abc',
    '/history?child=0',
    '/history?child=-1',
    '/history?child=3.5',
    '/history?child=1e2',
    '/history?child=%203',
    '/history?child=9007199254740993',
  ]

  it('routeFor parses the child at load and rejects garbage', async () => {
    for (const url of garbage) {
      history.replaceState(null, '', url)
      vi.resetModules()
      const m = await import('./api')
      expect(get(m.route), url).toBe('history')
      expect(get(m.historyChild), url).toBeNull()
      expect(get(m.historyFilters).badChild, url).toBe(url !== '/history')
    }

    history.replaceState(null, '', '/history?child=7')
    vi.resetModules()
    const ok = await import('./api')
    expect(get(ok.route)).toBe('history')
    expect(get(ok.historyChild)).toBe(7)
    expect(get(ok.historyFilters).badChild).toBe(false)

    // Multi-digit ids parse whole, zeros after the first digit included.
    for (const [url, want] of [
      ['/history?child=12', 12],
      ['/history?child=105', 105],
    ] as const) {
      history.replaceState(null, '', url)
      vi.resetModules()
      const m = await import('./api')
      expect(get(m.historyChild), url).toBe(want)
    }

    // A child query on another route never leaks into historyChild.
    history.replaceState(null, '', '/children?child=7')
    vi.resetModules()
    const other = await import('./api')
    expect(get(other.route)).toBe('children')
    expect(get(other.historyChild)).toBeNull()

    // Nor does any other route's query reach the filters.
    history.replaceState(null, '', '/children?child=abc&status=expired')
    vi.resetModules()
    const foreign = await import('./api')
    expect(get(foreign.historyFilters)).toStrictEqual({ ...noFilters, badChild: false })
  })

  it('the filters are read from the URL at load', async () => {
    history.replaceState(null, '', '/history?to=2026-10-31&from=2026-10-01&status=expired&service=custom:5&child=3')
    vi.resetModules()
    const m = await import('./api')
    expect(get(m.historyChild)).toBe(3)
    expect(get(m.historyFilters)).toStrictEqual({
      service: 'custom:5',
      status: 'expired',
      from: '2026-10-01',
      to: '2026-10-31',
      badChild: false,
    })
  })

  it('popstate re-parses the child', () => {
    history.replaceState(null, '', '/history?child=3')
    window.dispatchEvent(new PopStateEvent('popstate'))
    expect(get(route)).toBe('history')
    expect(get(historyChild)).toBe(3)

    history.replaceState(null, '', '/history?child=5')
    window.dispatchEvent(new PopStateEvent('popstate'))
    expect(get(route)).toBe('history')
    expect(get(historyChild)).toBe(5)

    history.replaceState(null, '', '/history?child=40')
    window.dispatchEvent(new PopStateEvent('popstate'))
    expect(get(historyChild)).toBe(40)

    for (const url of garbage) {
      historyChild.set(9)
      history.replaceState(null, '', url)
      window.dispatchEvent(new PopStateEvent('popstate'))
      expect(get(route), url).toBe('history')
      expect(get(historyChild), url).toBeNull()
      expect(get(historyFilters).badChild, url).toBe(url !== '/history')
    }

    history.replaceState(null, '', '/history?child=5')
    window.dispatchEvent(new PopStateEvent('popstate'))
    history.replaceState(null, '', '/?child=5')
    window.dispatchEvent(new PopStateEvent('popstate'))
    expect(get(route)).toBe('home')
    expect(get(historyChild)).toBeNull()
  })

  it('going back restores every filter, not just the child', async () => {
    const older: HistoryFilters = { ...noFilters, status: 'ongoing', from: '2026-03-01' }
    navigate('history', { filters: older })
    navigate('history', { filters: { ...noFilters, status: 'expired' } })
    expect(location.search).toBe('?status=expired')

    const popped = new Promise((resolve) => window.addEventListener('popstate', resolve, { once: true }))
    history.back()
    await popped

    expect(location.search).toBe('?status=ongoing&from=2026-03-01')
    expect(get(historyFilters)).toStrictEqual({ ...older, badChild: false })
  })

  it('popstate clears filters the URL no longer names', () => {
    history.replaceState(null, '', '/history?service=youtube&to=2026-10-31')
    window.dispatchEvent(new PopStateEvent('popstate'))
    expect(get(historyFilters)).toStrictEqual({ ...noFilters, service: 'youtube', to: '2026-10-31', badChild: false })

    history.replaceState(null, '', '/children?service=youtube')
    window.dispatchEvent(new PopStateEvent('popstate'))
    expect(get(route)).toBe('children')
    expect(get(historyFilters)).toStrictEqual({ ...noFilters, badChild: false })
  })

  it('popstate to the same filters does not wake the screen', () => {
    history.replaceState(null, '', '/history?status=expired')
    window.dispatchEvent(new PopStateEvent('popstate'))

    const heard = vi.fn()
    const stop = historyFilters.subscribe(heard)
    heard.mockClear()
    window.dispatchEvent(new PopStateEvent('popstate'))
    expect(heard).not.toHaveBeenCalled()
    stop()
  })
})

describe('fetchHistoryPage', () => {
  const noFilters: HistoryFilters = { service: null, status: null, from: null, to: null }
  const ada = { id: 3, name: 'Ada' }
  const row = {
    id: 41,
    status: 'ended_early',
    started_at: '2026-09-21T10:00:00Z',
    ends_at: '2026-09-21T10:40:00Z',
    ended_at: '2026-09-21T10:20:00Z',
    services: [{ id: 'tiktok', name: 'TikTok', removed_at: null, removed_reason: null }],
    extensions: [{ at: '2026-09-21T10:05:00Z', old_ends_at: '2026-09-21T10:30:00Z', new_ends_at: '2026-09-21T10:40:00Z' }],
    child: ada,
  }

  it('builds the URL the server reads, days as instants and until past the last day', async () => {
    const calls = mockFetch(json(200, { child: ada, grants: [row], next_before: null }))
    const filters: HistoryFilters = { service: 'custom:5', status: 'expired', from: '2026-03-08', to: '2026-03-08' }
    await fetchHistoryPage(3, filters, 40)
    expect(calls).toHaveLength(1)
    expect(calls[0].url).toBe(
      '/api/v1/history?child=3&service=custom%3A5&status=expired&from=2026-03-08T05%3A00%3A00.000Z&until=2026-03-09T04%3A00%3A00.000Z&before=40',
    )
    expect(calls[0].init.method).toBe('GET')
    expect(calls[0].init.body ?? null).toBeNull()
    expect(header(calls[0].init, CSRF_HEADER)).toBe(CSRF_VALUE)
  })

  it('sends only what is set', async () => {
    const empty = () => json(200, { child: null, grants: [], next_before: null })
    const calls = mockFetch(empty(), empty(), empty(), empty())
    await fetchHistoryPage(null, noFilters)
    await fetchHistoryPage(null, noFilters, 40)
    await fetchHistoryPage(null, { ...noFilters, from: '2026-03-01' })
    await fetchHistoryPage(3, { ...noFilters, to: '2026-03-01' })
    expect(calls.map((c) => c.url)).toEqual([
      '/api/v1/history',
      '/api/v1/history?before=40',
      '/api/v1/history?from=2026-03-01T05%3A00%3A00.000Z',
      '/api/v1/history?child=3&until=2026-03-02T05%3A00%3A00.000Z',
    ])
  })

  it('keeps each row and its child', async () => {
    const page = { child: null, grants: [row], next_before: 41 }
    mockFetch(json(200, page))
    expect(await fetchHistoryPage(null, noFilters)).toStrictEqual(page)
  })

  it('fills missing lists, page child and cursor', async () => {
    mockFetch(
      json(200, { child: null, grants: [{ ...row, services: null, extensions: undefined }] }),
      json(200, { grants: null }),
    )
    const got = await fetchHistoryPage(null, noFilters)
    expect(got.grants[0].services).toStrictEqual([])
    expect(got.grants[0].extensions).toStrictEqual([])
    expect(got.grants[0].child).toStrictEqual(ada)
    expect(got.next_before).toBeNull()

    expect(await fetchHistoryPage(null, noFilters)).toStrictEqual({ child: null, grants: [], next_before: null })
  })

  it('rejects with ApiError', async () => {
    mockFetch(json(404, { error: 'not_found', message: 'child 99 not found' }))
    const gone = await fails(fetchHistoryPage(99, noFilters))
    expect(gone.status).toBe(404)
    expect(gone.code).toBe('not_found')
    expect(unauthorized).not.toHaveBeenCalled()
  })
})

describe('fetchHistoryServices', () => {
  it('reads the options', async () => {
    const services = [{ id: 'custom:1', name: 'Roblox' }]
    const calls = mockFetch(json(200, { services }))
    expect(await fetchHistoryServices()).toStrictEqual(services)
    expect(calls.map((c) => [c.init.method, c.url])).toEqual([['GET', '/api/v1/history/services']])
  })

  it('fills a missing list', async () => {
    mockFetch(json(200, { services: null }))
    expect(await fetchHistoryServices()).toStrictEqual([])
  })
})

describe('fetchHistory', () => {
  const page = {
    child: { id: 3, name: 'Ada' },
    grants: [
      {
        id: 41,
        status: 'ended_early',
        started_at: '2026-09-21T10:00:00Z',
        ends_at: '2026-09-21T10:40:00Z',
        ended_at: '2026-09-21T10:20:00Z',
        services: [
          { id: 'tiktok', name: 'TikTok', removed_at: null, removed_reason: null },
          { id: 'youtube', name: 'YouTube', removed_at: '2026-09-21T10:10:00Z', removed_reason: 'permanent_block' },
        ],
        extensions: [
          { at: '2026-09-21T10:05:00Z', old_ends_at: '2026-09-21T10:30:00Z', new_ends_at: '2026-09-21T10:35:00Z' },
          { at: '2026-09-21T10:06:00Z', old_ends_at: '2026-09-21T10:35:00Z', new_ends_at: '2026-09-21T10:40:00Z' },
        ],
      },
    ],
    next_before: 41,
  }

  it('builds the URL with and without before', async () => {
    const last = { child: page.child, grants: [], next_before: null }
    const calls = mockFetch(json(200, page), json(200, last))
    expect(await fetchHistory(3)).toStrictEqual(page)
    expect(await fetchHistory(3, 40)).toStrictEqual(last)
    expect(calls.map((c) => [c.init.method, c.url, c.init.body ?? null])).toEqual([
      ['GET', '/api/v1/children/3/history', null],
      ['GET', '/api/v1/children/3/history?before=40', null],
    ])
    for (const c of calls) {
      expect(header(c.init, CSRF_HEADER)).toBe(CSRF_VALUE)
    }
  })

  it('fills missing lists and cursor', async () => {
    const bare = { ...page.grants[0], services: null, extensions: undefined }
    mockFetch(json(200, { child: page.child, grants: [bare] }), json(200, { child: page.child, grants: null }))
    const got = await fetchHistory(3)
    expect(got.grants[0].services).toStrictEqual([])
    expect(got.grants[0].extensions).toStrictEqual([])
    expect(got.next_before).toBeNull()
    expect((await fetchHistory(3)).grants).toStrictEqual([])
  })

  it('rejects with ApiError', async () => {
    mockFetch(json(404, { error: 'not_found', message: 'child 3 not found' }))
    const gone = await fails(fetchHistory(3))
    expect(gone.status).toBe(404)
    expect(gone.code).toBe('not_found')
    expect(unauthorized).not.toHaveBeenCalled()

    mockFetch(json(400, { error: 'invalid_request', message: 'bad before' }))
    const bad = await fails(fetchHistory(3, 40))
    expect(bad.status).toBe(400)

    mockFetch(json(401, { error: 'unauthorized', message: 'authentication required' }))
    const anon = await fails(fetchHistory(3))
    expect(anon.status).toBe(401)
    expect(unauthorized).toHaveBeenCalledTimes(1)
  })
})

describe('PIN lock routing', () => {
  afterEach(() => {
    history.replaceState(null, '', '/')
    route.set('home')
    locked.set(false)
  })

  it('a fresh load starts unlocked', async () => {
    vi.resetModules()
    const m = await import('./api')
    expect(get(m.locked)).toBe(false)
  })

  it('pin_required locks instead of signing out', async () => {
    const calls = mockFetch(json(401, { error: 'pin_required', message: 'enter the PIN' }))
    const err = await fails(listChildren())
    expect(calls[0].init.method).toBe('GET')
    expect(calls[0].url).toBe('/api/v1/children')
    expect(err.status).toBe(401)
    expect(err.code).toBe('pin_required')
    expect(pinRequired).toHaveBeenCalledTimes(1)
    expect(unauthorized).not.toHaveBeenCalled()

    // Any other 401 code is still signed-out.
    mockFetch(json(401, { error: 'unauthorized', message: 'authentication required' }))
    await fails(listChildren())
    expect(unauthorized).toHaveBeenCalledTimes(1)
    expect(pinRequired).toHaveBeenCalledTimes(1)
  })

  it('unlock errors are the caller\'s, never a global handler\'s', async () => {
    const calls = mockFetch(json(401, { error: 'bad_pin', message: 'wrong PIN', attempts_remaining: 3 }))
    const bad = await fails(unlock('1111'))
    expect(calls[0].url).toBe('/api/v1/unlock')
    expect(calls[0].init.method).toBe('POST')
    expect(bad.status).toBe(401)
    expect(bad.code).toBe('bad_pin')
    expect(bad.attemptsRemaining).toBe(3)

    // Zero is a real count; only a whole, non-negative number is.
    mockFetch(json(401, { error: 'bad_pin', message: 'wrong PIN', attempts_remaining: 0 }))
    expect((await fails(unlock('1111'))).attemptsRemaining).toBe(0)
    for (const left of [2.5, '3', null, -1, true]) {
      mockFetch(json(401, { error: 'bad_pin', message: 'wrong PIN', attempts_remaining: left }))
      expect((await fails(unlock('1111'))).attemptsRemaining, String(left)).toBeUndefined()
    }
    mockFetch(json(401, { error: 'bad_pin', message: 'wrong PIN' }))
    expect((await fails(unlock('1111'))).attemptsRemaining).toBeUndefined()

    for (const code of ['device_revoked', 'unauthorized', 'pin_required']) {
      mockFetch(json(401, { error: code, message: 'm' }))
      expect((await fails(unlock('1111'))).code).toBe(code)
    }

    // The login route is exempt the same way, pin_required included.
    mockFetch(json(401, { error: 'pin_required', message: 'm' }))
    await fails(login('a', 'b'))

    expect(unauthorized).not.toHaveBeenCalled()
    expect(pinRequired).not.toHaveBeenCalled()
  })

  it('attemptsRemaining is read on any error, absent otherwise', async () => {
    mockFetch(json(409, { error: 'conflict', message: 'm' }))
    expect((await fails(listChildren())).attemptsRemaining).toBeUndefined()
    expect(new ApiError(401, 'bad_pin', 'm').attemptsRemaining).toBeUndefined()
    expect(new ApiError(401, 'bad_pin', 'm', undefined, undefined, 2).attemptsRemaining).toBe(2)
  })

  it('a failed password re-check keeps the parent signed in', async () => {
    for (const code of ['bad_credentials', 'reauth_required']) {
      mockFetch(json(403, { error: code, message: 'm' }))
      const change = await fails(changePin('4567', 'wrong'))
      expect(change.status).toBe(403)
      expect(change.code).toBe(code)

      mockFetch(json(403, { error: code, message: 'm' }))
      const rm = await fails(removePin('wrong'))
      expect(rm.status).toBe(403)
      expect(rm.code).toBe(code)
    }
    expect(unauthorized).not.toHaveBeenCalled()
    expect(pinRequired).not.toHaveBeenCalled()
  })

  it('the default handlers lock in place and sign out', async () => {
    history.replaceState(null, '', '/children')
    vi.resetModules()
    const m = await import('./api')
    expect(get(m.route)).toBe('children')

    mockFetch(json(401, { error: 'pin_required', message: 'm' }))
    await expect(m.listChildren()).rejects.toBeInstanceOf(m.ApiError)
    expect(get(m.locked)).toBe(true)
    expect(get(m.route)).toBe('children')
    expect(location.pathname).toBe('/children')

    // A plain 401 while locked drops the lock and shows the login page.
    mockFetch(json(401, { error: 'unauthorized', message: 'm' }))
    await expect(m.listChildren()).rejects.toBeInstanceOf(m.ApiError)
    expect(get(m.locked)).toBe(false)
    expect(get(m.route)).toBe('login')
    expect(location.pathname).toBe('/login')
  })
})

describe('PIN endpoints', () => {
  it('send the wire shape with the CSRF header', async () => {
    const calls = mockFetch(
      new Response(null, { status: 204 }),
      new Response(null, { status: 204 }),
      new Response(null, { status: 204 }),
      new Response(null, { status: 204 }),
      json(200, { has_pin: true }),
    )
    expect(await setPin('0123')).toBeUndefined()
    expect(await changePin('4567', 'pw')).toBeUndefined()
    expect(await removePin('pw')).toBeUndefined()
    expect(await unlock('1234')).toBeUndefined()
    expect(await pinStatus()).toEqual({ has_pin: true })

    const seen = calls.map((c) => [c.init.method, c.url, c.init.body ?? null])
    expect(seen).toEqual([
      ['POST', '/api/v1/pin', '{"pin":"0123"}'],
      ['POST', '/api/v1/pin', '{"pin":"4567","password":"pw"}'],
      ['DELETE', '/api/v1/pin', '{"password":"pw"}'],
      ['POST', '/api/v1/unlock', '{"pin":"1234"}'],
      ['GET', '/api/v1/pin', null],
    ])
    for (const c of calls) {
      expect(header(c.init, CSRF_HEADER)).toBe(CSRF_VALUE)
      expect(c.init.credentials).toBe('same-origin')
    }
    for (const c of calls.slice(0, 4)) expect(header(c.init, 'Content-Type')).toBe('application/json')
    // A first PIN never carries a password, not even an empty one.
    expect(JSON.parse(calls[0].init.body as string)).not.toHaveProperty('password')
  })
})

describe('settings route', () => {
  afterEach(() => {
    history.replaceState(null, '', '/')
    route.set('home')
  })

  it('maps the settings route both ways', () => {
    expect(routeFor('/settings')).toBe('settings')
    expect(routeFor('/settings/x')).toBe('home')

    const push = vi.spyOn(history, 'pushState')
    try {
      navigate('settings')
      expect(push).toHaveBeenCalledWith(null, '', '/settings')
    } finally {
      push.mockRestore()
    }
    expect(location.pathname).toBe('/settings')
    expect(get(route)).toBe('settings')

    history.replaceState(null, '', '/')
    window.dispatchEvent(new PopStateEvent('popstate'))
    expect(get(route)).toBe('home')

    history.replaceState(null, '', '/settings')
    window.dispatchEvent(new PopStateEvent('popstate'))
    expect(get(route)).toBe('settings')
  })
})

describe('messageFor PIN codes', () => {
  it('gives the fixed line per code', () => {
    expect(messageFor(new ApiError(401, 'bad_pin', 'x', undefined, undefined, 3))).toBe('Wrong PIN — 3 attempts left')
    expect(messageFor(new ApiError(401, 'bad_pin', 'x', undefined, undefined, 1))).toBe('Wrong PIN — 1 attempt left')
    expect(messageFor(new ApiError(401, 'bad_pin', 'x'))).toBe('Wrong PIN')
    expect(messageFor(new ApiError(401, 'device_revoked', 'x'))).toBe(
      'This device was signed out — sign in with your password',
    )
    expect(messageFor(new ApiError(403, 'reauth_required', 'x'))).toBe('Enter your AdGuard password')
    expect(messageFor(new ApiError(403, 'bad_credentials', 'x'))).toBe('Wrong password')
    expect(messageFor(new ApiError(401, 'bad_credentials', 'x'))).toBe('Wrong username or password')
  })
})

describe('setup export and import', () => {
  const file = {
    format: 'adguard-reward-export',
    version: 1,
    children: [{ name: 'Ada', clients: ['Kid phone'] }],
    custom_services: [],
    buttons: [],
    toggles: [],
  }

  it('exportSetup GETs the file through request', async () => {
    const calls = mockFetch(json(200, file))
    expect(await exportSetup()).toStrictEqual(file)
    expect(calls).toHaveLength(1)
    expect(calls[0].url).toBe('/api/v1/export')
    expect(calls[0].init.method).toBe('GET')
    expect(calls[0].init.body).toBeUndefined()
    expect(header(calls[0].init, CSRF_HEADER)).toBe(CSRF_VALUE)
    expect(calls[0].init.credentials).toBe('same-origin')
  })

  it('importSetup posts the text as is, never JSON.stringified', async () => {
    const calls = mockFetch(
      json(200, { missing_clients: [], rules_applied: true }),
      json(400, { error: 'bad_request', message: 'invalid JSON' }),
    )
    const text = '{"format": "adguard-reward-export"}\n'
    expect(await importSetup(text)).toStrictEqual({ missing_clients: [], rules_applied: true })
    expect(calls[0].url).toBe('/api/v1/import')
    expect(calls[0].init.method).toBe('POST')
    expect(calls[0].init.body).toBe(text)
    expect(header(calls[0].init, CSRF_HEADER)).toBe(CSRF_VALUE)
    expect(header(calls[0].init, 'Content-Type')).toBe('application/json')
    expect(calls[0].init.credentials).toBe('same-origin')

    // An empty file still goes to the server, which names the problem.
    await fails(importSetup(''))
    expect(calls[1].init.body).toBe('')
  })

  it('importSetup fills a missing missing_clients and keeps a present one', async () => {
    mockFetch(
      json(200, { missing_clients: null, rules_applied: false }),
      json(200, { missing_clients: ['Kid tablet'], rules_applied: true }),
    )
    expect(await importSetup('{}')).toStrictEqual({ missing_clients: [], rules_applied: false })
    expect((await importSetup('{}')).missing_clients).toStrictEqual(['Kid tablet'])
  })

  it('a rejected import rejects with the server message', async () => {
    mockFetch(json(422, { error: 'unprocessable', message: 'buttons[0].child: "Ben" is not in the file' }))
    const bad = await fails(importSetup('{}'))
    expect(bad.status).toBe(422)
    expect(bad.code).toBe('unprocessable')
    expect(messageFor(bad)).toBe('buttons[0].child: "Ben" is not in the file')
    expect(unauthorized).not.toHaveBeenCalled()
  })

  it('both share the 401 handling', async () => {
    mockFetch(json(401, { error: 'pin_required', message: 'm' }), json(401, { error: 'pin_required', message: 'm' }))
    await fails(exportSetup())
    await fails(importSetup('{}'))
    expect(pinRequired).toHaveBeenCalledTimes(2)
    expect(unauthorized).not.toHaveBeenCalled()

    mockFetch(json(401, { error: 'unauthorized', message: 'm' }), json(401, { error: 'unauthorized', message: 'm' }))
    await fails(exportSetup())
    await fails(importSetup('{}'))
    expect(unauthorized).toHaveBeenCalledTimes(2)
    expect(pinRequired).toHaveBeenCalledTimes(2)
  })
})
