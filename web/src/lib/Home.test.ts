// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { get } from 'svelte/store'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Home from './Home.svelte'
import { refresh } from './activeGrants'
import { historyChild, route, setOnUnauthorized, type BlockedView, type Button, type Child, type Grant, type Service } from './api'

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

/** A mutable server for Home: grants, buttons, children and the catalogue's health are read live from `state`. */
function server(over: { buttons?: Button[]; grants?: Grant[]; children?: Child[]; services?: Service[] | 'down' } = {}, extra: Routes = {}) {
  const state = {
    buttons: over.buttons ?? [b3, b1, b2],
    grants: over.grants ?? [],
    children: over.children ?? [ada, ben],
    servicesDown: over.services === 'down',
  }
  const routes: Routes = {
    '/api/v1/migration': noOffer,
    '/api/v1/children': () => json(200, { children: state.children }),
    '/api/v1/buttons': () => json(200, { buttons: state.buttons }),
    '/api/v1/services': () =>
      state.servicesDown
        ? json(502, { error: 'adguard_unavailable', message: 'down' })
        : json(200, { services: over.services === 'down' ? [] : (over.services ?? services) }),
    '/api/v1/grants': () => json(200, { grants: state.grants }),
    '/api/v1/children/1/blocked': () => json(200, view(1, 'Ada')),
    '/api/v1/children/2/blocked': () => json(200, view(2, 'Ben')),
    ...extra,
  }
  const calls = mockFetch(routes)
  return { state, calls }
}

const grantGets = (calls: Call[]) => calls.filter((c) => c.method === 'GET' && c.url === '/api/v1/grants').length
const getsOf = (calls: Call[], url: string) => calls.filter((c) => c.method === 'GET' && c.url === url).length
const posts = (calls: Call[]) => calls.filter((c) => c.method === 'POST' && c.url.startsWith('/api/v1/grants'))

/** A migration offer whose Migrate button makes Home reload children, buttons and the catalogue. */
const migratable: Routes = {
  '/api/v1/migration': () => json(200, { global: ['tiktok'], clients: [{ name: 'Kid tablet', child: { id: 1, name: 'Ada' }, gains: ['tiktok'] }] }),
  'POST /api/v1/migration': () => json(200, { migrated: ['Kid tablet'] }),
}

async function migrate() {
  await fireEvent.click(await screen.findByRole('button', { name: 'Migrate' }))
}

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

/**
 * Flip a disclosure the way a tap does: jsdom queues the toggle event as a
 * macrotask when `open` changes, so wait one tick for it to land. Firing a
 * synthetic toggle instead would leave that native one pending, to fire in
 * a later test against a detached instance.
 */
async function toggle(d: HTMLDetailsElement, open: boolean) {
  d.open = open
  await new Promise((r) => setTimeout(r, 0))
}

