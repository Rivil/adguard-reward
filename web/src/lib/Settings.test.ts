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

/** A response the test releases by hand, to observe the in-flight UI. */
function held(): { answer: Answer; release: (r: Response) => void } {
  let release: (r: Response) => void = () => {}
  const p = new Promise<Response>((resolve) => (release = resolve))
  return { answer: () => p, release }
}

const value = (label: string) => (screen.getByLabelText(label) as HTMLInputElement).value
const alertText = async () => (await screen.findByRole('alert')).textContent
const tryAgain = () => screen.queryByRole('button', { name: 'Try again' })

describe('Settings — messages, field resets and gates', () => {
  it('every field starts empty and there is no alert, note or Try again', async () => {
    mockFetch({ 'GET /api/v1/pin': status(true) })
    render(Settings, { onBack: () => {} })
    await screen.findByText('This device has a PIN')

    expect(value('New PIN')).toBe('')
    expect(value('Confirm PIN')).toBe('')
    expect(value('AdGuard password')).toBe('')
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByRole('status')).toBeNull()
    expect(tryAgain()).toBeNull()
  })

  it('a malformed PIN reads "A PIN is 4 to 6 digits"; a mismatch reads "The PINs don\'t match"', async () => {
    mockFetch({ 'GET /api/v1/pin': status(false) })
    render(Settings, { onBack: () => {} })
    await screen.findByText('No PIN on this device')

    await type('PIN', '12')
    await type('Confirm PIN', '12')
    await fireEvent.click(screen.getByRole('button', { name: 'Set PIN' }))
    expect(await alertText()).toBe('A PIN is 4 to 6 digits')

    await type('PIN', '1234')
    await type('Confirm PIN', '1235')
    await fireEvent.click(screen.getByRole('button', { name: 'Set PIN' }))
    expect(await alertText()).toBe("The PINs don't match")
  })

  it('change and remove without a password both read "Enter your AdGuard password"', async () => {
    mockFetch({ 'GET /api/v1/pin': status(true) })
    render(Settings, { onBack: () => {} })
    await screen.findByText('This device has a PIN')

    await type('New PIN', '1111')
    await type('Confirm PIN', '1111')
    await fireEvent.click(screen.getByRole('button', { name: 'Change PIN' }))
    expect(await alertText()).toBe('Enter your AdGuard password')

    await fireEvent.click(screen.getByRole('button', { name: 'Remove PIN' }))
    expect(await alertText()).toBe('Enter your AdGuard password')
  })

  it('a first set notes "PIN set" and clears both PIN fields', async () => {
    mockFetch({ 'GET /api/v1/pin': status(false), 'POST /api/v1/pin': noContent })
    render(Settings, { onBack: () => {} })
    await screen.findByText('No PIN on this device')

    await type('PIN', '583920')
    await type('Confirm PIN', '583920')
    await fireEvent.click(screen.getByRole('button', { name: 'Set PIN' }))

    expect((await screen.findByRole('status')).textContent).toBe('PIN set')
    expect(value('New PIN')).toBe('')
    expect(value('Confirm PIN')).toBe('')
    expect(value('AdGuard password')).toBe('')
  })

  it('a change notes "PIN changed" and clears PIN, confirm and password', async () => {
    mockFetch({ 'GET /api/v1/pin': status(true), 'POST /api/v1/pin': noContent })
    render(Settings, { onBack: () => {} })
    await screen.findByText('This device has a PIN')

    await type('New PIN', '1111')
    await type('Confirm PIN', '1111')
    await type('AdGuard password', 'pw')
    await fireEvent.click(screen.getByRole('button', { name: 'Change PIN' }))

    expect((await screen.findByRole('status')).textContent).toBe('PIN changed')
    expect(value('New PIN')).toBe('')
    expect(value('Confirm PIN')).toBe('')
    expect(value('AdGuard password')).toBe('')
  })

  it('a remove notes "PIN removed" and clears both PIN fields', async () => {
    mockFetch({ 'GET /api/v1/pin': status(true), 'DELETE /api/v1/pin': noContent })
    render(Settings, { onBack: () => {} })
    await screen.findByText('This device has a PIN')

    await type('New PIN', '9999')
    await type('Confirm PIN', '9999')
    await type('AdGuard password', 'pw')
    await fireEvent.click(screen.getByRole('button', { name: 'Remove PIN' }))

    expect((await screen.findByRole('status')).textContent).toBe('PIN removed')
    expect(value('PIN')).toBe('')
    expect(value('Confirm PIN')).toBe('')
  })

  it('a failed first set keeps "No PIN", keeps what was typed and shows no note', async () => {
    mockFetch({
      'GET /api/v1/pin': status(false),
      'POST /api/v1/pin': () => json(500, { error: 'internal', message: 'boom' }),
    })
    render(Settings, { onBack: () => {} })
    await screen.findByText('No PIN on this device')

    await type('PIN', '583920')
    await type('Confirm PIN', '583920')
    await fireEvent.click(screen.getByRole('button', { name: 'Set PIN' }))

    expect((await screen.findByRole('alert')).getAttribute('data-error')).toBe('internal')
    expect(screen.getByText('No PIN on this device')).toBeTruthy()
    expect(value('PIN')).toBe('583920')
    expect(value('Confirm PIN')).toBe('583920')
    expect(screen.queryByRole('status')).toBeNull()
    // The status did load: a save failure never offers Try again.
    expect(tryAgain()).toBeNull()
  })

  it('a failed remove keeps the PIN, the password and shows no note', async () => {
    mockFetch({
      'GET /api/v1/pin': status(true),
      'DELETE /api/v1/pin': () => json(500, { error: 'internal', message: 'boom' }),
    })
    render(Settings, { onBack: () => {} })
    await screen.findByText('This device has a PIN')

    await type('AdGuard password', 'pw')
    await fireEvent.click(screen.getByRole('button', { name: 'Remove PIN' }))

    expect((await screen.findByRole('alert')).getAttribute('data-error')).toBe('internal')
    expect(screen.getByText('This device has a PIN')).toBeTruthy()
    expect(value('AdGuard password')).toBe('pw')
    expect(screen.queryByRole('status')).toBeNull()
    expect(tryAgain()).toBeNull()
  })

  it('a network failure shows data-error=network with the connection message', async () => {
    mockFetch({
      'GET /api/v1/pin': status(false),
      'POST /api/v1/pin': () => Promise.reject(new TypeError('Failed to fetch')),
    })
    render(Settings, { onBack: () => {} })
    await screen.findByText('No PIN on this device')

    await type('PIN', '1234')
    await type('Confirm PIN', '1234')
    await fireEvent.click(screen.getByRole('button', { name: 'Set PIN' }))

    const alert = await screen.findByRole('alert')
    expect(alert.getAttribute('data-error')).toBe('network')
    expect(alert.textContent).toBe("Can't reach the server — check your connection")
  })

  it('while a save is in flight the inputs and buttons are disabled; they re-enable after', async () => {
    const save = held()
    mockFetch({ 'GET /api/v1/pin': status(true), 'POST /api/v1/pin': save.answer })
    render(Settings, { onBack: () => {} })
    await screen.findByText('This device has a PIN')

    await type('New PIN', '1111')
    await type('Confirm PIN', '1111')
    await type('AdGuard password', 'pw')
    await fireEvent.click(screen.getByRole('button', { name: 'Change PIN' }))

    const change = screen.getByRole('button', { name: 'Change PIN' }) as HTMLButtonElement
    const remove = screen.getByRole('button', { name: 'Remove PIN' }) as HTMLButtonElement
    await vi.waitFor(() => expect(change.disabled).toBe(true))
    expect(remove.disabled).toBe(true)
    for (const label of ['New PIN', 'Confirm PIN', 'AdGuard password'])
      expect((screen.getByLabelText(label) as HTMLInputElement).disabled).toBe(true)

    save.release(noContent())
    expect((await screen.findByRole('status')).textContent).toBe('PIN changed')
    expect(change.disabled).toBe(false)
    expect(remove.disabled).toBe(false)
  })

  it('Try again hides while the reload is in flight and returns if it fails again', async () => {
    const reload = held()
    let n = 0
    mockFetch({
      'GET /api/v1/pin': (call) => (++n === 2 ? reload.answer(call) : json(502, { error: 'internal', message: 'boom' })),
    })
    render(Settings, { onBack: () => {} })
    await screen.findByRole('alert')
    expect(tryAgain()).toBeTruthy()

    await fireEvent.click(tryAgain()!)
    await vi.waitFor(() => expect(tryAgain()).toBeNull())
    expect(screen.queryByRole('alert')).toBeNull()

    reload.release(json(502, { error: 'internal', message: 'boom' }))
    await vi.waitFor(() => expect(tryAgain()).toBeTruthy())
    expect(screen.queryByRole('button', { name: 'Set PIN' })).toBeNull()
  })
})
