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

/** A mutable server: PUT replaces the list and hands back fresh ids. */
function server(buttons: Button[], opts: { services?: Service[] | 'down'; put?: Answer } = {}) {
  const state = { buttons, nextId: 100 }
  const routes: Routes = {
    '/api/v1/buttons': () => json(200, { buttons: state.buttons }),
    '/api/v1/children': () => json(200, { children: [ada, ben] }),
    '/api/v1/services':
      opts.services === 'down'
        ? () => json(502, { error: 'adguard_unavailable', message: 'down' })
        : () => json(200, { services: opts.services ?? [youtube, tiktok, roblox] }),
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

    await fireEvent.input(labelInput(), { target: { value: 'TikTok 30m' } })
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
    await waitFor(() => expect(labelInput().value).toBe(''))
  })

  it('edit keeps position and converts units', async () => {
    const s = await mount([b1, b2, b3])
    await fireEvent.click(rowButton(4, 'Edit'))
    expect(labelInput().value).toBe('Roblox 1h')
    expect(minutesInput().value).toBe('60')
    expect(childSelect().value).toBe('2')
    expect(checkbox('Roblox').checked).toBe(true)
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeTruthy()

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
    await waitFor(() => expect(rows()[1].textContent).toContain('Renamed'))
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull())
    expect(labelInput().value).toBe('')
  })

  it('delete PUTs without the item', async () => {
    const s = await mount([b1, b2])
    await fireEvent.click(rowButton(3, 'Delete'))
    await waitFor(() => expect(rows().length).toBe(1))
    expect(puts(s.calls)).toEqual([{ buttons: [{ label: 'Roblox 1h', child_id: 2, services: ['roblox'], duration: 3600 }] }])
    expect(rows()[0].textContent).toContain('Roblox 1h')
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
    expect(labelInput().value).toBe('Draft')
    expect(minutesInput().value).toBe('25')
    expect(checkbox('YouTube').checked).toBe(true)
    await waitFor(() => expect(getsOf(s.calls, '/api/v1/buttons')).toBe(2))
    expect(rows().length).toBe(1)
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

  it('Back calls onBack', async () => {
    server([])
    const onBack = vi.fn()
    render(Buttons, { onBack })
    await fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    expect(onBack).toHaveBeenCalledTimes(1)
  })
})
