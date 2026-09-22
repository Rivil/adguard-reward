// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ActiveGrants from './ActiveGrants.svelte'
import { refresh } from './activeGrants'
import { setOnUnauthorized, type Grant } from './api'

type Call = { method: string; url: string; body: string | null }
type Answer = () => Response | Promise<Response>
type Routes = Record<string, Answer>

const T0 = Date.parse('2026-09-21T10:00:00Z')

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

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

function grant(id: number, endsInSeconds: number, services = ['youtube'], spanSeconds = endsInSeconds): Grant {
  return {
    id,
    child_id: 1,
    services,
    clients: ['Kid phone'],
    started_at: new Date(T0 + (endsInSeconds - spanSeconds) * 1000).toISOString(),
    ends_at: new Date(T0 + endsInSeconds * 1000).toISOString(),
  }
}

/** A mutable server: the GET reads `state.grants` so a re-fetch sees changes. */
function server(grants: Grant[], extra: Routes = {}) {
  const state = { grants }
  const routes: Routes = {
    'GET /api/v1/grants': () => json(200, { grants: state.grants }),
    ...extra,
  }
  return { state, calls: mockFetch(routes) }
}

const gets = (calls: Call[]) => calls.filter((c) => c.method === 'GET' && c.url === '/api/v1/grants').length

function row(id: number): HTMLElement {
  const el = document.querySelector(`li[data-grant="${id}"]`)
  if (!el) throw new Error(`no row for grant ${id}`)
  return el as HTMLElement
}

function countdown(id: number): string {
  return row(id).querySelector('[data-countdown]')?.textContent?.trim() ?? ''
}

function button(id: number, label: RegExp): HTMLButtonElement {
  for (const b of row(id).querySelectorAll('button')) {
    if (label.test(b.textContent ?? '')) return b
  }
  throw new Error(`no ${label} button on grant ${id}`)
}

/** Let fetch promise chains finish under fake timers. */
async function settle() {
  for (let i = 0; i < 6; i++) await vi.advanceTimersByTimeAsync(0)
}

const names = { childNames: new Map([[1, 'Ada']]), serviceNames: { youtube: 'YouTube', tiktok: 'TikTok' } }

