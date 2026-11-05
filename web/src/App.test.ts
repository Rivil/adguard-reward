// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen } from '@testing-library/svelte'
import { tick } from 'svelte'
import { get } from 'svelte/store'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from './App.svelte'
import { historyChild, historyFilters, locked, me, navigate, route, setOnUnauthorized } from './lib/api'

type Call = { method: string; url: string; init: RequestInit }
type Answer = () => Response | Promise<Response>
/** Keyed by "METHOD /path" or just "/path"; the method form wins. */
type Routes = Record<string, Answer>

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

/**
 * A URL-keyed fetch stub: pages fetch in any order and extra requests from
 * later components cannot reorder a queue.
 */
function mockFetch(routes: Routes): Call[] {
  const calls: Call[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit) => {
      const method = init?.method ?? 'GET'
      calls.push({ method, url, init })
      const answer = routes[`${method} ${url}`] ?? routes[url]
      if (!answer) throw new Error('unexpected fetch ' + method + ' ' + url)
      return answer()
    }),
  )
  return calls
}

const signedIn = () => json(200, { username: 'mum', expires_at: '2026-10-20T00:00:00Z' })
const noChildren = () => json(200, { children: [] })
const noSession = () => json(401, { error: 'unauthorized', message: 'no session' })

afterEach(() => {
  vi.unstubAllGlobals()
  history.replaceState(null, '', '/')
  route.set('home')
  historyChild.set(null)
  historyFilters.set({ service: null, status: null, from: null, to: null, badChild: false })
  locked.set(false)
})

