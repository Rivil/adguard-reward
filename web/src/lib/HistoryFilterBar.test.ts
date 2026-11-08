// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen } from '@testing-library/svelte'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
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

/** How long a typed date waits before it goes out. */
const SETTLE_MS = 700
const typeDay = (label: string, value: string) => fireEvent.change(date(label), { target: { value } })
const settled = () => vi.advanceTimersByTime(SETTLE_MS)

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

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
    settled()
    expect(onChange).not.toHaveBeenCalled()

    await typeDay('From', '2026-03-01')
    settled()
    expect(onChange).toHaveBeenCalledExactlyOnceWith({ child: null, filters: { ...NONE, from: '2026-03-01' } })
  })

  it('emits the to date under its own key', async () => {
    const { onChange } = bar()
    await typeDay('To', '2026-03-31')
    settled()
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
    for (const value of ['0002-11-05', '0020-11-05', '0202-11-05']) await typeDay('From', value)
    settled()
    expect(onChange).not.toHaveBeenCalled()

    await typeDay('From', '2026-11-05')
    settled()
    expect(onChange).toHaveBeenCalledExactlyOnceWith({ child: null, filters: { ...NONE, from: '2026-11-05' } })
  })

  describe('while a typed date settles', () => {
    const MARCH = { ...NONE, from: '2026-03-15' }

    it('emits once, with the last date, for two changes inside the delay', async () => {
      // Recorded from Chromium 156: retyping the day of the 15th as 25 passes through the 2nd.
      const { onChange } = bar({ filters: MARCH })
      await typeDay('From', '2026-03-02')
      vi.advanceTimersByTime(SETTLE_MS - 1)
      await typeDay('From', '2026-03-25')
      vi.advanceTimersByTime(SETTLE_MS - 1)
      expect(onChange).not.toHaveBeenCalled()

      vi.advanceTimersByTime(1)
      expect(onChange).toHaveBeenCalledExactlyOnceWith({ child: null, filters: { ...NONE, from: '2026-03-25' } })
    })

    it('fires by itself for a picker that changes the date and keeps focus', async () => {
      const { onChange } = bar()
      await typeDay('To', '2026-03-25')
      vi.advanceTimersByTime(SETTLE_MS - 1)
      expect(onChange).not.toHaveBeenCalled()

      vi.advanceTimersByTime(1)
      expect(onChange).toHaveBeenCalledExactlyOnceWith({ child: null, filters: { ...NONE, to: '2026-03-25' } })
    })

    it('reads the filters and child when it fires, not when it was armed', async () => {
      const { onChange, rerender } = bar()
      await typeDay('From', '2026-03-25')
      await rerender({ child: 3, filters: { ...NONE, status: 'expired' } })

      settled()
      expect(onChange).toHaveBeenCalledExactlyOnceWith({
        child: 3,
        filters: { ...NONE, status: 'expired', from: '2026-03-25' },
      })
    })

    it('goes out at once on blur, once, and the input keeps the new date', async () => {
      const { onChange } = bar({ filters: MARCH })
      await typeDay('From', '2026-03-25')
      await fireEvent.blur(date('From'))
      expect(onChange).toHaveBeenCalledExactlyOnceWith({ child: null, filters: { ...NONE, from: '2026-03-25' } })
      expect(date('From').value).toBe('2026-03-25')

      settled()
      expect(onChange).toHaveBeenCalledOnce()
    })

    it('stays pending when the other date loses focus', async () => {
      const { onChange } = bar({ filters: { ...MARCH, to: '2026-03-31' } })
      await typeDay('From', '2026-03-25')
      await typeDay('To', '0202-03-31')
      await fireEvent.blur(date('To'))
      expect(onChange).not.toHaveBeenCalled()
      expect(date('To').value).toBe('2026-03-31')
      expect(date('From').value).toBe('2026-03-25')

      settled()
      expect(onChange).toHaveBeenCalledExactlyOnceWith({
        child: null,
        filters: { ...NONE, from: '2026-03-25', to: '2026-03-31' },
      })
    })

    it.each([
      { label: 'Child', value: '3', child: 3, set: {} },
      { label: 'Service', value: 'youtube', child: null, set: { service: 'youtube' } },
      { label: 'How it ended', value: 'expired', child: null, set: { status: 'expired' } },
    ])('rides out with a $label change, and the timer adds no second emit', async ({ label, value, child, set }) => {
      const { onChange } = bar({ filters: MARCH })
      await typeDay('From', '2026-03-25')
      await fireEvent.change(select(label), { target: { value } })
      expect(onChange).toHaveBeenCalledExactlyOnceWith({ child, filters: { ...NONE, from: '2026-03-25', ...set } })

      settled()
      expect(onChange).toHaveBeenCalledOnce()
    })

    it('goes out before the other date starts to settle', async () => {
      const { onChange, rerender } = bar()
      await typeDay('From', '2026-03-25')
      await typeDay('To', '2026-03-31')
      const from = { ...NONE, from: '2026-03-25' }
      expect(onChange).toHaveBeenCalledExactlyOnceWith({ child: null, filters: from })
      await rerender({ filters: from })

      settled()
      expect(onChange).toHaveBeenCalledTimes(2)
      expect(onChange).toHaveBeenLastCalledWith({ child: null, filters: { ...from, to: '2026-03-31' } })
    })

    it('rides out with the other date being cleared', async () => {
      const { onChange } = bar({ filters: { ...NONE, to: '2026-03-31' } })
      await typeDay('From', '2026-03-25')
      await typeDay('To', '')
      expect(onChange).toHaveBeenCalledExactlyOnceWith({ child: null, filters: { ...NONE, from: '2026-03-25' } })

      settled()
      expect(onChange).toHaveBeenCalledOnce()
    })

    it('gives way to clearing, which emits null at once and nothing later', async () => {
      const { onChange } = bar({ filters: MARCH })
      await typeDay('From', '2026-03-25')
      await typeDay('From', '')
      expect(onChange).toHaveBeenCalledExactlyOnceWith({ child: null, filters: NONE })

      settled()
      expect(onChange).toHaveBeenCalledOnce()
    })

    it('is cancelled by a rejected date on the same input, which stays held back', async () => {
      const { onChange } = bar({ filters: MARCH })
      await typeDay('From', '2026-03-25')
      await typeDay('From', '0202-11-05')
      settled()
      expect(onChange).not.toHaveBeenCalled()

      await fireEvent.blur(date('From'))
      expect(onChange).not.toHaveBeenCalled()
      expect(date('From').value).toBe('2026-03-15')
    })

    it('outlives a rejected date on the other input', async () => {
      const { onChange } = bar()
      await typeDay('From', '2026-03-25')
      await typeDay('To', '0202-11-05')

      settled()
      expect(onChange).toHaveBeenCalledExactlyOnceWith({ child: null, filters: { ...NONE, from: '2026-03-25' } })
    })

    it('emits nothing once the bar is gone', async () => {
      const { onChange, unmount } = bar()
      await typeDay('From', '2026-03-25')
      unmount()

      settled()
      expect(onChange).not.toHaveBeenCalled()
    })

    it('is dropped by a popstate: nothing after the delay, nothing on a later blur', async () => {
      const { onChange } = bar({ filters: MARCH })
      await typeDay('From', '2026-03-25')
      window.dispatchEvent(new PopStateEvent('popstate'))

      settled()
      expect(onChange).not.toHaveBeenCalled()

      await fireEvent.blur(date('From'))
      expect(onChange).not.toHaveBeenCalled()
      expect(date('From').value).toBe('2026-03-15')
    })

    it.each([
      { label: 'From', filters: { ...NONE, from: '2026-03-15' }, shows: '2026-03-15' },
      { label: 'To', filters: { ...NONE, to: '2026-03-31' }, shows: '2026-03-31' },
      { label: 'From', filters: NONE, shows: '' },
      { label: 'To', filters: NONE, shows: '' },
    ])('leaves $label showing "$shows" after a popstate, before any blur', async ({ label, filters, shows }) => {
      bar({ filters })
      await typeDay(label, '2026-03-25')
      window.dispatchEvent(new PopStateEvent('popstate'))
      expect(date(label).value).toBe(shows)
    })

    it('shows the filters the parent passes in after the popstate, even the date that was pending', async () => {
      const { rerender } = bar({ filters: MARCH })
      await typeDay('From', '2026-03-25')
      window.dispatchEvent(new PopStateEvent('popstate'))
      await rerender({ filters: { ...NONE, from: '2026-03-25' } })
      expect(date('From').value).toBe('2026-03-25')
    })

    it('is dropped by the teardown behind a blur from removing the focused input', async () => {
      // Recorded from Chromium 156: the blur lands while the bar is being removed,
      // and Svelte runs teardown right after, in the same synchronous pass.
      const { onChange, unmount } = bar()
      await typeDay('From', '2026-03-25')
      date('From').dispatchEvent(new FocusEvent('blur'))
      unmount()

      await Promise.resolve()
      settled()
      expect(onChange).not.toHaveBeenCalled()
    })

    it('stops listening for popstate once the bar is gone', () => {
      const add = vi.spyOn(window, 'addEventListener')
      const remove = vi.spyOn(window, 'removeEventListener')
      const popstates = (spy: { mock: { calls: unknown[][] } }) => spy.mock.calls.filter(([type]) => type === 'popstate')
      const { unmount } = bar()
      expect(popstates(add)).toHaveLength(1)
      expect(popstates(remove)).toEqual([])

      unmount()
      expect(popstates(remove)).toEqual(popstates(add))
    })
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

  it('puts both dates back on the filter at a popstate when they were held back', async () => {
    const { onChange } = bar({ filters: { ...NONE, from: '2025-11-05', to: '2025-11-30' } })
    await typeDay('From', '0202-11-05')
    await typeDay('To', '0202-11-30')
    window.dispatchEvent(new PopStateEvent('popstate'))

    expect(date('From').value).toBe('2025-11-05')
    expect(date('To').value).toBe('2025-11-30')
    expect(onChange).not.toHaveBeenCalled()
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
