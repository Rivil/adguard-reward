// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen, within } from '@testing-library/svelte'
import { get } from 'svelte/store'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from '../App.svelte'
import Settings from './Settings.svelte'
import { CSRF_HEADER, CSRF_VALUE, historyChild, locked, navigate, route } from './api'

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

const EXPORT = '/api/v1/export'
const IMPORT = '/api/v1/import'

const backup = () => within(screen.getByRole('region', { name: 'Backup' }))
const pinSection = () => within(screen.getByRole('region', { name: 'Device PIN' }))
const posts = (calls: Call[]) => calls.filter((c) => c.method === 'POST' && c.url === IMPORT)
const imported = (missing: string[] = [], rulesApplied = true) =>
  () => json(200, { missing_clients: missing, rules_applied: rulesApplied })
const backupFile = (text = '{"format":"adguard-reward-export"}') =>
  new File([text], 'backup.json', { type: 'application/json' })

/** Render Settings and wait for the PIN status; routes stub everything else. */
async function renderBackup(routes: Record<string, Answer> = {}): Promise<Call[]> {
  const calls = mockFetch({ 'GET /api/v1/pin': status(true), ...routes })
  render(Settings, { onBack: () => {} })
  await screen.findByText('This device has a PIN')
  return calls
}

/**
 * Model the browser's file picker on the Backup input: choosing the file
 * already selected fires no change, and only value = '' forgets the selection.
 */
function filePicker(): (file: File | null) => Promise<void> {
  const input = screen.getByLabelText('Import a file')
  let selected: File | null = null
  Object.defineProperty(input, 'files', { configurable: true, get: () => (selected ? [selected] : []) })
  Object.defineProperty(input, 'value', {
    configurable: true,
    get: () => (selected ? `C:\\fakepath\\${selected.name}` : ''),
    set: (v: string) => {
      if (v === '') selected = null
    },
  })
  return async (file) => {
    if (file !== null && file === selected) return
    selected = file
    await fireEvent.change(input)
  }
}