async function mount(grants: Grant[], extra: Routes = {}) {
  const s = server(grants, extra)
  await refresh()
  const r = render(ActiveGrants, names)
  return { ...s, ...r }
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(T0)
  setOnUnauthorized(vi.fn())
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('ActiveGrants', () => {
  it('countdown follows ends_at', async () => {
    await mount([grant(1, 600), grant(2, 7200)])
    expect(countdown(1)).toBe('10:00')
    expect(countdown(2)).toBe('2:00:00')
    await vi.advanceTimersByTimeAsync(1000)
    expect(countdown(1)).toBe('9:59')

    vi.setSystemTime(T0 + 1000 + 5 * 60 * 1000)
    await vi.advanceTimersByTimeAsync(1000)
    expect(countdown(1)).toBe('4:58')
    expect(countdown(2)).toBe('1:54:58')
  })

  it('expiry asks the server', async () => {
    const s = await mount([grant(1, 600)])
    const before = gets(s.calls)
    await vi.advanceTimersByTimeAsync(600 * 1000)
    expect(countdown(1)).toBe('0:00')
    expect(gets(s.calls)).toBe(before + 1)
    await settle()
    expect(row(1)).toBeTruthy()

    await vi.advanceTimersByTimeAsync(5000)
    expect(gets(s.calls)).toBe(before + 1)
    expect(countdown(1)).toBe('0:00')

    s.state.grants = []
    await refresh()
    await settle()
    expect(document.querySelector('li[data-grant="1"]')).toBeNull()
    expect(screen.getByText('Nothing is unlocked right now')).toBeTruthy()
  })

  it('extend posts own duration', async () => {
    const s = await mount([grant(1, 3600), grant(2, 5400), grant(3, 48 * 3600)], {
      'POST /api/v1/grants/1/extend': () => {
        s.state.grants = s.state.grants.map((g) => (g.id === 1 ? { ...g, ends_at: new Date(T0 + 7200 * 1000).toISOString() } : g))
        return json(200, { id: 1, ends_at: new Date(T0 + 7200 * 1000).toISOString() })
      },
      'POST /api/v1/grants/2/extend': () => json(200, { id: 2, ends_at: 'x' }),
      'POST /api/v1/grants/3/extend': () => json(200, { id: 3, ends_at: 'x' }),
    })
    expect(button(1, /^Extend/).textContent?.trim()).toBe('Extend 1 h')
    expect(button(2, /^Extend/).textContent?.trim()).toBe('Extend 1 h 30 min')
    expect(button(3, /^Extend/).textContent?.trim()).toBe('Extend 24 h')

    const before = gets(s.calls)
    await fireEvent.click(button(1, /^Extend/))
    await settle()
    await fireEvent.click(button(2, /^Extend/))
    await settle()
    await fireEvent.click(button(3, /^Extend/))
    await settle()
    const posts = s.calls.filter((c) => c.method === 'POST')
    expect(posts.map((c) => [c.url, c.body])).toEqual([
      ['/api/v1/grants/1/extend', '{"duration":3600}'],
      ['/api/v1/grants/2/extend', '{"duration":5400}'],
      ['/api/v1/grants/3/extend', '{"duration":86400}'],
    ])
    expect(gets(s.calls)).toBe(before + 3)
    expect(countdown(1)).toBe('2:00:00')
  })

  it('end 502 keeps the entry', async () => {
    let ok = false
    const s = await mount([grant(7, 600)], {
      'POST /api/v1/grants/7/end': () => {
        if (!ok) return json(502, { error: 'adguard_unavailable', message: 'could not re-block Kid phone — the grant stays active and will be retried' })
        s.state.grants = []
        return new Response(null, { status: 204 })
      },
    })
    const before = gets(s.calls)
    await fireEvent.click(button(7, /^End/))
    await settle()
    const alert = row(7).querySelector('[role="alert"]')
    expect(alert?.textContent).toContain('Kid phone')
    expect(gets(s.calls)).toBe(before)

    ok = true
    await fireEvent.click(button(7, /^End/))
    await settle()
    expect(gets(s.calls)).toBe(before + 1)
    expect(document.querySelector('li[data-grant="7"]')).toBeNull()
    expect(screen.getByText('Nothing is unlocked right now')).toBeTruthy()
  })

  it('row is busy while its own request is pending', async () => {
    let release!: (r: Response) => void
    const s = await mount([grant(7, 600), grant(8, 600)], {
      'POST /api/v1/grants/7/end': () => new Promise<Response>((r) => (release = r)),
    })
    await fireEvent.click(button(7, /^End/))
    expect(button(7, /^End/).disabled).toBe(true)
    expect(button(7, /^Extend/).disabled).toBe(true)
    expect(button(8, /^End/).disabled).toBe(false)
    expect(button(8, /^Extend/).disabled).toBe(false)

    s.state.grants = [grant(8, 600)]
    release(new Response(null, { status: 204 }))
    await settle()
    expect(document.querySelector('li[data-grant="7"]')).toBeNull()
    expect(button(8, /^End/).disabled).toBe(false)

    // A row whose request fails is enabled again afterwards.
    s.state.grants = [grant(8, 600), grant(9, 600)]
    await refresh()
    await settle()
    mockFetch({
      'GET /api/v1/grants': () => json(200, { grants: s.state.grants }),
      'POST /api/v1/grants/9/end': () => json(502, { error: 'adguard_unavailable', message: 'could not re-block Kid phone' }),
    })
    await fireEvent.click(button(9, /^End/))
    await settle()
    expect(button(9, /^End/).disabled).toBe(false)
    expect(button(9, /^Extend/).disabled).toBe(false)
  })

  it('extend 404 refreshes silently', async () => {
    const s = await mount([grant(1, 3600)], {
      'POST /api/v1/grants/1/extend': () => {
        s.state.grants = []
        return json(404, { error: 'not_found', message: 'no such grant' })
      },
    })
    const before = gets(s.calls)
    await fireEvent.click(button(1, /^Extend/))
    await settle()
    expect(document.querySelector('[role="alert"]')).toBeNull()
    expect(gets(s.calls)).toBe(before + 1)
    expect(document.querySelector('li[data-grant="1"]')).toBeNull()
  })

  it('unreachable badge', async () => {
    const s = await mount([grant(1, 600), grant(2, 7200)])
    expect(document.querySelector('[data-badge="unreachable"]')).toBeNull()

    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new TypeError('Failed to fetch')
      }),
    )
    await refresh()
    await settle()
    expect(document.querySelector('[data-badge="unreachable"]')).not.toBeNull()
    expect(row(1)).toBeTruthy()
    expect(row(2)).toBeTruthy()
    const was = countdown(1)
    await vi.advanceTimersByTimeAsync(1000)
    expect(countdown(1)).not.toBe(was)
    expect(countdown(1)).toBe('9:59')

    mockFetch({ 'GET /api/v1/grants': () => json(200, { grants: s.state.grants }) })
    await refresh()
    await settle()
    expect(document.querySelector('[data-badge="unreachable"]')).toBeNull()
  })

  it('unmount stops ticking', async () => {
    const s = await mount([grant(1, 600)])
    const before = gets(s.calls)
    s.unmount()
    await vi.advanceTimersByTimeAsync(60 * 1000)
    expect(gets(s.calls)).toBe(before)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('names', async () => {
    await mount([grant(1, 600, ['youtube', 'gone']), { ...grant(2, 600), child_id: 7 }])
    const text = row(1).textContent ?? ''
    expect(text).toContain('Ada')
    expect(text).toContain('YouTube')
    expect(text).toContain('gone')
    expect(text).not.toContain('TikTok')
    // A child the list does not know is still identified, by id.
    expect(row(2).querySelector('strong')?.textContent).toBe('child 7')
  })

  it('row messages', async () => {
    // 502 carries the client names AdGuard would not re-block, so its own
    // message wins — but only when it has one. Anything else goes through
    // messageFor, so a 429's raw text is replaced and a dropped connection
    // gets the network line.
    let mode: 'network' | 'rate' | 'empty' = 'network'
    await mount([grant(7, 600)], {
      'POST /api/v1/grants/7/end': () => {
        if (mode === 'network') return Promise.reject(new TypeError('Failed to fetch'))
        if (mode === 'rate') return new Response(JSON.stringify({ error: 'rate_limited', message: 'slow down' }), { status: 429, headers: { 'Content-Type': 'application/json', 'Retry-After': '7' } })
        return json(502, { error: 'adguard_unavailable', message: '' })
      },
    })
    const alert = () => row(7).querySelector('[role="alert"]')?.textContent

    await fireEvent.click(button(7, /^End/))
    await settle()
    expect(alert()).toBe("Can't reach the server — check your connection")

    mode = 'rate'
    await fireEvent.click(button(7, /^End/))
    await settle()
    expect(alert()).toBe('Too many attempts — wait 7s')

    mode = 'empty'
    await fireEvent.click(button(7, /^End/))
    await settle()
    expect(alert()).toBe("Can't reach AdGuard Home — try again in a moment")
  })

  it('empty state', async () => {
    await mount([])
    await waitFor(() => expect(screen.getByText('Nothing is unlocked right now')).toBeTruthy())
    expect(screen.getByRole('heading', { name: /Unlocked now/ })).toBeTruthy()
  })
})
