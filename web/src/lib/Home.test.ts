// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { get } from 'svelte/store'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Home from './Home.svelte'
import { route, type BlockedView } from './api'

type Call = { method: string; url: string; init: RequestInit }
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
      calls.push({ method, url, init })
      const answer = routes[`${method} ${url}`] ?? routes[url]
      if (!answer) throw new Error('unexpected fetch ' + method + ' ' + url)
      return answer()
    }),
  )
  return calls
}

const ICON = 'PHN2Zy8+'

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

const twoChildren = {
  children: [
    { id: 1, name: 'Ada', clients: ['Kid phone'] },
    { id: 2, name: 'Ben', clients: [] },
  ],
}

const noOffer = () => json(200, { global: [], clients: [] })

function routesFor(ada: BlockedView, ben: BlockedView): Routes {
  return {
    '/api/v1/migration': noOffer,
    '/api/v1/children': () => json(200, twoChildren),
    '/api/v1/children/1/blocked': () => json(200, ada),
    '/api/v1/children/2/blocked': () => json(200, ben),
  }
}

function section(id: number): HTMLElement {
  const el = document.querySelector(`section[data-child="${id}"]`)
  if (!el) throw new Error(`no section for child ${id}`)
  return el as HTMLElement
}

afterEach(() => {
  vi.unstubAllGlobals()
  history.replaceState(null, '', '/')
  route.set('home')
})

