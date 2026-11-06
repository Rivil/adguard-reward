// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen } from '@testing-library/svelte'
import { afterEach, describe, expect, it, vi } from 'vitest'
import HistoryFilterBar from './HistoryFilterBar.svelte'
import type { HistoryFilters } from './history'

const NONE: HistoryFilters = { service: null, status: null, from: null, to: null }
const CHILDREN = [
  { id: 1, name: 'Ada' },
  { id: 3, name: 'Cleo' },
]
const SERVICES = [
  { id: 'youtube', name: 'YouTube' },
  { id: 'custom:5', name: 'Games' },
]

function bar(props: Partial<{ child: number | null; filters: HistoryFilters; children: typeof CHILDREN; services: typeof SERVICES }> = {}) {
  const onChange = vi.fn()
  const view = render(HistoryFilterBar, {
    child: null,
    filters: NONE,
    children: CHILDREN,
    services: SERVICES,
    onChange,
    ...props,
  })
  return { onChange, ...view }
}

const select = (label: string) => screen.getByLabelText(label) as HTMLSelectElement
const date = (label: string) => screen.getByLabelText(label) as HTMLInputElement
const options = (el: HTMLSelectElement) => [...el.options].map((o) => o.textContent)
const shown = (el: HTMLSelectElement) => el.selectedOptions[0]?.textContent

afterEach(() => vi.restoreAllMocks())

