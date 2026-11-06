// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { get } from 'svelte/store'
import { afterEach, describe, expect, it, vi } from 'vitest'
import History from './History.svelte'
import {
  historyChild,
  historyFilters,
  navigate,
  route,
  type HistoryFilterState,
  type OverviewGrant,
  type OverviewPage,
} from './api'
import { formatTime } from './history'

type Answer = () => Response | Promise<Response>

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}
const fail = (status: number, code: string) => () => json(status, { error: { code, message: code } })

const CHILDREN = '/api/v1/children'
const SERVICES = '/api/v1/history/services'

const NO_OPTIONS: Record<string, Answer> = {
  [CHILDREN]: () => json(200, { children: [] }),
  [SERVICES]: () => json(200, { services: [] }),
}

/**
 * Routes keyed by URL; each is a queue of answers, consumed in order. The two
 * option reads answer empty unless routed; the calls returned are the list's
 * alone, and fetched() counts the rest.
 */
function mockFetch(routes: Record<string, Answer[]>): string[] {
  const calls: string[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => {
      const option = NO_OPTIONS[url]
      if (!option) calls.push(url)
      const answer = routes[url]?.shift() ?? option
      if (!answer) throw new Error('unexpected fetch ' + url)
      return answer()
    }),
  )
  return calls
}

/** How many times url went out, option reads included. */
function fetched(url: string): number {
  return vi.mocked(fetch).mock.calls.filter(([u]) => u === url).length
}

const URL3 = '/api/v1/history?child=3'

