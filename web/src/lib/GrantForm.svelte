<script module lang="ts">
  /** What the fieldset edits. Minutes here; the pages convert to seconds at the API edge. */
  export interface GrantFormValue {
    child_id: number | null
    /** Tick order, first one supplies the icon (locked button_icon). */
    services: string[]
    minutes: number
  }

  export const MAX_MINUTES = 1440
</script>

<script lang="ts">
  import { type Child, type Service } from './api'
  import { sharedDevices, sharedText } from './grants'
  import ServiceIcon from './ServiceIcon.svelte'

  let {
    children,
    services,
    value = $bindable({ child_id: null, services: [], minutes: 60 }),
    valid = $bindable(false),
    disabled = false,
  }: {
    children: Child[]
    /** null: the catalogue could not be loaded. */
    services: Service[] | null
    value?: GrantFormValue
    valid?: boolean
    disabled?: boolean
  } = $props()

  // Exactly one child: nothing to choose, so choose it.
  $effect(() => {
    if (value.child_id === null && children.length === 1) {
      value = { ...value, child_id: children[0].id }
    }
  })

  const isValid = $derived(
    value.child_id !== null &&
      value.services.length > 0 &&
      Number.isInteger(value.minutes) &&
      value.minutes >= 1 &&
      value.minutes <= MAX_MINUTES,
  )
  $effect(() => {
    valid = isValid
  })

  // The selected child's devices other children also hold (locked
  // shared_notice): a passive note only — no control, no effect on valid.
  const shared = $derived(value.child_id === null ? [] : sharedDevices(value.child_id, children))

  // Ids the stored value names that the catalogue does not: shown, never
  // dropped behind the parent's back.
  const unknown = $derived(services === null ? [] : value.services.filter((id) => !services.some((s) => s.id === id)))

  // Search narrows the checklist only; it never touches the bound value.
  let query = $state('')
  const needle = $derived(query.trim().toLowerCase())
  // Ticked rows always stay visible, so a filter can't hide what a save keeps.
  const shown = $derived(
    services === null
      ? []
      : needle === ''
        ? services
        : services.filter(
            (s) =>
              value.services.includes(s.id) || s.name.toLowerCase().includes(needle) || s.id.toLowerCase().includes(needle),
          ),
  )

  function pickChild(e: Event & { currentTarget: HTMLSelectElement }) {
    const raw = e.currentTarget.value
    value = { ...value, child_id: raw === '' ? null : Number(raw) }
  }

  function toggle(id: string, checked: boolean) {
    const next = checked
      ? value.services.includes(id)
        ? value.services
        : [...value.services, id]
      : value.services.filter((s) => s !== id)
    value = { ...value, services: next }
  }

  function setMinutes(e: Event & { currentTarget: HTMLInputElement }) {
    value = { ...value, minutes: e.currentTarget.valueAsNumber }
  }
</script>

<!-- data-value / data-valid mirror the bound state for tests and debugging. -->
<fieldset class="grant-form" data-value={JSON.stringify(value)} data-valid={valid} {disabled}>
  <label class="field">
    <span>Child</span>
    <select name="child" aria-label="Child" value={value.child_id === null ? '' : String(value.child_id)} onchange={pickChild} {disabled}>
      {#if value.child_id === null}
        <option value="">Choose a child…</option>
      {/if}
      {#each children as c (c.id)}
        <option value={String(c.id)}>{c.name}</option>
      {/each}
    </select>
  </label>
  {#if shared.length > 0}
    <p class="hint" role="note" data-shared-hint>Also unblocks on {sharedText(shared)}</p>
  {/if}

  <div class="field">
    <span>Services</span>
    {#if services === null}
      <p class="hint" data-unavailable>Service list unavailable — can't reach AdGuard Home</p>
      {#if value.services.length > 0}
        <p class="hint">Keeps: {value.services.join(', ')}</p>
      {/if}
    {:else}
      <div class="search">
        <input type="search" name="service-search" aria-label="Search services" placeholder="Search services…" autocomplete="off" bind:value={query} onkeydown={(e) => e.key === 'Enter' && e.preventDefault()} {disabled} />
        {#if query !== ''}
          <button type="button" aria-label="Clear search" onclick={() => (query = '')} {disabled}>✕</button>
        {/if}
      </div>
      {#if needle !== '' && shown.length === 0 && unknown.length === 0}
        <p class="hint" data-no-match>No services match “{query.trim()}”</p>
      {/if}
      <ul class="services">
        {#each shown as s (s.id)}
          <li>
            <label>
              <input
                type="checkbox"
                name="service"
                value={s.id}
                checked={value.services.includes(s.id)}
                onchange={(e) => toggle(s.id, e.currentTarget.checked)}
                {disabled}
              />
              <ServiceIcon name={s.name} icon={s.icon} size={20} />
              {s.name}
            </label>
          </li>
        {/each}
        {#each unknown as id (id)}
          <li class="missing">
            <label>
              <input type="checkbox" name="service" value={id} checked onchange={(e) => toggle(id, e.currentTarget.checked)} {disabled} />
              <s data-missing>{id}</s>
              <span class="hint">not in AdGuard Home</span>
            </label>
          </li>
        {/each}
      </ul>
    {/if}
  </div>

  <label class="field">
    <span>Minutes</span>
    <input
      type="number"
      name="minutes"
      inputmode="numeric"
      min="1"
      max={MAX_MINUTES}
      step="1"
      aria-label="Minutes"
      value={value.minutes}
      oninput={setMinutes}
      {disabled}
    />
  </label>
</fieldset>

<style>
  .grant-form {
    display: grid;
    gap: 0.75rem;
    border: 0;
    padding: 0;
    margin: 0;
    min-width: 0;
  }
  .field {
    display: grid;
    gap: 0.25rem;
  }
  .field > span {
    font-size: 0.9rem;
    color: var(--text-h);
  }
  select,
  input[type='number'] {
    font: inherit;
    padding: 0.5rem;
    min-height: 2.75rem;
  }
  .search {
    display: flex;
    gap: 0.25rem;
  }
  .search input {
    flex: 1;
    min-width: 0;
    font: inherit;
    padding: 0.5rem;
    min-height: 2.75rem;
  }
  .search button {
    min-width: 2.75rem;
    min-height: 2.75rem;
  }
  .services {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.25rem;
  }
  .services label {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    min-height: 2.75rem;
  }
  .services input {
    width: 1.25rem;
    height: 1.25rem;
  }
  .hint {
    margin: 0;
    opacity: 0.75;
    font-size: 0.9rem;
  }
</style>
