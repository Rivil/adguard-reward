// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen } from '@testing-library/svelte'
import { tick } from 'svelte'
import { get } from 'svelte/store'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from './App.svelte'
import { me, navigate, route, setOnUnauthorized } from './lib/api'

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
