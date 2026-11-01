// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { afterEach, describe, expect, it, vi } from 'vitest'
import UnlabelledDevices from './UnlabelledDevices.svelte'
import type { Child, UnlabelledDevice } from './api'

type Call = { method: string; url: string; body: string | null }

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function mockFetch(answer: () => Response | Promise<Response>): Call[] {
  const calls: Call[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit) => {
      calls.push({ method: init?.method ?? 'GET', url, body: typeof init?.body === 'string' ? init.body : null })
      return answer()
    }),
  )
  return calls
}

const printer: UnlabelledDevice = {
  ip: '192.168.1.70',
  hostname: 'printer',
  source: 'ARP',
  suggested_ids: ['192.168.1.70', 'aa:bb:cc:dd:ee:70'],
}
const blank: UnlabelledDevice = { ip: '192.168.1.40', hostname: '', source: 'WHOIS', suggested_ids: ['192.168.1.40'] }
const alpha: UnlabelledDevice = { ip: '192.168.1.20', hostname: 'alpha', source: 'DHCP', suggested_ids: ['192.168.1.20'] }
const kids: Child[] = [
  { id: 1, name: 'Ada', clients: [] },
  { id: 2, name: 'Ben', clients: [] },
]
const created = { name: 'printer', ids: ['192.168.1.70'], use_global_blocked_services: true, child: null }

function row(ip: string): HTMLFormElement {
  const el = document.querySelector(`form[data-device="${ip}"]`)
  if (!el) throw new Error(`no row ${ip}`)
  return el as HTMLFormElement
}

function field(f: HTMLElement, label: string): HTMLInputElement {
  const el = f.querySelector(`[aria-label="${label}"]`)
  if (!el) throw new Error(`no field ${label}`)
  return el as HTMLInputElement
}

