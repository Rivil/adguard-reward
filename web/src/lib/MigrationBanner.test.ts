// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { tick } from 'svelte'
import { afterEach, describe, expect, it, vi } from 'vitest'
import MigrationBanner from './MigrationBanner.svelte'
import { migrationDismissed, type MigrationOffer } from './api'

type Call = { method: string; url: string }
type Answer = () => Response | Promise<Response>
type Routes = Record<string, Answer>

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function mockFetch(routes: Routes): Call[] {
  const calls: Call[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit) => {
      const method = init?.method ?? 'GET'
      calls.push({ method, url })
      const answer = routes[`${method} ${url}`] ?? routes[url]
      if (!answer) throw new Error('unexpected fetch ' + method + ' ' + url)
      return answer()
    }),
  )
  return calls
}

const offer: MigrationOffer = {
  global: ['tiktok', 'roblox'],
  clients: [{ name: 'Kid tablet', child: { id: 1, name: 'Ada' }, gains: ['roblox', 'tiktok'] }],
}
const empty: MigrationOffer = { global: [], clients: [] }

function banner(): Element | null {
  return document.querySelector('[data-migration]')
}

function posts(calls: Call[]): Call[] {
  return calls.filter((c) => c.method === 'POST')
}

afterEach(() => {
  vi.unstubAllGlobals()
  migrationDismissed.set(false)
  sessionStorage.clear()
  localStorage.clear()
})

describe('MigrationBanner', () => {
  it('shows who gains what', async () => {
    mockFetch({ '/api/v1/migration': () => json(200, offer) })
    render(MigrationBanner, { onMigrated: vi.fn() })
    const el = await screen.findByRole('status')
    expect(el.hasAttribute('data-migration')).toBe(true)
    expect(el.textContent?.replace(/\s+/g, ' ')).toContain('Kid tablet (Ada) gains roblox, tiktok')
  })

  it('says "keeps its list" when nothing is gained', async () => {
    mockFetch({
      '/api/v1/migration': () =>
        json(200, { global: ['tiktok'], clients: [{ name: 'Kid tablet', child: { id: 1, name: 'Ada' }, gains: [] }] }),
    })
    render(MigrationBanner, { onMigrated: vi.fn() })
    const el = await screen.findByRole('status')
    expect(el.textContent?.replace(/\s+/g, ' ')).toContain('Kid tablet (Ada) keeps its list')
  })

  it('renders nothing for an empty offer or a failed call', async () => {
    mockFetch({ '/api/v1/migration': () => json(200, empty) })
    const first = render(MigrationBanner, { onMigrated: vi.fn() })
    await tick()
    await tick()
    expect(banner()).toBeNull()
    first.unmount()

    mockFetch({ '/api/v1/migration': () => json(502, { error: 'adguard_unavailable', message: 'down' }) })
    render(MigrationBanner, { onMigrated: vi.fn() })
    await tick()
    await tick()
    expect(banner()).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('nothing written until Migrate', async () => {
    const onMigrated = vi.fn()
    const calls = mockFetch({
      '/api/v1/migration': () => json(200, offer),
      'POST /api/v1/migration': () => json(200, { migrated: ['Kid tablet'] }),
    })
    render(MigrationBanner, { onMigrated })
    await screen.findByRole('status')
    await tick()
    expect(calls).toEqual([{ method: 'GET', url: '/api/v1/migration' }])

    await fireEvent.click(screen.getByRole('button', { name: 'Migrate' }))
    await waitFor(() => expect(onMigrated).toHaveBeenCalledTimes(1))
    expect(posts(calls)).toEqual([{ method: 'POST', url: '/api/v1/migration' }])
    expect(banner()).toBeNull()
  })

  it('Not now hides until next login', async () => {
    const calls = mockFetch({ '/api/v1/migration': () => json(200, offer) })
    const first = render(MigrationBanner, { onMigrated: vi.fn() })
    await screen.findByRole('status')
    await fireEvent.click(screen.getByRole('button', { name: 'Not now' }))
    expect(banner()).toBeNull()
    expect(posts(calls)).toHaveLength(0)
    first.unmount()

    // Navigating away and back does not resurface it.
    const second = render(MigrationBanner, { onMigrated: vi.fn() })
    await tick()
    await tick()
    expect(banner()).toBeNull()
    second.unmount()

    // What login() does: the next sign-in shows it again.
    migrationDismissed.set(false)
    render(MigrationBanner, { onMigrated: vi.fn() })
    expect(await screen.findByRole('status')).toBeTruthy()

    expect(sessionStorage.length).toBe(0)
    expect(localStorage.length).toBe(0)
  })

  it('migrate 502 shows an alert', async () => {
    const onMigrated = vi.fn()
    mockFetch({
      '/api/v1/migration': () => json(200, offer),
      'POST /api/v1/migration': () => json(502, { error: 'adguard_unavailable', message: 'down' }),
    })
    render(MigrationBanner, { onMigrated })
    await screen.findByRole('status')
    await fireEvent.click(screen.getByRole('button', { name: 'Migrate' }))
    const alert = await screen.findByRole('alert')
    expect(alert.getAttribute('data-error')).toBe('adguard_unavailable')
    expect(banner()).not.toBeNull()
    expect(onMigrated).not.toHaveBeenCalled()
  })
})
