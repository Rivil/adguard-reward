// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { get } from 'svelte/store'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Home from './Home.svelte'
import { route, setOnUnauthorized, type BlockedView, type Button, type Child, type Grant, type Service } from './api'

type Call = { method: string; url: string; body: string | null }
type Answer = () => Response | Promise<Response>
/** Keyed by "METHOD /path" or just "/path"; the method form wins. */
type Routes = Record<string, Answer>

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

/**
 * A URL-keyed fetch stub: components may fetch in any order and extra
 * requests from later components cannot reorder a queue.
 */
function mockFetch(routes: Routes): Call[] {
  const calls: Call[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit) => {
      const method = init?.method ?? 'GET'
      calls.push({ method, url, body: typeof init?.body === 'string' ? init.body : null })
      const answer = routes[`${method} ${url}`] ?? routes[url]
      if (!answer) throw new Error('unexpected fetch ' + method + ' ' + url)
      return answer()
    }),
  )
  return calls
}

const ICON = 'PHN2Zy8+'
const YT_ICON = 'WVQ='
const T0 = Date.parse('2026-09-21T10:00:00Z')

const ada: Child = { id: 1, name: 'Ada', clients: ['Kid phone'] }
const ben: Child = { id: 2, name: 'Ben', clients: ['Kid tablet'] }
const services: Service[] = [
  { id: 'youtube', name: 'YouTube', icon: YT_ICON },
  { id: 'tiktok', name: 'TikTok', icon: ICON },
  { id: 'roblox', name: 'Roblox', icon: ICON },
]
const b1: Button = { id: 11, label: 'YouTube + TikTok', child_id: 1, services: ['youtube', 'tiktok'], duration: 3600 }
const b2: Button = { id: 12, label: 'Roblox', child_id: 1, services: ['roblox'], duration: 1800 }
const b3: Button = { id: 13, label: 'TikTok', child_id: 2, services: ['tiktok'], duration: 900 }

function view(id: number, name: string, over: Partial<BlockedView> = {}): BlockedView {
  return {
    child: { id, name },
    clients: [{ name: 'Kid phone', missing: false, uses_global: false }],
    services: [
      { id: 'youtube', name: 'YouTube', icon: ICON, state: 'blocked', differs: [] },
      { id: 'tiktok', name: 'TikTok', icon: ICON, state: 'blocked', differs: [] },
      { id: 'roblox', name: 'Roblox', icon: ICON, state: 'unblocked', differs: [] },
    ],
    ...over,
  }
}

const noOffer = () => json(200, { global: [], clients: [] })

/** A mutable server for Home: grants and buttons are read live from `state`. */
function server(over: { buttons?: Button[]; grants?: Grant[]; children?: Child[]; services?: Service[] | 'down' } = {}, extra: Routes = {}) {
  const state = {
    buttons: over.buttons ?? [b3, b1, b2],
    grants: over.grants ?? [],
    children: over.children ?? [ada, ben],
  }
  const routes: Routes = {
    '/api/v1/migration': noOffer,
    '/api/v1/children': () => json(200, { children: state.children }),
    '/api/v1/buttons': () => json(200, { buttons: state.buttons }),
    '/api/v1/services':
      over.services === 'down'
        ? () => json(502, { error: 'adguard_unavailable', message: 'down' })
        : () => json(200, { services: over.services ?? services }),
    '/api/v1/grants': () => json(200, { grants: state.grants }),
    '/api/v1/children/1/blocked': () => json(200, view(1, 'Ada')),
    '/api/v1/children/2/blocked': () => json(200, view(2, 'Ben')),
    ...extra,
  }
  const calls = mockFetch(routes)
  return { state, calls }
}

const grantGets = (calls: Call[]) => calls.filter((c) => c.method === 'GET' && c.url === '/api/v1/grants').length
const posts = (calls: Call[]) => calls.filter((c) => c.method === 'POST' && c.url.startsWith('/api/v1/grants'))

function section(id: number): HTMLElement {
  const el = document.querySelector(`section[data-child="${id}"]`)
  if (!el) throw new Error(`no section for child ${id}`)
  return el as HTMLElement
}

