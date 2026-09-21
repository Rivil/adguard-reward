// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen } from '@testing-library/svelte'
import { tick } from 'svelte'
import { get } from 'svelte/store'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from './App.svelte'
import { me, route } from './lib/api'

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