function save(f: HTMLElement): HTMLButtonElement {
  return f.querySelector('button[type="submit"]') as HTMLButtonElement
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('UnlabelledDevices', () => {
  it('prefills name from hostname and ids joined', async () => {
    mockFetch(() => json(201, created))
    render(UnlabelledDevices, { devices: [printer, blank], children: kids, onCreated: vi.fn() })
    expect(field(row(printer.ip), 'Device name').value).toBe('printer')
    expect(field(row(printer.ip), 'Device ids').value).toBe('192.168.1.70, aa:bb:cc:dd:ee:70')
    expect(field(row(printer.ip), 'Child').value).toBe('')
    expect(save(row(printer.ip)).disabled).toBe(false)

    expect(field(row(blank.ip), 'Device name').value).toBe('')
    expect(save(row(blank.ip)).disabled).toBe(true)
    await fireEvent.submit(row(blank.ip))
    expect(vi.mocked(fetch)).not.toHaveBeenCalled()
  })

  it('renders rows in the order given', () => {
    mockFetch(() => json(201, created))
    render(UnlabelledDevices, { devices: [printer, blank, alpha], children: kids, onCreated: vi.fn() })
    const order = [...document.querySelectorAll('form[data-device]')].map((f) => f.getAttribute('data-device'))
    expect(order).toEqual([printer.ip, blank.ip, alpha.ip])
  })

  it('posts the edited ids only', async () => {
    const calls = mockFetch(() => json(201, created))
    const onCreated = vi.fn()
    render(UnlabelledDevices, { devices: [printer], children: kids, onCreated })
    await fireEvent.input(field(row(printer.ip), 'Device ids'), { target: { value: '192.168.1.70' } })
    await fireEvent.submit(row(printer.ip))
    await waitFor(() => expect(onCreated).toHaveBeenCalledTimes(1))
    expect(calls).toHaveLength(1)
    expect(calls[0].method).toBe('POST')
    expect(calls[0].url).toBe('/api/v1/clients')
    expect(JSON.parse(calls[0].body!)).toEqual({ name: 'printer', ids: ['192.168.1.70'], child_id: null })
  })

  it('sends the picked child and blank ids block submit', async () => {
    const calls = mockFetch(() => json(201, created))
    const onCreated = vi.fn()
    render(UnlabelledDevices, { devices: [printer], children: kids, onCreated })
    await fireEvent.input(field(row(printer.ip), 'Device ids'), { target: { value: ' , ' } })
    expect(save(row(printer.ip)).disabled).toBe(true)
    await fireEvent.input(field(row(printer.ip), 'Device ids'), { target: { value: '192.168.1.70' } })
    await fireEvent.input(field(row(printer.ip), 'Device name'), { target: { value: '  Office printer ' } })
    await fireEvent.change(field(row(printer.ip), 'Child'), { target: { value: '2' } })
    await fireEvent.submit(row(printer.ip))
    await waitFor(() => expect(onCreated).toHaveBeenCalled())
    expect(JSON.parse(calls[0].body!)).toEqual({ name: 'Office printer', ids: ['192.168.1.70'], child_id: 2 })
  })

  it('two rapid submits issue one POST', async () => {
    let release!: () => void
    const held = new Promise<void>((r) => (release = r))
    const calls = mockFetch(async () => {
      await held
      return json(201, created)
    })
    const onCreated = vi.fn()
    render(UnlabelledDevices, { devices: [printer], children: kids, onCreated })
    await fireEvent.submit(row(printer.ip))
    await fireEvent.submit(row(printer.ip))
    expect(save(row(printer.ip)).disabled).toBe(true)
    release()
    await waitFor(() => expect(onCreated).toHaveBeenCalledTimes(1))
    expect(calls).toHaveLength(1)
  })

  it('a 409 shows the server message and keeps the typed values', async () => {
    mockFetch(() => json(409, { error: 'conflict', message: 'a client named "TV" already exists' }))
    const onCreated = vi.fn()
    render(UnlabelledDevices, { devices: [printer], children: kids, onCreated })
    await fireEvent.input(field(row(printer.ip), 'Device name'), { target: { value: 'TV' } })
    await fireEvent.input(field(row(printer.ip), 'Device ids'), { target: { value: '192.168.1.70' } })
    await fireEvent.submit(row(printer.ip))
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toBe('a client named "TV" already exists')
    expect(field(row(printer.ip), 'Device name').value).toBe('TV')
    expect(field(row(printer.ip), 'Device ids').value).toBe('192.168.1.70')
    expect(save(row(printer.ip)).disabled).toBe(false)
    expect(onCreated).not.toHaveBeenCalled()
  })

  it('a network failure shows the offline line', async () => {
    mockFetch(() => {
      throw new TypeError('Failed to fetch')
    })
    render(UnlabelledDevices, { devices: [printer], children: kids, onCreated: vi.fn() })
    await fireEvent.submit(row(printer.ip))
    expect((await screen.findByRole('alert')).textContent).toContain("Can't reach the server")
  })

  it('a whitespace-only name blocks submit', async () => {
    mockFetch(() => json(201, created))
    render(UnlabelledDevices, { devices: [printer], children: kids, onCreated: vi.fn() })
    await fireEvent.input(field(row(printer.ip), 'Device name'), { target: { value: '   ' } })
    expect(save(row(printer.ip)).disabled).toBe(true)
    await fireEvent.submit(row(printer.ip))
    expect(vi.mocked(fetch)).not.toHaveBeenCalled()
  })

  it('fields are editable when idle and locked while a create is out', async () => {
    let release!: () => void
    const held = new Promise<void>((r) => (release = r))
    mockFetch(async () => {
      await held
      return json(201, created)
    })
    const onCreated = vi.fn()
    render(UnlabelledDevices, { devices: [printer, alpha], children: kids, onCreated })
    for (const label of ['Device name', 'Device ids', 'Child']) {
      expect(field(row(alpha.ip), label).disabled, label).toBe(false)
    }
    await fireEvent.submit(row(printer.ip))
    for (const label of ['Device name', 'Device ids', 'Child']) {
      expect(field(row(alpha.ip), label).disabled, label).toBe(true)
    }
    release()
    await waitFor(() => expect(onCreated).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(field(row(alpha.ip), 'Device name').disabled).toBe(false))
  })

  it('the disabled prop locks every field', () => {
    mockFetch(() => json(201, created))
    render(UnlabelledDevices, { devices: [printer], children: kids, disabled: true, onCreated: vi.fn() })
    for (const label of ['Device name', 'Device ids', 'Child']) {
      expect(field(row(printer.ip), label).disabled, label).toBe(true)
    }
    expect(save(row(printer.ip)).disabled).toBe(true)
  })

  it('the child picker starts on No child and keeps the pick when the row returns', async () => {
    mockFetch(() => json(201, created))
    const { rerender } = render(UnlabelledDevices, { devices: [printer], children: kids, onCreated: vi.fn() })
    const picker = () => field(row(printer.ip), 'Child') as unknown as HTMLSelectElement
    expect(picker().value).toBe('')
    expect(picker().options[0].selected).toBe(true)

    await fireEvent.change(picker(), { target: { value: '1' } })
    expect(picker().value).toBe('1')

    await rerender({ devices: [] })
    await rerender({ devices: [printer] })
    expect(picker().value).toBe('1')
    expect(picker().options[0].selected).toBe(false)
  })

  it('shows the connect-to-network sentence only when empty', () => {
    mockFetch(() => json(201, created))
    const { unmount } = render(UnlabelledDevices, { devices: [], children: kids, onCreated: vi.fn() })
    expect(screen.getByText(/must connect to the network and appear in AdGuard Home before it can be named/)).toBeTruthy()
    unmount()
    render(UnlabelledDevices, { devices: [printer], children: kids, onCreated: vi.fn() })
    expect(screen.queryByText(/must connect to the network/)).toBeNull()
  })
})