describe('App', () => {
  it('401 on /me lands on login', async () => {
    const calls = mockFetch({ '/api/v1/me': noSession })
    render(App)

    // The default onUnauthorized handler (not a test spy) must route here.
    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeTruthy()
    expect(location.pathname).toBe('/login')
    expect(calls.map((c) => c.url)).toEqual(['/api/v1/me'])
    expect(screen.queryByText(/Signed in as/)).toBeNull()
  })

  it('logged-in parent lands on home', async () => {
    mockFetch({ '/api/v1/me': signedIn, '/api/v1/children': noChildren })
    render(App)

    const p = await screen.findByText(/Signed in as/)
    expect(p.textContent).toBe('Signed in as mum')
    expect(location.pathname).toBe('/')
    expect(screen.queryByRole('button', { name: 'Sign in' })).toBeNull()
  })

  it('log out returns to the login page', async () => {
    const calls = mockFetch({
      '/api/v1/me': signedIn,
      '/api/v1/children': noChildren,
      'POST /api/v1/logout': () => new Response(null, { status: 204 }),
    })
    render(App)

    await fireEvent.click(await screen.findByRole('button', { name: 'Log out' }))

    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeTruthy()
    expect(location.pathname).toBe('/login')
    expect(calls.map((c) => `${c.method} ${c.url}`)).toContain('POST /api/v1/logout')
  })

  it('a 401 after login returns to the login page', async () => {
    let session = true
    mockFetch({
      '/api/v1/me': () => (session ? signedIn() : json(401, { error: 'unauthorized', message: 'session expired' })),
      '/api/v1/children': noChildren,
    })
    render(App)
    await screen.findByText(/Signed in as/)
    session = false

    // The session lapses server-side; the next call from any page 401s. Only
    // the default onUnauthorized handler can move the app off Home here —
    // nothing in App or Home reacts to this rejection.
    await expect(me()).rejects.toMatchObject({ status: 401 })

    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeTruthy()
    expect(location.pathname).toBe('/login')
    expect(screen.queryByText(/Signed in as/)).toBeNull()
  })

  it('browser back to / while signed out keeps the login page', async () => {
    mockFetch({ '/api/v1/me': noSession })
    render(App)
    await screen.findByRole('button', { name: 'Sign in' })
    expect(location.pathname).toBe('/login')

    history.pushState(null, '', '/')
    window.dispatchEvent(new PopStateEvent('popstate'))
    await tick()

    expect(screen.getByRole('button', { name: 'Sign in' })).toBeTruthy()
    expect(screen.queryByText(/Signed in as/)).toBeNull()
  })

  it('browser back and forward switch pages while signed in', async () => {
    mockFetch({ '/api/v1/me': signedIn, '/api/v1/children': noChildren })
    render(App)
    await screen.findByText(/Signed in as/)

    history.pushState(null, '', '/login')
    window.dispatchEvent(new PopStateEvent('popstate'))
    await tick()
    expect(get(route)).toBe('login')
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeTruthy()
    expect(screen.queryByText(/Signed in as/)).toBeNull()

    history.pushState(null, '', '/')
    window.dispatchEvent(new PopStateEvent('popstate'))
    await tick()
    expect(get(route)).toBe('home')
    expect(screen.getByText(/Signed in as/)).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Sign in' })).toBeNull()
  })

  it('reload on /children stays there', async () => {
    history.replaceState(null, '', '/children')
    route.set('children')
    const calls = mockFetch({
      '/api/v1/me': signedIn,
      '/api/v1/children': noChildren,
      '/api/v1/clients': () => json(200, { clients: [] }),
    })
    render(App)
    expect(await screen.findByRole('heading', { name: 'Children' })).toBeTruthy()
    expect(location.pathname).toBe('/children')
    expect(get(route)).toBe('children')
    expect(calls.map((c) => c.url)).toContain('/api/v1/children')
  })

  it('a 401 at /children lands on login', async () => {
    history.replaceState(null, '', '/children')
    route.set('children')
    mockFetch({ '/api/v1/me': noSession })
    render(App)
    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeTruthy()
    expect(location.pathname).toBe('/login')
  })

  it('sign in through the login page lands on home', async () => {
    let session = false
    const calls = mockFetch({
      '/api/v1/me': () => (session ? signedIn() : noSession()),
      'POST /api/v1/login': () => {
        session = true
        return new Response(null, { status: 204 })
      },
      '/api/v1/children': noChildren,
    })
    render(App)
    await screen.findByRole('button', { name: 'Sign in' })
    expect(location.pathname).toBe('/login')

    await fireEvent.input(screen.getByLabelText('Username'), { target: { value: 'mum' } })
    await fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'pw' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))

    // refresh() must move a parent off the login page, not keep them there.
    const p = await screen.findByText(/Signed in as/)
    expect(p.textContent).toBe('Signed in as mum')
    expect(location.pathname).toBe('/')
    expect(get(route)).toBe('home')
    expect(calls.map((c) => `${c.method} ${c.url}`)).toContain('POST /api/v1/login')
  })

  it('Back from Children returns home', async () => {
    history.replaceState(null, '', '/children')
    route.set('children')
    mockFetch({
      '/api/v1/me': signedIn,
      '/api/v1/children': noChildren,
      '/api/v1/clients': () => json(200, { clients: [] }),
      '/api/v1/migration': () => json(200, { global: [], clients: [] }),
    })
    render(App)
    await screen.findByRole('heading', { name: 'Children' })

    await fireEvent.click(screen.getByRole('button', { name: 'Back' }))

    expect(await screen.findByText(/Signed in as/)).toBeTruthy()
    expect(location.pathname).toBe('/')
    expect(get(route)).toBe('home')
    expect(screen.queryByRole('heading', { name: 'Children' })).toBeNull()
  })

  it('reload on /buttons stays there', async () => {
    history.replaceState(null, '', '/buttons')
    route.set('buttons')
    const calls = mockFetch({
      '/api/v1/me': signedIn,
      '/api/v1/buttons': () => json(200, { buttons: [] }),
      '/api/v1/children': noChildren,
      '/api/v1/services': () => json(200, { services: [] }),
    })
    render(App)
    expect(await screen.findByRole('heading', { name: 'Buttons' })).toBeTruthy()
    expect(location.pathname).toBe('/buttons')
    expect(get(route)).toBe('buttons')
    expect(calls.map((c) => c.url)).toContain('/api/v1/buttons')
  })

  it('buttons needs a session', async () => {
    history.replaceState(null, '', '/buttons')
    route.set('buttons')
    const calls = mockFetch({ '/api/v1/me': noSession })
    render(App)
    await screen.findByRole('button', { name: 'Sign in' })
    await tick()
    expect(location.pathname).toBe('/login')
    expect(calls.map((c) => c.url)).toEqual(['/api/v1/me'])
    expect(screen.queryByRole('heading', { name: 'Buttons' })).toBeNull()
  })

  it('Back from Buttons returns home', async () => {
    history.replaceState(null, '', '/buttons')
    route.set('buttons')
    mockFetch({
      '/api/v1/me': signedIn,
      '/api/v1/buttons': () => json(200, { buttons: [] }),
      '/api/v1/children': noChildren,
      '/api/v1/services': () => json(200, { services: [] }),
      '/api/v1/migration': () => json(200, { global: [], clients: [] }),
    })
    render(App)
    await screen.findByRole('heading', { name: 'Buttons' })

    await fireEvent.click(screen.getByRole('button', { name: 'Back' }))

    expect(await screen.findByText(/Signed in as/)).toBeTruthy()
    expect(location.pathname).toBe('/')
    expect(get(route)).toBe('home')
    expect(screen.queryByRole('heading', { name: 'Buttons' })).toBeNull()
  })

  it('a non-401 failure on /me lands on /login', async () => {
    // A 502 never goes through onUnauthorized, so only App's own catch can
    // move the page: the URL must follow, not just the rendered component.
    for (const start of ['children', 'buttons'] as const) {
      const spy = vi.fn()
      setOnUnauthorized(spy)
      history.replaceState(null, '', `/${start}`)
      route.set(start)
      mockFetch({ '/api/v1/me': () => json(502, { error: 'adguard_unavailable', message: 'down' }) })
      const { unmount } = render(App)
      expect(await screen.findByRole('button', { name: 'Sign in' })).toBeTruthy()
      expect(location.pathname).toBe('/login')
      expect(get(route)).toBe('login')
      expect(spy).not.toHaveBeenCalled()
      unmount()
      vi.unstubAllGlobals()
    }
    setOnUnauthorized(() => navigate('login'))
  })

  it('back to /children while signed out stays on login', async () => {
    const calls = mockFetch({ '/api/v1/me': noSession })
    render(App)
    await screen.findByRole('button', { name: 'Sign in' })

    history.pushState(null, '', '/children')
    window.dispatchEvent(new PopStateEvent('popstate'))
    await tick()

    expect(screen.getByRole('button', { name: 'Sign in' })).toBeTruthy()
    expect(screen.queryByRole('heading', { name: 'Children' })).toBeNull()
    expect(calls.map((c) => c.url)).toEqual(['/api/v1/me'])
  })

  it('back to /buttons while signed out stays on login', async () => {
    // The route alone must not open Buttons: username is still null, so the
    // page (and its /api/v1/buttons load) never mounts.
    const calls = mockFetch({ '/api/v1/me': noSession })
    render(App)
    await screen.findByRole('button', { name: 'Sign in' })

    history.pushState(null, '', '/buttons')
    window.dispatchEvent(new PopStateEvent('popstate'))
    await tick()

    expect(get(route)).toBe('buttons')
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeTruthy()
    expect(screen.queryByRole('heading', { name: 'Buttons' })).toBeNull()
    expect(calls.map((c) => c.url)).toEqual(['/api/v1/me'])
  })

  it('children needs a session', async () => {
    history.replaceState(null, '', '/children')
    route.set('children')
    const calls = mockFetch({ '/api/v1/me': noSession })
    render(App)
    await screen.findByRole('button', { name: 'Sign in' })
    await tick()
    expect(calls.map((c) => c.url)).toEqual(['/api/v1/me'])
    expect(screen.queryByRole('heading', { name: 'Children' })).toBeNull()
  })
})