describe('Home', () => {
  it('lists blocked services per child', async () => {
    mockFetch(routesFor(view(1, 'Ada'), view(2, 'Ben', { clients: [], services: view(2, 'Ben').services.map((s) => ({ ...s, state: 'unblocked' })) })))
    render(Home, { username: 'mum', onLogout: vi.fn() })

    expect(await screen.findByRole('heading', { name: 'Ada' })).toBeTruthy()
    expect(document.querySelectorAll('section[data-child]')).toHaveLength(2)

    const ada = section(1)
    const names = [...ada.querySelectorAll('li')].map((li) => li.textContent?.trim())
    expect(names).toEqual(['YouTube', 'TikTok'])
    for (const img of ada.querySelectorAll('img')) {
      expect(img.getAttribute('src')).toBe('data:image/svg+xml;base64,PHN2Zy8+')
      expect(img.getAttribute('alt')).toBe('')
    }
    expect(ada.textContent).not.toContain('Roblox')

    const ben = section(2)
    expect(ben.querySelectorAll('li')).toHaveLength(0)
    expect(ben.textContent).toContain('Nothing is blocked')
  })

  it('marks partial services', async () => {
    const ada = view(1, 'Ada', {
      services: [{ id: 'roblox', name: 'Roblox', icon: ICON, state: 'partial', differs: ['Kid phone'] }],
    })
    mockFetch(routesFor(ada, view(2, 'Ben')))
    render(Home, { username: 'mum', onLogout: vi.fn() })
    await screen.findByRole('heading', { name: 'Ada' })

    const li = section(1).querySelector('li[data-state="partial"]')
    expect(li).not.toBeNull()
    expect(li?.textContent).toContain('Roblox')
    expect(li?.textContent).toContain('unblocked on Kid phone')
  })

  it('shows the uses-global badge', async () => {
    const ada = view(1, 'Ada', {
      clients: [
        { name: 'Kid tablet', missing: false, uses_global: true },
        { name: 'Ghost', missing: true, uses_global: false },
      ],
    })
    mockFetch(routesFor(ada, view(2, 'Ben')))
    render(Home, { username: 'mum', onLogout: vi.fn() })
    await screen.findByRole('heading', { name: 'Ada' })

    const badge = section(1).querySelector('[data-badge="global"]')
    expect(badge).not.toBeNull()
    expect(badge?.parentElement?.textContent).toContain('Kid tablet')
    expect(section(1).querySelector('[data-badge="missing"]')?.parentElement?.textContent).toContain('Ghost')
    expect(section(1).querySelector('[data-badge="missing"]')?.textContent).toBe('missing')
  })

  it('AdGuard down shows an error state', async () => {
    mockFetch(routesFor(view(1, 'Ada'), view(2, 'Ben')))
    const first = render(Home, { username: 'mum', onLogout: vi.fn() })
    await screen.findByRole('heading', { name: 'Ada' })
    expect(screen.getAllByText('YouTube').length).toBeGreaterThan(0)
    first.unmount()

    mockFetch({
      '/api/v1/migration': noOffer,
      '/api/v1/children': () => json(200, twoChildren),
      '/api/v1/children/1/blocked': () => json(502, { error: 'adguard_unavailable', message: 'down' }),
      '/api/v1/children/2/blocked': () => json(200, view(2, 'Ben')),
    })
    render(Home, { username: 'mum', onLogout: vi.fn() })
    const alert = await screen.findByRole('alert')
    expect(alert.getAttribute('data-error')).toBe('adguard_unavailable')
    expect(alert.textContent).toContain("Can't reach AdGuard Home")
    expect(screen.queryAllByRole('list')).toHaveLength(0)
    expect(document.querySelectorAll('section[data-child]')).toHaveLength(0)
    expect(screen.queryByText('YouTube')).toBeNull()
    expect(screen.queryByText('Ada')).toBeNull()
    cleanup()

    mockFetch({
      '/api/v1/migration': noOffer,
      '/api/v1/children': () => {
        throw new TypeError('Failed to fetch')
      },
    })
    render(Home, { username: 'mum', onLogout: vi.fn() })
    expect((await screen.findByRole('alert')).getAttribute('data-error')).toBe('network')
  })

  it('refetches on every mount', async () => {
    const calls = mockFetch(routesFor(view(1, 'Ada'), view(2, 'Ben')))
    const first = render(Home, { username: 'mum', onLogout: vi.fn() })
    await screen.findByRole('heading', { name: 'Ada' })
    first.unmount()
    render(Home, { username: 'mum', onLogout: vi.fn() })
    await screen.findByRole('heading', { name: 'Ada' })
    expect(calls.filter((c) => c.url === '/api/v1/children')).toHaveLength(2)
  })

  it('shows "No children yet" for an empty list', async () => {
    mockFetch({ '/api/v1/migration': noOffer, '/api/v1/children': () => json(200, { children: [] }) })
    render(Home, { username: 'mum', onLogout: vi.fn() })
    expect(await screen.findByText('No children yet')).toBeTruthy()
  })

  it('Children button routes', async () => {
    mockFetch({ '/api/v1/migration': noOffer, '/api/v1/children': () => json(200, { children: [] }) })
    render(Home, { username: 'mum', onLogout: vi.fn() })
    await fireEvent.click(screen.getByRole('button', { name: 'Children' }))
    expect(location.pathname).toBe('/children')
    expect(get(route)).toBe('children')
  })

  it('hosts the migration banner', async () => {
    mockFetch({
      ...routesFor(view(1, 'Ada'), view(2, 'Ben')),
      '/api/v1/migration': () =>
        json(200, {
          global: ['tiktok', 'roblox'],
          clients: [{ name: 'Kid tablet', child: { id: 1, name: 'Ada' }, gains: ['roblox', 'tiktok'] }],
        }),
    })
    const first = render(Home, { username: 'mum', onLogout: vi.fn() })
    await screen.findByRole('heading', { name: 'Ada' })
    expect(await screen.findByRole('status')).toBeTruthy()
    expect(document.querySelector('[data-migration]')).not.toBeNull()
    first.unmount()

    mockFetch(routesFor(view(1, 'Ada'), view(2, 'Ben')))
    render(Home, { username: 'mum', onLogout: vi.fn() })
    await screen.findByRole('heading', { name: 'Ada' })
    expect(document.querySelector('[data-migration]')).toBeNull()
  })

  it('migration applied re-fetches blocked views', async () => {
    const calls = mockFetch({
      ...routesFor(view(1, 'Ada'), view(2, 'Ben')),
      '/api/v1/migration': () =>
        json(200, {
          global: ['tiktok'],
          clients: [{ name: 'Kid tablet', child: { id: 1, name: 'Ada' }, gains: ['tiktok'] }],
        }),
      'POST /api/v1/migration': () => json(200, { migrated: ['Kid tablet'] }),
    })
    render(Home, { username: 'mum', onLogout: vi.fn() })
    await screen.findByRole('heading', { name: 'Ada' })
    const blocked = () => calls.filter((c) => c.url === '/api/v1/children/1/blocked').length
    expect(blocked()).toBe(1)
    await fireEvent.click(await screen.findByRole('button', { name: 'Migrate' }))
    await waitFor(() => expect(blocked()).toBe(2))
  })
})
