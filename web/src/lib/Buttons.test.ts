// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Buttons from './Buttons.svelte'
import type { Button, Child, Service } from './api'

type Call = { method: string; url: string; body: string | null }
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
      calls.push({ method, url, body: typeof init?.body === 'string' ? init.body : null })
      const answer = routes[`${method} ${url}`] ?? routes[url]
      if (!answer) throw new Error('unexpected fetch ' + method + ' ' + url)
      return answer()
    }),
  )
  return calls
}

const ada: Child = { id: 1, name: 'Ada', clients: ['Kid phone'] }
const ben: Child = { id: 2, name: 'Ben', clients: ['Kid tablet'] }
const youtube: Service = { id: 'youtube', name: 'YouTube', icon: 'PHN2Zy8+' }
const tiktok: Service = { id: 'tiktok', name: 'TikTok', icon: 'PHN2Zy8+' }
const roblox: Service = { id: 'roblox', name: 'Roblox', icon: 'PHN2Zy8+' }

const b1: Button = { id: 3, label: 'YouTube 90m', child_id: 1, services: ['youtube'], duration: 5400 }
const b2: Button = { id: 4, label: 'Roblox 1h', child_id: 2, services: ['roblox'], duration: 3600 }
const b3: Button = { id: 5, label: 'TikTok 30m', child_id: 1, services: ['tiktok'], duration: 1800 }

type Stored = { label: string; child_id: number; services: string[]; duration: number }

/** A mutable server: PUT replaces the list and hands back fresh ids; `state.servicesDown` flips the catalogue mid-test. */
function server(buttons: Button[], opts: { services?: Service[] | 'down'; put?: Answer; children?: Child[] } = {}) {
  const state = { buttons, nextId: 100, servicesDown: opts.services === 'down' }
  const routes: Routes = {
    '/api/v1/buttons': () => json(200, { buttons: state.buttons }),
    '/api/v1/children': () => json(200, { children: opts.children ?? [ada, ben] }),
    '/api/v1/services': () =>
      state.servicesDown
        ? json(502, { error: 'adguard_unavailable', message: 'down' })
        : json(200, { services: opts.services === 'down' ? [] : (opts.services ?? [youtube, tiktok, roblox]) }),
    'PUT /api/v1/buttons':
      opts.put ??
      (() => {
        const last = calls[calls.length - 1]
        const body = JSON.parse(last.body ?? '{}') as { buttons: Stored[] }
        state.buttons = body.buttons.map((b) => ({ ...b, id: state.nextId++ }))
        return json(200, { buttons: state.buttons })
      }),
  }
  const calls = mockFetch(routes)
  return { state, calls }
}

const puts = (calls: Call[]) => calls.filter((c) => c.method === 'PUT').map((c) => JSON.parse(c.body ?? '{}') as { buttons: Stored[] })
const getsOf = (calls: Call[], url: string) => calls.filter((c) => c.method === 'GET' && c.url === url).length

function rows(): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>('li[data-button]')]
}

function rowButton(id: number, name: string): HTMLButtonElement {
  const li = document.querySelector(`li[data-button="${id}"]`)
  if (!li) throw new Error(`no row ${id}`)
  for (const b of li.querySelectorAll('button')) if (b.textContent?.trim() === name) return b
  throw new Error(`no ${name} on row ${id}`)
}

const saveButton = () => screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement
const labelInput = () => screen.getByLabelText('Label') as HTMLInputElement
const minutesInput = () => screen.getByLabelText('Minutes') as HTMLInputElement
const childSelect = () => screen.getByLabelText('Child') as HTMLSelectElement
const formHeading = () => document.querySelector('form h2')?.textContent
const grantForm = () => document.querySelector('fieldset.grant-form') as HTMLFieldSetElement
const formValue = () => JSON.parse(grantForm().dataset.value ?? '{}') as { child_id: number | null; services: string[]; minutes: number }

function checkbox(label: string): HTMLInputElement {
  for (const l of document.querySelectorAll('fieldset.grant-form label')) {
    if (l.textContent?.replace(/\s+/g, ' ').trim().startsWith(label)) {
      const cb = l.querySelector('input[type="checkbox"]')
      if (cb) return cb as HTMLInputElement
    }
  }
  throw new Error(`no checkbox labelled ${label}`)
}