describe('services route', () => {
  it('loading /services renders the Services heading', async () => {
    history.replaceState(null, '', '/services')
    route.set('services')
    const calls = mockFetch({
      '/api/v1/me': signedIn,
      '/api/v1/custom-services': () => json(200, { custom_services: [] }),
    })
    render(App)
    expect(await screen.findByRole('heading', { name: 'Services' })).toBeTruthy()
    expect(location.pathname).toBe('/services')
    expect(get(route)).toBe('services')
    expect(calls.map((c) => c.url)).toContain('/api/v1/custom-services')
  })

  it('services needs a session', async () => {
    history.replaceState(null, '', '/services')
    route.set('services')
    const calls = mockFetch({ '/api/v1/me': noSession })
    render(App)
    await screen.findByRole('button', { name: 'Sign in' })
    await tick()
    expect(location.pathname).toBe('/login')
    expect(calls.map((c) => c.url)).toEqual(['/api/v1/me'])
    expect(screen.queryByRole('heading', { name: 'Services' })).toBeNull()
  })

  it('back to /services while signed out stays on login', async () => {
    // The route alone must not open Services: username is still null, so the
    // page (and its /api/v1/custom-services load) never mounts.
    const calls = mockFetch({ '/api/v1/me': noSession })
    render(App)
    await screen.findByRole('button', { name: 'Sign in' })

    history.pushState(null, '', '/services')
    window.dispatchEvent(new PopStateEvent('popstate'))
    await tick()

    expect(get(route)).toBe('services')
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeTruthy()
    expect(screen.queryByRole('heading', { name: 'Services' })).toBeNull()
    expect(calls.map((c) => c.url)).toEqual(['/api/v1/me'])
  })

  it('Back from Services returns home', async () => {
    history.replaceState(null, '', '/services')
    route.set('services')
    mockFetch({
      '/api/v1/me': signedIn,
      '/api/v1/custom-services': () => json(200, { custom_services: [] }),
      '/api/v1/buttons': () => json(200, { buttons: [] }),
      '/api/v1/children': noChildren,
      '/api/v1/services': () => json(200, { services: [] }),
      '/api/v1/migration': () => json(200, { global: [], clients: [] }),
    })
    render(App)
    await screen.findByRole('heading', { name: 'Services' })

    await fireEvent.click(screen.getByRole('button', { name: 'Back' }))

    expect(await screen.findByText(/Signed in as/)).toBeTruthy()
    expect(location.pathname).toBe('/')
    expect(get(route)).toBe('home')
    expect(screen.queryByRole('heading', { name: 'Services' })).toBeNull()
  })
})

