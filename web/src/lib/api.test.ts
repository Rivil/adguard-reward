// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { get } from 'svelte/store'
import {
  ApiError,
  CSRF_HEADER,
  CSRF_VALUE,
  applyMigration,
  childBlocked,
  createChild,
  deleteChild,
  iconUrl,
  listChildren,
  listClients,
  listServices,
  login,
  logout,
  me,
  messageFor,
  migrationDismissed,
  migrationOffer,
  navigate,
  request,
  route,
  setOnUnauthorized,
  updateChild,
} from './api'

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

beforeEach(() => {
  unauthorized = vi.fn<() => void>()
  setOnUnauthorized(unauthorized)
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

describe('routing', () => {
  afterEach(() => {
    history.replaceState(null, '', '/')
    route.set('home')
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
