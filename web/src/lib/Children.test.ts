// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Children from './Children.svelte'
import type { Child, ClientView } from './api'

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
const ben: Child = { id: 2, name: 'Ben', clients: [] }

function client(name: string, child: ClientView['child'] = null): ClientView {
  return { name, ids: [], use_global_blocked_services: false, child }
}

/** A mutable server: routes read `state` so a re-fetch after a mutation sees the change. */
function server(children: Child[], clients: ClientView[]) {
  const state = { children, clients }
  const routes: Routes = {
    '/api/v1/children': () => json(200, { children: state.children }),
    '/api/v1/clients': () => json(200, { clients: state.clients }),
  }
  return { state, routes }
}

function form(id: number): HTMLFormElement {
  const el = document.querySelector(`form[data-child="${id}"]`)
  if (!el) throw new Error(`no form for child ${id}`)
  return el as HTMLFormElement
}

function checkbox(f: HTMLElement, label: string): HTMLInputElement {
  for (const l of f.querySelectorAll('label')) {
    if (l.textContent?.trim().startsWith(label)) {
      const cb = l.querySelector('input[type="checkbox"]')
      if (cb) return cb as HTMLInputElement
    }
  }
  throw new Error(`no checkbox ${label}`)
}

function button(f: HTMLElement, name: string): HTMLButtonElement {
  for (const b of f.querySelectorAll('button')) {
    if (b.textContent?.trim() === name) return b
  }
  throw new Error(`no button ${name}`)
}