async function mount(buttons: Button[], opts: Parameters<typeof server>[1] = {}) {
  const s = server(buttons, opts)
  render(Buttons, { onBack: vi.fn() })
  await waitFor(() => expect(getsOf(s.calls, '/api/v1/services')).toBe(1))
  if (buttons.length > 0) await waitFor(() => expect(rows().length).toBe(buttons.length))
  return s
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('Buttons', () => {
  it('add appends and PUTs everything', async () => {
    const s = await mount([b1])
    expect(rows()[0].textContent).toContain('YouTube 90m')
    expect(rows()[0].textContent).toContain('Ada')
    expect(rows()[0].textContent).toContain('1 h 30 min')
    expect(screen.queryByText(/No buttons yet/)).toBeNull()
    // The form starts as a blank "new" form.
    expect(formHeading()).toBe('New button')
    expect(labelInput().value).toBe('')
    expect(formValue()).toEqual({ child_id: null, services: [], minutes: 60 })

    // Surrounding whitespace is trimmed off the stored label.
    await fireEvent.input(labelInput(), { target: { value: '  TikTok 30m  ' } })
    await fireEvent.change(childSelect(), { target: { value: '1' } })
    await fireEvent.click(checkbox('TikTok'))
    await fireEvent.input(minutesInput(), { target: { value: '30' } })
    await fireEvent.click(saveButton())

    await waitFor(() => expect(rows().length).toBe(2))
    const sent = puts(s.calls)
    expect(sent).toHaveLength(1)
    expect(sent[0]).toEqual({
      buttons: [
        { label: 'YouTube 90m', child_id: 1, services: ['youtube'], duration: 5400 },
        { label: 'TikTok 30m', child_id: 1, services: ['tiktok'], duration: 1800 },
      ],
    })
    expect(s.calls.find((c) => c.method === 'PUT')?.body).not.toContain('"id"')
    expect(rows()[1].dataset.button).toBe('101')
    // A saved form is fully reset: label, child, services and minutes.
    await waitFor(() => expect(labelInput().value).toBe(''))
    expect(formValue()).toEqual({ child_id: null, services: [], minutes: 60 })
    expect(checkbox('TikTok').checked).toBe(false)
    expect(document.querySelector('[data-missing]')).toBeNull()
  })

  it('rows name the child and every service', async () => {
    const two: Button = { id: 6, label: 'Both', child_id: 2, services: ['youtube', 'tiktok'], duration: 3600 }
    const orphan: Button = { id: 7, label: 'Orphan', child_id: 9, services: ['gone'], duration: 600 }
    await mount([b1, b2, two, orphan])
    const text = (id: number) => document.querySelector(`li[data-button="${id}"] .meta`)?.textContent ?? ''
    expect(text(3)).toBe('Ada · YouTube · 1 h 30 min')
    expect(text(4)).toBe('Ben · Roblox · 1 h')
    expect(text(6)).toBe('Ben · YouTube, TikTok · 1 h')
    // Unknown ids are shown as-is rather than dropped or crashed on.
    expect(text(7)).toBe('child 9 · gone · 10 min')
  })

  it('empty state, then the list', async () => {
    const s = await mount([])
    expect(screen.getByText(/No buttons yet/)).toBeTruthy()
    expect(rows().length).toBe(0)

    await fireEvent.input(labelInput(), { target: { value: 'First' } })
    await fireEvent.change(childSelect(), { target: { value: '1' } })
    await fireEvent.click(checkbox('YouTube'))
    await fireEvent.click(saveButton())
    await waitFor(() => expect(rows().length).toBe(1))
    expect(puts(s.calls)).toHaveLength(1)
    expect(screen.queryByText(/No buttons yet/)).toBeNull()
  })

  it('edit keeps position and converts units', async () => {
    const s = await mount([b1, b2, b3])
    await fireEvent.click(rowButton(4, 'Edit'))
    expect(formHeading()).toBe('Edit button')
    expect(labelInput().value).toBe('Roblox 1h')
    expect(minutesInput().value).toBe('60')
    expect(childSelect().value).toBe('2')
    expect(checkbox('Roblox').checked).toBe(true)
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeTruthy()

    // Cancel is back to a blank "new" form.
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(formHeading()).toBe('New button')
    expect(labelInput().value).toBe('')
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull()

    await fireEvent.click(rowButton(3, 'Edit'))
    expect(minutesInput().value).toBe('90')

    await fireEvent.click(rowButton(4, 'Edit'))
    await fireEvent.input(labelInput(), { target: { value: 'Renamed' } })
    await fireEvent.click(checkbox('TikTok'))
    await fireEvent.input(minutesInput(), { target: { value: '45' } })
    await fireEvent.click(saveButton())

    await waitFor(() => expect(puts(s.calls)).toHaveLength(1))
    const sent = puts(s.calls)[0].buttons
    expect(sent.map((b) => b.label)).toEqual(['YouTube 90m', 'Renamed', 'TikTok 30m'])
    expect(sent[1]).toEqual({ label: 'Renamed', child_id: 2, services: ['roblox', 'tiktok'], duration: 2700 })
    // Grouped by child, Ben's renamed button renders after both of Ada's.
    await waitFor(() => expect(rows()[2].textContent).toContain('Renamed'))
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull())
    expect(labelInput().value).toBe('')
  })

  describe('delete', () => {
    const confirmOf = (id: number) => document.querySelector(`li[data-button="${id}"] .confirm`)
    const confirming = () => [...document.querySelectorAll('li[data-button] .confirm')].map((c) => c.closest('li')?.getAttribute('data-button'))

    it('delete asks first', async () => {
      const dialog = vi.spyOn(window, 'confirm')
      const s = await mount([b1, b2])
      await fireEvent.click(rowButton(3, 'Delete'))
      expect(puts(s.calls)).toHaveLength(0)
      expect(confirmOf(3)?.textContent).toContain('Delete YouTube 90m?')
      expect(rowButton(3, 'Confirm')).toBeTruthy()
      expect(rowButton(3, 'Cancel')).toBeTruthy()
      // The row's normal actions give way to the question.
      expect(() => rowButton(3, 'Delete')).toThrow()
      expect(confirmOf(4)).toBeNull()

      await fireEvent.click(rowButton(3, 'Cancel'))
      await fireEvent.click(rowButton(3, 'Delete'))
      await fireEvent.click(rowButton(3, 'Confirm'))
      await waitFor(() => expect(puts(s.calls)).toHaveLength(1))
      expect(dialog).not.toHaveBeenCalled()
      dialog.mockRestore()
    })

    it('cancel delete', async () => {
      const s = await mount([b1, b2])
      await fireEvent.click(rowButton(3, 'Delete'))
      await fireEvent.click(rowButton(3, 'Cancel'))
      expect(puts(s.calls)).toHaveLength(0)
      expect(rows().map((r) => r.dataset.button)).toEqual(['3', '4'])
      expect(confirmOf(3)).toBeNull()
      expect(rowButton(3, 'Delete').disabled).toBe(false)
    })

    it('delete PUTs without the item', async () => {
      const s = await mount([b1, b2])
      await fireEvent.click(rowButton(3, 'Delete'))
      await fireEvent.click(rowButton(3, 'Confirm'))
      await waitFor(() => expect(rows().length).toBe(1))
      expect(puts(s.calls)).toEqual([{ buttons: [{ label: 'Roblox 1h', child_id: 2, services: ['roblox'], duration: 3600 }] }])
      expect(rows()[0].textContent).toContain('Roblox 1h')
      expect(confirming()).toEqual([])
    })

    it('delete in a mixed list', async () => {
      // [Ada1, Ben1, Ada2] displays as Ada1, Ada2 | Ben1: Ada2 is second on
      // screen but third in the list the PUT is built from.
      const s = await mount([b1, b2, b3])
      await fireEvent.click(rowButton(5, 'Delete'))
      await fireEvent.click(rowButton(5, 'Confirm'))
      await waitFor(() => expect(puts(s.calls)).toHaveLength(1))
      expect(puts(s.calls)[0].buttons.map((b) => b.label)).toEqual(['YouTube 90m', 'Roblox 1h'])
    })

    it('delete in a mixed list, middle of the list', async () => {
      const s = await mount([b1, b2, b3])
      await fireEvent.click(rowButton(4, 'Delete'))
      await fireEvent.click(rowButton(4, 'Confirm'))
      await waitFor(() => expect(puts(s.calls)).toHaveLength(1))
      expect(puts(s.calls)[0].buttons.map((b) => b.label)).toEqual(['YouTube 90m', 'TikTok 30m'])
    })

    it('one confirmation at a time', async () => {
      await mount([b1, b2, b3])
      await fireEvent.click(rowButton(3, 'Delete'))
      await fireEvent.click(rowButton(4, 'Delete'))
      expect(confirming()).toEqual(['4'])
      expect(rowButton(3, 'Delete')).toBeTruthy()
    })

    it('Confirm and Cancel lock while the delete PUT is in flight', async () => {
      let release!: (r: Response) => void
      await mount([b1, b2], { put: () => new Promise<Response>((r) => (release = r)) })
      await fireEvent.click(rowButton(3, 'Delete'))
      await fireEvent.click(rowButton(3, 'Confirm'))
      expect(rowButton(3, 'Confirm').disabled).toBe(true)
      expect(rowButton(3, 'Cancel').disabled).toBe(true)
      release(json(200, { buttons: [b2] }))
      await waitFor(() => expect(rows().length).toBe(1))
      expect(confirming()).toEqual([])
    })

    it('a confirmation follows its button through a reorder', async () => {
      // Ada1 is asking; Ada2 moves up past it. The question stays on Ada1.
      const s = await mount([b1, b2, b3])
      await fireEvent.click(rowButton(3, 'Delete'))
      await fireEvent.click(rowButton(5, 'Up'))
      await waitFor(() => expect(puts(s.calls)).toHaveLength(1))
      // [Ada2, Ben1, Ada1] with fresh ids 100, 101, 102.
      await waitFor(() => expect(confirming()).toEqual(['102']))
      expect(confirmOf(102)?.textContent).toContain('Delete YouTube 90m?')
      await fireEvent.click(rowButton(102, 'Confirm'))
      await waitFor(() => expect(puts(s.calls)).toHaveLength(2))
      expect(puts(s.calls)[1].buttons.map((b) => b.label)).toEqual(['TikTok 30m', 'Roblox 1h'])
    })

    it('edit survives delete', async () => {
      // Editing Ada2 (third in the list) while Ada1 (first) is deleted: the
      // edit shifts up with Ada2 instead of landing on whatever is now third.
      const s = await mount([b1, b2, b3])
      await fireEvent.click(rowButton(5, 'Edit'))
      await fireEvent.click(rowButton(3, 'Delete'))
      await fireEvent.click(rowButton(3, 'Confirm'))
      await waitFor(() => expect(puts(s.calls)).toHaveLength(1))
      await waitFor(() => expect(rows().length).toBe(2))
      expect(document.querySelector('li.editing')?.getAttribute('data-button')).toBe('101')
      expect(formHeading()).toBe('Edit button')

      await fireEvent.input(labelInput(), { target: { value: 'Renamed' } })
      await fireEvent.click(saveButton())
      await waitFor(() => expect(puts(s.calls)).toHaveLength(2))
      expect(puts(s.calls)[1].buttons).toEqual([
        { label: 'Roblox 1h', child_id: 2, services: ['roblox'], duration: 3600 },
        { label: 'Renamed', child_id: 1, services: ['tiktok'], duration: 1800 },
      ])
    })

    it('deleting the button being edited resets the form', async () => {
      const s = await mount([b1, b2])
      await fireEvent.click(rowButton(3, 'Edit'))
      await fireEvent.click(rowButton(3, 'Delete'))
      await fireEvent.click(rowButton(3, 'Confirm'))
      await waitFor(() => expect(puts(s.calls)).toHaveLength(1))
      await waitFor(() => expect(formHeading()).toBe('New button'))
      expect(labelInput().value).toBe('')
      expect(document.querySelector('li.editing')).toBeNull()
    })
  })

  it('422 shows the server message and keeps the draft', async () => {
    const message = 'buttons[0].services: unknown service "gone"'
    const s = await mount([b1], { put: () => json(422, { error: 'unprocessable', message }) })
    await fireEvent.input(labelInput(), { target: { value: 'Draft' } })
    await fireEvent.change(childSelect(), { target: { value: '1' } })
    await fireEvent.click(checkbox('YouTube'))
    await fireEvent.input(minutesInput(), { target: { value: '25' } })
    await fireEvent.click(saveButton())

    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toBe(message)
    expect(alert.getAttribute('data-error')).toBe('unprocessable')
    // Only once the reload has landed and the form is live again is "kept"
    // meaningful — a reset that ran after the alert would have cleared it.
    await waitFor(() => expect(getsOf(s.calls, '/api/v1/buttons')).toBe(2))
    await waitFor(() => expect(saveButton().disabled).toBe(false))
    expect(labelInput().value).toBe('Draft')
    expect(minutesInput().value).toBe('25')
    expect(checkbox('YouTube').checked).toBe(true)
    expect(formValue()).toEqual({ child_id: 1, services: ['youtube'], minutes: 25 })
    expect(rows().length).toBe(1)
  })

  it('a PUT that never reaches the server is the network alert', async () => {
    const s = await mount([b1], { put: () => Promise.reject(new TypeError('Failed to fetch')) })
    await fireEvent.click(rowButton(3, 'Delete'))
    await fireEvent.click(rowButton(3, 'Confirm'))
    const alert = await screen.findByRole('alert')
    expect(alert.getAttribute('data-error')).toBe('network')
    expect(alert.textContent).toBe("Can't reach the server — check your connection")
    await waitFor(() => expect(getsOf(s.calls, '/api/v1/buttons')).toBe(2))
    expect(rows().length).toBe(1)
  })

  it('everything is locked while a PUT is in flight', async () => {
    let release!: (r: Response) => void
    const s = await mount([b1, b2], { put: () => new Promise<Response>((r) => (release = r)) })
    await fireEvent.input(labelInput(), { target: { value: 'Held' } })
    await fireEvent.change(childSelect(), { target: { value: '1' } })
    await fireEvent.click(checkbox('YouTube'))
    expect(saveButton().disabled).toBe(false)
    expect(rowButton(3, 'Edit').disabled).toBe(false)

    await fireEvent.click(saveButton())
    expect(puts(s.calls)).toHaveLength(1)
    expect(saveButton().disabled).toBe(true)
    expect(labelInput().disabled).toBe(true)
    expect(grantForm().disabled).toBe(true)
    expect(rowButton(3, 'Edit').disabled).toBe(true)
    expect(rowButton(4, 'Delete').disabled).toBe(true)

    release(json(200, { buttons: [b1, b2, { id: 102, label: 'Held', child_id: 1, services: ['youtube'], duration: 3600 }] }))
    await waitFor(() => expect(rows().length).toBe(3))
    expect(labelInput().disabled).toBe(false)
    expect(grantForm().disabled).toBe(false)
    expect(rowButton(3, 'Edit').disabled).toBe(false)

    // A reorder PUT locks every row control too, not just the one tapped.
    expect(rowButton(3, 'Down').disabled).toBe(false)
    await fireEvent.click(rowButton(3, 'Down'))
    expect(puts(s.calls)).toHaveLength(2)
    for (const id of [3, 4, 102]) {
      for (const name of ['Up', 'Down', 'Edit', 'Delete']) {
        expect(rowButton(id, name).disabled, `${name} on row ${id}`).toBe(true)
      }
    }
    expect(saveButton().disabled).toBe(true)
    release(json(200, { buttons: [{ ...b1, id: 103 }, b2, { id: 104, label: 'Held', child_id: 1, services: ['youtube'], duration: 3600 }] }))
    await waitFor(() => expect(rowButton(103, 'Edit').disabled).toBe(false))
  })

  describe('reorder', () => {
    const order = (sent: { buttons: Stored[] }) => sent.buttons.map((b) => b.label)

    it('down swaps within the child', async () => {
      // [Ada1, Ben1, Ada2]: Ada1's neighbour is Ada2, not Ben's button between them.
      const s = await mount([b1, b2, b3])
      await fireEvent.click(rowButton(3, 'Down'))
      await waitFor(() => expect(puts(s.calls)).toHaveLength(1))
      // Everything else about each button rides along unchanged.
      expect(puts(s.calls)[0].buttons).toEqual([
        { label: 'TikTok 30m', child_id: 1, services: ['tiktok'], duration: 1800 },
        { label: 'Roblox 1h', child_id: 2, services: ['roblox'], duration: 3600 },
        { label: 'YouTube 90m', child_id: 1, services: ['youtube'], duration: 5400 },
      ])
      // The list renders the server's answer: Ada's group is now TikTok then YouTube.
      await waitFor(() => expect(rows().map((r) => r.querySelector('strong')?.textContent)).toEqual(['TikTok 30m', 'YouTube 90m', 'Roblox 1h']))
    })

    it('up swaps within the child', async () => {
      const s = await mount([b1, b2, b3])
      await fireEvent.click(rowButton(5, 'Up'))
      await waitFor(() => expect(puts(s.calls)).toHaveLength(1))
      expect(order(puts(s.calls)[0])).toEqual(['TikTok 30m', 'Roblox 1h', 'YouTube 90m'])
    })

    it('edge controls disabled', async () => {
      const b4: Button = { id: 6, label: 'Middle', child_id: 1, services: ['roblox'], duration: 600 }
      await mount([b1, b2, b4, b3])
      // Ada: [b1, b4, b3]; Ben: [b2] alone.
      expect(rowButton(3, 'Up').disabled).toBe(true)
      expect(rowButton(3, 'Down').disabled).toBe(false)
      expect(rowButton(6, 'Up').disabled).toBe(false)
      expect(rowButton(6, 'Down').disabled).toBe(false)
      expect(rowButton(5, 'Up').disabled).toBe(false)
      expect(rowButton(5, 'Down').disabled).toBe(true)
      expect(rowButton(4, 'Up').disabled).toBe(true)
      expect(rowButton(4, 'Down').disabled).toBe(true)
    })

    it('failed reorder reloads', async () => {
      // The PUT fails, and meanwhile the server's list has moved on: what
      // renders is the fresh GET, not the order the tap asked for.
      const s = await mount([b1, b2, b3], {
        put: () => {
          s.state.buttons = [b3, b2, b1]
          return json(500, { error: 'internal', message: 'boom' })
        },
      })
      await fireEvent.click(rowButton(3, 'Down'))
      await screen.findByRole('alert')
      await waitFor(() => expect(getsOf(s.calls, '/api/v1/buttons')).toBe(2))
      await waitFor(() => expect(rows().map((r) => r.dataset.button)).toEqual(['5', '3', '4']))
    })

    it('grouped by child', async () => {
      // Children come back Ben first, so Ben's group heads the page even
      // though Ada's button is first in the stored list.
      await mount([b1, b2, b3], { children: [ben, ada] })
      const sections = [...document.querySelectorAll<HTMLElement>('section[data-child]')]
      expect(sections.map((sec) => sec.querySelector('h2')?.textContent)).toEqual(['Ben', 'Ada'])
      const inGroup = (sec: HTMLElement) => [...sec.querySelectorAll<HTMLElement>('li[data-button]')].map((li) => li.dataset.button)
      expect(inGroup(sections[0])).toEqual(['4'])
      expect(inGroup(sections[1])).toEqual(['3', '5'])
      expect(document.querySelectorAll('ul.list')).toHaveLength(2)
    })

    it('a button for an unknown child still gets a group', async () => {
      const orphan: Button = { id: 7, label: 'Orphan', child_id: 9, services: ['gone'], duration: 600 }
      await mount([orphan, b1])
      const sections = [...document.querySelectorAll<HTMLElement>('section[data-child]')]
      expect(sections.map((sec) => sec.querySelector('h2')?.textContent)).toEqual(['Ada', 'child 9'])
    })

    it('edit survives reorder', async () => {
      const s = await mount([b1, b2, b3])
      await fireEvent.click(rowButton(3, 'Edit'))
      await fireEvent.click(rowButton(3, 'Down'))
      await waitFor(() => expect(puts(s.calls)).toHaveLength(1))
      // [Ada2, Ben1, Ada1] with fresh ids 100, 101, 102: the edit follows Ada1 to 102.
      await waitFor(() => expect(rowButton(102, 'Edit').disabled).toBe(false))
      expect(document.querySelector('li.editing')?.getAttribute('data-button')).toBe('102')
      expect(labelInput().value).toBe('YouTube 90m')

      await fireEvent.input(labelInput(), { target: { value: 'Renamed' } })
      await fireEvent.click(saveButton())
      await waitFor(() => expect(puts(s.calls)).toHaveLength(2))
      expect(puts(s.calls)[1].buttons).toEqual([
        { label: 'TikTok 30m', child_id: 1, services: ['tiktok'], duration: 1800 },
        { label: 'Roblox 1h', child_id: 2, services: ['roblox'], duration: 3600 },
        { label: 'Renamed', child_id: 1, services: ['youtube'], duration: 5400 },
      ])
    })

    it('edit on the neighbour survives reorder', async () => {
      // Editing Ada2 while Ada1 moves down past it: the edit stays on Ada2.
      const s = await mount([b1, b2, b3])
      await fireEvent.click(rowButton(5, 'Edit'))
      await fireEvent.click(rowButton(3, 'Down'))
      await waitFor(() => expect(puts(s.calls)).toHaveLength(1))
      await waitFor(() => expect(rowButton(100, 'Edit').disabled).toBe(false))
      expect(document.querySelector('li.editing')?.getAttribute('data-button')).toBe('100')
      await fireEvent.input(labelInput(), { target: { value: 'Renamed' } })
      await fireEvent.click(saveButton())
      await waitFor(() => expect(puts(s.calls)).toHaveLength(2))
      expect(order(puts(s.calls)[1])).toEqual(['Renamed', 'Roblox 1h', 'YouTube 90m'])
    })
  })

  it('a failed PUT reloads the catalogue too', async () => {
    // The reload is a fresh load: a catalogue that has gone away since the
    // page opened is dropped, not kept stale.
    const s = await mount([b1], { put: () => json(422, { error: 'unprocessable', message: 'nope' }) })
    expect(document.querySelector('[data-unavailable]')).toBeNull()
    s.state.servicesDown = true
    await fireEvent.click(rowButton(3, 'Delete'))
    await fireEvent.click(rowButton(3, 'Confirm'))
    await screen.findByRole('alert')
    await waitFor(() => expect(getsOf(s.calls, '/api/v1/services')).toBe(2))
    await waitFor(() => expect(document.querySelector('[data-unavailable]')).not.toBeNull())
    expect(document.querySelector('input[type="checkbox"]')).toBeNull()
  })

  it('Save waits for a valid form', async () => {
    await mount([])
    expect(saveButton().disabled).toBe(true)
    await fireEvent.input(labelInput(), { target: { value: 'Only a label' } })
    expect(saveButton().disabled).toBe(true)
    await fireEvent.change(childSelect(), { target: { value: '2' } })
    expect(saveButton().disabled).toBe(true)
    await fireEvent.click(checkbox('Roblox'))
    expect(saveButton().disabled).toBe(false)
    await fireEvent.input(labelInput(), { target: { value: '   ' } })
    expect(saveButton().disabled).toBe(true)
  })

  it('services 502 still lists buttons', async () => {
    const s = await mount([b1, b2], { services: 'down' })
    expect(rows().length).toBe(2)
    expect(rows()[0].textContent).toContain('youtube')
    expect(document.querySelector('[data-unavailable]')).not.toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()

    await fireEvent.click(rowButton(3, 'Edit'))
    expect(saveButton().disabled).toBe(false)
    await fireEvent.click(saveButton())
    await waitFor(() => expect(puts(s.calls)).toHaveLength(1))
    expect(puts(s.calls)[0].buttons[0]).toEqual({ label: 'YouTube 90m', child_id: 1, services: ['youtube'], duration: 5400 })
  })

  it('sharedHint', async () => {
    const s = await mount([b1, b2], { children: [ada, { ...ben, clients: ['Kid tablet', 'Kid phone'] }] })
    await fireEvent.click(rowButton(3, 'Edit'))
    expect(childSelect().value).toBe('1')
    expect(screen.getByRole('note').textContent).toContain('Kid phone (shared with Ben)')

    await fireEvent.click(saveButton())
    await waitFor(() => expect(puts(s.calls)).toHaveLength(1))
    expect(puts(s.calls)[0].buttons[0]).toEqual({ label: 'YouTube 90m', child_id: 1, services: ['youtube'], duration: 5400 })
    expect(s.calls.filter((c) => c.method === 'PUT' && c.url === '/api/v1/buttons')).toHaveLength(1)
  })

  it('Back calls onBack', async () => {
    server([])
    const onBack = vi.fn()
    render(Buttons, { onBack })
    await fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    expect(onBack).toHaveBeenCalledTimes(1)
  })
})