describe('HistoryFilterBar', () => {
  it('offers All and every end status, emitting the status with other filters unchanged', async () => {
    const filters = { ...NONE, service: 'youtube' }
    const { onChange } = bar({ child: 3, filters })
    expect(options(select('How it ended'))).toEqual([
      'All',
      'Ongoing',
      'Expired',
      'Ended early',
      'Ended by permanent block',
      'Ended (reason unknown)',
    ])

    await fireEvent.change(select('How it ended'), { target: { value: 'ended_unknown' } })
    expect(onChange).toHaveBeenCalledExactlyOnceWith({ child: 3, filters: { ...filters, status: 'ended_unknown' } })
  })

  it('lists a filtered service the options lack, showing its bare id', () => {
    bar({ filters: { ...NONE, service: 'custom:9' } })
    expect(select('Service').value).toBe('custom:9')
    expect(shown(select('Service'))).toBe('custom:9')
    expect(options(select('Service'))).toEqual(['All', 'YouTube', 'Games', 'custom:9'])
  })

  it('still shows the filter with no options at all', () => {
    bar({ child: 3, filters: { ...NONE, service: 'custom:9' }, children: [], services: [] })
    expect(select('Service').value).toBe('custom:9')
    expect(select('Child').value).toBe('3')
    expect(shown(select('Child'))).toBe('Child 3')
  })

  it('lists a filtered child the options lack, beside the children they have', () => {
    bar({ child: 9 })
    expect(select('Child').value).toBe('9')
    expect(shown(select('Child'))).toBe('Child 9')
    expect(options(select('Child'))).toEqual(['All', 'Ada', 'Cleo', 'Child 9'])
  })

  it('swaps the stand-in for the real name when the options arrive', async () => {
    const { rerender } = bar({ child: 3, filters: { ...NONE, service: 'custom:5' }, children: [], services: [] })
    await rerender({ children: CHILDREN, services: SERVICES })
    expect(shown(select('Child'))).toBe('Cleo')
    expect(shown(select('Service'))).toBe('Games')
    expect(options(select('Child'))).toEqual(['All', 'Ada', 'Cleo'])
  })

  it('selects each current value', () => {
    bar({ child: 1, filters: { service: 'custom:5', status: 'expired', from: '2026-03-01', to: '2026-03-31' } })
    expect(shown(select('Child'))).toBe('Ada')
    expect(shown(select('Service'))).toBe('Games')
    expect(shown(select('How it ended'))).toBe('Expired')
    expect(date('From').value).toBe('2026-03-01')
    expect(date('To').value).toBe('2026-03-31')
  })

  it('keeps two services sharing a name apart, emitting ids', async () => {
    const { onChange } = bar({
      services: [
        { id: 'custom:1', name: 'Games' },
        { id: 'custom:2', name: 'Games' },
      ],
    })
    expect(options(select('Service'))).toEqual(['All', 'Games', 'Games'])

    await fireEvent.change(select('Service'), { target: { value: 'custom:2' } })
    expect(onChange).toHaveBeenCalledExactlyOnceWith({ child: null, filters: { ...NONE, service: 'custom:2' } })
  })

  it('picks one value per control', () => {
    bar()
    for (const label of ['Child', 'Service', 'How it ended']) expect(select(label).multiple).toBe(false)
  })

  it('keeps the rest of the selection when one control changes', async () => {
    const filters = { ...NONE, from: '2026-03-01' }
    const { onChange } = bar({ child: 3, filters })

    await fireEvent.change(select('How it ended'), { target: { value: 'expired' } })
    expect(onChange).toHaveBeenCalledExactlyOnceWith({ child: 3, filters: { ...filters, status: 'expired' } })
  })

  it('emits a date once on change, never per input event', async () => {
    const { onChange } = bar()
    await fireEvent.input(date('From'), { target: { value: '2026-03-01' } })
    expect(onChange).not.toHaveBeenCalled()

    await fireEvent.change(date('From'), { target: { value: '2026-03-01' } })
    expect(onChange).toHaveBeenCalledExactlyOnceWith({ child: null, filters: { ...NONE, from: '2026-03-01' } })
  })

  it('emits the to date under its own key', async () => {
    const { onChange } = bar()
    await fireEvent.change(date('To'), { target: { value: '2026-03-31' } })
    expect(onChange).toHaveBeenCalledExactlyOnceWith({ child: null, filters: { ...NONE, to: '2026-03-31' } })
  })

  it('clears a date to null, never an empty string', async () => {
    const { onChange } = bar({ filters: { ...NONE, from: '2026-03-01', to: '2026-03-31' } })
    await fireEvent.change(date('From'), { target: { value: '' } })
    expect(onChange).toHaveBeenCalledExactlyOnceWith({ child: null, filters: { ...NONE, to: '2026-03-31' } })
  })

  it('emits only the finished year of a date typed in Chromium', async () => {
    // Recorded from Chromium 156: change fires on each key of the year segment.
    const { onChange } = bar({ filters: { ...NONE, from: '2025-11-05' } })
    for (const value of ['0002-11-05', '0020-11-05', '0202-11-05']) {
      await fireEvent.change(date('From'), { target: { value } })
    }
    expect(onChange).not.toHaveBeenCalled()

    await fireEvent.change(date('From'), { target: { value: '2026-11-05' } })
    expect(onChange).toHaveBeenCalledExactlyOnceWith({ child: null, filters: { ...NONE, from: '2026-11-05' } })
  })

  it('puts a date back on the filter when the parent leaves a half-typed year', async () => {
    const { onChange } = bar({ filters: { ...NONE, from: '2025-11-05', to: '2025-11-30' } })
    await fireEvent.change(date('From'), { target: { value: '0202-11-05' } })
    await fireEvent.blur(date('From'))
    expect(date('From').value).toBe('2025-11-05')

    await fireEvent.change(date('To'), { target: { value: '0202-11-30' } })
    await fireEvent.blur(date('To'))
    expect(date('To').value).toBe('2025-11-30')
    expect(onChange).not.toHaveBeenCalled()
  })

  it('empties a date with no filter behind it when the parent leaves a half-typed year', async () => {
    bar()
    await fireEvent.change(date('From'), { target: { value: '0202-11-05' } })
    await fireEvent.blur(date('From'))
    expect(date('From').value).toBe('')
  })

  it('leaves the other filters alone when choosing All children', async () => {
    const filters = { ...NONE, status: 'expired' as const }
    const { onChange } = bar({ child: 3, filters })
    await fireEvent.change(select('Child'), { target: { value: '' } })
    expect(onChange).toHaveBeenCalledExactlyOnceWith({ child: null, filters })
  })

  it('emits a chosen child as a number', async () => {
    const { onChange } = bar()
    await fireEvent.change(select('Child'), { target: { value: '3' } })
    expect(onChange).toHaveBeenCalledExactlyOnceWith({ child: 3, filters: NONE })
  })

  it('clears the service and status filters to null', async () => {
    const filters = { ...NONE, service: 'youtube', status: 'expired' as const }
    const { onChange } = bar({ filters })
    await fireEvent.change(select('Service'), { target: { value: '' } })
    await fireEvent.change(select('How it ended'), { target: { value: '' } })
    expect(onChange.mock.calls).toEqual([
      [{ child: null, filters: { ...filters, service: null } }],
      [{ child: null, filters: { ...filters, status: null } }],
    ])
  })

  it('renders every control with All when there are no options', () => {
    bar({ children: [], services: [] })
    expect(options(select('Child'))).toEqual(['All'])
    expect(options(select('Service'))).toEqual(['All'])
    for (const label of ['Child', 'Service', 'How it ended']) expect(shown(select(label))).toBe('All')
    expect(date('From').value).toBe('')
    expect(date('To').value).toBe('')
  })

  it('never lets a submit reload the page', () => {
    bar()
    const submit = new Event('submit', { bubbles: true, cancelable: true })
    screen.getByRole('form', { name: 'Filter history' }).dispatchEvent(submit)
    expect(submit.defaultPrevented).toBe(true)
  })
})