function tile(id: number): HTMLButtonElement {
  const el = document.querySelector(`button[data-button="${id}"]`)
  if (!el) throw new Error(`no button ${id}`)
  return el as HTMLButtonElement
}

function details(selector: string): HTMLDetailsElement {
  const el = document.querySelector(selector)
  if (!el) throw new Error(`no ${selector}`)
  return el as HTMLDetailsElement
}

async function open(d: HTMLDetailsElement) {
  d.open = true
  await fireEvent(d, new Event('toggle'))
}

/** Let fetch promise chains finish under fake timers. */
async function settle() {
  for (let i = 0; i < 8; i++) await vi.advanceTimersByTimeAsync(0)
}

function mount() {
  return render(Home, { username: 'mum', onLogout: vi.fn() })
}

async function mountLoaded() {
  const r = mount()
  await screen.findByRole('heading', { name: 'Ada' })
  return r
}

function checkbox(label: string): HTMLInputElement {
  for (const l of document.querySelectorAll('[data-adhoc] fieldset.grant-form label')) {
    if (l.textContent?.replace(/\s+/g, ' ').trim().startsWith(label)) {
      const cb = l.querySelector('input[type="checkbox"]')
      if (cb) return cb as HTMLInputElement
    }
  }
  throw new Error(`no checkbox labelled ${label}`)
}

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  history.replaceState(null, '', '/')
  route.set('home')
})

