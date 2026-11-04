// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen } from '@testing-library/svelte'
import { describe, expect, it, vi } from 'vitest'
import BlockPicker from './BlockPicker.svelte'
import type { BlockedView, Service, ServiceState } from './api'

const catalogue: Service[] = [
  { id: 'youtube', name: 'YouTube', icon: 'PHN2Zy8+', custom: false },
  { id: 'tiktok', name: 'TikTok', icon: 'PHN2Zy8+', custom: false },
  { id: 'netflix', name: 'Netflix', icon: 'PHN2Zy8+', custom: false },
  { id: 'custom:1', name: 'School Portal', icon: '', custom: true },
]

function row(id: string, state: ServiceState['state']): ServiceState {
  const s = catalogue.find((c) => c.id === id)!
  return { ...s, state, differs: [], unblocked_by: [], toggle: null }
}

const view: BlockedView = {
  child: { id: 1, name: 'Ada' },
  clients: [],
  services: [row('youtube', 'blocked'), row('tiktok', 'partial'), row('netflix', 'unblocked'), row('custom:1', 'unblocked')],
}

function setup(onpick: (s: Service) => void | Promise<void> = () => {}) {
  const r = render(BlockPicker, { catalogue, view, onpick })
  return r
}

async function openPicker() {
  await fireEvent.click(screen.getByRole('button', { name: 'Block a service…' }))
}

function optionIds(container: HTMLElement): string[] {
  return Array.from(container.querySelectorAll<HTMLButtonElement>('button[data-service]')).map((b) => b.dataset.service!)
}

async function search(q: string) {
  await fireEvent.input(screen.getByRole('searchbox', { name: 'Search services to block' }), { target: { value: q } })
}

describe('BlockPicker', () => {
  it('closed until the button is pressed', async () => {
    const { container } = setup()
    expect(optionIds(container)).toEqual([])
    await openPicker()
    expect(optionIds(container).length).toBeGreaterThan(0)
  })

  it('excludes blocked', async () => {
    const { container } = setup()
    await openPicker()
    expect(optionIds(container)).toEqual(['tiktok', 'netflix', 'custom:1'])
    expect(container.querySelector('button[data-service="youtube"]')).toBeNull()
  })

  it('search', async () => {
    const { container } = setup()
    await openPicker()
    await search('TIK')
    expect(optionIds(container)).toEqual(['tiktok'])
    await search('por')
    expect(optionIds(container)).toEqual(['custom:1'])
    await search('zzz')
    expect(optionIds(container)).toEqual([])
    expect(screen.getByRole('status').textContent).toMatch(/no service matches/i)
  })

  it('pick once', async () => {
    let resolve!: () => void
    const onpick = vi.fn(() => new Promise<void>((r) => (resolve = r)))
    const { container } = setup(onpick)
    await openPicker()
    const tiktok = container.querySelector<HTMLButtonElement>('button[data-service="tiktok"]')!
    await fireEvent.click(tiktok)
    await fireEvent.click(tiktok)
    expect(onpick).toHaveBeenCalledTimes(1)
    expect(onpick).toHaveBeenCalledWith(expect.objectContaining({ id: 'tiktok' }))
    for (const b of container.querySelectorAll<HTMLButtonElement>('button[data-service]')) {
      expect(b.disabled).toBe(true)
    }
    resolve()
    await vi.waitFor(() => expect(optionIds(container)).toEqual([]))
    expect(screen.getByRole('button', { name: 'Block a service…' }).getAttribute('aria-expanded')).toBe('false')
  })

  it('stays open for a retry when the pick fails', async () => {
    const onpick = vi.fn(() => Promise.reject(new Error('502')))
    const { container } = setup(onpick)
    await openPicker()
    await fireEvent.click(container.querySelector<HTMLButtonElement>('button[data-service="netflix"]')!)
    await vi.waitFor(() =>
      expect(container.querySelector<HTMLButtonElement>('button[data-service="netflix"]')?.disabled).toBe(false),
    )
    expect(optionIds(container)).toEqual(['tiktok', 'netflix', 'custom:1'])
  })

  it('custom avatar', async () => {
    const portalOnly: Service = { id: 'custom:2', name: 'Portal', icon: '', custom: true }
    const { container } = render(BlockPicker, { catalogue: [...catalogue, portalOnly], view, onpick: () => {} })
    await openPicker()
    const portal = container.querySelector('button[data-service="custom:2"]')!
    expect(portal.querySelector('[data-letter-avatar]')?.textContent).toBe('P')
    expect(portal.querySelector('img')).toBeNull()
    expect(container.querySelector('button[data-service="tiktok"] img')).not.toBeNull()
  })
})
