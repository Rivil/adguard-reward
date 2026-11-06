<script lang="ts">
  import { onDestroy, onMount } from 'svelte'
  import type { Child, HistoryServiceOption, HistoryStatus } from './api'
  import { parseFilters, statusLabel, type HistoryFilters } from './history'

  // The overview's filter controls, one value each (locked filter_values). The
  // bar holds no filters of its own: every change goes out through onChange with
  // the rest of the selection intact, and the parent passes the new values back
  // in. Only a date waiting out its settle delay stays here until then.
  let {
    child,
    filters,
    children,
    services,
    onChange,
  }: {
    child: number | null
    filters: HistoryFilters
    children: Pick<Child, 'id' | 'name'>[]
    services: HistoryServiceOption[]
    onChange: (next: { child: number | null; filters: HistoryFilters }) => void
  } = $props()

  type DayKey = 'from' | 'to'

  const STATUSES: HistoryStatus[] = ['ongoing', 'expired', 'ended_early', 'permanent_block', 'ended_unknown']

  /** Long enough for the next key of a date segment, short enough to feel like a picker. */
  const SETTLE_MS = 700

  /** The date waiting out SETTLE_MS, not yet on the filter. */
  let pending: { key: DayKey; value: string } | null = null
  let timer: ReturnType<typeof setTimeout> | undefined

  // A filter can name something the options do not, while they load or after
  // they fail. It stays listed so the control never reads All with a filter on.
  const childOptions = $derived(
    child === null || children.some((c) => c.id === child)
      ? children
      : [...children, { id: child, name: `Child ${child}` }],
  )
  const serviceOptions = $derived(
    filters.service === null || services.some((s) => s.id === filters.service)
      ? services
      : [...services, { id: filters.service, name: filters.service }],
  )

  /** Every emit goes through here, so none leaves a pending date behind. next wins over it. */
  function emit(nextChild: number | null, next: Partial<HistoryFilters> = {}) {
    const held = pending === null ? {} : { [pending.key]: pending.value }
    dropDay()
    onChange({ child: nextChild, filters: { ...filters, ...held, ...next } })
  }

  function setFilter(next: Partial<HistoryFilters>) {
    emit(child, next)
  }

  /** Reads child and filters when it runs, not when the delay was armed. */
  function flushDay() {
    emit(child)
  }

  function dropDay() {
    clearTimeout(timer)
    pending = null
  }

  function holdDay(key: DayKey, value: string) {
    if (pending !== null && pending.key !== key) flushDay()
    clearTimeout(timer)
    pending = { key, value }
    timer = setTimeout(flushDay, SETTLE_MS)
  }

  function setStatus(value: string) {
    setFilter({ status: (value || null) as HistoryStatus | null })
  }

  /**
   * Chromium fires change on every keystroke of a date segment, so typing a
   * year emits 0002, 0020, 0202, then 2026, and retyping the 15th as the 25th
   * passes through the 2nd. A valid date therefore waits out SETTLE_MS, and the
   * delay fires by itself because Android's picker fires change and keeps focus.
   * A value the URL would drop goes nowhere: passing it on would clear the input
   * while the parent is typing, and the date before it was only a step to it.
   */
  function setDay(key: DayKey, value: string) {
    if (value === '') setFilter({ [key]: null })
    else if (parseFilters(`${key}=${value}`)[key] !== null) holdDay(key, value)
    else if (pending?.key === key) dropDay()
  }

  /**
   * Leaving a date sends one still pending on; one setDay held back returns to
   * the filter. The send waits a microtask because Chromium also fires blur on a
   * focused input it removes, and Svelte removes the DOM before its teardown in
   * one synchronous pass: by the microtask onDestroy has dropped the date, so a
   * browser Back that unmounts the bar is not undone by a push.
   */
  function leaveDay(key: DayKey, input: HTMLInputElement) {
    if (pending?.key === key) {
      queueMicrotask(() => {
        if (pending?.key === key) flushDay()
      })
    } else input.value = filters[key] ?? ''
  }

  // A Back or Forward that keeps the bar drops a pending date unsent: the timer
  // would push it over the filters the URL just restored. This runs after the
  // route listener in api.ts, which is added first, so it cannot cover a Back
  // that unmounts the bar; leaveDay and onDestroy cover that.
  onMount(() => {
    window.addEventListener('popstate', dropDay)
    return () => window.removeEventListener('popstate', dropDay)
  })
  onDestroy(dropDay)
</script>

<form class="filters" aria-label="Filter history" onsubmit={(e) => e.preventDefault()}>
  <label>
    Child
    <select
      value={child ?? ''}
      onchange={(e) => {
        const v = e.currentTarget.value
        emit(v === '' ? null : Number(v))
      }}
    >
      <option value="">All</option>
      {#each childOptions as c (c.id)}
        <option value={c.id}>{c.name}</option>
      {/each}
    </select>
  </label>
  <label>
    Service
    <select value={filters.service ?? ''} onchange={(e) => setFilter({ service: e.currentTarget.value || null })}>
      <option value="">All</option>
      {#each serviceOptions as s (s.id)}
        <option value={s.id}>{s.name}</option>
      {/each}
    </select>
  </label>
  <label>
    How it ended
    <select
      value={filters.status ?? ''}
      onchange={(e) => setStatus(e.currentTarget.value)}
    >
      <option value="">All</option>
      {#each STATUSES as s (s)}
        <option value={s}>{statusLabel(s)}</option>
      {/each}
    </select>
  </label>
  <label>
    From
    <input
      type="date"
      value={filters.from ?? ''}
      onchange={(e) => setDay('from', e.currentTarget.value)}
      onblur={(e) => leaveDay('from', e.currentTarget)}
    />
  </label>
  <label>
    To
    <input
      type="date"
      value={filters.to ?? ''}
      onchange={(e) => setDay('to', e.currentTarget.value)}
      onblur={(e) => leaveDay('to', e.currentTarget)}
    />
  </label>
</form>

<style>
  .filters {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(9rem, 1fr));
    gap: 0.5rem;
    margin: 0.75rem 0;
  }
  label {
    display: grid;
    gap: 0.25rem;
    font-size: 0.875rem;
  }
  select,
  input {
    width: 100%;
    min-width: 0;
    box-sizing: border-box;
  }
</style>