const open = (d: HTMLDetailsElement) => toggle(d, true)
const close = (d: HTMLDetailsElement) => toggle(d, false)

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
  historyChild.set(null)
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
    expect(screen.queryByText('No children yet')).toBeNull()
    expect(screen.queryByText(/No buttons yet/)).toBeNull()
  })

  it('a child without buttons is pointed at the Buttons page', async () => {
    server({ buttons: [b1] })
    await mountLoaded()
    await waitFor(() => expect(tile(11)).toBeTruthy())
    expect(section(1).textContent).not.toContain('No buttons yet')
    expect(section(2).textContent).toContain('No buttons yet')
    expect(section(2).querySelector('button[data-button]')).toBeNull()

    await fireEvent.click(section(2).querySelector('.none button.link')!)
    expect(location.pathname).toBe('/buttons')
    expect(get(route)).toBe('buttons')
  })

  it('a button whose first service is unknown renders no icon', async () => {
    server({ buttons: [{ ...b1, services: ['gone', 'youtube'] }] })
    await mountLoaded()
    await waitFor(() => expect(document.querySelector('[data-adhoc] fieldset')).toBeTruthy())
    expect(tile(11).querySelector('img')).toBeNull()
  })

  // A catalogue whose icons all differ, so each src names exactly one service.
  const distinct: Service[] = [
    { id: 'youtube', name: 'YouTube', icon: YT_ICON },
    { id: 'tiktok', name: 'TikTok', icon: 'VFQ=' },
    { id: 'roblox', name: 'Roblox', icon: 'Ulg=' },
  ]
  // The tile has no img until the catalogue arrives, so wait on the src itself.
  const expectIcon = (id: number, icon: string) =>
    waitFor(() => expect(tile(id).querySelector('img')?.getAttribute('src')).toBe('data:image/svg+xml;base64,' + icon))

  it('button shows its chosen icon', async () => {
    server({ buttons: [{ ...b1, services: ['youtube'], icon: 'tiktok' }], services: distinct })
    await mountLoaded()
    await expectIcon(11, 'VFQ=')
  })

  it('unknown icon falls back', async () => {
    server({ buttons: [{ ...b1, services: ['youtube', 'tiktok'], icon: 'gone' }], services: distinct })
    await mountLoaded()
    await expectIcon(11, YT_ICON)
  })

  it('no icon uses first service', async () => {
    server({ buttons: [{ ...b1, services: ['roblox', 'tiktok'], icon: '' }], services: distinct })
    await mountLoaded()
    await expectIcon(11, 'Ulg=')
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
            return json(201, { id: 43, ends_at, applied: false, failed: ['Kid tablet', 'Kid phone'] })
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
    expect(alert.textContent).toBe('Unlocked, except on Kid tablet, Kid phone')
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
    // Only the service the active grant already covers is named; TikTok is
    // what the accepted offer will create, not what is already unlocked.
    expect(dialog.querySelector('p')?.textContent).toBe('YouTube is already unlocked for Ada — extend by 1 h?')
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

  it('offer wording: every covered service, "That" for a stale list, "this child" for a gone child', async () => {
    let mode: 'both' | 'stale' = 'both'
    const both: Grant = {
      id: 5,
      child_id: 1,
      services: ['tiktok', 'youtube'],
      clients: ['Kid phone'],
      started_at: new Date(Date.now() - 600 * 1000).toISOString(),
      ends_at: new Date(Date.now() + 600 * 1000).toISOString(),
    }
    const s = server(
      {},
      {
        ...migratable,
        'POST /api/v1/grants': () => {
          // stale: the 409 names grant 5 but the re-fetched list does not have it.
          s.state.grants = mode === 'both' ? [both] : []
          return json(409, { error: 'conflict', message: 'youtube is already granted by grant 5', grant_id: 5 })
        },
      },
    )
    await mountLoaded()
    await waitFor(() => expect(tile(11)).toBeTruthy())
    const text = () => document.querySelector('[role="dialog"] p')?.textContent

    await fireEvent.click(tile(11))
    await screen.findByRole('dialog')
    expect(text()).toBe('YouTube, TikTok is already unlocked for Ada — extend by 1 h?')
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))

    mode = 'stale'
    await fireEvent.click(tile(11))
    await screen.findByRole('dialog')
    expect(text()).toBe('That is already unlocked for Ada — extend by 1 h?')

    // The child list reloads without Ada while the offer is up: the dialog
    // stays, and the name falls back rather than crashing the page.
    s.state.children = [ben]
    await migrate()
    await waitFor(() => expect(getsOf(s.calls, '/api/v1/children')).toBe(2))
    await waitFor(() => expect(document.querySelector('section[data-child="1"]')).toBeNull())
    expect(text()).toBe('That is already unlocked for this child — extend by 1 h?')
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

    // The hint's link is a real route to the Buttons page.
    await fireEvent.click(adhoc.querySelector('.hint button.link')!)
    expect(location.pathname).toBe('/buttons')
    expect(get(route)).toBe('buttons')
  })

  it('ad-hoc form is locked while its tap is out', async () => {
    let release!: (r: Response) => void
    const s = server({ buttons: [] }, { 'POST /api/v1/grants': () => new Promise<Response>((r) => (release = r)) })
    await mountLoaded()
    await waitFor(() => expect(document.querySelector('[data-adhoc] fieldset')).toBeTruthy())
    const fieldset = () => document.querySelector('[data-adhoc] fieldset') as HTMLFieldSetElement
    const unlock = () => screen.getByRole('button', { name: 'Unlock' }) as HTMLButtonElement

    expect(fieldset().disabled).toBe(false)
    await fireEvent.change(screen.getByLabelText('Child'), { target: { value: '2' } })
    await fireEvent.click(checkbox('TikTok'))
    expect(unlock().disabled).toBe(false)

    await fireEvent.click(unlock())
    expect(posts(s.calls)).toHaveLength(1)
    expect(unlock().disabled).toBe(true)
    expect(fieldset().disabled).toBe(true)
    // A second submit while the first is out is not a second POST.
    await fireEvent.submit(unlock().closest('form')!)
    expect(posts(s.calls)).toHaveLength(1)

    const ends_at = new Date(Date.now() + 3600 * 1000).toISOString()
    s.state.grants = [{ id: 47, child_id: 2, services: ['tiktok'], clients: ['Kid tablet'], started_at: new Date().toISOString(), ends_at }]
    release(json(201, { id: 47, ends_at, applied: true, failed: [] }))
    await waitFor(() => expect(document.querySelector('li[data-grant="47"]')).not.toBeNull())
    await waitFor(() => expect(unlock().disabled).toBe(false))
    expect(fieldset().disabled).toBe(false)
  })

  it('ad-hoc submit for a child that has since gone does nothing', async () => {
    const errors: ErrorEvent[] = []
    const onError = (e: ErrorEvent) => errors.push(e)
    window.addEventListener('error', onError)
    try {
      const s = server({ buttons: [] }, migratable)
      await mountLoaded()
      await waitFor(() => expect(document.querySelector('[data-adhoc] fieldset')).toBeTruthy())
      await fireEvent.change(screen.getByLabelText('Child'), { target: { value: '2' } })
      await fireEvent.click(checkbox('TikTok'))
      const unlock = screen.getByRole('button', { name: 'Unlock' }) as HTMLButtonElement
      expect(unlock.disabled).toBe(false)

      // Ben is removed server-side and the list reloads; the form still holds child 2.
      s.state.children = [ada]
      await migrate()
      await waitFor(() => expect(document.querySelector('section[data-child="2"]')).toBeNull())
      expect((screen.getByLabelText('Child') as HTMLSelectElement).selectedIndex).toBe(-1)

      await fireEvent.click(unlock)
      await new Promise((r) => setTimeout(r, 20))
      expect(posts(s.calls)).toHaveLength(0)
      expect(errors).toHaveLength(0)
      expect(unlock.disabled).toBe(false)
    } finally {
      window.removeEventListener('error', onError)
    }
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
    expect(names[0]).toBe('YouTube Unblock')
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

  it('blocked list is fetched on open only, once, and names a dropped connection', async () => {
    let hold: ((r: Response) => void) | null = null
    const s = server(
      {},
      {
        '/api/v1/children/1/blocked': () => new Promise<Response>((r) => (hold = r)),
        '/api/v1/children/2/blocked': () => Promise.reject(new TypeError('Failed to fetch')),
      },
    )
    await mountLoaded()
    const blockedCalls = (id: number) => s.calls.filter((c) => c.url === `/api/v1/children/${id}/blocked`).length
    const d1 = details('details[data-blocked="1"]')

    // A toggle that closes (never opened) fetches nothing.
    d1.open = false
    await fireEvent(d1, new Event('toggle'))
    expect(blockedCalls(1)).toBe(0)

    // Opening fetches; re-opening while that fetch is still out does not.
    await open(d1)
    expect(blockedCalls(1)).toBe(1)
    await close(d1)
    await open(d1)
    expect(blockedCalls(1)).toBe(1)

    hold!(json(200, view(1, 'Ada')))
    await waitFor(() => expect(d1.querySelectorAll('li').length).toBe(2))

    // Once loaded, the view is kept across close/open.
    await close(d1)
    await open(d1)
    expect(blockedCalls(1)).toBe(1)
    expect(d1.querySelectorAll('li').length).toBe(2)

    const d2 = details('details[data-blocked="2"]')
    await open(d2)
    await waitFor(() => expect(d2.querySelector('[role="alert"]')).not.toBeNull())
    expect(d2.querySelector('[role="alert"]')?.getAttribute('data-error')).toBe('network')
  })

  it('migrating reloads open disclosures and the catalogue', async () => {
    const s = server({}, migratable)
    await mountLoaded()
    await waitFor(() => expect(tile(11).querySelector('img')).not.toBeNull())
    const blockedCalls = (id: number) => s.calls.filter((c) => c.url === `/api/v1/children/${id}/blocked`).length
    const d1 = details('details[data-blocked="1"]')
    const d2 = details('details[data-blocked="2"]')
    await open(d1)
    await waitFor(() => expect(d1.querySelectorAll('li').length).toBe(2))
    // Ben's was opened, then closed again: closed disclosures are not re-fetched.
    await open(d2)
    await waitFor(() => expect(d2.querySelectorAll('li').length).toBe(2))
    await close(d2)
    expect(blockedCalls(1)).toBe(1)
    expect(blockedCalls(2)).toBe(1)

    // The catalogue goes away before the reload: icons and names fall back.
    s.state.servicesDown = true
    await migrate()
    await waitFor(() => expect(getsOf(s.calls, '/api/v1/services')).toBe(2))
    await waitFor(() => expect(blockedCalls(1)).toBe(2))
    expect(blockedCalls(2)).toBe(1)
    await waitFor(() => expect(tile(11).querySelector('img')).toBeNull())
    expect(document.querySelector('[data-adhoc] [data-unavailable]')).not.toBeNull()
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

  it('each child has a History link to its own history', async () => {
    server()
    await mountLoaded()
    const adaLink = screen.getByRole('button', { name: 'History for Ada' })
    const benLink = screen.getByRole('button', { name: 'History for Ben' })
    expect(adaLink.textContent?.trim()).toBe('History')
    expect(section(1).contains(adaLink)).toBe(true)
    expect(section(2).contains(benLink)).toBe(true)

    // Both in turn: a shared closure would send both links to the same child.
    await fireEvent.click(benLink)
    expect(location.pathname + location.search).toBe('/history?child=2')
    expect(get(route)).toBe('history')
    expect(get(historyChild)).toBe(2)

    await fireEvent.click(adaLink)
    expect(location.pathname + location.search).toBe('/history?child=1')
    expect(get(route)).toBe('history')
    expect(get(historyChild)).toBe(1)
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

  describe('shared devices', () => {
    const adaS: Child = { id: 1, name: 'Ada', clients: ['Kid phone'] }
    const benS: Child = { id: 2, name: 'Ben', clients: ['Kid phone'] }
    const cleo: Child = { id: 3, name: 'Cleo', clients: ['Cleo tablet'] }
    const b4: Button = { id: 14, label: 'Roblox', child_id: 3, services: ['roblox'], duration: 1800 }
    const kidPhone = [{ client: 'Kid phone', others: [{ id: 2, name: 'Ben' }] }]
    const NOTICE = 'Also unblocked on Kid phone (shared with Ben)'

    const created = (id: number, over: Record<string, unknown> = {}) =>
      json(201, { id, ends_at: new Date(Date.now() + 3600 * 1000).toISOString(), applied: true, failed: [], ...over })

    /** The text aria-describedby points at, resolved the way a screen reader does for plain text. */
    function description(el: Element): string {
      return (el.getAttribute('aria-describedby') ?? '')
        .split(/\s+/)
        .filter(Boolean)
        .map((id) => document.getElementById(id)?.textContent ?? '')
        .join(' ')
    }

    const status = (id: number) => section(id).querySelector('[role="status"]')
    const flat = (el: Element | null | undefined) => el?.textContent?.replace(/\s+/g, ' ').trim() ?? ''

    it('sharedHint: a button whose child shares a device is described by one passive note', async () => {
      const s = server({ children: [adaS, benS, cleo], buttons: [b1, b2, b4] })
      await mountLoaded()
      await waitFor(() => expect(tile(14)).toBeTruthy())

      const notes = section(1).querySelectorAll('[role="note"]')
      expect(notes).toHaveLength(1)
      expect(notes[0].textContent).toContain('Kid phone (shared with Ben)')
      expect(notes[0].querySelector('button, input')).toBeNull()
      for (const id of [11, 12]) expect(description(tile(id))).toContain('Kid phone (shared with Ben)')
      expect(screen.getByRole('button', { name: /YouTube \+ TikTok/, description: /Kid phone \(shared with Ben\)/ })).toBe(tile(11))

      expect(tile(14).hasAttribute('aria-describedby')).toBe(false)
      expect(description(tile(14))).toBe('')
      expect(section(3).querySelector('[role="note"]')).toBeNull()
      expect(posts(s.calls)).toHaveLength(0)
    })

    it('noConfirm: one click on a hinted tile is one POST, no confirmation', async () => {
      const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
      try {
        const s = server({ children: [adaS, benS], buttons: [b1] }, { 'POST /api/v1/grants': () => created(50, { shared: kidPhone }) })
        await mountLoaded()
        await waitFor(() => expect(tile(11)).toBeTruthy())
        expect(description(tile(11))).toContain('Kid phone (shared with Ben)')

        await fireEvent.click(tile(11))
        await waitFor(() => expect(status(1)).not.toBeNull())
        expect(posts(s.calls)).toHaveLength(1)
        expect(posts(s.calls)[0].body).toBe('{"child_id":1,"services":["youtube","tiktok"],"duration":3600}')
        expect(confirm).not.toHaveBeenCalled()
        expect(document.querySelector('[role="dialog"]')).toBeNull()
      } finally {
        confirm.mockRestore()
      }
    })

    it('sharedNotice: the 201 names the shared device; shared [] shows no status line', async () => {
      let shared: unknown[] = kidPhone
      let id = 60
      server({ children: [adaS, benS], buttons: [b1] }, { 'POST /api/v1/grants': () => created(++id, { shared }) })
      await mountLoaded()
      await waitFor(() => expect(tile(11)).toBeTruthy())
      expect(status(1)).toBeNull()

      await fireEvent.click(tile(11))
      await waitFor(() => expect(status(1)).not.toBeNull())
      expect(flat(status(1))).toContain('Kid phone (shared with Ben)')
      expect(flat(status(1))).toBe(NOTICE)
      expect(section(1).querySelector('[role="alert"]')).toBeNull()
      expect(status(2)).toBeNull()

      // The next tap's notice replaces the last one: none when nothing is shared.
      shared = []
      await fireEvent.click(tile(11))
      await waitFor(() => expect(document.querySelector('li[data-grant="62"]')).not.toBeNull())
      await waitFor(() => expect(tile(11).disabled).toBe(false))
      expect(status(1)).toBeNull()
    })

    it('partialShared: a partial tap keeps both the except-on alert and the shared status', async () => {
      server(
        { children: [adaS, benS], buttons: [b1] },
        { 'POST /api/v1/grants': () => created(70, { applied: false, failed: ['Kid phone'], shared: kidPhone }) },
      )
      await mountLoaded()
      await waitFor(() => expect(tile(11)).toBeTruthy())

      await fireEvent.click(tile(11))
      await waitFor(() => expect(status(1)).not.toBeNull())
      const alert = section(1).querySelector('[role="alert"]')
      expect(alert?.getAttribute('data-error')).toBe('partial')
      expect(alert?.textContent).toBe('Unlocked, except on Kid phone')
      expect(flat(status(1))).toBe(NOTICE)
    })

    it('adhocShared: the ad-hoc form shows the GrantForm note and its 201 the same status line', async () => {
      const s = server({ children: [adaS, benS], buttons: [] }, { 'POST /api/v1/grants': () => created(80, { shared: kidPhone }) })
      await mountLoaded()
      await waitFor(() => expect(document.querySelector('[data-adhoc] fieldset')).toBeTruthy())
      const adhoc = details('[data-adhoc]')
      expect(adhoc.querySelector('[role="note"]')).toBeNull()

      await fireEvent.change(screen.getByLabelText('Child'), { target: { value: '1' } })
      expect(adhoc.querySelector('[role="note"]')?.textContent).toContain('Kid phone (shared with Ben)')
      await fireEvent.click(checkbox('TikTok'))
      await fireEvent.click(screen.getByRole('button', { name: 'Unlock' }))
      await waitFor(() => expect(status(1)).not.toBeNull())
      expect(posts(s.calls)).toHaveLength(1)
      expect(flat(status(1))).toBe(NOTICE)
    })

    it('unblockedBy: a service another child holds open is listed with who holds it', async () => {
      const benView = view(2, 'Ben', {
        services: [
          { id: 'youtube', name: 'YouTube', icon: ICON, state: 'unblocked', differs: [], unblocked_by: [{ client: 'Kid phone', grant_id: 7, child: { id: 1, name: 'Ada' } }] },
          { id: 'tiktok', name: 'TikTok', icon: ICON, state: 'partial', differs: ['Ben tablet', 'Kid phone'], unblocked_by: [{ client: 'Kid phone', grant_id: 7, child: { id: 1, name: 'Ada' } }] },
          { id: 'roblox', name: 'Roblox', icon: ICON, state: 'unblocked', differs: [], unblocked_by: [] },
        ],
      })
      server({ children: [adaS, benS] }, { '/api/v1/children/2/blocked': () => json(200, benView) })
      await mountLoaded()
      const d2 = details('details[data-blocked="2"]')
      await open(d2)
      await waitFor(() => expect(d2.querySelectorAll('li').length).toBe(2))

      const yt = d2.querySelector('li[data-state="unblocked"]')
      expect(flat(yt)).toContain('YouTube')
      expect(flat(yt?.querySelector('.partial'))).toBe("— unblocked on Kid phone by Ada's grant")
      // A partial service names its plain clients once and the shared one by its grant.
      const tt = d2.querySelector('li[data-state="partial"]')
      expect(flat(tt)).toContain('TikTok')
      expect(flat(tt?.querySelector('.partial'))).toBe("— unblocked on Ben tablet, Kid phone by Ada's grant")
      expect(d2.textContent).not.toContain('Roblox')
      expect(d2.textContent).not.toContain('Nothing is blocked')
    })

    it('sharedWithLabel: a shared client is marked with its other owners', async () => {
      const benView = view(2, 'Ben', {
        clients: [
          { name: 'Kid phone', missing: false, uses_global: false, shared_with: [{ id: 1, name: 'Ada' }] },
          { name: 'Ben tablet', missing: false, uses_global: false, shared_with: [] },
        ],
      })
      server({ children: [adaS, benS] }, { '/api/v1/children/2/blocked': () => json(200, benView) })
      await mountLoaded()
      const d2 = details('details[data-blocked="2"]')
      await open(d2)
      await waitFor(() => expect(d2.querySelectorAll('.client').length).toBe(2))

      const [phone, tablet] = [...d2.querySelectorAll('.client')]
      expect(flat(phone)).toBe('Kid phone (shared with Ada)')
      expect(flat(tablet)).toBe('Ben tablet')
      expect(tablet.textContent).not.toContain('(shared with')
    })

    it('unblockedByRemoved: a deleted granter is named as a removed child', async () => {
      const benView = view(2, 'Ben', {
        services: [
          { id: 'youtube', name: 'YouTube', icon: ICON, state: 'unblocked', differs: [], unblocked_by: [{ client: 'Kid phone', grant_id: 7, child: { id: 1, name: '' } }] },
        ],
      })
      server({ children: [benS] }, { '/api/v1/children/2/blocked': () => json(200, benView) })
      mount()
      await screen.findByRole('heading', { name: 'Ben' })
      const d2 = details('details[data-blocked="2"]')
      await open(d2)
      await waitFor(() => expect(d2.querySelectorAll('li').length).toBe(1))
      expect(flat(d2.querySelector('li'))).toContain('YouTube')
      expect(flat(d2.querySelector('li .partial'))).toBe("— unblocked on Kid phone by a removed child's grant")
      expect(d2.textContent).not.toContain("by 's grant")
    })
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

describe('Home custom services', () => {
  const portal: Service = { id: 'custom:1', name: 'Portal', icon: '', custom: true }
  const avatarIn = (el: Element) => el.querySelector('[data-letter-avatar]')

  it('custom blocked entry', async () => {
    server(
      {},
      {
        '/api/v1/children/1/blocked': () =>
          json(
            200,
            view(1, 'Ada', {
              services: [
                { id: 'custom:1', name: 'Portal', icon: '', custom: true, state: 'blocked', differs: [] },
                { id: 'custom:2', name: 'homework hub', icon: '', custom: true, state: 'partial', differs: ['Kid phone'] },
                { id: 'youtube', name: 'YouTube', icon: ICON, state: 'blocked', differs: [] },
              ],
            }),
          ),
      },
    )
    await mountLoaded()
    const d1 = details('details[data-blocked="1"]')
    await open(d1)
    await waitFor(() => expect(d1.querySelectorAll('li').length).toBe(3))

    const blockedLi = d1.querySelector('li[data-state="blocked"]')!
    expect(avatarIn(blockedLi)?.textContent).toBe('P')
    expect(blockedLi.querySelector('img')).toBeNull()
    expect(blockedLi.textContent?.replace(/\s+/g, ' ').trim()).toBe('P Portal Unblock')
    expect(blockedLi.querySelector('.partial')).toBeNull()

    const partialLi = d1.querySelector('li[data-state="partial"]')!
    expect(avatarIn(partialLi)?.textContent).toBe('H')
    expect(partialLi.querySelector('img')).toBeNull()
    expect(partialLi.textContent).toContain('homework hub')
    expect(partialLi.querySelector('.partial')?.textContent).toContain('unblocked on Kid phone')

    // A built-in in the same list keeps its real icon.
    const yt = [...d1.querySelectorAll('li')].find((li) => li.textContent?.includes('YouTube'))!
    expect(yt.querySelector('img')?.getAttribute('src')).toBe('data:image/svg+xml;base64,' + ICON)
    expect(avatarIn(yt)).toBeNull()
  })

  it('custom unblocked entry', async () => {
    const flat = (el: Element | null | undefined) => el?.textContent?.replace(/\s+/g, ' ').trim() ?? ''
    server(
      {},
      {
        '/api/v1/children/1/blocked': () =>
          json(
            200,
            view(1, 'Ada', {
              services: [
                {
                  id: 'custom:1', name: 'Portal', icon: '', custom: true, state: 'unblocked', differs: [],
                  unblocked_by: [{ client: 'Kid phone', grant_id: 7, child: { id: 2, name: 'Ben' } }],
                },
                { id: 'custom:2', name: 'homework hub', icon: '', custom: true, state: 'unblocked', differs: [], unblocked_by: [] },
                { id: 'youtube', name: 'YouTube', icon: ICON, state: 'blocked', differs: [] },
              ],
            }),
          ),
      },
    )
    await mountLoaded()
    const d1 = details('details[data-blocked="1"]')
    await open(d1)
    await waitFor(() => expect(d1.querySelectorAll('li').length).toBe(2))

    // Held open by another child's grant: listed, with its letter avatar and who holds it.
    const li = d1.querySelector('li[data-state="unblocked"]')!
    expect(avatarIn(li)?.textContent).toBe('P')
    expect(li.querySelector('img')).toBeNull()
    expect(flat(li)).toContain('Portal')
    expect(flat(li.querySelector('.partial'))).toBe("— unblocked on Kid phone by Ben's grant")
    // Plainly unblocked with nobody else holding it: not listed.
    expect(d1.textContent).not.toContain('homework hub')
  })

  it('custom button tile', async () => {
    server({
      buttons: [{ ...b1, services: ['custom:1'], icon: '' }, { ...b2, services: ['roblox'], icon: 'custom:1' }, { ...b3, services: ['youtube', 'custom:1'] }],
      services: [...services, portal],
    })
    await mountLoaded()
    // First service custom, and a custom service picked as the icon.
    for (const id of [11, 12]) {
      await waitFor(() => expect(avatarIn(tile(id))?.textContent).toBe('P'))
      expect(tile(id).querySelector('img')).toBeNull()
      expect(tile(id).querySelector('[data-letter-avatar]')?.getAttribute('style')).toContain('width: 28px')
    }
    // A built-in-first button beside them still shows its <img>.
    expect(tile(13).querySelector('img')?.getAttribute('src')).toBe('data:image/svg+xml;base64,' + YT_ICON)
    expect(avatarIn(tile(13))).toBeNull()
  })

  it('services nav', async () => {
    server()
    mount()
    const nav = screen.getByRole('navigation')
    const labels = [...nav.querySelectorAll('button')].map((b) => b.textContent?.trim())
    expect(labels.indexOf('Services')).toBe(labels.indexOf('Buttons') + 1)
    await fireEvent.click(screen.getByRole('button', { name: 'Services' }))
    expect(location.pathname).toBe('/services')
    expect(get(route)).toBe('services')
  })
})

describe('Home permanent toggles', () => {
  const TOGGLES = '/api/v1/children/1/toggles'
  const flat = (el: Element | null | undefined) => el?.textContent?.replace(/\s+/g, ' ').trim() ?? ''
  const toggleCalls = (calls: Call[]) => calls.filter((c) => c.method === 'POST' && c.url === TOGGLES)
  const blockedGets = (calls: Call[]) => getsOf(calls, '/api/v1/children/1/blocked')
  const stored = (over: Record<string, unknown> = {}) =>
    json(200, { service_id: 'youtube', state: 'unblocked', applied: true, failed: [], shared: [], grant: null, ...over })

  function row(d: Element, id: string): HTMLElement {
    const el = d.querySelector(`li[data-row="${id}"]`)
    if (!el) throw new Error(`no row ${id}`)
    return el as HTMLElement
  }

  const controls = (li: Element) => [...li.querySelectorAll('.controls button')].map((b) => b.textContent?.trim())

  function control(li: Element, label: 'Unblock' | 'Block'): HTMLButtonElement {
    const el = [...li.querySelectorAll('.controls button')].find((b) => b.textContent?.trim() === label)
    if (!el) throw new Error(`no ${label} control`)
    return el as HTMLButtonElement
  }

  /** Mount, open Ada's blocked view and wait for its list. */
  async function openAda(): Promise<HTMLDetailsElement> {
    await mountLoaded()
    const d1 = details('details[data-blocked="1"]')
    await open(d1)
    await waitFor(() => expect(d1.querySelector('ul.services')).not.toBeNull())
    return d1
  }

  it('toggle controls', async () => {
    const adaView = view(1, 'Ada', {
      services: [
        { id: 'youtube', name: 'YouTube', icon: ICON, state: 'blocked', differs: [], toggle: null },
        { id: 'tiktok', name: 'TikTok', icon: ICON, state: 'partial', differs: ['Kid phone'], toggle: null },
        { id: 'roblox', name: 'Roblox', icon: ICON, state: 'unblocked', differs: [], unblocked_by: [], toggle: 'unblocked' },
      ],
    })
    const answers: Answer[] = [
      () => stored(),
      () =>
        stored({ service_id: 'tiktok', state: 'blocked', applied: false, failed: ['tablet'], shared: [{ client: 'tv', others: [{ id: 2, name: 'B' }] }] }),
    ]
    const s = server({}, { '/api/v1/children/1/blocked': () => json(200, adaView), [`POST ${TOGGLES}`]: () => answers.shift()!() })
    const d1 = await openAda()
    await waitFor(() => expect(d1.querySelectorAll('li[data-row]').length).toBe(3))

    expect(controls(row(d1, 'youtube'))).toEqual(['Unblock'])
    expect(controls(row(d1, 'tiktok'))).toEqual(['Unblock', 'Block'])
    // This child's own permanent unblock is listed as unblocked, with only Block.
    const roblox = row(d1, 'roblox')
    expect(roblox.getAttribute('data-state')).toBe('unblocked')
    expect(flat(roblox.querySelector('.partial'))).toBe('— unblocked')
    expect(controls(roblox)).toEqual(['Block'])
    expect(screen.getByRole('button', { name: 'Unblock YouTube' })).toBe(control(row(d1, 'youtube'), 'Unblock'))
    expect(screen.getByRole('button', { name: 'Block TikTok' })).toBe(control(row(d1, 'tiktok'), 'Block'))

    await fireEvent.click(control(row(d1, 'youtube'), 'Unblock'))
    await waitFor(() => expect(toggleCalls(s.calls)).toHaveLength(1))
    expect(toggleCalls(s.calls)[0].body).toBe('{"service_id":"youtube","state":"unblocked"}')

    await waitFor(() => expect(control(row(d1, 'tiktok'), 'Block').disabled).toBe(false))
    // Applied with nothing shared: no alert, no shared notice.
    expect(d1.querySelector('[data-toggle-error]')).toBeNull()
    expect(d1.querySelector('[data-toggle-shared]')).toBeNull()

    await fireEvent.click(control(row(d1, 'tiktok'), 'Block'))
    await waitFor(() => expect(toggleCalls(s.calls)).toHaveLength(2))
    expect(toggleCalls(s.calls)[1].body).toBe('{"service_id":"tiktok","state":"blocked"}')
    // A block's outcome is worded as a block.
    await waitFor(() => expect(d1.querySelector('[data-toggle-error]')).not.toBeNull())
    expect(flat(d1.querySelector('[data-toggle-error]'))).toBe('Blocked TikTok, except on tablet')
    expect(flat(d1.querySelector('[data-toggle-shared]'))).toBe('Also blocked on tv (shared with B)')
  })

  it('untoggled unblocked hidden', async () => {
    const adaView = view(1, 'Ada', {
      services: [
        { id: 'youtube', name: 'YouTube', icon: ICON, state: 'blocked', differs: [], toggle: null },
        { id: 'roblox', name: 'Roblox', icon: ICON, state: 'unblocked', differs: [], unblocked_by: [], toggle: null },
      ],
    })
    server({}, { '/api/v1/children/1/blocked': () => json(200, adaView) })
    const d1 = await openAda()
    await waitFor(() => expect(d1.querySelectorAll('li[data-row]').length).toBe(1))
    expect(row(d1, 'youtube')).toBeTruthy()
    expect(d1.querySelector('li[data-row="roblox"]')).toBeNull()
    expect(d1.textContent).not.toContain('Roblox')
  })

  it('reverse', async () => {
    // The server stores the toggle and the next blocked view reflects it.
    let yt: { state: 'blocked' | 'unblocked'; toggle: 'blocked' | 'unblocked' | null } = { state: 'blocked', toggle: null }
    let calls: Call[] = []
    const s = server(
      {},
      {
        '/api/v1/children/1/blocked': () =>
          json(200, view(1, 'Ada', { services: [{ id: 'youtube', name: 'YouTube', icon: ICON, state: yt.state, differs: [], toggle: yt.toggle }] })),
        [`POST ${TOGGLES}`]: () => {
          const body = JSON.parse(toggleCalls(calls).at(-1)!.body!) as { service_id: string; state: 'blocked' | 'unblocked' }
          yt = { state: body.state, toggle: body.state }
          return stored({ state: body.state })
        },
      },
    )
    calls = s.calls
    const d1 = await openAda()

    await fireEvent.click(control(row(d1, 'youtube'), 'Unblock'))
    await waitFor(() => expect(row(d1, 'youtube').getAttribute('data-state')).toBe('unblocked'))
    expect(controls(row(d1, 'youtube'))).toEqual(['Block'])
    expect(flat(row(d1, 'youtube').querySelector('.partial'))).toBe('— unblocked')

    await waitFor(() => expect(control(row(d1, 'youtube'), 'Block').disabled).toBe(false))
    await fireEvent.click(control(row(d1, 'youtube'), 'Block'))
    await waitFor(() => expect(row(d1, 'youtube').getAttribute('data-state')).toBe('blocked'))
    expect(toggleCalls(s.calls).map((c) => c.body)).toEqual([
      '{"service_id":"youtube","state":"unblocked"}',
      '{"service_id":"youtube","state":"blocked"}',
    ])
    expect(controls(row(d1, 'youtube'))).toEqual(['Unblock'])
  })

  it('toggle inflight', async () => {
    let hold: ((r: Response) => void) | null = null
    const s = server({}, { [`POST ${TOGGLES}`]: () => new Promise<Response>((r) => (hold = r)) })
    const d1 = await openAda()
    const unblock = control(row(d1, 'youtube'), 'Unblock')

    // Two clicks in one task: the second lands before the DOM disables the control.
    unblock.click()
    unblock.click()
    await waitFor(() => expect(unblock.disabled).toBe(true))
    expect(toggleCalls(s.calls)).toHaveLength(1)

    // The picker shares the child's guard: a pick while the toggle is out is
    // dropped, and the list stays open.
    await fireEvent.click(d1.querySelector('.block-picker-toggle')!)
    await fireEvent.click(d1.querySelector('button[data-service="roblox"]')!)
    expect(toggleCalls(s.calls)).toHaveLength(1)
    await waitFor(() => expect(d1.querySelector('button[data-service="roblox"]')).not.toBeNull())

    hold!(stored())
    await waitFor(() => expect(control(row(d1, 'youtube'), 'Unblock').disabled).toBe(false))
    expect(toggleCalls(s.calls)).toHaveLength(1)
  })

  it('toggle refresh', async () => {
    let hold: ((r: Response) => void) | null = null
    const s = server({}, { [`POST ${TOGGLES}`]: () => new Promise<Response>((r) => (hold = r)) })
    const d1 = await openAda()
    await waitFor(() => expect(grantGets(s.calls)).toBeGreaterThan(0))
    const grants0 = grantGets(s.calls)
    expect(blockedGets(s.calls)).toBe(1)

    await fireEvent.click(control(row(d1, 'youtube'), 'Unblock'))
    await waitFor(() => expect(toggleCalls(s.calls)).toHaveLength(1))
    // Nothing is refetched while the toggle is still out.
    await new Promise((r) => setTimeout(r, 0))
    expect(blockedGets(s.calls)).toBe(1)
    expect(grantGets(s.calls)).toBe(grants0)

    hold!(stored())
    await waitFor(() => expect(blockedGets(s.calls)).toBe(2))
    await waitFor(() => expect(grantGets(s.calls)).toBe(grants0 + 1))
  })

  it('toggle refresh after a stale poll', async () => {
    // A poll that left before the block answers with the grant the block
    // ended; the toggle's refresh must poll again rather than join it.
    const now = Date.now()
    const g7: Grant = {
      id: 7,
      child_id: 1,
      services: ['youtube'],
      clients: ['Kid phone'],
      started_at: new Date(now - 60_000).toISOString(),
      ends_at: new Date(now + 3_600_000).toISOString(),
      shared: [],
    }
    const adaView = view(1, 'Ada', {
      services: [{ id: 'youtube', name: 'YouTube', icon: ICON, state: 'unblocked', differs: [], unblocked_by: [], toggle: 'unblocked' }],
    })
    let grants: Grant[] = [g7]
    let holdNext = false
    let release: (() => void) | null = null
    const s = server(
      {},
      {
        '/api/v1/children/1/blocked': () => json(200, adaView),
        '/api/v1/grants': () => {
          if (!holdNext) return json(200, { grants })
          holdNext = false
          const leftWith = grants
          return new Promise<Response>((r) => (release = () => r(json(200, { grants: leftWith }))))
        },
        [`POST ${TOGGLES}`]: () => {
          grants = []
          return stored({ state: 'blocked', grant: { id: 7, ended: true } })
        },
      },
    )
    const d1 = await openAda()
    await waitFor(() => expect(document.querySelector('li[data-grant="7"]')).not.toBeNull())

    holdNext = true
    void refresh()
    await waitFor(() => expect(release).not.toBeNull())
    const before = grantGets(s.calls)

    await fireEvent.click(control(row(d1, 'youtube'), 'Block'))
    await waitFor(() => expect(toggleCalls(s.calls)).toHaveLength(1))
    release!()

    await waitFor(() => expect(grantGets(s.calls)).toBe(before + 1))
    await waitFor(() => expect(document.querySelector('li[data-grant="7"]')).toBeNull())
  })

  it('toggle outcome', async () => {
    const answers: Answer[] = [
      () => stored({ applied: false, failed: ['tablet'], shared: [{ client: 'tv', others: [{ id: 2, name: 'B' }] }] }),
      () => json(502, { error: 'adguard_unavailable', message: 'down' }),
      () => Promise.reject(new TypeError('Failed to fetch')),
      () => stored({ service_id: 'tiktok', applied: false, failed: ['tablet', 'tv'] }),
    ]
    const s = server({}, { [`POST ${TOGGLES}`]: () => answers.shift()!() })
    const d1 = await openAda()
    const alert = () => d1.querySelector('[data-toggle-error]')
    const notice = () => d1.querySelector('[data-toggle-shared]')

    // applied=false: the intent is stored, the refused client is named.
    await fireEvent.click(control(row(d1, 'youtube'), 'Unblock'))
    await waitFor(() => expect(alert()).not.toBeNull())
    expect(alert()?.getAttribute('role')).toBe('alert')
    expect(alert()?.getAttribute('data-error')).toBe('partial')
    expect(flat(alert())).toBe('Unblocked YouTube, except on tablet')
    expect(notice()?.getAttribute('role')).toBe('status')
    expect(flat(notice())).toBe('Also unblocked on tv (shared with B)')
    await waitFor(() => expect(blockedGets(s.calls)).toBe(2))

    // 502: an alert, no refetch, the row as it was; the last notice is gone.
    await waitFor(() => expect(control(row(d1, 'tiktok'), 'Unblock').disabled).toBe(false))
    await fireEvent.click(control(row(d1, 'tiktok'), 'Unblock'))
    await waitFor(() => expect(alert()?.getAttribute('data-error')).toBe('adguard_unavailable'))
    expect(flat(alert())).toBe("Can't reach AdGuard Home — try again in a moment")
    expect(notice()).toBeNull()
    expect(blockedGets(s.calls)).toBe(2)
    expect(row(d1, 'tiktok').getAttribute('data-state')).toBe('blocked')
    expect(controls(row(d1, 'tiktok'))).toEqual(['Unblock'])

    // A dropped connection is named as such.
    await waitFor(() => expect(control(row(d1, 'tiktok'), 'Unblock').disabled).toBe(false))
    await fireEvent.click(control(row(d1, 'tiktok'), 'Unblock'))
    await waitFor(() => expect(alert()?.getAttribute('data-error')).toBe('network'))
    expect(flat(alert())).toBe("Can't reach the server — check your connection")
    expect(toggleCalls(s.calls)).toHaveLength(3)

    // Several refused clients are listed comma-separated.
    await waitFor(() => expect(control(row(d1, 'tiktok'), 'Unblock').disabled).toBe(false))
    await fireEvent.click(control(row(d1, 'tiktok'), 'Unblock'))
    await waitFor(() => expect(alert()?.getAttribute('data-error')).toBe('partial'))
    expect(alert()?.getAttribute('role')).toBe('alert')
    expect(flat(alert())).toContain('except on tablet, tv')
    expect(toggleCalls(s.calls)).toHaveLength(4)
  })

  it('picker blocks', async () => {
    const adaView = view(1, 'Ada', {
      services: [
        { id: 'youtube', name: 'YouTube', icon: ICON, state: 'blocked', differs: [], toggle: null },
        { id: 'tiktok', name: 'TikTok', icon: ICON, state: 'unblocked', differs: [], unblocked_by: [], toggle: null },
      ],
    })
    const answers: Answer[] = [() => json(502, { error: 'adguard_unavailable', message: 'down' }), () => stored({ service_id: 'tiktok', state: 'blocked' })]
    const s = server({}, { '/api/v1/children/1/blocked': () => json(200, adaView), [`POST ${TOGGLES}`]: () => answers.shift()!() })
    const d1 = await openAda()
    const pickerToggle = () => d1.querySelector('.block-picker-toggle') as HTMLButtonElement
    expect(flat(pickerToggle())).toBe('Block a service…')

    await fireEvent.click(pickerToggle())
    // Already blocked: not offered.
    expect(d1.querySelector('button[data-service="youtube"]')).toBeNull()

    // A failed pick alerts and leaves the list open for another try.
    await fireEvent.click(d1.querySelector('button[data-service="tiktok"]')!)
    await waitFor(() => expect(d1.querySelector('[data-toggle-error]')?.getAttribute('data-error')).toBe('adguard_unavailable'))
    await waitFor(() => expect((d1.querySelector('button[data-service="tiktok"]') as HTMLButtonElement | null)?.disabled).toBe(false))
    expect(pickerToggle().getAttribute('aria-expanded')).toBe('true')
    expect(blockedGets(s.calls)).toBe(1)

    await fireEvent.click(d1.querySelector('button[data-service="tiktok"]')!)
    await waitFor(() => expect(blockedGets(s.calls)).toBe(2))
    expect(toggleCalls(s.calls).map((c) => c.body)).toEqual([
      '{"service_id":"tiktok","state":"blocked"}',
      '{"service_id":"tiktok","state":"blocked"}',
    ])
    await waitFor(() => expect(pickerToggle().getAttribute('aria-expanded')).toBe('false'))
    expect(d1.querySelector('[data-toggle-error]')).toBeNull()
  })

  it('picker needs the catalogue', async () => {
    server({ services: 'down' })
    const d1 = await openAda()
    expect(d1.querySelector('.block-picker')).toBeNull()
  })

  it('permanent attribution', async () => {
    const benView = view(2, 'Ben', {
      services: [
        {
          id: 'youtube', name: 'YouTube', icon: ICON, state: 'unblocked', differs: [], toggle: null,
          unblocked_by: [{ client: 'tv', grant_id: 0, child: { id: 3, name: 'Bob' }, permanent: true }],
        },
      ],
    })
    server({}, { '/api/v1/children/2/blocked': () => json(200, benView) })
    await mountLoaded()
    const d2 = details('details[data-blocked="2"]')
    await open(d2)
    await waitFor(() => expect(d2.querySelectorAll('li[data-row]').length).toBe(1))
    expect(flat(d2.querySelector('li .partial'))).toBe('— unblocked on tv by Bob (permanent)')
    expect(d2.textContent).not.toContain("Bob's grant")
  })
})