function puts(calls: Call[]): Call[] {
  return calls.filter((c) => c.method === 'PUT')
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('Children', () => {
  it('renders ownership', async () => {
    const { routes } = server([ada, ben], [client('Kid phone', { id: 1, name: 'Ada' }), client('Kid tablet')])
    mockFetch(routes)
    render(Children, { onBack: vi.fn() })
    await screen.findByDisplayValue('Ada')

    const benPhone = checkbox(form(2), 'Kid phone')
    expect(benPhone.disabled).toBe(true)
    expect(benPhone.closest('label')?.textContent).toContain('assigned to Ada')
    const adaPhone = checkbox(form(1), 'Kid phone')
    expect(adaPhone.disabled).toBe(false)
    expect(adaPhone.checked).toBe(true)
    for (const id of [1, 2]) {
      const tab = checkbox(form(id), 'Kid tablet')
      expect(tab.disabled).toBe(false)
      expect(tab.checked).toBe(false)
    }
  })

  it('assign by checkbox', async () => {
    const { state, routes } = server([ada, ben], [client('Kid phone', { id: 1, name: 'Ada' }), client('Kid tablet')])
    const calls = mockFetch({
      ...routes,
      'PUT /api/v1/children/1': () => {
        state.children = [{ ...ada, clients: ['Kid phone', 'Kid tablet'] }, ben]
        return json(200, state.children[0])
      },
    })
    render(Children, { onBack: vi.fn() })
    await screen.findByDisplayValue('Ada')

    await fireEvent.click(checkbox(form(1), 'Kid tablet'))
    expect(puts(calls)).toHaveLength(0)
    await fireEvent.click(button(form(1), 'Save'))
    await waitFor(() => expect(puts(calls)).toHaveLength(1))
    expect(puts(calls)[0].url).toBe('/api/v1/children/1')
    expect(puts(calls)[0].body).toBe('{"name":"Ada","clients":["Kid phone","Kid tablet"]}')
  })

  it('unassign by checkbox', async () => {
    const { routes } = server([ada, ben], [client('Kid phone', { id: 1, name: 'Ada' }), client('Kid tablet')])
    const calls = mockFetch({ ...routes, 'PUT /api/v1/children/1': () => json(200, { ...ada, clients: [] }) })
    render(Children, { onBack: vi.fn() })
    await screen.findByDisplayValue('Ada')

    await fireEvent.click(checkbox(form(1), 'Kid phone'))
    expect(checkbox(form(1), 'Kid phone').checked).toBe(false)
    expect(puts(calls)).toHaveLength(0)
    await fireEvent.click(button(form(1), 'Save'))
    await waitFor(() => expect(puts(calls)).toHaveLength(1))
    expect(puts(calls)[0].body).toBe('{"name":"Ada","clients":[]}')
  })

  it('unassign one of two by checkbox', async () => {
    const two: Child = { ...ada, clients: ['Kid phone', 'Kid tablet'] }
    const owner = { id: 1, name: 'Ada' }
    const { routes } = server([two, ben], [client('Kid phone', owner), client('Kid tablet', owner)])
    const calls = mockFetch({ ...routes, 'PUT /api/v1/children/1': () => json(200, { ...two, clients: ['Kid phone'] }) })
    render(Children, { onBack: vi.fn() })
    await screen.findByDisplayValue('Ada')
    expect(checkbox(form(1), 'Kid phone').checked).toBe(true)
    expect(checkbox(form(1), 'Kid tablet').checked).toBe(true)

    await fireEvent.click(checkbox(form(1), 'Kid tablet'))
    expect(checkbox(form(1), 'Kid tablet').checked).toBe(false)
    expect(checkbox(form(1), 'Kid phone').checked).toBe(true)
    expect(puts(calls)).toHaveLength(0)
    await fireEvent.click(button(form(1), 'Save'))
    await waitFor(() => expect(puts(calls)).toHaveLength(1))
    expect(puts(calls)[0].body).toBe('{"name":"Ada","clients":["Kid phone"]}')
  })

  it('rename and delete', async () => {
    const { state, routes } = server([ada, ben], [client('Kid phone', { id: 1, name: 'Ada' })])
    const calls = mockFetch({
      ...routes,
      'PUT /api/v1/children/1': () => json(200, { ...ada, name: 'Ada M' }),
      'DELETE /api/v1/children/2': () => {
        state.children = [ada]
        return new Response(null, { status: 204 })
      },
    })
    render(Children, { onBack: vi.fn() })
    await screen.findByDisplayValue('Ada')

    const name = form(1).querySelector('input[aria-label="Name"]') as HTMLInputElement
    await fireEvent.input(name, { target: { value: 'Ada M' } })
    await fireEvent.click(button(form(1), 'Save'))
    await waitFor(() => expect(puts(calls)).toHaveLength(1))
    expect(puts(calls)[0].body).toBe('{"name":"Ada M","clients":["Kid phone"]}')

    await fireEvent.click(button(form(2), 'Delete'))
    await waitFor(() => expect(document.querySelector('form[data-child="2"]')).toBeNull())
    expect(calls.some((c) => c.method === 'DELETE' && c.url === '/api/v1/children/2')).toBe(true)
  })

  it('missing client is struck through', async () => {
    const withGhost = { ...ada, clients: ['Kid phone', 'Ghost'] }
    // Two known clients: a client is "known" if ANY AdGuard client matches it,
    // so Kid phone must keep its checkbox while only Ghost is struck through.
    const { routes } = server([withGhost], [client('Kid phone', { id: 1, name: 'Ada' }), client('Kid tablet')])
    const calls = mockFetch({ ...routes, 'PUT /api/v1/children/1': () => json(200, withGhost) })
    render(Children, { onBack: vi.fn() })
    await screen.findByDisplayValue('Ada')

    const struck = form(1).querySelectorAll('s[data-missing]')
    expect(struck).toHaveLength(1)
    expect(struck[0].textContent).toBe('Ghost')
    expect(() => checkbox(form(1), 'Ghost')).toThrow()
    expect(checkbox(form(1), 'Kid phone').checked).toBe(true)
    expect(button(form(1), 'Remove')).toBeTruthy()

    await fireEvent.click(button(form(1), 'Save'))
    await waitFor(() => expect(puts(calls)).toHaveLength(1))
    expect(puts(calls)[0].body).toBe('{"name":"Ada","clients":["Kid phone","Ghost"]}')
    // The re-fetch after Save rebuilds the drafts; wait for it to settle.
    await waitFor(() => expect(button(form(1), 'Save').disabled).toBe(false))

    await fireEvent.click(button(form(1), 'Remove'))
    expect(form(1).querySelector('s[data-missing]')).toBeNull()
    await fireEvent.click(button(form(1), 'Save'))
    await waitFor(() => expect(puts(calls)).toHaveLength(2))
    expect(puts(calls)[1].body).toBe('{"name":"Ada","clients":["Kid phone"]}')
  })

  it('add child', async () => {
    const { state, routes } = server([ada], [client('Kid phone', { id: 1, name: 'Ada' })])
    const calls = mockFetch({
      ...routes,
      'POST /api/v1/children': () => {
        state.children = [ada, { id: 3, name: 'Cy', clients: [] }]
        return json(201, state.children[1])
      },
    })
    render(Children, { onBack: vi.fn() })
    await screen.findByDisplayValue('Ada')

    const field = screen.getByLabelText('New child') as HTMLInputElement
    expect(field.value).toBe('')
    await fireEvent.input(field, { target: { value: 'Cy' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Add child' }))
    await waitFor(() => expect(document.querySelector('form[data-child="3"]')).not.toBeNull())
    const post = calls.find((c) => c.method === 'POST')
    expect(post?.url).toBe('/api/v1/children')
    expect(post?.body).toBe('{"name":"Cy","clients":[]}')
    expect(document.querySelectorAll('form[data-child="3"]')).toHaveLength(1)
    // A successful add clears the field for the next child.
    await waitFor(() => expect((screen.getByLabelText('New child') as HTMLInputElement).value).toBe(''))
  })

  it('network failure shows an alert', async () => {
    const { routes } = server([ada], [client('Kid phone', { id: 1, name: 'Ada' })])
    mockFetch({
      ...routes,
      'PUT /api/v1/children/1': () => {
        throw new TypeError('Failed to fetch')
      },
    })
    render(Children, { onBack: vi.fn() })
    await screen.findByDisplayValue('Ada')

    await fireEvent.click(button(form(1), 'Save'))
    const alert = await screen.findByRole('alert')
    expect(alert.getAttribute('data-error')).toBe('network')
    expect(alert.textContent).toBe("Can't reach the server — check your connection")
  })

  it('renders the server after save', async () => {
    const { state, routes } = server([ada], [client('Kid phone', { id: 1, name: 'Ada' })])
    mockFetch({
      ...routes,
      'PUT /api/v1/children/1': () => {
        state.children = [{ ...ada, name: 'Ada Server' }]
        return json(200, { ...ada, name: 'Ada Typed' })
      },
    })
    render(Children, { onBack: vi.fn() })
    await screen.findByDisplayValue('Ada')
    const name = form(1).querySelector('input[aria-label="Name"]') as HTMLInputElement
    await fireEvent.input(name, { target: { value: 'Ada Typed' } })
    await fireEvent.click(button(form(1), 'Save'))
    await screen.findByDisplayValue('Ada Server')
    expect(screen.queryByDisplayValue('Ada Typed')).toBeNull()
  })

  it('shows a conflict', async () => {
    const { routes } = server([ada, ben], [client('Kid phone', { id: 1, name: 'Ada' }), client('Kid tablet')])
    const calls = mockFetch({
      ...routes,
      'PUT /api/v1/children/1': () =>
        json(409, { error: 'conflict', message: 'client Kid phone is already assigned to Ben' }),
      'POST /api/v1/children': () => json(409, { error: 'conflict', message: 'a child named Ada already exists' }),
    })
    render(Children, { onBack: vi.fn() })
    await screen.findByDisplayValue('Ada')

    await fireEvent.click(checkbox(form(1), 'Kid tablet'))
    expect(checkbox(form(1), 'Kid tablet').checked).toBe(true)
    const listsBefore = calls.filter((c) => c.method === 'GET' && c.url === '/api/v1/children').length
    await fireEvent.click(button(form(1), 'Save'))
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toBe('client Kid phone is already assigned to Ben')
    expect(alert.getAttribute('data-error')).toBe('conflict')
    await waitFor(() =>
      expect(calls.filter((c) => c.method === 'GET' && c.url === '/api/v1/children').length).toBe(listsBefore + 1),
    )
    await waitFor(() => expect(checkbox(form(1), 'Kid tablet').checked).toBe(false))

    await fireEvent.input(screen.getByLabelText('New child'), { target: { value: 'Ada' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Add child' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toBe('a child named Ada already exists'))
    expect((screen.getByLabelText('New child') as HTMLInputElement).value).toBe('Ada')
    expect(document.querySelectorAll('form[data-child]')).toHaveLength(2)
  })

  it('Back calls onBack', async () => {
    const { routes } = server([], [])
    mockFetch(routes)
    const onBack = vi.fn()
    render(Children, { onBack })
    await fireEvent.click(await screen.findByRole('button', { name: 'Back' }))
    expect(onBack).toHaveBeenCalledTimes(1)
  })
})
