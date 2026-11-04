// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { afterEach, describe, expect, it, vi } from 'vitest'
import History from './History.svelte'
import type { HistoryGrant, HistoryPage } from './api'
import { formatTime } from './history'

type Answer = () => Response | Promise<Response>

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}
const fail = (status: number, code: string) => () => json(status, { error: { code, message: code } })

/** Routes keyed by URL; each is a queue of answers, consumed in order. */
function mockFetch(routes: Record<string, Answer[]>): string[] {
  const calls: string[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => {
      calls.push(url)
      const answer = routes[url]?.shift()
      if (!answer) throw new Error('unexpected fetch ' + url)
      return answer()
    }),
  )
  return calls
}

const URL3 = '/api/v1/children/3/history'

function grant(id: number, over: Partial<HistoryGrant> = {}): HistoryGrant {
  return {
    id,
    status: 'expired',
    started_at: '2026-10-01T10:00:00Z',
    ends_at: '2026-10-01T11:00:00Z',
    ended_at: '2026-10-01T11:00:05Z',
    services: [{ id: 'youtube', name: 'YouTube', removed_at: null, removed_reason: null }],
    extensions: [],
    ...over,
  }
}

function page(
  grants: HistoryGrant[],
  next_before: number | null = null,
  child: { id: number; name: string } = { id: 3, name: 'Ada' },
): HistoryPage {
  return { child, grants, next_before }
}

const URL4 = '/api/v1/children/4/history'
const BOB = { id: 4, name: 'Bob' }

/** An answer held until release() — for responses that arrive after the screen moved on. */
function gated(answer: Answer): { answer: Answer; release: () => void } {
  let release!: () => void
  const gate = new Promise<void>((r) => (release = r))
  return {
    answer: async () => {
      await gate
      return answer()
    },
    release,
  }
}

const ok = (p: HistoryPage) => () => json(200, p)

function shownIds(): number[] {
  return [...document.querySelectorAll('li[data-grant]')].map((el) => Number((el as HTMLElement).dataset.grant))
}

function grantEl(id: number): HTMLElement {
  const el = document.querySelector(`li[data-grant="${id}"]`)
  if (!el) throw new Error('no grant ' + id)
  return el as HTMLElement
}

