// @vitest-environment jsdom
import { fireEvent, render, screen } from '@testing-library/svelte'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from './App.svelte'

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
  })
})
