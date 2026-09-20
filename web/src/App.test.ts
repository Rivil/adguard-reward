// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen } from '@testing-library/svelte'
import { tick } from 'svelte'
import { get } from 'svelte/store'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from './App.svelte'
import { me, route } from './lib/api'

type Call = { url: string; init: RequestInit }

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
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

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('App', () => {
  it('401 on /me lands on login', async () => {
    const calls = mockFetch(json(401, { error: 'unauthorized', message: 'no session' }))
    render(App)

    // The default onUnauthorized handler (not a test spy) must route here.
    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeTruthy()
    expect(location.pathname).toBe('/login')
    expect(calls.map((c) => c.url)).toEqual(['/api/v1/me'])
    expect(screen.queryByText(/Signed in as/)).toBeNull()
  })

  it('logged-in parent lands on home', async () => {
    mockFetch(json(200, { username: 'mum', expires_at: '2026-10-20T00:00:00Z' }))
    render(App)

    const p = await screen.findByText(/Signed in as/)
    expect(p.textContent).toBe('Signed in as mum')
    expect(location.pathname).toBe('/')
    expect(screen.queryByRole('button', { name: 'Sign in' })).toBeNull()
  })

  it('log out returns to the login page', async () => {
    const calls = mockFetch(
      json(200, { username: 'mum', expires_at: '2026-10-20T00:00:00Z' }),
      new Response(null, { status: 204 }),
    )
    render(App)

    await fireEvent.click(await screen.findByRole('button', { name: 'Log out' }))

    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeTruthy()
    expect(location.pathname).toBe('/login')
    expect(calls.map((c) => c.url)).toEqual(['/api/v1/me', '/api/v1/logout'])
    expect(calls[1].init.method).toBe('POST')
  })

  it('a 401 after login returns to the login page', async () => {
    mockFetch(
      json(200, { username: 'mum', expires_at: '2026-10-20T00:00:00Z' }),
      json(401, { error: 'unauthorized', message: 'session expired' }),
    )
    render(App)
    await screen.findByText(/Signed in as/)

    // The session lapses server-side; the next call from any page 401s. Only
    // the default onUnauthorized handler can move the app off Home here —
    // nothing in App or Home reacts to this rejection.
    await expect(me()).rejects.toMatchObject({ status: 401 })

    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeTruthy()
    expect(location.pathname).toBe('/login')
    expect(screen.queryByText(/Signed in as/)).toBeNull()
  })

  it('browser back to / while signed out keeps the login page', async () => {
    mockFetch(json(401, { error: 'unauthorized', message: 'no session' }))
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
    mockFetch(json(200, { username: 'mum', expires_at: '2026-10-20T00:00:00Z' }))
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
})