function grant(id: number, over: Partial<OverviewGrant> = {}): OverviewGrant {
  return {
    id,
    child: { id: 3, name: 'Ada' },
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
  grants: OverviewGrant[],
  next_before: number | null = null,
  child: { id: number; name: string } | null = { id: 3, name: 'Ada' },
): OverviewPage {
  return { child, grants, next_before }
}

const URL4 = '/api/v1/history?child=4'
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

const ok = (p: OverviewPage) => () => json(200, p)

/** Lets a released response run to completion (fetch, JSON parse, state writes, render). */
const settle = () => new Promise((r) => setTimeout(r, 30))

function shownIds(): number[] {
  return [...document.querySelectorAll('li[data-grant]')].map((el) => Number((el as HTMLElement).dataset.grant))
}

function grantEl(id: number): HTMLElement {
  const el = document.querySelector(`li[data-grant="${id}"]`)
  if (!el) throw new Error('no grant ' + id)
  return el as HTMLElement
}

const NONE: HistoryFilterState = { service: null, status: null, from: null, to: null, badChild: false }
const BAD_CHILD: HistoryFilterState = { ...NONE, badChild: true }

async function mount(
  child: number | null,
  routes: Record<string, Answer[]>,
  onBack = () => {},
  filters: HistoryFilterState = NONE,
) {
  const calls = mockFetch(routes)
  render(History, { child, filters, onBack })
  return calls
}

let stopFollowing: (() => void)[] = []

/** Point the routing stores at url, as a browser navigation there would. */
function goTo(url: string) {
  history.replaceState(null, '', url)
  window.dispatchEvent(new PopStateEvent('popstate'))
}

/** Mounts at url with the props following the routing stores, as App wires them. */
function mountAt(url: string, routes: Record<string, Answer[]>) {
  const calls = mockFetch(routes)
  goTo(url)
  const props = () => ({ child: get(historyChild), filters: get(historyFilters), onBack: () => {} })
  const view = render(History, props())
  const follow = () => void view.rerender(props())
  stopFollowing = [historyChild.subscribe(follow), historyFilters.subscribe(follow)]
  return calls
}

const select = (label: string) => screen.getByLabelText(label) as HTMLSelectElement
const shown = (el: HTMLSelectElement) => el.selectedOptions[0]?.textContent
const options = (el: HTMLSelectElement) => [...el.options].map((o) => o.textContent)
const bar = () => screen.queryByRole('form', { name: 'Filter history' })

afterEach(() => {
  for (const stop of stopFollowing) stop()
  stopFollowing = []
  goTo('/')
  vi.useRealTimers()
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
      [`${URL3}&before=8`]: [
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
    expect(calls).toEqual([URL3, `${URL3}&before=8`])
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()
  })

  it('keeps shown rows on a paging error and retries with the same cursor', async () => {
    const calls = await mount(3, {
      [URL3]: [ok(page([grant(9)], 9))],
      [`${URL3}&before=9`]: [fail(500, 'internal'), ok(page([grant(4)], null))],
    })
    await fireEvent.click(await screen.findByRole('button', { name: 'Load more' }))

    await screen.findByRole('alert')
    expect(shownIds()).toEqual([9])
    await fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(shownIds()).toEqual([9, 4]))
    expect(calls).toEqual([URL3, `${URL3}&before=9`, `${URL3}&before=9`])
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('shows "No such child" with back, no fetch and no filter bar for a bad child', async () => {
    const onBack = vi.fn()
    await mount(null, {}, onBack, BAD_CHILD)
    expect(await screen.findByText('No such child')).toBeTruthy()
    expect(document.querySelector('ul.list')).toBeNull()
    expect(bar()).toBeNull()
    await fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    expect(onBack).toHaveBeenCalledOnce()
    await settle()
    expect(fetch).not.toHaveBeenCalled()
  })

  it('shows "No such child" with back, no list and no filter bar for a 404', async () => {
    const url = '/api/v1/history?child=99'
    await mount(99, { [url]: [fail(404, 'not_found')] })
    expect(await screen.findByText('No such child')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Back' })).toBeTruthy()
    expect(document.querySelector('ul.list')).toBeNull()
    expect(screen.queryByText('No grants yet')).toBeNull()
    expect(bar()).toBeNull()
    expect(screen.getByRole('heading').textContent).toBe('History')
  })

  it('reaches the overview from a bad child, reading the options then', async () => {
    const calls = mountAt('/history?child=abc', { '/api/v1/history': [ok(page([grant(1)], null, null))] })
    await screen.findByText('No such child')
    expect(fetch).not.toHaveBeenCalled()

    goTo('/history')
    await waitFor(() => expect(shownIds()).toEqual([1]))
    expect(screen.queryByText('No such child')).toBeNull()
    expect(bar()).not.toBeNull()
    expect(calls).toEqual(['/api/v1/history'])
    expect(fetched(CHILDREN)).toBe(1)
    expect(fetched(SERVICES)).toBe(1)
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
    render(History, { child: 3, filters: NONE, onBack: () => {} })

    // What was on screen when the fetch went out: the bar and the loading line.
    await waitFor(() => expect(atFetch).not.toBe(''))
    expect(atFetch).toContain('Loading…')
    expect(atFetch).not.toContain('No grants yet')
    expect(atFetch).not.toContain('No such child')
    expect(screen.getByRole('heading').textContent).toBe('History')
    expect(screen.getByRole('status').textContent).toBe('Loading…')

    first.release()
    await screen.findByRole('heading', { name: 'Ada — history' })
  })

  it('titles a bad child plainly "History"', async () => {
    await mount(null, {}, () => {}, BAD_CHILD)
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
    render(History, { child: 3, filters: NONE, onBack: () => {} })
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
    const calls = await mount(3, { [URL3]: [ok(page([grant(9)], 9))], [`${URL3}&before=9`]: [more.answer] })
    const button = await screen.findByRole('button', { name: 'Load more' })

    // Both clicks land before Svelte flushes disabled, so only the loadingMore guard stops the second.
    button.click()
    button.click()
    await waitFor(() => expect((button as HTMLButtonElement).disabled).toBe(true))
    expect(calls).toEqual([URL3, `${URL3}&before=9`])

    more.release()
    await waitFor(() => expect(shownIds()).toEqual([9, 7]))
  })

  describe('after switching child', () => {
    function mountSwitch(routes: Record<string, Answer[]>) {
      const calls = mockFetch(routes)
      const view = render(History, { child: 3, filters: NONE, onBack: () => {} })
      return { calls, toBob: () => view.rerender({ child: 4, filters: NONE, onBack: () => {} }) }
    }

    it("takes the earlier child's name off the heading while the new child loads", async () => {
      const held = gated(ok(page([grant(2)], null, BOB)))
      const { toBob } = mountSwitch({ [URL3]: [ok(page([grant(1)]))], [URL4]: [held.answer] })
      await screen.findByRole('heading', { name: 'Ada — history' })

      await toBob()

      await screen.findByText('Loading…')
      expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('History')
      held.release()
      await screen.findByRole('heading', { name: 'Bob — history' })
    })

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
        [`${URL3}&before=9`]: [late.answer],
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
        [`${URL3}&before=9`]: [late.answer],
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
        [`${URL3}&before=9`]: [late.answer],
        [URL4]: [ok(page([grant(2)], 2, BOB))],
        [`${URL4}&before=2`]: [bobMore.answer],
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
      expect(calls.filter((u) => u === `${URL4}&before=2`).length).toBe(1)

      bobMore.release()
      await waitFor(() => expect(shownIds()).toEqual([2, 1]))
    })
  })

  describe('as the overview', () => {
    const ALL = '/api/v1/history'

    it('needs no child: every row names its child beside its existing detail', async () => {
      const at = '2026-10-01T10:30:00Z'
      const x = { at, old_ends_at: '2026-10-01T11:00:00Z', new_ends_at: '2026-10-01T11:30:00Z' }
      const trimmed = { id: 'tiktok', name: 'TikTok', removed_at: at, removed_reason: 'permanent_block' as const }
      const ada = grant(9, { child: { id: 3, name: 'Ada' }, extensions: [x] })
      const bob = grant(8, { child: BOB, status: 'ended_early', services: [trimmed] })
      const calls = await mount(null, { [ALL]: [ok(page([ada, bob], null, null))] })

      await waitFor(() => expect(shownIds()).toEqual([9, 8]))
      expect(calls).toEqual([ALL])
      expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('History')
      expect(grantEl(9).querySelector('[data-child]')?.textContent).toBe('Ada')
      expect(grantEl(8).querySelector('[data-child]')?.textContent).toBe('Bob')
      expect(grantEl(9).querySelector('[data-status]')?.textContent).toBe('Expired')
      expect(grantEl(8).querySelector('[data-status]')?.textContent).toBe('Ended early')
      expect(grantEl(9).querySelector('[data-started]')?.textContent).toBe(formatTime(ada.started_at))
      expect(grantEl(9).querySelector('[data-ended]')?.textContent).toBe(formatTime('2026-10-01T11:00:05Z'))
      expect(grantEl(9).querySelector('li[data-service="youtube"]')?.textContent).toContain('YouTube')
      expect(grantEl(8).querySelector('[data-removed]')?.textContent).toContain('permanent block')
      expect(grantEl(9).querySelectorAll('[data-extension]').length).toBe(1)
    })

    it('selects the child a link names, under its heading', async () => {
      const cleo = { id: 3, name: 'Cleo' }
      const calls = await mount(3, {
        [URL3]: [ok(page([grant(1, { child: cleo })], null, cleo))],
        [CHILDREN]: [() => json(200, { children: [{ ...cleo, clients: [] }] })],
      })

      await screen.findByRole('heading', { name: 'Cleo — history' })
      await waitFor(() => expect(shown(select('Child'))).toBe('Cleo'))
      expect(calls).toEqual([URL3])
    })

    it('lists the served service options, even an id no live catalogue has', async () => {
      await mount(null, {
        [ALL]: [ok(page([grant(1)], null, null))],
        [SERVICES]: [
          () =>
            json(200, {
              services: [
                { id: 'custom:9', name: 'Roblox' },
                { id: 'youtube', name: 'YouTube' },
              ],
            }),
        ],
      })

      await waitFor(() => expect(options(select('Service'))).toEqual(['All', 'Roblox', 'YouTube']))
      expect((select('Service').options[1] as HTMLOptionElement).value).toBe('custom:9')
      expect(fetched('/api/v1/services')).toBe(0)
    })

    it.each([
      { label: 'Child', value: '3', url: '/history?child=3', api: URL3 },
      { label: 'Service', value: 'custom:9', url: '/history?service=custom%3A9', api: `${ALL}?service=custom%3A9` },
      { label: 'How it ended', value: 'expired', url: '/history?status=expired', api: `${ALL}?status=expired` },
      {
        label: 'From',
        value: '2026-03-08',
        url: '/history?from=2026-03-08',
        api: `${ALL}?from=2026-03-08T05%3A00%3A00.000Z`,
      },
      {
        label: 'To',
        value: '2026-03-08',
        url: '/history?to=2026-03-08',
        api: `${ALL}?until=2026-03-09T04%3A00%3A00.000Z`,
      },
    ])('$label pushes $url and fetches once', async ({ label, value, url, api }) => {
      const calls = mountAt('/history', {
        [ALL]: [ok(page([grant(9)], null, null))],
        [api]: [ok(page([grant(1)], null, null))],
        [CHILDREN]: [() => json(200, { children: [{ id: 3, name: 'Cleo', clients: [] }] })],
        [SERVICES]: [() => json(200, { services: [{ id: 'custom:9', name: 'Roblox' }] })],
      })
      await screen.findByRole('option', { name: 'Cleo' })
      await screen.findByRole('option', { name: 'Roblox' })
      await waitFor(() => expect(shownIds()).toEqual([9]))
      const push = vi.spyOn(history, 'pushState')
      const control = screen.getByLabelText(label) as HTMLInputElement | HTMLSelectElement

      // Leaving the control sends a date on without waiting out its settle delay.
      await fireEvent.change(control, { target: { value } })
      await fireEvent.blur(control)

      await waitFor(() => expect(shownIds()).toEqual([1]))
      await settle()
      expect(push).toHaveBeenCalledExactlyOnceWith(null, '', url)
      expect(location.pathname + location.search).toBe(url)
      expect(calls).toEqual([ALL, api])
      expect(control.value).toBe(value)
    })

    it('pushes once and fetches once for a day typed a segment at a time', async () => {
      // Recorded from Chromium 156: retyping the 15th as 25 fires change for the 2nd, then the 25th.
      const first = `${ALL}?from=2026-03-15T04%3A00%3A00.000Z`
      const typed = `${ALL}?from=2026-03-25T04%3A00%3A00.000Z`
      const calls = mountAt('/history?from=2026-03-15', {
        [first]: [ok(page([grant(9)], null, null))],
        [typed]: [ok(page([grant(1)], null, null))],
      })
      await waitFor(() => expect(shownIds()).toEqual([9]))
      const push = vi.spyOn(history, 'pushState')
      const from = screen.getByLabelText('From') as HTMLInputElement
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })

      await fireEvent.change(from, { target: { value: '2026-03-02' } })
      await fireEvent.change(from, { target: { value: '2026-03-25' } })
      expect(push).not.toHaveBeenCalled()
      await vi.advanceTimersByTimeAsync(700)
      vi.useRealTimers()

      await waitFor(() => expect(shownIds()).toEqual([1]))
      expect(push).toHaveBeenCalledExactlyOnceWith(null, '', '/history?from=2026-03-25')
      expect(calls).toEqual([first, typed])
      expect(from.value).toBe('2026-03-25')
    })

    it('pushes a pending date and a status together, and nothing when the delay ends', async () => {
      const both = `${ALL}?status=expired&from=2026-03-25T04%3A00%3A00.000Z`
      const calls = mountAt('/history', {
        [ALL]: [ok(page([grant(9)], null, null))],
        [both]: [ok(page([grant(1)], null, null))],
      })
      await waitFor(() => expect(shownIds()).toEqual([9]))
      const push = vi.spyOn(history, 'pushState')
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })

      await fireEvent.change(screen.getByLabelText('From'), { target: { value: '2026-03-25' } })
      await fireEvent.change(select('How it ended'), { target: { value: 'expired' } })
      await vi.advanceTimersByTimeAsync(700)
      vi.useRealTimers()

      await waitFor(() => expect(shownIds()).toEqual([1]))
      expect(push).toHaveBeenCalledExactlyOnceWith(null, '', '/history?status=expired&from=2026-03-25')
      expect(calls).toEqual([ALL, both])
    })

    describe('with a date pending when the browser goes back', () => {
      /**
       * jsdom runs a window's capture listeners first, but in Chromium 156 the
       * blur reached a pending date before the bar's popstate listener cleared
       * it. Dropping capture keeps window listeners in the order they were added:
       * api.ts's route listener, added at import, ahead of the bar's.
       */
      function listenInOrderAdded() {
        const add = window.addEventListener.bind(window)
        vi.spyOn(window, 'addEventListener').mockImplementation(((type: string, listener: EventListener) =>
          add(type, listener)) as typeof window.addEventListener)
      }

      /** The pending date, typed on a loaded screen with the delay's clock in the test's hands. */
      async function typeDate(input: HTMLElement) {
        vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
        await fireEvent.change(input, { target: { value: '2026-04-02' } })
      }

      it('ends on Home with no push when the route change removes the bar', async () => {
        listenInOrderAdded()
        mockFetch({ [ALL]: [ok(page([grant(9)], null, null))] })
        goTo('/history')
        const view = render(History, { child: null, filters: NONE, onBack: () => {} })
        await waitFor(() => expect(shownIds()).toEqual([9]))
        const from = screen.getByLabelText('From')
        // Recorded from Chromium 156: removing the focused input fires blur while
        // popstate is handled, and Svelte removes the DOM before it runs teardown.
        stopFollowing = [
          route.subscribe((r) => {
            if (r === 'history') return
            from.dispatchEvent(new FocusEvent('blur'))
            view.unmount()
          }),
        ]
        const push = vi.spyOn(history, 'pushState')
        await typeDate(from)

        goTo('/')
        await vi.advanceTimersByTimeAsync(700)

        expect(push).not.toHaveBeenCalled()
        expect(location.pathname + location.search).toBe('/')
      })

      it('pushes nothing when the bar stays, on a Back inside History', async () => {
        listenInOrderAdded()
        const expired = `${ALL}?status=expired`
        const calls = mountAt('/history?status=expired', {
          [expired]: [ok(page([grant(9)], null, null))],
          [ALL]: [ok(page([grant(1)], null, null))],
        })
        await waitFor(() => expect(shownIds()).toEqual([9]))
        const push = vi.spyOn(history, 'pushState')
        await typeDate(screen.getByLabelText('From'))

        goTo('/history')
        await vi.advanceTimersByTimeAsync(700)
        vi.useRealTimers()

        await waitFor(() => expect(shownIds()).toEqual([1]))
        expect(push).not.toHaveBeenCalled()
        expect(location.pathname + location.search).toBe('/history')
        expect(calls).toEqual([expired, ALL])
      })

      it('shows an empty From with no blur when a Back inside History keeps it unset', async () => {
        listenInOrderAdded()
        const expired = `${ALL}?status=expired`
        mountAt('/history?status=expired', {
          [expired]: [ok(page([grant(9)], null, null))],
          [ALL]: [ok(page([grant(1)], null, null))],
        })
        await waitFor(() => expect(shownIds()).toEqual([9]))
        const from = screen.getByLabelText('From') as HTMLInputElement
        from.focus()
        await typeDate(from)
        expect(from.value).toBe('2026-04-02')

        goTo('/history')
        vi.useRealTimers()

        await waitFor(() => expect(shownIds()).toEqual([1]))
        expect(document.activeElement).toBe(from)
        expect(from.value).toBe('')
      })

      it('shows the From the URL restored when a Back inside History changes it', async () => {
        listenInOrderAdded()
        const march = `${ALL}?from=2026-03-01T05%3A00%3A00.000Z`
        const february = `${ALL}?from=2026-02-01T05%3A00%3A00.000Z`
        mountAt('/history?from=2026-03-01', {
          [march]: [ok(page([grant(9)], null, null))],
          [february]: [ok(page([grant(1)], null, null))],
        })
        await waitFor(() => expect(shownIds()).toEqual([9]))
        const from = screen.getByLabelText('From') as HTMLInputElement
        from.focus()
        await typeDate(from)

        goTo('/history?from=2026-02-01')
        vi.useRealTimers()

        await waitFor(() => expect(shownIds()).toEqual([1]))
        expect(document.activeElement).toBe(from)
        expect(from.value).toBe('2026-02-01')
      })

      it('still flushes for the in-app Back button, which ends on Home', async () => {
        mockFetch({ [ALL]: [ok(page([grant(9)], null, null))] })
        goTo('/history')
        render(History, { child: null, filters: NONE, onBack: () => navigate('home') })
        await waitFor(() => expect(shownIds()).toEqual([9]))
        const push = vi.spyOn(history, 'pushState')
        const from = screen.getByLabelText('From')
        await typeDate(from)

        // Pressing the button takes focus before its click lands.
        await fireEvent.blur(from)
        await fireEvent.click(screen.getByRole('button', { name: 'Back' }))

        expect(push.mock.calls.map(([, , url]) => url)).toEqual(['/history?from=2026-04-02', '/'])
        expect(location.pathname + location.search).toBe('/')
      })
    })

    it('keeps Load more inside the filter and appends deduped', async () => {
      const first = `${ALL}?status=expired`
      const calls = mountAt('/history?status=expired', {
        [first]: [ok(page([grant(41), grant(40)], 40, null))],
        [`${first}&before=40`]: [ok(page([grant(40), grant(39)], null, null))],
      })

      await fireEvent.click(await screen.findByRole('button', { name: 'Load more' }))

      await waitFor(() => expect(shownIds()).toEqual([41, 40, 39]))
      expect(calls).toEqual([first, '/api/v1/history?status=expired&before=40'])
    })

    it('drops the cursor, the rows and Load more when a filter changes', async () => {
      const next = gated(ok(page([grant(1)], null, null)))
      const calls = mountAt('/history', {
        [ALL]: [ok(page([grant(41), grant(40)], 40, null))],
        [`${ALL}?status=expired`]: [next.answer],
      })
      await screen.findByRole('button', { name: 'Load more' })

      await fireEvent.change(select('How it ended'), { target: { value: 'expired' } })

      await screen.findByText('Loading…')
      expect(shownIds()).toEqual([])
      expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()
      expect(calls).toEqual([ALL, `${ALL}?status=expired`])

      next.release()
      await waitFor(() => expect(shownIds()).toEqual([1]))
      expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()
    })

    it('keeps the bar and the child heading through a filter change', async () => {
      const next = gated(ok(page([grant(1)])))
      mountAt('/history?child=3', {
        [URL3]: [ok(page([grant(9)]))],
        [`${URL3}&status=expired`]: [next.answer],
      })
      await screen.findByRole('heading', { name: 'Ada — history' })
      const control = select('How it ended')

      await fireEvent.change(control, { target: { value: 'expired' } })

      await screen.findByText('Loading…')
      expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('Ada — history')
      expect(select('How it ended')).toBe(control)
      next.release()
      await waitFor(() => expect(shownIds()).toEqual([1]))
    })

    it('makes no fetch and keeps loaded pages when identical filters are emitted again', async () => {
      const first = `${ALL}?status=expired`
      const calls = mountAt('/history?status=expired', {
        [first]: [ok(page([grant(41)], 41, null))],
        [`${first}&before=41`]: [ok(page([grant(40)], 40, null))],
      })
      await fireEvent.click(await screen.findByRole('button', { name: 'Load more' }))
      await waitFor(() => expect(shownIds()).toEqual([41, 40]))

      historyFilters.set({ ...get(historyFilters) })
      await settle()

      expect(calls).toEqual([first, `${first}&before=41`])
      expect(shownIds()).toEqual([41, 40])
      expect(screen.getByRole('button', { name: 'Load more' })).toBeTruthy()
    })

    it('tells a filtered empty page from an unfiltered one, keeping the bar', async () => {
      mountAt('/history', {
        [ALL]: [ok(page([], null, null))],
        [`${ALL}?status=expired`]: [ok(page([], null, null))],
      })
      expect(await screen.findByText('No grants yet')).toBeTruthy()
      expect(screen.queryByText('No grants match these filters')).toBeNull()

      await fireEvent.change(select('How it ended'), { target: { value: 'expired' } })

      expect(await screen.findByText('No grants match these filters')).toBeTruthy()
      expect(screen.queryByText('No grants yet')).toBeNull()
      expect(bar()).not.toBeNull()
    })

    it('shows retry, not an empty state, when a filtered page fails', async () => {
      await mount(null, { [`${ALL}?status=expired`]: [fail(500, 'internal')] }, () => {}, {
        ...NONE,
        status: 'expired',
      })
      await screen.findByRole('alert')
      expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy()
      expect(screen.queryByText('No grants match these filters')).toBeNull()
      expect(screen.queryByText('No grants yet')).toBeNull()
      expect(screen.queryByText('No such child')).toBeNull()
    })

    it.each([
      { name: 'children', url: CHILDREN },
      { name: 'services', url: SERVICES },
    ])('renders grants and a usable bar when the $name options fail', async ({ url }) => {
      const calls = mountAt('/history', {
        [ALL]: [ok(page([grant(9)], null, null))],
        [`${ALL}?status=expired`]: [ok(page([grant(1)], null, null))],
        [url]: [fail(500, 'internal')],
      })
      await waitFor(() => expect(shownIds()).toEqual([9]))
      await settle()
      expect(screen.queryByRole('alert')).toBeNull()
      expect(options(select('Child'))).toEqual(['All'])
      expect(options(select('Service'))).toEqual(['All'])

      await fireEvent.change(select('How it ended'), { target: { value: 'expired' } })

      await waitFor(() => expect(shownIds()).toEqual([1]))
      expect(calls).toEqual([ALL, `${ALL}?status=expired`])
    })

    it('renders grants while the options are still on their way', async () => {
      const held = gated(() => json(200, { children: [{ id: 3, name: 'Cleo', clients: [] }] }))
      const heldServices = gated(() => json(200, { services: [{ id: 'custom:9', name: 'Roblox' }] }))
      await mount(null, {
        [ALL]: [ok(page([grant(9)], null, null))],
        [CHILDREN]: [held.answer],
        [SERVICES]: [heldServices.answer],
      })

      await waitFor(() => expect(shownIds()).toEqual([9]))
      expect(options(select('Child'))).toEqual(['All'])

      held.release()
      heldServices.release()
      await waitFor(() => expect(options(select('Child'))).toEqual(['All', 'Cleo']))
      await waitFor(() => expect(options(select('Service'))).toEqual(['All', 'Roblox']))
      expect(shownIds()).toEqual([9])
    })

    it('leaves the bar usable through a failed list and its retry', async () => {
      const calls = mountAt('/history', {
        [ALL]: [fail(500, 'internal'), ok(page([grant(9)], null, null))],
        [`${ALL}?status=expired`]: [ok(page([grant(1)], null, null))],
        [CHILDREN]: [() => json(200, { children: [{ id: 3, name: 'Cleo', clients: [] }] })],
      })
      await screen.findByRole('alert')
      await screen.findByRole('option', { name: 'Cleo' })

      await fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
      await waitFor(() => expect(shownIds()).toEqual([9]))
      expect(options(select('Child'))).toEqual(['All', 'Cleo'])

      await fireEvent.change(select('How it ended'), { target: { value: 'expired' } })
      await waitFor(() => expect(shownIds()).toEqual([1]))
      expect(calls).toEqual([ALL, ALL, `${ALL}?status=expired`])
    })

    it('reads the options once per mount, not per filter change or retry', async () => {
      mountAt('/history', {
        [ALL]: [fail(500, 'internal'), ok(page([grant(9)], null, null))],
        [`${ALL}?status=expired`]: [ok(page([grant(1)], null, null))],
      })
      await fireEvent.click(await screen.findByRole('button', { name: 'Retry' }))
      await waitFor(() => expect(shownIds()).toEqual([9]))
      await fireEvent.change(select('How it ended'), { target: { value: 'expired' } })
      await waitFor(() => expect(shownIds()).toEqual([1]))

      expect(fetched(CHILDREN)).toBe(1)
      expect(fetched(SERVICES)).toBe(1)
    })
  })

  describe('after changing a filter', () => {
    const EXPIRED = '/api/v1/history?status=expired'
    const ONGOING = '/api/v1/history?status=ongoing'
    const toOngoing = () => fireEvent.change(select('How it ended'), { target: { value: 'ongoing' } })
    const mountExpired = (routes: Record<string, Answer[]>) => mountAt('/history?status=expired', routes)

    it("drops the earlier filter's first page when it lands late", async () => {
      const late = gated(ok(page([grant(1)], 1, null)))
      mountExpired({ [EXPIRED]: [late.answer], [ONGOING]: [ok(page([grant(2)], null, null))] })
      await toOngoing()
      await waitFor(() => expect(shownIds()).toEqual([2]))

      late.release()
      await settle()
      expect(shownIds()).toEqual([2])
      expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()
    })

    it.each([
      { status: 404, code: 'not_found' },
      { status: 500, code: 'internal' },
    ])("drops the earlier filter's first-page $status when it lands late", async ({ status, code }) => {
      const late = gated(fail(status, code))
      mountExpired({ [EXPIRED]: [late.answer], [ONGOING]: [ok(page([grant(2)], null, null))] })
      await toOngoing()
      await waitFor(() => expect(shownIds()).toEqual([2]))

      late.release()
      await settle()
      expect(screen.queryByText('No such child')).toBeNull()
      expect(screen.queryByRole('alert')).toBeNull()
      expect(shownIds()).toEqual([2])
    })

    it("drops the earlier filter's further page when it lands late", async () => {
      const late = gated(ok(page([grant(1)], null, null)))
      mountExpired({
        [EXPIRED]: [ok(page([grant(9)], 9, null))],
        [`${EXPIRED}&before=9`]: [late.answer],
        [ONGOING]: [ok(page([grant(2)], null, null))],
      })
      await fireEvent.click(await screen.findByRole('button', { name: 'Load more' }))
      await toOngoing()
      await waitFor(() => expect(shownIds()).toEqual([2]))

      late.release()
      await settle()
      expect(shownIds()).toEqual([2])
    })

    it("drops the earlier filter's further-page error when it lands late", async () => {
      const late = gated(fail(500, 'internal'))
      mountExpired({
        [EXPIRED]: [ok(page([grant(9)], 9, null))],
        [`${EXPIRED}&before=9`]: [late.answer],
        [ONGOING]: [ok(page([grant(2)], null, null))],
      })
      await fireEvent.click(await screen.findByRole('button', { name: 'Load more' }))
      await toOngoing()
      await waitFor(() => expect(shownIds()).toEqual([2]))

      late.release()
      await settle()
      expect(screen.queryByRole('alert')).toBeNull()
      expect(shownIds()).toEqual([2])
    })

    it('leaves Load more usable for the new filter, whatever the earlier page does', async () => {
      const late = gated(ok(page([grant(1)], null, null)))
      const calls = mountExpired({
        [EXPIRED]: [ok(page([grant(9)], 9, null))],
        [`${EXPIRED}&before=9`]: [late.answer],
        [ONGOING]: [ok(page([grant(2)], 2, null))],
        [`${ONGOING}&before=2`]: [ok(page([grant(1)], null, null))],
      })
      await fireEvent.click(await screen.findByRole('button', { name: 'Load more' }))
      await toOngoing()
      const button = (await screen.findByRole('button', { name: 'Load more' })) as HTMLButtonElement
      expect(button.disabled).toBe(false)

      late.release()
      await settle()
      expect(button.disabled).toBe(false)
      await fireEvent.click(button)

      await waitFor(() => expect(shownIds()).toEqual([2, 1]))
      expect(calls).toEqual([EXPIRED, `${EXPIRED}&before=9`, ONGOING, `${ONGOING}&before=2`])
    })

    it("keeps the new filter's Load more disabled when the earlier page settles", async () => {
      const late = gated(ok(page([grant(1)], null, null)))
      const more = gated(ok(page([grant(1)], null, null)))
      const calls = mountExpired({
        [EXPIRED]: [ok(page([grant(9)], 9, null))],
        [`${EXPIRED}&before=9`]: [late.answer],
        [ONGOING]: [ok(page([grant(2)], 2, null))],
        [`${ONGOING}&before=2`]: [more.answer],
      })
      await fireEvent.click(await screen.findByRole('button', { name: 'Load more' }))
      await toOngoing()
      const button = (await screen.findByRole('button', { name: 'Load more' })) as HTMLButtonElement
      await fireEvent.click(button)
      expect(button.disabled).toBe(true)

      late.release()
      await settle()
      expect(button.disabled).toBe(true)
      await fireEvent.click(button)
      expect(calls.filter((u) => u === `${ONGOING}&before=2`).length).toBe(1)

      more.release()
      await waitFor(() => expect(shownIds()).toEqual([2, 1]))
    })
  })
})