describe('settings route', () => {
  const noPin = () => json(200, { has_pin: false })

  it('reload on /settings while signed in renders Settings', async () => {
    history.replaceState(null, '', '/settings')
    route.set('settings')
    const calls = mockFetch({ '/api/v1/me': signedIn, '/api/v1/pin': noPin })
    render(App)
    expect(await screen.findByRole('heading', { name: 'Settings' })).toBeTruthy()
    expect(location.pathname).toBe('/settings')
    expect(get(route)).toBe('settings')
    expect(calls.map((c) => c.url)).toContain('/api/v1/pin')
  })

  it('back to /settings while signed out stays on login', async () => {
    // The route alone must not open Settings: username is still null, so the
    // page (and its /api/v1/pin load) never mounts.
    const calls = mockFetch({ '/api/v1/me': noSession })
    render(App)
    await screen.findByRole('button', { name: 'Sign in' })

    history.pushState(null, '', '/settings')
    window.dispatchEvent(new PopStateEvent('popstate'))
    await tick()

    expect(get(route)).toBe('settings')
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeTruthy()
    expect(screen.queryByRole('heading', { name: 'Settings' })).toBeNull()
    expect(calls.map((c) => c.url)).toEqual(['/api/v1/me'])
  })
})

describe('history route', () => {
  const cleoPage = () =>
    json(200, {
      child: { id: 3, name: 'Cleo' },
      grants: [
        {
          id: 7,
          status: 'expired',
          started_at: '2026-10-01T10:00:00Z',
          ends_at: '2026-10-01T11:00:00Z',
          ended_at: '2026-10-01T11:00:00Z',
          services: [{ id: 'youtube', name: 'YouTube', removed_at: null, removed_reason: null }],
          extensions: [],
        },
      ],
      next_before: null,
    })

  /** Start the SPA as a browser reload at /history?child=3 would. */
  function loadAt(url: string, child: number | null) {
    history.replaceState(null, '', url)
    route.set('history')
    historyChild.set(child)
  }

  /** Point every routing store at url, as loading the module there would. */
  function bootAt(url: string) {
    history.replaceState(null, '', url)
    window.dispatchEvent(new PopStateEvent('popstate'))
  }

  it('reload on /history?child=3 keeps the query and shows that child', async () => {
    loadAt('/history?child=3', 3)
    const calls = mockFetch({ '/api/v1/me': signedIn, '/api/v1/children/3/history': cleoPage })
    render(App)

    expect(await screen.findByRole('heading', { name: 'Cleo — history' })).toBeTruthy()
    expect(location.pathname + location.search).toBe('/history?child=3')
    expect(get(route)).toBe('history')
    expect(get(historyChild)).toBe(3)
    expect(document.querySelector('[data-grant="7"]')).not.toBeNull()
    expect(calls.map((c) => c.url)).toEqual(['/api/v1/me', '/api/v1/children/3/history'])
  })

  it('reload leaves a filtered URL byte-for-byte, whatever order it was written in', async () => {
    const url = '/history?status=expired&service=custom:5&child=3'
    bootAt(url)
    const push = vi.spyOn(history, 'pushState')
    try {
      mockFetch({ '/api/v1/me': signedIn, '/api/v1/children/3/history': cleoPage })
      render(App)

      expect(await screen.findByRole('heading', { name: 'Cleo — history' })).toBeTruthy()
      expect(location.pathname + location.search).toBe(url)
      expect(push).toHaveBeenCalledTimes(0)
      expect(get(historyChild)).toBe(3)
      expect(get(historyFilters)).toStrictEqual({
        service: 'custom:5',
        status: 'expired',
        from: null,
        to: null,
        badChild: false,
      })
    } finally {
      push.mockRestore()
    }
  })

  it('reload on a bad child keeps the URL and shows No such child', async () => {
    const url = '/history?child=abc'
    bootAt(url)
    const push = vi.spyOn(history, 'pushState')
    try {
      const calls = mockFetch({ '/api/v1/me': signedIn })
      render(App)

      expect(await screen.findByText('No such child')).toBeTruthy()
      expect(location.pathname + location.search).toBe(url)
      expect(push).toHaveBeenCalledTimes(0)
      expect(get(historyFilters).badChild).toBe(true)
      expect(calls.map((c) => c.url)).toEqual(['/api/v1/me'])
    } finally {
      push.mockRestore()
    }
  })

  it('a PIN unlock leaves the query and the filters intact', async () => {
    const url = '/history?child=3&status=expired&from=2026-03-01'
    bootAt(url)
    const filters = { service: null, status: 'expired', from: '2026-03-01', to: null, badChild: false }
    const push = vi.spyOn(history, 'pushState')
    try {
      let unlockedYet = false
      mockFetch({
        '/api/v1/me': () =>
          unlockedYet ? signedIn() : json(401, { error: 'pin_required', message: 'enter your PIN' }),
        'POST /api/v1/unlock': () => {
          unlockedYet = true
          return new Response(null, { status: 204 })
        },
        '/api/v1/children/3/history': cleoPage,
      })
      render(App)
      await screen.findByRole('dialog')
      expect(location.pathname + location.search).toBe(url)

      await fireEvent.input(screen.getByLabelText('PIN'), { target: { value: '1234' } })
      await fireEvent.click(screen.getByRole('button', { name: 'Unlock' }))

      expect(await screen.findByRole('heading', { name: 'Cleo — history' })).toBeTruthy()
      expect(location.pathname + location.search).toBe(url)
      expect(push).toHaveBeenCalledTimes(0)
      expect(get(historyChild)).toBe(3)
      expect(get(historyFilters)).toStrictEqual(filters)
    } finally {
      push.mockRestore()
    }
  })

  it('history needs a session', async () => {
    loadAt('/history?child=3', 3)
    const calls = mockFetch({ '/api/v1/me': noSession })
    render(App)
    await screen.findByRole('button', { name: 'Sign in' })
    await tick()
    expect(location.pathname).toBe('/login')
    expect(calls.map((c) => c.url)).toEqual(['/api/v1/me'])
    expect(screen.queryByRole('heading', { name: /history/i })).toBeNull()
  })

  it('Back from History returns home', async () => {
    loadAt('/history?child=3', 3)
    mockFetch({
      '/api/v1/me': signedIn,
      '/api/v1/children/3/history': cleoPage,
      '/api/v1/children': noChildren,
      '/api/v1/buttons': () => json(200, { buttons: [] }),
      '/api/v1/services': () => json(200, { services: [] }),
      '/api/v1/grants': () => json(200, { grants: [] }),
      '/api/v1/migration': () => json(200, { global: [], clients: [] }),
    })
    render(App)
    await screen.findByRole('heading', { name: 'Cleo — history' })

    await fireEvent.click(screen.getByRole('button', { name: 'Back' }))

    expect(await screen.findByText(/Signed in as/)).toBeTruthy()
    expect(location.pathname + location.search).toBe('/')
    expect(get(route)).toBe('home')
    expect(get(historyChild)).toBeNull()
    expect(screen.queryByRole('heading', { name: 'Cleo — history' })).toBeNull()
  })

  it('a History link on Home opens that child', async () => {
    const cleo = { id: 3, name: 'Cleo', clients: ['Kid phone'] }
    mockFetch({
      '/api/v1/me': signedIn,
      '/api/v1/children': () => json(200, { children: [cleo] }),
      '/api/v1/buttons': () => json(200, { buttons: [] }),
      '/api/v1/services': () => json(200, { services: [] }),
      '/api/v1/grants': () => json(200, { grants: [] }),
      '/api/v1/migration': () => json(200, { global: [], clients: [] }),
      '/api/v1/children/3/history': cleoPage,
    })
    render(App)

    await fireEvent.click(await screen.findByRole('button', { name: 'History for Cleo' }))

    expect(await screen.findByRole('heading', { name: 'Cleo — history' })).toBeTruthy()
    expect(location.pathname + location.search).toBe('/history?child=3')
    expect(screen.queryByText(/Signed in as/)).toBeNull()
  })
})