describe('Home', () => {
  it('renders buttons per child in stored order', async () => {
    server({ buttons: [b3, b1, b2, { ...b2, id: 99, child_id: 99 }] })
    await mountLoaded()
    await waitFor(() => expect(document.querySelectorAll('button[data-button]').length).toBe(3))
    const adaIds = [...section(1).querySelectorAll('button[data-button]')].map((b) => b.getAttribute('data-button'))
    expect(adaIds).toEqual(['11', '12'])
    const benIds = [...section(2).querySelectorAll('button[data-button]')].map((b) => b.getAttribute('data-button'))
    expect(benIds).toEqual(['13'])
    await waitFor(() => expect(tile(11).querySelector('img')?.getAttribute('src')).toBe('data:image/svg+xml;base64,' + YT_ICON))
    expect(tile(11).textContent).toContain('YouTube + TikTok')
    expect(tile(11).textContent).toContain('1 h')
    expect(tile(13).textContent).toContain('15 min')
    expect(document.querySelector('button[data-button="99"]')).toBeNull()
  })

  it('a button whose first service is unknown renders no icon', async () => {
    server({ buttons: [{ ...b1, services: ['gone', 'youtube'] }] })
    await mountLoaded()
    await waitFor(() => expect(document.querySelector('[data-adhoc] fieldset')).toBeTruthy())
    expect(tile(11).querySelector('img')).toBeNull()
  })

  it('layout order', async () => {
    server()
    await mountLoaded()
    const panel = document.querySelector('section[aria-label="Active grants"]')!
    for (const s of document.querySelectorAll('section[data-child]')) {
      expect(panel.compareDocumentPosition(s) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    }
    const s1 = section(1)
    const h2 = s1.querySelector('h2')!
    const firstTile = s1.querySelector('button[data-button]')!
    const blocked = s1.querySelector('details[data-blocked]') as HTMLDetailsElement
    expect(h2.compareDocumentPosition(firstTile) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(firstTile.compareDocumentPosition(blocked) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(blocked.open).toBe(false)
    expect(details('[data-adhoc]').open).toBe(false)
  })

  it('tap posts the grant immediately', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(T0)
    let releaseGet: (() => void) | null = null
    let grantGetCount = 0
    const s = server(
      {},
      {
        '/api/v1/grants': () => {
          grantGetCount++
          if (grantGetCount === 1) return json(200, { grants: [] })
          return new Promise<Response>((r) => {
            releaseGet = () => r(json(200, { grants: s.state.grants }))
          })
        },
        'POST /api/v1/grants': () => json(201, { id: 42, ends_at: new Date(T0 + 3600 * 1000).toISOString(), applied: true, failed: [] }),
      },
    )
    mount()
    await settle()
    expect(screen.getByRole('heading', { name: 'Ada' })).toBeTruthy()

    await fireEvent.click(tile(11))
    expect(tile(11).disabled).toBe(true)
    await settle()
    expect(posts(s.calls).map((c) => c.body)).toEqual(['{"child_id":1,"services":["youtube","tiktok"],"duration":3600}'])
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    const li = document.querySelector('li[data-grant="42"]')
    expect(li).not.toBeNull()
    expect(li?.querySelector('[data-countdown]')?.textContent?.trim()).toBe('1:00:00')
    expect(li?.textContent).toContain('Ada')
    expect(li?.textContent).toContain('YouTube')
    expect(releaseGet).not.toBeNull()

    s.state.grants = [
      { id: 42, child_id: 1, services: ['tiktok', 'youtube'], clients: ['Kid phone'], started_at: new Date(T0).toISOString(), ends_at: new Date(T0 + 3600 * 1000).toISOString() },
    ]
    releaseGet!()
    await settle()
    expect(tile(11).disabled).toBe(false)
    expect(document.querySelector('li[data-grant="42"]')).not.toBeNull()
  })

  it('outcomes render next to the child', async () => {
    let mode: 'partial' | 'network' | 'down' = 'partial'
    const s = server(
      {},
      {
        'POST /api/v1/grants': () => {
          if (mode === 'partial') {
            const ends_at = new Date(Date.now() + 1800 * 1000).toISOString()
            s.state.grants = [{ id: 43, child_id: 1, services: ['roblox'], clients: ['Kid phone'], started_at: new Date().toISOString(), ends_at }]
            return json(201, { id: 43, ends_at, applied: false, failed: ['Kid tablet'] })
          }
          if (mode === 'network') throw new TypeError('Failed to fetch')
          return json(502, { error: 'adguard_unavailable', message: 'down' })
        },
      },
    )
    await mountLoaded()
    await waitFor(() => expect(tile(12)).toBeTruthy())

    await fireEvent.click(tile(12))
    const alert = await waitFor(() => {
      const a = section(1).querySelector('[role="alert"]')
      if (!a) throw new Error('no alert yet')
      return a
    })
    expect(alert.getAttribute('data-error')).toBe('partial')
    expect(alert.textContent).toContain('Kid tablet')
    expect(document.querySelector('li[data-grant="43"]')).not.toBeNull()
    expect(section(2).querySelector('[role="alert"]')).toBeNull()

    mode = 'network'
    s.state.grants = []
    await fireEvent.click(tile(12))
    await waitFor(() => expect(section(1).querySelector('[role="alert"]')?.getAttribute('data-error')).toBe('network'))

    mode = 'down'
    await fireEvent.click(tile(12))
    await waitFor(() => expect(section(1).querySelector('[role="alert"]')?.getAttribute('data-error')).toBe('adguard_unavailable'))
    expect(section(1).querySelector('[role="alert"]')?.textContent).toContain("Can't reach AdGuard Home")
  })

  it('409 offers extend and Extend delivers', async () => {
    const existing: Grant = {
      id: 5,
      child_id: 1,
      services: ['youtube'],
      clients: ['Kid phone'],
      started_at: new Date(Date.now() - 18 * 60 * 1000).toISOString(),
      ends_at: new Date(Date.now() + 42 * 60 * 1000).toISOString(),
    }
    let extended = false
    const s = server(
      { grants: [] },
      {
        'POST /api/v1/grants': () => {
          const last = s.calls[s.calls.length - 1]
          const body = JSON.parse(last.body ?? '{}') as { services: string[] }
          if (body.services.includes('youtube') && !extended) {
            s.state.grants = [existing]
            return json(409, { error: 'conflict', message: 'youtube is already granted by grant 5', grant_id: 5 })
          }
          return json(201, { id: 44, ends_at: new Date(Date.now() + 3600 * 1000).toISOString(), applied: true, failed: [] })
        },
        'POST /api/v1/grants/5/extend': () => {
          extended = true
          return json(200, { id: 5, ends_at: new Date(Date.now() + 102 * 60 * 1000).toISOString() })
        },
      },
    )
    await mountLoaded()
    await waitFor(() => expect(tile(11)).toBeTruthy())

    await fireEvent.click(tile(11))
    const dialog = await screen.findByRole('dialog')
    expect(dialog.getAttribute('data-offer')).not.toBeNull()
    expect(dialog.textContent).toContain('YouTube')
    expect(dialog.textContent).toContain('Ada')
    expect(dialog.textContent).toContain('1 h')
    const before = s.calls.length
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(s.calls.length).toBe(before)

    await fireEvent.click(tile(11))
    await screen.findByRole('dialog')
    const mark = s.calls.length
    await fireEvent.click(screen.getByRole('button', { name: 'Extend' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(document.querySelector('li[data-grant="44"]')).not.toBeNull())
    const after = s.calls.slice(mark).map((c) => `${c.method} ${c.url} ${c.body ?? ''}`.trim())
    expect(after[0]).toBe('POST /api/v1/grants/5/extend {"duration":3600}')
    expect(after[1]).toBe('POST /api/v1/grants {"child_id":1,"services":["tiktok"],"duration":3600}')
    expect(after.slice(2)).toContain('GET /api/v1/grants')
    expect(document.querySelector('section[data-child="1"] [role="alert"]')).toBeNull()
  })

  it('ad-hoc form creates a grant', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(T0)
    let conflict = false
    const s = server(
      { buttons: [] },
      {
        'POST /api/v1/grants': () => {
          if (conflict) {
            s.state.grants = [
              { id: 6, child_id: 2, services: ['tiktok'], clients: ['Kid tablet'], started_at: new Date(T0).toISOString(), ends_at: new Date(T0 + 600 * 1000).toISOString() },
            ]
            return json(409, { error: 'conflict', message: 'tiktok is already granted by grant 6', grant_id: 6 })
          }
          const ends_at = new Date(T0 + 1800 * 1000).toISOString()
          s.state.grants = [{ id: 45, child_id: 2, services: ['tiktok'], clients: ['Kid tablet'], started_at: new Date(T0).toISOString(), ends_at }]
          return json(201, { id: 45, ends_at, applied: true, failed: [] })
        },
      },
    )
    mount()
    await settle()
    const adhoc = details('[data-adhoc]')
    expect(adhoc.open).toBe(true)
    expect(adhoc.textContent).toContain('Buttons')
    expect(document.querySelector('button[data-button]')).toBeNull()

    const unlock = screen.getByRole('button', { name: 'Unlock' }) as HTMLButtonElement
    expect(unlock.disabled).toBe(true)
    await fireEvent.change(screen.getByLabelText('Child'), { target: { value: '2' } })
    await fireEvent.click(checkbox('TikTok'))
    await fireEvent.input(screen.getByLabelText('Minutes'), { target: { value: '30' } })
    expect(unlock.disabled).toBe(false)

    await fireEvent.click(unlock)
    await settle()
    expect(posts(s.calls).map((c) => c.body)).toEqual(['{"child_id":2,"services":["tiktok"],"duration":1800}'])
    const li = document.querySelector('li[data-grant="45"]')
    expect(li).not.toBeNull()
    expect(li?.querySelector('[data-countdown]')?.textContent?.trim()).toBe('30:00')
    expect(li?.textContent).toContain('Ben')

    conflict = true
    s.state.grants = []
    await fireEvent.click(unlock)
    await settle()
    const dialog = document.querySelector('[role="dialog"][data-offer]')
    expect(dialog).not.toBeNull()
    expect(dialog?.textContent).toContain('TikTok')
    expect(dialog?.textContent).toContain('Ben')
  })

  it('ad-hoc disclosure is closed when buttons exist', async () => {
    server()
    await mountLoaded()
    await waitFor(() => expect(document.querySelector('button[data-button]')).toBeTruthy())
    expect(details('[data-adhoc]').open).toBe(false)
    expect(details('[data-adhoc]').querySelector('.hint')).toBeNull()
  })

  it('services 502 still renders buttons', async () => {
    const s = server(
      { services: 'down' },
      {
        'POST /api/v1/grants': () => {
          s.state.grants = [
            { id: 46, child_id: 1, services: ['youtube', 'tiktok'], clients: ['Kid phone'], started_at: new Date().toISOString(), ends_at: new Date(Date.now() + 3600 * 1000).toISOString() },
          ]
          return json(201, { id: 46, ends_at: new Date(Date.now() + 3600 * 1000).toISOString(), applied: true, failed: [] })
        },
      },
    )
    await mountLoaded()
    await waitFor(() => expect(s.calls.filter((c) => c.url === '/api/v1/services')).toHaveLength(1))
    expect(document.querySelectorAll('button[data-button]').length).toBe(3)
    expect(tile(11).querySelector('img')).toBeNull()
    expect(document.querySelector('main > [role="alert"]')).toBeNull()

    await fireEvent.click(tile(11))
    await waitFor(() => expect(document.querySelector('li[data-grant="46"]')).not.toBeNull())
    expect(document.querySelector('li[data-grant="46"]')?.textContent).toContain('youtube, tiktok')
  })

  it('blocked list loads on open', async () => {
    const adaView = view(1, 'Ada', {
      clients: [
        { name: 'Kid tablet', missing: false, uses_global: true },
        { name: 'Ghost', missing: true, uses_global: false },
      ],
      services: [
        { id: 'youtube', name: 'YouTube', icon: ICON, state: 'blocked', differs: [] },
        { id: 'roblox', name: 'Roblox', icon: ICON, state: 'partial', differs: ['Kid phone', 'Kid tablet'] },
        { id: 'tiktok', name: 'TikTok', icon: ICON, state: 'unblocked', differs: [] },
      ],
    })
    let benDown = true
    const s = server(
      {},
      {
        '/api/v1/children/1/blocked': () => json(200, adaView),
        '/api/v1/children/2/blocked': () => (benDown ? json(502, { error: 'adguard_unavailable', message: 'down' }) : json(200, view(2, 'Ben', { services: [] }))),
        '/api/v1/migration': () =>
          json(200, { global: ['tiktok'], clients: [{ name: 'Kid tablet', child: { id: 1, name: 'Ada' }, gains: ['tiktok'] }] }),
        'POST /api/v1/migration': () => json(200, { migrated: ['Kid tablet'] }),
      },
    )
    await mountLoaded()
    const blockedCalls = (id: number) => s.calls.filter((c) => c.url === `/api/v1/children/${id}/blocked`).length
    expect(blockedCalls(1)).toBe(0)
    expect(blockedCalls(2)).toBe(0)

    const d1 = details('details[data-blocked="1"]')
    await open(d1)
    await waitFor(() => expect(d1.querySelectorAll('li').length).toBe(2))
    expect(blockedCalls(1)).toBe(1)
    const names = [...d1.querySelectorAll('li')].map((li) => li.textContent?.replace(/\s+/g, ' ').trim())
    expect(names[0]).toBe('YouTube')
    expect(names[1]).toContain('Roblox')
    expect(names[1]).toContain('unblocked on Kid phone, Kid tablet')
    expect(d1.querySelector('li[data-state="partial"]')).not.toBeNull()
    expect(d1.textContent).not.toContain('TikTok')
    expect(d1.querySelector('[data-badge="global"]')?.parentElement?.textContent).toContain('Kid tablet')
    expect(d1.querySelector('[data-badge="missing"]')?.parentElement?.textContent).toContain('Ghost')
    for (const img of d1.querySelectorAll('img')) expect(img.getAttribute('alt')).toBe('')

    const d2 = details('details[data-blocked="2"]')
    await open(d2)
    await waitFor(() => expect(d2.querySelector('[role="alert"]')).not.toBeNull())
    expect(d2.querySelector('[role="alert"]')?.getAttribute('data-error')).toBe('adguard_unavailable')
    expect(document.querySelector('main > [role="alert"]')).toBeNull()
    expect(tile(13).disabled).toBe(false)

    benDown = false
    await fireEvent.click(await screen.findByRole('button', { name: 'Migrate' }))
    await waitFor(() => expect(blockedCalls(1)).toBe(2))
    await waitFor(() => expect(blockedCalls(2)).toBe(2))
    await waitFor(() => expect(d2.textContent).toContain('Nothing is blocked'))
  })

  it('children failure is the page-level error', async () => {
    server({}, {
      '/api/v1/children': () => {
        throw new TypeError('Failed to fetch')
      },
    })
    mount()
    const alert = await screen.findByRole('alert')
    expect(alert.getAttribute('data-error')).toBe('network')
    expect(document.querySelectorAll('section[data-child]')).toHaveLength(0)
  })

  it('unmount stops polling', async () => {
    vi.useFakeTimers()
    const s = server()
    const r = mount()
    await settle()
    const before = grantGets(s.calls)
    r.unmount()
    await vi.advanceTimersByTimeAsync(60 * 1000)
    expect(grantGets(s.calls)).toBe(before)
  })

  it('log out is enabled, then locked, then enabled', async () => {
    let release!: (r: Response) => void
    let reject = false
    server({}, {
      'POST /api/v1/logout': () => (reject ? Promise.reject(new TypeError('Failed to fetch')) : new Promise<Response>((r) => (release = r))),
    })
    const onLogout = vi.fn()
    render(Home, { username: 'mum', onLogout })
    const logoutButton = () => screen.getByRole('button', { name: 'Log out' }) as HTMLButtonElement
    expect(logoutButton().disabled).toBe(false)

    await fireEvent.click(logoutButton())
    expect(logoutButton().disabled).toBe(true)
    expect(onLogout).not.toHaveBeenCalled()
    release(new Response(null, { status: 204 }))
    await waitFor(() => expect(onLogout).toHaveBeenCalledTimes(1))
    expect(logoutButton().disabled).toBe(false)

    reject = true
    await fireEvent.click(logoutButton())
    await waitFor(() => expect(onLogout).toHaveBeenCalledTimes(2))
    expect(logoutButton().disabled).toBe(false)
  })

  it('Buttons button routes', async () => {
    server()
    mount()
    await fireEvent.click(screen.getByRole('button', { name: 'Buttons' }))
    expect(location.pathname).toBe('/buttons')
    expect(get(route)).toBe('buttons')
  })

  it('Children button routes', async () => {
    server()
    mount()
    await fireEvent.click(screen.getByRole('button', { name: 'Children' }))
    expect(location.pathname).toBe('/children')
    expect(get(route)).toBe('children')
  })

  it('shows "No children yet" for an empty list', async () => {
    server({ children: [], buttons: [] })
    mount()
    expect(await screen.findByText('No children yet')).toBeTruthy()
  })

  it('refetches on every mount', async () => {
    const s = server()
    const first = await mountLoaded()
    first.unmount()
    await mountLoaded()
    expect(s.calls.filter((c) => c.url === '/api/v1/children')).toHaveLength(2)
    expect(s.calls.filter((c) => c.url === '/api/v1/buttons')).toHaveLength(2)
  })

  it('hosts the migration banner', async () => {
    server({}, {
      '/api/v1/migration': () =>
        json(200, {
          global: ['tiktok', 'roblox'],
          clients: [{ name: 'Kid tablet', child: { id: 1, name: 'Ada' }, gains: ['roblox', 'tiktok'] }],
        }),
    })
    const first = await mountLoaded()
    expect(await screen.findByRole('status')).toBeTruthy()
    expect(document.querySelector('[data-migration]')).not.toBeNull()
    first.unmount()

    server()
    await mountLoaded()
    expect(document.querySelector('[data-migration]')).toBeNull()
  })

  it('401 while polling routes through onUnauthorized', async () => {
    const spy = vi.fn()
    setOnUnauthorized(spy)
    try {
      server({}, { '/api/v1/grants': () => json(401, { error: 'unauthorized', message: 'no session' }) })
      await mountLoaded()
      await waitFor(() => expect(spy).toHaveBeenCalled())
      expect(document.querySelector('[data-badge="unreachable"]')).toBeNull()
    } finally {
      setOnUnauthorized(() => route.set('login'))
    }
  })
})
