// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen } from '@testing-library/svelte'
import { get } from 'svelte/store'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from '../App.svelte'
import Settings from './Settings.svelte'
import { historyChild, locked, navigate, route } from './api'

type Call = { method: string; url: string; body: string | undefined }
type Answer = (call: Call) => Response | Promise<Response>

function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } })
}

/** URL-keyed fetch stub; "METHOD /path" wins over "/path". */
function mockFetch(routes: Record<string, Answer>): Call[] {
  const calls: Call[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit) => {
      const method = init?.method ?? 'GET'
      const call = { method, url, body: init?.body as string | undefined }
      calls.push(call)
      const answer = routes[`${method} ${url}`] ?? routes[url]
      if (!answer) throw new Error('unexpected fetch ' + method + ' ' + url)
      return answer(call)
    }),
  )
  return calls
}

const writes = (calls: Call[]) => calls.filter((c) => c.method !== 'GET')
const noContent = () => new Response(null, { status: 204 })
const status = (hasPin: boolean) => () => json(200, { has_pin: hasPin })

async function type(label: string, value: string) {
  await fireEvent.input(screen.getByLabelText(label), { target: { value } })
}

afterEach(() => {
  vi.unstubAllGlobals()
  history.replaceState(null, '', '/')
  route.set('home')
  historyChild.set(null)
  locked.set(false)
})

