<script lang="ts">
  import type { Child, HistoryServiceOption, HistoryStatus } from './api'
  import { parseFilters, statusLabel, type HistoryFilters } from './history'

  // The overview's filter controls, one value each (locked filter_values). The
  // bar holds no state: every change goes out through onChange with the rest of
  // the selection intact, and the parent passes the new values back in.
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

  const STATUSES: HistoryStatus[] = ['ongoing', 'expired', 'ended_early', 'permanent_block', 'ended_unknown']

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

  function setFilter(next: Partial<HistoryFilters>) {
    onChange({ child, filters: { ...filters, ...next } })
  }

  /**
   * Chromium fires change on every keystroke of a date segment, so typing a
   * year emits 0002, 0020, 0202, then 2026. A value the URL would drop goes
   * nowhere: passing it on would clear the input while the parent is typing.
   */
  function setDay(key: 'from' | 'to', value: string) {
    if (value === '') setFilter({ [key]: null })
    else if (parseFilters(`${key}=${value}`)[key] !== null) setFilter({ [key]: value })
  }

  /** Leaving a date setDay held back puts the control back on the real filter. */
  function showDay(key: 'from' | 'to', input: HTMLInputElement) {
    input.value = filters[key] ?? ''
  }
</script>

<form class="filters" aria-label="Filter history" onsubmit={(e) => e.preventDefault()}>
  <label>
    Child
    <select
      value={child ?? ''}
      onchange={(e) => {
        const v = e.currentTarget.value
        onChange({ child: v === '' ? null : Number(v), filters })
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
      onchange={(e) => setFilter({ status: (e.currentTarget.value || null) as HistoryStatus | null })}
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
      onblur={(e) => showDay('from', e.currentTarget)}
    />
  </label>
  <label>
    To
    <input
      type="date"
      value={filters.to ?? ''}
      onchange={(e) => setDay('to', e.currentTarget.value)}
      onblur={(e) => showDay('to', e.currentTarget)}
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