describe('Settings — Backup', () => {
  it('fetches nothing on mount', async () => {
    const calls = await renderBackup()
    expect(calls).toEqual([{ method: 'GET', url: '/api/v1/pin', body: undefined }])
    expect(backup().queryByRole('alert')).toBeNull()
    expect(backup().queryByRole('status')).toBeNull()
  })

  describe('export', () => {
    const urlApi = { createObjectURL: URL.createObjectURL, revokeObjectURL: URL.revokeObjectURL }
    const file = { format: 'adguard-reward-export', version: 1, children: [{ name: 'Ada', clients: ['Kid phone'] }] }

    /** Stub the Blob URL calls jsdom lacks, and record what each link click would save. */
    function stubDownload() {
      const create = vi.fn((_: Blob) => 'blob:export')
      const revoke = vi.fn((_: string) => {})
      Object.assign(URL, { createObjectURL: create, revokeObjectURL: revoke })
      const saved: Array<{ name: string; href: string }> = []
      vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
        saved.push({ name: this.download, href: this.href })
      })
      return { create, revoke, saved }
    }

    afterEach(() => {
      vi.useRealTimers()
      vi.restoreAllMocks()
      Object.assign(URL, urlApi)
    })

    it('GETs the export, saves one named Blob and revokes the URL once the save has had time', async () => {
      const { create, revoke, saved } = stubDownload()
      const calls = await renderBackup({ [`GET ${EXPORT}`]: () => json(200, file) })
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })

      await fireEvent.click(backup().getByRole('button', { name: 'Export' }))
      await vi.waitFor(() => expect(saved).toHaveLength(1))

      expect(calls.filter((c) => c.url === EXPORT)).toEqual([{ method: 'GET', url: EXPORT, body: undefined }])
      expect(saved).toEqual([{ name: 'adguard-reward-export.json', href: 'blob:export' }])
      expect(create).toHaveBeenCalledTimes(1)
      const blob = create.mock.calls[0][0]
      expect(blob.type).toBe('application/json')
      expect(JSON.parse(await blob.text())).toStrictEqual(file)

      expect(revoke).not.toHaveBeenCalled()
      vi.runOnlyPendingTimers()
      expect(revoke.mock.calls).toEqual([['blob:export']])
      await vi.waitFor(() => expect(backup().getByRole('status').textContent).toContain('adguard-reward-export.json'))
    })

    it('a failed export shows the alert and saves nothing', async () => {
      const { create, saved } = stubDownload()
      await renderBackup({ [`GET ${EXPORT}`]: () => json(500, { error: 'internal', message: 'export failed' }) })

      await fireEvent.click(backup().getByRole('button', { name: 'Export' }))

      const alert = await backup().findByRole('alert')
      expect(alert.textContent).toBe('export failed')
      expect(alert.getAttribute('data-error')).toBe('internal')
      expect(create).not.toHaveBeenCalled()
      expect(saved).toHaveLength(0)
      expect(backup().queryByRole('status')).toBeNull()
    })

    it('a 401 pin_required on export sets locked and saves nothing', async () => {
      const { create } = stubDownload()
      await renderBackup({ [`GET ${EXPORT}`]: () => json(401, { error: 'pin_required', message: 'enter the PIN' }) })

      await fireEvent.click(backup().getByRole('button', { name: 'Export' }))

      await vi.waitFor(() => expect(get(locked)).toBe(true))
      expect(create).not.toHaveBeenCalled()
    })

    it('while an export is in flight Export and the file input are disabled', async () => {
      stubDownload()
      const answer = held()
      await renderBackup({ [`GET ${EXPORT}`]: answer.answer })
      const button = backup().getByRole('button', { name: 'Export' }) as HTMLButtonElement
      const input = backup().getByLabelText('Import a file') as HTMLInputElement

      await fireEvent.click(button)
      await vi.waitFor(() => expect(button.disabled).toBe(true))
      expect(input.disabled).toBe(true)

      answer.release(json(200, file))
      await vi.waitFor(() => expect(button.disabled).toBe(false))
      expect(input.disabled).toBe(false)
    })
  })

  describe('import', () => {
    it('POSTs exactly the bytes of the chosen file with the CSRF header', async () => {
      const calls = await renderBackup({ [`POST ${IMPORT}`]: imported() })
      const pick = filePicker()

      await pick(backupFile('{not json'))

      await vi.waitFor(() => expect(writes(calls)).toEqual([{ method: 'POST', url: IMPORT, body: '{not json' }]))
      const [, init] = vi.mocked(fetch).mock.calls.at(-1) as [string, RequestInit]
      expect((init.headers as Record<string, string>)[CSRF_HEADER]).toBe(CSRF_VALUE)
      expect(await backup().findByRole('status')).toBeTruthy()
    })

    it('closing the picker with no file sends nothing', async () => {
      const calls = await renderBackup()
      await filePicker()(null)
      expect(writes(calls)).toHaveLength(0)
      expect(backup().queryByRole('alert')).toBeNull()
    })

    it('a 401 pin_required on import sets locked', async () => {
      await renderBackup({ [`POST ${IMPORT}`]: () => json(401, { error: 'pin_required', message: 'enter the PIN' }) })
      await filePicker()(backupFile())
      await vi.waitFor(() => expect(get(locked)).toBe(true))
    })

    it('picking the same file again after a failure re-fires the import', async () => {
      let up = false
      const calls = await renderBackup({
        [`POST ${IMPORT}`]: () => (up ? imported()() : json(422, { error: 'unprocessable', message: 'bad file' })),
      })
      const pick = filePicker()
      const file = backupFile()

      await pick(file)
      expect(await backup().findByRole('alert')).toBeTruthy()

      up = true
      await pick(file)
      expect((await backup().findByRole('status')).textContent).toContain('Setup imported')
      expect(posts(calls)).toHaveLength(2)
      expect(backup().queryByRole('alert')).toBeNull()
    })

    it('a second pick during an in-flight import issues no second POST', async () => {
      const first = held()
      let n = 0
      const calls = await renderBackup({
        [`POST ${IMPORT}`]: (call) => (++n === 1 ? first.answer(call) : imported()()),
      })
      const pick = filePicker()
      const input = backup().getByLabelText('Import a file') as HTMLInputElement
      const other = backupFile('{"second":true}')

      await pick(backupFile())
      await vi.waitFor(() => expect(posts(calls)).toHaveLength(1))
      expect(input.disabled).toBe(true)
      expect((backup().getByRole('button', { name: 'Export' }) as HTMLButtonElement).disabled).toBe(true)

      await pick(other)
      // Let a wrongly admitted pick read its file and reach fetch.
      await new Promise((resolve) => setTimeout(resolve, 20))
      expect(posts(calls)).toHaveLength(1)

      first.release(imported()())
      await backup().findByRole('status')
      expect(input.disabled).toBe(false)

      // The refused pick left nothing selected, so the same file goes through now.
      await pick(other)
      await vi.waitFor(() => expect(posts(calls)).toHaveLength(2))
      expect(posts(calls)[1].body).toBe('{"second":true}')
      await backup().findByRole('status')
    })

    it('two picks in one tick send one POST', async () => {
      const calls = await renderBackup({ [`POST ${IMPORT}`]: imported() })
      const pick = filePicker()
      await Promise.all([pick(backupFile('{"a":1}')), pick(backupFile('{"b":2}'))])
      await backup().findByRole('status')
      expect(posts(calls).map((c) => c.body)).toEqual(['{"a":1}'])
    })

    it.each([
      [422, 'unprocessable', 'buttons[0].child: "Ben" is not in the file'],
      [409, 'conflict', 'this install already has 1 child and 2 buttons'],
      [400, 'bad_request', 'invalid JSON at offset 1'],
    ])('a %i shows the server message in Backup, not in the PIN section', async (code, error, message) => {
      await renderBackup({ [`POST ${IMPORT}`]: () => json(code, { error, message }) })
      await filePicker()(backupFile())

      const alert = await backup().findByRole('alert')
      expect(alert.textContent).toBe(message)
      expect(alert.getAttribute('data-error')).toBe(error)
      expect(pinSection().queryByRole('alert')).toBeNull()
      expect(backup().queryByRole('status')).toBeNull()
    })

    it('a PIN failure stays out of Backup', async () => {
      await renderBackup({ 'POST /api/v1/pin': () => json(500, { error: 'internal', message: 'boom' }) })
      await type('New PIN', '1111')
      await type('Confirm PIN', '1111')
      await type('AdGuard password', 'pw')
      await fireEvent.click(screen.getByRole('button', { name: 'Change PIN' }))

      expect(await pinSection().findByRole('alert')).toBeTruthy()
      expect(backup().queryByRole('alert')).toBeNull()
    })

    it('a 502 keeps the server message and says nothing was imported and a retry is safe', async () => {
      await renderBackup({
        [`POST ${IMPORT}`]: () =>
          json(502, { error: 'adguard_unavailable', message: 'AdGuard refused the update for client "Kid tablet"' }),
      })
      await filePicker()(backupFile())

      const alert = await backup().findByRole('alert')
      expect(alert.textContent).toContain('Kid tablet')
      expect(alert.textContent).toContain('nothing was imported')
      expect(alert.textContent).toContain("it's safe to try again")
      expect(alert.getAttribute('data-error')).toBe('adguard_unavailable')
      expect(pinSection().queryByRole('alert')).toBeNull()
    })

    it('a network failure shows the connection message without claiming a retry is safe', async () => {
      await renderBackup({ [`POST ${IMPORT}`]: () => Promise.reject(new TypeError('Failed to fetch')) })
      await filePicker()(backupFile())

      const alert = await backup().findByRole('alert')
      expect(alert.getAttribute('data-error')).toBe('network')
      expect(alert.textContent).toBe("Can't reach the server — check your connection")
    })

    it('an unreadable file shows its own alert and sends nothing', async () => {
      const calls = await renderBackup()
      const file = backupFile()
      vi.spyOn(file, 'text').mockRejectedValue(new DOMException('gone', 'NotReadableError'))

      await filePicker()(file)

      const alert = await backup().findByRole('alert')
      expect(alert.getAttribute('data-error')).toBe('unreadable_file')
      expect(alert.textContent).toBe("Can't read that file — choose it again")
      expect(writes(calls)).toHaveLength(0)
      expect((backup().getByLabelText('Import a file') as HTMLInputElement).disabled).toBe(false)
    })

    it('a 200 with missing clients lists each name', async () => {
      await renderBackup({ [`POST ${IMPORT}`]: imported(['Gone phone', 'Kid tablet']) })
      await filePicker()(backupFile())

      const note = await backup().findByRole('status')
      expect(note.textContent).toContain('Setup imported')
      expect(note.textContent).not.toContain('catch up')
      expect(within(note).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['Gone phone', 'Kid tablet'])
    })

    it('an empty missing list shows no warning', async () => {
      await renderBackup({ [`POST ${IMPORT}`]: imported() })
      await filePicker()(backupFile())

      const note = await backup().findByRole('status')
      expect(note.textContent?.trim()).toBe('Setup imported')
      expect(within(note).queryByRole('list')).toBeNull()
    })

    it('rules_applied false shows the catch-up note', async () => {
      await renderBackup({ [`POST ${IMPORT}`]: imported([], false) })
      await filePicker()(backupFile())
      expect((await backup().findByRole('status')).textContent?.trim()).toBe('Setup imported — AdGuard will catch up')
    })

    it('a new import clears the last result', async () => {
      let up = true
      await renderBackup({
        [`POST ${IMPORT}`]: () =>
          up ? imported(['Gone phone'])() : json(409, { error: 'conflict', message: 'this install is not empty' }),
      })
      const pick = filePicker()
      await pick(backupFile())
      await backup().findByRole('status')

      up = false
      await pick(backupFile())
      expect(await backup().findByRole('alert')).toBeTruthy()
      expect(backup().queryByRole('status')).toBeNull()
      expect(backup().queryByRole('listitem')).toBeNull()
    })
  })
})