describe('Settings', () => {
  it('first set asks for no password and POSTs exactly {"pin":"583920"}', async () => {
    const calls = mockFetch({ 'GET /api/v1/pin': status(false), 'POST /api/v1/pin': noContent })
    render(Settings, { onBack: () => {} })

    expect(await screen.findByText('No PIN on this device')).toBeTruthy()
    expect(screen.queryByLabelText('AdGuard password')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Remove PIN' })).toBeNull()

    await type('PIN', '583920')
    await type('Confirm PIN', '583920')
    await fireEvent.click(screen.getByRole('button', { name: 'Set PIN' }))

    expect(await screen.findByText('This device has a PIN')).toBeTruthy()
    expect(writes(calls)).toEqual([{ method: 'POST', url: '/api/v1/pin', body: '{"pin":"583920"}' }])
  })

  it.each([
    ['12a4', '12a4'],
    ['123', '123'],
    ['1234567', '1234567'],
    ['583920', '583921'],
  ])('rejects PIN %s / confirm %s inline with no request', async (p, c) => {
    const calls = mockFetch({ 'GET /api/v1/pin': status(false) })
    render(Settings, { onBack: () => {} })
    await screen.findByText('No PIN on this device')

    await type('PIN', p)
    await type('Confirm PIN', c)
    await fireEvent.click(screen.getByRole('button', { name: 'Set PIN' }))

    expect((await screen.findByRole('alert')).getAttribute('data-error')).toBe('invalid_pin')
    expect(writes(calls)).toHaveLength(0)
    expect(screen.getByText('No PIN on this device')).toBeTruthy()
  })

  it('change POSTs {pin,password}', async () => {
    const calls = mockFetch({ 'GET /api/v1/pin': status(true), 'POST /api/v1/pin': noContent })
    render(Settings, { onBack: () => {} })
    expect(await screen.findByText('This device has a PIN')).toBeTruthy()

    await type('New PIN', '1111')
    await type('Confirm PIN', '1111')
    await type('AdGuard password', 'pw')
    await fireEvent.click(screen.getByRole('button', { name: 'Change PIN' }))

    expect(await screen.findByRole('status')).toBeTruthy()
    expect(writes(calls)).toEqual([{ method: 'POST', url: '/api/v1/pin', body: '{"pin":"1111","password":"pw"}' }])
    expect(screen.getByText('This device has a PIN')).toBeTruthy()
  })

  it('change without a password is refused inline', async () => {
    const calls = mockFetch({ 'GET /api/v1/pin': status(true) })
    render(Settings, { onBack: () => {} })
    await screen.findByText('This device has a PIN')
    await type('New PIN', '1111')
    await type('Confirm PIN', '1111')
    await fireEvent.click(screen.getByRole('button', { name: 'Change PIN' }))
    expect((await screen.findByRole('alert')).getAttribute('data-error')).toBe('reauth_required')
    expect(writes(calls)).toHaveLength(0)
  })

  it('remove sends DELETE {password} and shows "No PIN on this device" without reload', async () => {
    const calls = mockFetch({ 'GET /api/v1/pin': status(true), 'DELETE /api/v1/pin': noContent })
    render(Settings, { onBack: () => {} })
    await screen.findByText('This device has a PIN')

    await type('AdGuard password', 'pw')
    await fireEvent.click(screen.getByRole('button', { name: 'Remove PIN' }))

    expect(await screen.findByText('No PIN on this device')).toBeTruthy()
    expect(writes(calls)).toEqual([{ method: 'DELETE', url: '/api/v1/pin', body: '{"password":"pw"}' }])
    expect(calls.filter((c) => c.method === 'GET' && c.url === '/api/v1/pin')).toHaveLength(1)
    expect(screen.queryByLabelText('AdGuard password')).toBeNull()
  })

  it('remove without a password is refused inline', async () => {
    const calls = mockFetch({ 'GET /api/v1/pin': status(true) })
    render(Settings, { onBack: () => {} })
    await screen.findByText('This device has a PIN')
    await fireEvent.click(screen.getByRole('button', { name: 'Remove PIN' }))
    expect((await screen.findByRole('alert')).getAttribute('data-error')).toBe('reauth_required')
    expect(writes(calls)).toHaveLength(0)
  })

  it('403 bad_credentials shows "Wrong password" and stays on settings', async () => {
    route.set('settings')
    mockFetch({
      'GET /api/v1/pin': status(true),
      'DELETE /api/v1/pin': () => json(403, { error: 'bad_credentials', message: 'invalid username or password' }),
    })
    render(Settings, { onBack: () => {} })
    await screen.findByText('This device has a PIN')

    await type('AdGuard password', 'nope')
    await fireEvent.click(screen.getByRole('button', { name: 'Remove PIN' }))

    expect((await screen.findByRole('alert')).textContent).toBe('Wrong password')
    expect(screen.getByText('This device has a PIN')).toBeTruthy()
    expect(get(route)).toBe('settings')
  })

  it('429 shows a retry message with seconds', async () => {
    mockFetch({
      'GET /api/v1/pin': status(true),
      'POST /api/v1/pin': () =>
        json(429, { error: 'rate_limited', message: 'too many' }, { 'Retry-After': '42' }),
    })
    render(Settings, { onBack: () => {} })
    await screen.findByText('This device has a PIN')
    await type('New PIN', '1111')
    await type('Confirm PIN', '1111')
    await type('AdGuard password', 'pw')
    await fireEvent.click(screen.getByRole('button', { name: 'Change PIN' }))

    expect((await screen.findByRole('alert')).textContent).toContain('42s')
    expect(screen.getByText('This device has a PIN')).toBeTruthy()
  })

  it('a failed status load offers Try again, which loads the form', async () => {
    let up = false
    const calls = mockFetch({
      'GET /api/v1/pin': () => (up ? json(200, { has_pin: false }) : json(502, { error: 'internal', message: 'boom' })),
    })
    render(Settings, { onBack: () => {} })
    expect(await screen.findByRole('alert')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Set PIN' })).toBeNull()

    up = true
    await fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByText('No PIN on this device')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Set PIN' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull()
    expect(calls.filter((c) => c.url === '/api/v1/pin')).toHaveLength(2)
  })

  it("Home's Settings button navigates to /settings and App renders Settings", async () => {
    mockFetch({
      '/api/v1/me': () => json(200, { username: 'mum', expires_at: '2026-12-01T00:00:00Z' }),
      '/api/v1/children': () => json(200, { children: [] }),
      'GET /api/v1/pin': status(false),
    })
    render(App)
    await screen.findByText(/Signed in as/)

    const nav = screen.getByRole('navigation')
    const button = [...nav.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Settings')
    expect(button).toBeTruthy()
    await fireEvent.click(button!)

    expect(await screen.findByRole('heading', { name: 'Settings' })).toBeTruthy()
    expect(location.pathname).toBe('/settings')
    expect(get(route)).toBe('settings')
    expect(await screen.findByText('No PIN on this device')).toBeTruthy()

    await fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    expect(await screen.findByText(/Signed in as/)).toBeTruthy()
    navigate('home')
  })
})