async function mount(child: number | null, routes: Record<string, Answer[]>, onBack = () => {}) {
  const calls = mockFetch(routes)
  render(History, { child, onBack })
  return calls
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('History', () => {
  it('shows the child name and grants newest first with start, end and status', async () => {
    const live = grant(5, { status: 'ongoing', ends_at: '2000-01-01T00:00:00Z', ended_at: null })
    const legacy = grant(4, { status: 'ended_unknown', ended_at: '2026-10-01T10:40:00Z' })
    await mount(3, { [URL3]: [ok(page([live, legacy]))] })

    await screen.findByRole('heading', { name: 'Ada — history' })
    expect(shownIds()).toEqual([5, 4])
    expect(grantEl(5).querySelector('[data-status]')?.textContent).toBe('Ongoing')
    expect(grantEl(5).querySelector('[data-ended]')).toBeNull()
    expect(grantEl(4).querySelector('[data-status]')?.textContent).toBe('Ended (reason unknown)')
    expect(grantEl(4).textContent).not.toContain('Ended early')
    expect(grantEl(4).querySelector('[data-started]')?.textContent).toBe(formatTime(legacy.started_at))
    expect(grantEl(4).querySelector('[data-ended]')?.textContent).toBe(formatTime('2026-10-01T10:40:00Z'))
  })

  it('lists a removed service inside its grant with its marker, and "—" for no services', async () => {
    const at = '2026-10-01T10:30:00Z'
    const trimmed = grant(2, {
      status: 'ongoing',
      ended_at: null,
      services: [
        { id: 'tiktok', name: 'TikTok', removed_at: null, removed_reason: null },
        { id: 'youtube', name: 'YouTube', removed_at: at, removed_reason: 'permanent_block' },
      ],
    })
    await mount(3, { [URL3]: [ok(page([trimmed, grant(1, { services: [] })]))] })

    await waitFor(() => expect(shownIds()).toEqual([2, 1]))
    const yt = grantEl(2).querySelector('li[data-service="youtube"]') as HTMLElement
    expect(yt.textContent).toContain('YouTube')
    expect(yt.querySelector('[data-removed]')?.textContent).toBe(`removed ${formatTime(at)} · permanent block`)
    expect(grantEl(2).querySelector('li[data-service="tiktok"] [data-removed]')).toBeNull()
    expect(yt.classList.contains('removed')).toBe(true)
    expect(grantEl(2).querySelector('li[data-service="tiktok"]')?.classList.contains('removed')).toBe(false)
    expect(grantEl(1).querySelector('[data-no-services]')?.textContent).toBe('—')
  })

  it('renders every extension in chronological order, old -> new', async () => {
    const x1 = { at: '2026-10-01T10:30:00Z', old_ends_at: '2026-10-01T11:00:00Z', new_ends_at: '2026-10-01T11:30:00Z' }
    const x2 = { at: '2026-10-01T11:10:00Z', old_ends_at: '2026-10-01T11:30:00Z', new_ends_at: '2026-10-01T12:00:00Z' }
    await mount(3, { [URL3]: [ok(page([grant(1, { status: 'ended_early', extensions: [x1, x2] })]))] })

    await waitFor(() => expect(shownIds()).toEqual([1]))
    const entries = [...grantEl(1).querySelectorAll('[data-extension]')].map((el) => el.textContent?.trim())
    expect(entries).toEqual([
      `Extended ${formatTime(x1.at)}: ${formatTime(x1.old_ends_at)} → ${formatTime(x1.new_ends_at)}`,
      `Extended ${formatTime(x2.at)}: ${formatTime(x2.old_ends_at)} → ${formatTime(x2.new_ends_at)}`,
    ])
  })

  it('loads more once on two rapid clicks, appends deduped, and hides when done', async () => {
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    const calls = await mount(3, {
      [URL3]: [ok(page([grant(9), grant(8)], 8))],
      [`${URL3}?before=8`]: [
        async () => {
          await gate
          return json(200, page([grant(8), grant(7)], null))
        },
      ],
    })
    const more = await screen.findByRole('button', { name: 'Load more' })
    await fireEvent.click(more)
    await fireEvent.click(more)
    expect(calls.filter((u) => u.includes('before=')).length).toBe(1)
    expect(shownIds()).toEqual([9, 8])

    release()
    await waitFor(() => expect(shownIds()).toEqual([9, 8, 7]))
    expect(calls).toEqual([URL3, `${URL3}?before=8`])
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()
  })

  it('keeps shown rows on a paging error and retries with the same cursor', async () => {
    const calls = await mount(3, {
      [URL3]: [ok(page([grant(9)], 9))],
      [`${URL3}?before=9`]: [fail(500, 'internal'), ok(page([grant(4)], null))],
    })
    await fireEvent.click(await screen.findByRole('button', { name: 'Load more' }))

    await screen.findByRole('alert')
    expect(shownIds()).toEqual([9])
    await fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(shownIds()).toEqual([9, 4]))
    expect(calls).toEqual([URL3, `${URL3}?before=9`, `${URL3}?before=9`])
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('shows "No such child" with back and no fetch for a null child', async () => {
    const onBack = vi.fn()
    const calls = await mount(null, {}, onBack)
    expect(await screen.findByText('No such child')).toBeTruthy()
    expect(document.querySelector('ul.list')).toBeNull()
    await fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    expect(onBack).toHaveBeenCalledOnce()
    expect(calls).toEqual([])
  })

  it('shows "No such child" with back and no list for a 404', async () => {
    await mount(3, { [URL3]: [fail(404, 'not_found')] })
    expect(await screen.findByText('No such child')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Back' })).toBeTruthy()
    expect(document.querySelector('ul.list')).toBeNull()
    expect(screen.queryByText('No grants yet')).toBeNull()
  })

  it('shows retry, not the empty state, when the first page fails', async () => {
    const calls = await mount(3, { [URL3]: [fail(500, 'internal'), ok(page([grant(1)]))] })
    await screen.findByRole('alert')
    expect(screen.queryByText('No grants yet')).toBeNull()
    expect(screen.queryByText('No such child')).toBeNull()
    await fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(shownIds()).toEqual([1]))
    expect(calls).toEqual([URL3, URL3])
  })

  it('shows "No grants yet" for a child with no grants', async () => {
    await mount(3, { [URL3]: [ok(page([]))] })
    expect(await screen.findByText('No grants yet')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()
  })

  it('shows only "Loading…" under a plain History heading until the first page lands', async () => {
    let atFetch = ''
    const first = gated(ok(page([grant(1)])))
    mockFetch({
      [URL3]: [
        () => {
          atFetch = document.body.textContent ?? ''
          return first.answer()
        },
      ],
    })
    render(History, { child: 3, onBack: () => {} })

    // What was on screen when the fetch went out: nothing but the loading line.
    await waitFor(() => expect(atFetch).not.toBe(''))
    expect(atFetch).toContain('Loading…')
    expect(atFetch).not.toContain('No grants yet')
    expect(atFetch).not.toContain('No such child')
    expect(screen.getByRole('heading').textContent).toBe('History')
    expect(screen.getByRole('status').textContent).toBe('Loading…')

    first.release()
    await screen.findByRole('heading', { name: 'Ada — history' })
  })

  it('titles a null child plainly "History"', async () => {
    await mount(null, {})
    await screen.findByText('No such child')
    expect(screen.getByRole('heading').textContent).toBe('History')
  })

  it('shows "—" for the end of a finished grant the server sent no ended_at for', async () => {
    await mount(3, { [URL3]: [ok(page([grant(1, { status: 'ended_unknown', ended_at: null })]))] })
    await waitFor(() => expect(shownIds()).toEqual([1]))
    expect(grantEl(1).querySelector('[data-ended]')?.textContent).toBe('—')
  })

  it('shows the API error code and its message when the first page fails', async () => {
    await mount(3, { [URL3]: [() => json(500, { error: 'internal', message: 'Database is locked' })] })
    const alert = await screen.findByRole('alert')
    expect(alert.dataset.error).toBe('internal')
    expect(alert.textContent).toBe('Database is locked')
  })

  it('shows a network failure, not a blank alert, when the server is unreachable', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new TypeError('Failed to fetch')
      }),
    )
    render(History, { child: 3, onBack: () => {} })
    const alert = await screen.findByRole('alert')
    expect(alert.dataset.error).toBe('network')
    expect(alert.textContent).toBe("Can't reach the server — check your connection")
  })

  it('shows "Loading…", not the empty state, while a retry is in flight', async () => {
    const retry = gated(ok(page([grant(1)])))
    await mount(3, { [URL3]: [fail(500, 'internal'), retry.answer] })
    await screen.findByRole('alert')

    await fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await screen.findByText('Loading…')
    expect(screen.queryByText('No grants yet')).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()

    retry.release()
    await waitFor(() => expect(shownIds()).toEqual([1]))
  })

  it('fetches a further page once when Load more is clicked twice before the screen updates', async () => {
    const more = gated(ok(page([grant(7)])))
    const calls = await mount(3, { [URL3]: [ok(page([grant(9)], 9))], [`${URL3}?before=9`]: [more.answer] })
    const button = await screen.findByRole('button', { name: 'Load more' })

    // Both clicks land before Svelte flushes disabled, so only the loadingMore guard stops the second.
    button.click()
    button.click()
    await waitFor(() => expect((button as HTMLButtonElement).disabled).toBe(true))
    expect(calls).toEqual([URL3, `${URL3}?before=9`])

    more.release()
    await waitFor(() => expect(shownIds()).toEqual([9, 7]))
  })

  describe('after switching child', () => {
    function mountSwitch(routes: Record<string, Answer[]>) {
      const calls = mockFetch(routes)
      const view = render(History, { child: 3, onBack: () => {} })
      return { calls, toBob: () => view.rerender({ child: 4, onBack: () => {} }) }
    }

    /** Lets a released response run to completion (fetch, JSON parse, state writes, render). */
    const settle = () => new Promise((r) => setTimeout(r, 30))

    it("drops the earlier child's first page when it lands late", async () => {
      const late = gated(ok(page([grant(1)])))
      const { toBob } = mountSwitch({ [URL3]: [late.answer], [URL4]: [ok(page([grant(2)], null, BOB))] })
      await toBob()
      await screen.findByRole('heading', { name: 'Bob — history' })

      late.release()
      await settle()
      expect(screen.getByRole('heading').textContent).toBe('Bob — history')
      expect(shownIds()).toEqual([2])
    })

    it("drops the earlier child's first-page error when it lands late", async () => {
      const late = gated(fail(404, 'not_found'))
      const { toBob } = mountSwitch({ [URL3]: [late.answer], [URL4]: [ok(page([grant(2)], null, BOB))] })
      await toBob()
      await waitFor(() => expect(shownIds()).toEqual([2]))

      late.release()
      await settle()
      expect(screen.queryByText('No such child')).toBeNull()
      expect(shownIds()).toEqual([2])
    })

    it("drops the earlier child's further page when it lands late", async () => {
      const late = gated(ok(page([grant(1)])))
      const { toBob } = mountSwitch({
        [URL3]: [ok(page([grant(9)], 9))],
        [`${URL3}?before=9`]: [late.answer],
        [URL4]: [ok(page([grant(2)], null, BOB))],
      })
      await fireEvent.click(await screen.findByRole('button', { name: 'Load more' }))
      await toBob()
      await waitFor(() => expect(shownIds()).toEqual([2]))

      late.release()
      await settle()
      expect(shownIds()).toEqual([2])
    })

    it("drops the earlier child's further-page error when it lands late", async () => {
      const late = gated(fail(500, 'internal'))
      const { toBob } = mountSwitch({
        [URL3]: [ok(page([grant(9)], 9))],
        [`${URL3}?before=9`]: [late.answer],
        [URL4]: [ok(page([grant(2)], null, BOB))],
      })
      await fireEvent.click(await screen.findByRole('button', { name: 'Load more' }))
      await toBob()
      await waitFor(() => expect(shownIds()).toEqual([2]))

      late.release()
      await settle()
      expect(screen.queryByRole('alert')).toBeNull()
    })

    it("keeps the new child's Load more disabled when the earlier child's page settles", async () => {
      const late = gated(ok(page([grant(1)])))
      const bobMore = gated(ok(page([grant(1)], null, BOB)))
      const { calls, toBob } = mountSwitch({
        [URL3]: [ok(page([grant(9)], 9))],
        [`${URL3}?before=9`]: [late.answer],
        [URL4]: [ok(page([grant(2)], 2, BOB))],
        [`${URL4}?before=2`]: [bobMore.answer],
      })
      await fireEvent.click(await screen.findByRole('button', { name: 'Load more' }))
      await toBob()
      await waitFor(() => expect(shownIds()).toEqual([2]))
      const button = screen.getByRole('button', { name: 'Load more' }) as HTMLButtonElement
      await fireEvent.click(button)
      expect(button.disabled).toBe(true)

      late.release()
      await settle()
      expect(button.disabled).toBe(true)
      await fireEvent.click(button)
      expect(calls.filter((u) => u === `${URL4}?before=2`).length).toBe(1)

      bobMore.release()
      await waitFor(() => expect(shownIds()).toEqual([2, 1]))
    })
  })
})
