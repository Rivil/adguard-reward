// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Children from './Children.svelte'
import ClientNames from './ClientNames.svelte'
import type { Child, ChildRef, ClientView } from './api'

type Call = { method: string; url: string; body: string | null }
type Routes = Record<string, () => Response | Promise<Response>>

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

function client(name: string, ...owners: ChildRef[]): ClientView {
  return { name, ids: [], use_global_blocked_services: false, children: owners }
}

function row(name: string): HTMLFormElement {
  const el = document.querySelector(`form[data-client="${name}"]`)
  if (!el) throw new Error(`no row ${name}`)
  return el as HTMLFormElement
}

const input = (name: string) => row(name).querySelector('input') as HTMLInputElement
const save = (name: string) => row(name).querySelector('button[type="submit"]') as HTMLButtonElement

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('ClientNames', () => {
  it('renames and the page reloads with the new name checked', async () => {
    const state = {
      children: [{ id: 1, name: 'Ada', clients: ['Kid phone'] }] as Child[],
      clients: [client('Kid phone', { id: 1, name: 'Ada' })],
    }
    const calls = mockFetch({
      '/api/v1/children': () => json(200, { children: state.children }),
      'GET /api/v1/clients': () => json(200, { clients: state.clients, unlabelled: [] }),
      'POST /api/v1/clients/rename': () => {
        state.children = [{ id: 1, name: 'Ada', clients: ['Ada phone'] }]
        state.clients = [client('Ada phone', { id: 1, name: 'Ada' })]
        return json(200, state.clients[0])
      },
    })
    render(Children, { onBack: vi.fn() })
    await screen.findByDisplayValue('Ada')

    await fireEvent.input(input('Kid phone'), { target: { value: 'Ada phone' } })
    await fireEvent.submit(row('Kid phone'))
    await waitFor(() => expect(document.querySelector('form[data-client="Ada phone"]')).not.toBeNull())

    const post = calls.find((c) => c.method === 'POST')!
    expect(post.url).toBe('/api/v1/clients/rename')
    expect(JSON.parse(post.body!)).toEqual({ from: 'Kid phone', to: 'Ada phone' })
    const ada = document.querySelector('form[data-child="1"]')!
    const label = [...ada.querySelectorAll('label')].find((l) => l.textContent?.trim().startsWith('Ada phone'))!
    expect((label.querySelector('input[type="checkbox"]') as HTMLInputElement).checked).toBe(true)
    expect(document.querySelector('[data-missing]')).toBeNull()
  })

  it('offers rename on an unassigned client', () => {
    mockFetch({})
    render(ClientNames, { clients: [client('Old laptop'), client('Kid phone', { id: 1, name: 'Ada' })], onRenamed: vi.fn() })
    expect(input('Old laptop')).toBeTruthy()
    expect(save('Old laptop')).toBeTruthy()
    expect(row('Old laptop').querySelector('.current')!.textContent).toBe('Old laptop')
    expect(row('Kid phone').querySelector('.current')!.textContent).toBe('Kid phone — Ada')
  })

  it('names every owner of a shared client, in server order', () => {
    mockFetch({})
    const ada = { id: 1, name: 'Ada' }
    const ben = { id: 2, name: 'Ben' }
    render(ClientNames, { clients: [client('Kid phone', ada, ben), client('TV', ben)], onRenamed: vi.fn() })
    expect(row('Kid phone').querySelector('.current')!.textContent).toBe('Kid phone — Ada, Ben')
    expect(row('TV').querySelector('.current')!.textContent).toBe('TV — Ben')
  })

  it('blank or unchanged names disable Save and send nothing', async () => {
    mockFetch({})
    render(ClientNames, { clients: [client('Kid phone')], onRenamed: vi.fn() })
    expect(save('Kid phone').disabled).toBe(true)
    await fireEvent.submit(row('Kid phone'))
    await fireEvent.input(input('Kid phone'), { target: { value: '   ' } })
    expect(save('Kid phone').disabled).toBe(true)
    await fireEvent.submit(row('Kid phone'))
    await fireEvent.input(input('Kid phone'), { target: { value: ' Kid phone ' } })
    expect(save('Kid phone').disabled).toBe(true)
    await fireEvent.submit(row('Kid phone'))
    expect(vi.mocked(fetch)).not.toHaveBeenCalled()
  })

  it('a 409 keeps the typed name and shows the server message', async () => {
    mockFetch({
      'POST /api/v1/clients/rename': () => json(409, { error: 'conflict', message: 'a client named "Kid tablet" already exists' }),
    })
    const onRenamed = vi.fn()
    render(ClientNames, { clients: [client('Kid phone'), client('Kid tablet')], onRenamed })
    await fireEvent.input(input('Kid phone'), { target: { value: 'Kid tablet' } })
    await fireEvent.submit(row('Kid phone'))
    expect((await screen.findByRole('alert')).textContent).toBe('a client named "Kid tablet" already exists')
    expect(input('Kid phone').value).toBe('Kid tablet')
    expect(row('Kid phone').textContent).toContain('Kid phone')
    expect(onRenamed).not.toHaveBeenCalled()
  })

  it('a changed name enables Save; the disabled prop locks the row', async () => {
    mockFetch({})
    const { rerender } = render(ClientNames, { clients: [client('Kid phone')], onRenamed: vi.fn() })
    expect(input('Kid phone').disabled).toBe(false)
    await fireEvent.input(input('Kid phone'), { target: { value: 'TV' } })
    expect(save('Kid phone').disabled).toBe(false)
    await rerender({ disabled: true })
    expect(input('Kid phone').disabled).toBe(true)
    expect(save('Kid phone').disabled).toBe(true)
  })

  it('a network failure shows the offline line and the row can retry', async () => {
    let fail = true
    const calls = mockFetch({
      'POST /api/v1/clients/rename': () => {
        if (fail) throw new TypeError('Failed to fetch')
        return json(200, client('TV'))
      },
    })
    const onRenamed = vi.fn()
    render(ClientNames, { clients: [client('Kid phone')], onRenamed })
    await fireEvent.input(input('Kid phone'), { target: { value: 'TV' } })
    await fireEvent.submit(row('Kid phone'))
    expect((await screen.findByRole('alert')).textContent).toContain("Can't reach the server")
    expect(save('Kid phone').disabled).toBe(false)
    expect(input('Kid phone').disabled).toBe(false)

    fail = false
    await fireEvent.submit(row('Kid phone'))
    await waitFor(() => expect(onRenamed).toHaveBeenCalledTimes(1))
    expect(calls).toHaveLength(2)
  })

  it('trims the new name and locks while a rename is out', async () => {
    let release!: () => void
    const held = new Promise<void>((r) => (release = r))
    const calls = mockFetch({
      'POST /api/v1/clients/rename': async () => {
        await held
        return json(200, client('TV'))
      },
    })
    const onRenamed = vi.fn()
    render(ClientNames, { clients: [client('Kid phone'), client('Den')], onRenamed })
    await fireEvent.input(input('Kid phone'), { target: { value: '  TV ' } })
    await fireEvent.submit(row('Kid phone'))
    await fireEvent.submit(row('Kid phone'))
    expect(save('Kid phone').disabled).toBe(true)
    expect(save('Den').disabled).toBe(true)
    expect(input('Den').disabled).toBe(true)
    release()
    await waitFor(() => expect(onRenamed).toHaveBeenCalledTimes(1))
    expect(calls).toHaveLength(1)
    expect(JSON.parse(calls[0].body!)).toEqual({ from: 'Kid phone', to: 'TV' })
  })
})
