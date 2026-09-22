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
  import { iconUrl, type Child, type Service } from './api'

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

  // Ids the stored value names that the catalogue does not: shown, never
  // dropped behind the parent's back.
  const unknown = $derived(services === null ? [] : value.services.filter((id) => !services.some((s) => s.id === id)))

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

  <div class="field">
    <span>Services</span>
    {#if services === null}
      <p class="hint" data-unavailable>Service list unavailable — can't reach AdGuard Home</p>
      {#if value.services.length > 0}
        <p class="hint">Keeps: {value.services.join(', ')}</p>
      {/if}
    {:else}
      <ul class="services">
        {#each services as s (s.id)}
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
              <img alt="" src={iconUrl(s.icon)} width="20" height="20" />
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
