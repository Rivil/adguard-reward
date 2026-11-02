// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Services from './Services.svelte'
import type { CustomService } from './api'

type Call = { method: string; url: string; body: unknown }
type Answer = (body: unknown) => Response | Promise<Response>

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

/** Routes keyed "METHOD url"; each may be a queue of answers, consumed in order. */
function mockFetch(routes: Record<string, Answer | Answer[]>): Call[] {
  const calls: Call[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit) => {
      const method = init?.method ?? 'GET'
      const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined
      calls.push({ method, url, body })
      const r = routes[`${method} ${url}`]
      const answer = Array.isArray(r) ? r.shift() : r
      if (!answer) throw new Error('unexpected fetch ' + method + ' ' + url)
      return answer(body)
    }),
  )
  return calls
}

const portal: CustomService = { id: 1, service_id: 'custom:1', name: 'Portal', domains: ['a.com', 'b.com'], applied: true }

function nameInput(): HTMLInputElement {
  return screen.getByLabelText('Name') as HTMLInputElement
}
function domainsInput(): HTMLTextAreaElement {
  return screen.getByLabelText('Domains, one per line') as HTMLTextAreaElement
}
function rows(): HTMLElement[] {
  return [...document.querySelectorAll('li[data-service]')] as HTMLElement[]
}
function row(serviceId: string): HTMLElement {
  const el = document.querySelector(`li[data-service="${serviceId}"]`)
  if (!el) throw new Error('no row ' + serviceId)
  return el as HTMLElement
}
async function fill(name: string, domains: string) {
  await fireEvent.input(nameInput(), { target: { value: name } })
  await fireEvent.input(domainsInput(), { target: { value: domains } })
}
async function mount(list: CustomService[], routes: Record<string, Answer | Answer[]> = {}) {
  const calls = mockFetch({ 'GET /api/v1/custom-services': () => json(200, { custom_services: list }), ...routes })
  render(Services, { onBack: () => {} })
  await screen.findByRole('heading', { name: 'Services' })
  await waitFor(() => expect(calls.length).toBeGreaterThan(0))
  await waitFor(() => expect(rows().length).toBe(list.length))
  return calls
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('Services', () => {
  it('lists each service with avatar, name and domains', async () => {
    await mount([portal])
    const r = row('custom:1')
    expect(r.querySelector('[data-letter-avatar]')?.textContent).toBe('P')
    expect(r.textContent).toContain('Portal')
    expect(r.querySelector('[data-domains]')?.textContent).toBe('a.com, b.com')
  })

  it('create', async () => {
    const created: CustomService = { id: 2, service_id: 'custom:2', name: 'Portal', domains: ['a.com', 'b.com'], applied: true }
    const calls = await mount([], { 'POST /api/v1/custom-services': () => json(201, created) })
    await fill('  Portal ', 'a.com\n\n b.com \n')
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(rows().length).toBe(1))
    expect(calls.at(-1)).toEqual({ method: 'POST', url: '/api/v1/custom-services', body: { name: 'Portal', domains: ['a.com', 'b.com'] } })
    expect(row('custom:2').querySelector('[data-domains]')?.textContent).toBe('a.com, b.com')
    // The form resets after a successful create.
    expect(nameInput().value).toBe('')
    expect(domainsInput().value).toBe('')
  })

  it('renders the row from the server response, not the draft', async () => {
    const normalised: CustomService = { id: 2, service_id: 'custom:2', name: 'Portal', domains: ['example.com'], applied: true }
    await mount([], { 'POST /api/v1/custom-services': () => json(201, normalised) })
    await fill('Portal', 'Example.COM.')
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(rows().length).toBe(1))
    expect(row('custom:2').querySelector('[data-domains]')?.textContent).toBe('example.com')
  })

  it('invalid domain', async () => {
    await mount([portal], {
      'POST /api/v1/custom-services': () =>
        json(400, { error: 'invalid_request', message: 'domains[1]: domain "a.com/x": not a plain hostname' }),
    })
    await fill('Other', 'ok.com\na.com/x')
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('a.com/x')
    expect(domainsInput().value).toBe('ok.com\na.com/x')
    expect(nameInput().value).toBe('Other')
    expect(rows().map((r) => r.dataset.service)).toEqual(['custom:1'])
  })

  it('Save is disabled without a name or a domain', async () => {
    await mount([])
    const save = screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement
    expect(save.disabled).toBe(true)
    await fill('Portal', ' \n\n')
    expect(save.disabled).toBe(true)
    await fill('  ', 'a.com')
    expect(save.disabled).toBe(true)
    await fill('Portal', 'a.com')
    expect(save.disabled).toBe(false)
  })

  it('edit', async () => {
    const updated: CustomService = { ...portal, name: 'Portal 2', domains: ['b.com', 'c.com'] }
    const calls = await mount([portal], { 'PUT /api/v1/custom-services/1': () => json(200, updated) })
    await fireEvent.click(screen.getByRole('button', { name: 'Edit' }))
    expect(screen.getByRole('heading', { name: 'Edit service' })).toBeTruthy()
    expect(nameInput().value).toBe('Portal')
    expect(domainsInput().value).toBe('a.com\nb.com')

    await fill('Portal 2', 'b.com\nc.com\nlocal-draft-only.com')
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(row('custom:1').textContent).toContain('Portal 2'))
    expect(calls.at(-1)).toEqual({
      method: 'PUT',
      url: '/api/v1/custom-services/1',
      body: { name: 'Portal 2', domains: ['b.com', 'c.com', 'local-draft-only.com'] },
    })
    // Replaced from the response, which dropped the draft's third domain.
    expect(row('custom:1').querySelector('[data-domains]')?.textContent).toBe('b.com, c.com')
    expect(rows().length).toBe(1)
    expect(screen.getByRole('heading', { name: 'New service' })).toBeTruthy()
  })

  it('delete', async () => {
    const confirmSpy = vi.spyOn(window, 'confirm')
    const calls = await mount([portal], {
      'DELETE /api/v1/custom-services/1': [
        () => json(409, { error: 'conflict', message: 'Portal is in use by active grant 7', grant_id: 7 }),
        () => new Response(null, { status: 204 }),
      ],
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    // Nothing fires until Confirm.
    expect(calls.filter((c) => c.method === 'DELETE')).toEqual([])
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('button', { name: 'Confirm' })).toBeNull()

    await fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('grant 7')
    expect(rows().map((r) => r.dataset.service)).toEqual(['custom:1'])

    await fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
    await waitFor(() => expect(rows().length).toBe(0))
    expect(calls.filter((c) => c.method === 'DELETE').map((c) => c.url)).toEqual([
      '/api/v1/custom-services/1',
      '/api/v1/custom-services/1',
    ])
    expect(confirmSpy).not.toHaveBeenCalled()
  })

  it('applied false', async () => {
    const pending: CustomService = { id: 2, service_id: 'custom:2', name: 'Portal', domains: ['a.com'], applied: false }
    await mount([], { 'POST /api/v1/custom-services': () => json(201, pending) })
    await fill('Portal', 'a.com')
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    const status = await screen.findByRole('status')
    expect(status.textContent).toContain('saved — AdGuard will catch up')
    expect(rows().length).toBe(1)
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('applied true shows no catch-up note', async () => {
    await mount([], { 'POST /api/v1/custom-services': () => json(201, { ...portal, id: 2, service_id: 'custom:2' }) })
    await fill('Portal', 'a.com')
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(rows().length).toBe(1))
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('Back calls onBack', async () => {
    mockFetch({ 'GET /api/v1/custom-services': () => json(200, { custom_services: [] }) })
    const onBack = vi.fn()
    render(Services, { onBack })
    await fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    expect(onBack).toHaveBeenCalledOnce()
  })
})