describe('App PIN lock', () => {
  const pinRequired = () => json(401, { error: 'pin_required', message: 'enter your PIN' })
  const clients = () => json(200, { clients: [] })
  const atChildren = () => {
    history.replaceState(null, '', '/children')
    route.set('children')
  }

  it('pin_required on /me shows the pad on the same URL; unauthorized shows Sign in', async () => {
    atChildren()
    mockFetch({ '/api/v1/me': pinRequired })
    render(App)

    expect(await screen.findByRole('dialog')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Unlock' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Sign in' })).toBeNull()
    expect(location.pathname).toBe('/children')
    expect(get(locked)).toBe(true)
  })

  it('a plain 401 on /me at /children shows Sign in, not the pad', async () => {
    atChildren()
    mockFetch({ '/api/v1/me': noSession })
    render(App)
    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeTruthy()
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('a wrong PIN keeps the pad and the location', async () => {
    atChildren()
    mockFetch({
      '/api/v1/me': pinRequired,
      'POST /api/v1/unlock': () => json(401, { error: 'bad_pin', message: 'wrong pin', attempts_remaining: 4 }),
    })
    render(App)
    await screen.findByRole('dialog')
    await fireEvent.input(screen.getByLabelText('PIN'), { target: { value: '0000' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Unlock' }))

    expect((await screen.findByRole('alert')).textContent).toContain('4 attempts left')
    expect(screen.getByRole('dialog')).toBeTruthy()
    expect((screen.getByLabelText('PIN') as HTMLInputElement).value).toBe('')
    expect(location.pathname).toBe('/children')
    expect(screen.queryByRole('button', { name: 'Sign in' })).toBeNull()
  })

  it('device_revoked from unlock falls back to the login page', async () => {
    atChildren()
    mockFetch({
      '/api/v1/me': pinRequired,
      'POST /api/v1/unlock': () => json(401, { error: 'device_revoked', message: 'revoked' }),
    })
    render(App)
    await screen.findByRole('dialog')
    await fireEvent.input(screen.getByLabelText('PIN'), { target: { value: '1234' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Unlock' }))

    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeTruthy()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(location.pathname).toBe('/login')
    expect(get(locked)).toBe(false)
  })

  it('unlocking removes the pad and the route refetches', async () => {
    atChildren()
    let state: 'ok' | 'locked' = 'ok'
    const calls = mockFetch({
      '/api/v1/me': () => (state === 'ok' ? signedIn() : pinRequired()),
      '/api/v1/children': () => (state === 'ok' ? noChildren() : pinRequired()),
      '/api/v1/clients': () => (state === 'ok' ? clients() : pinRequired()),
      'POST /api/v1/unlock': () => {
        state = 'ok'
        return new Response(null, { status: 204 })
      },
    })
    render(App)
    expect(await screen.findByRole('heading', { name: 'Children' })).toBeTruthy()
    const childrenFetches = () => calls.filter((c) => c.url === '/api/v1/children').length
    const before = childrenFetches()
    expect(before).toBeGreaterThan(0)

    // The idle window lapses server-side; the next request 401s pin_required.
    state = 'locked'
    await expect(me()).rejects.toMatchObject({ status: 401, code: 'pin_required' })
    expect(await screen.findByRole('dialog')).toBeTruthy()
    expect(screen.queryByRole('heading', { name: 'Children' })).toBeNull()
    expect(location.pathname).toBe('/children')

    await fireEvent.input(screen.getByLabelText('PIN'), { target: { value: '1234' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Unlock' }))

    expect(await screen.findByRole('heading', { name: 'Children' })).toBeTruthy()
    expect(screen.queryByRole('dialog')).toBeNull()
    await vi.waitFor(() => expect(childrenFetches()).toBeGreaterThan(before))
    expect(location.pathname).toBe('/children')
    expect(get(locked)).toBe(false)
  })

  it('while unlock re-reads /me nothing renders: no flash of Sign in', async () => {
    // Locked at load, so username is still null when the pad goes away. Until
    // the second /me answers the app must show nothing, not the login page.
    atChildren()
    let answerMe!: (r: Response) => void
    const heldMe = new Promise<Response>((resolve) => {
      answerMe = resolve
    })
    let unlockedYet = false
    const calls = mockFetch({
      '/api/v1/me': () => (unlockedYet ? heldMe : pinRequired()),
      '/api/v1/children': noChildren,
      '/api/v1/clients': clients,
      'POST /api/v1/unlock': () => {
        unlockedYet = true
        return new Response(null, { status: 204 })
      },
    })
    render(App)
    await screen.findByRole('dialog')
    await fireEvent.input(screen.getByLabelText('PIN'), { target: { value: '1234' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Unlock' }))

    const meCalls = () => calls.filter((c) => c.url === '/api/v1/me').length
    await vi.waitFor(() => expect(meCalls()).toBe(2))
    await tick()
    expect(get(locked)).toBe(false)
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Sign in' })).toBeNull()
    expect(screen.queryByRole('heading', { name: 'Children' })).toBeNull()

    answerMe(signedIn())
    expect(await screen.findByRole('heading', { name: 'Children' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Sign in' })).toBeNull()
    expect(location.pathname).toBe('/children')
  })

  it("concurrent pin_required responses from Home's fetches render exactly one pad", async () => {
    // /me succeeds, so Home mounts; every fetch Home makes on mount then
    // races back 401 pin_required.
    let pinResponses = 0
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) => {
        if (url === '/api/v1/me') return signedIn()
        pinResponses++
        return pinRequired()
      }),
    )
    render(App)

    expect(await screen.findAllByRole('dialog')).toHaveLength(1)
    await vi.waitFor(() => expect(pinResponses).toBeGreaterThanOrEqual(2))
    await tick()
    expect(screen.getAllByRole('dialog')).toHaveLength(1)
    expect(screen.getAllByRole('button', { name: 'Unlock' })).toHaveLength(1)
    expect(screen.queryByText(/Signed in as/)).toBeNull()
  })
})
