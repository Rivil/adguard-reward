<script lang="ts">
  import { onMount } from 'svelte'
  import {
    ApiError,
    listButtons,
    listChildren,
    listServices,
    messageFor,
    saveButtons,
    type Button,
    type ButtonInput,
    type Child,
    type Service,
  } from './api'
  import GrantForm, { type GrantFormValue } from './GrantForm.svelte'
  import { formatDuration, minutesToSeconds, secondsToMinutes } from './grants'

  let { onBack }: { onBack: () => void } = $props()

  let buttons = $state<Button[]>([])
  let children = $state<Child[]>([])
  // null: the catalogue could not be loaded; the page stays editable and a
  // stored button can still be saved as-is (GrantForm handles the note).
  let services = $state<Service[] | null>(null)
  let pending = $state(false)
  let error = $state<{ code: string; text: string } | null>(null)

  // The one form: a new button, or the row at `editing` being replaced in place.
  let label = $state('')
  let value = $state<GrantFormValue>({ child_id: null, services: [], minutes: 60 })
  let valid = $state(false)
  let editing = $state<number | null>(null)

  const canSave = $derived(!pending && label.trim() !== '' && valid)

  async function load() {
    const [b, c] = await Promise.all([listButtons(), listChildren()])
    buttons = b
    children = c
    try {
      services = await listServices()
    } catch {
      services = null
    }
  }

  onMount(() => {
    void run(load)
  })

  /** Run a call; any failure becomes the alert and the list reloads from the server. The draft is kept. */
  async function run(fn: () => Promise<void>, reloadOnError = true): Promise<boolean> {
    pending = true
    error = null
    try {
      await fn()
      return true
    } catch (err) {
      error =
        err instanceof ApiError
          ? { code: err.code, text: messageFor(err) }
          : { code: 'network', text: "Can't reach the server — check your connection" }
      if (reloadOnError) {
        try {
          await load()
        } catch {
          // The alert already says the server is unreachable.
        }
      }
      return false
    } finally {
      pending = false
    }
  }

  function toInput(b: Button): ButtonInput {
    return { label: b.label, child_id: b.child_id, services: b.services, duration: b.duration }
  }

  function draft(): ButtonInput {
    return {
      label: label.trim(),
      child_id: value.child_id ?? 0,
      services: value.services,
      duration: minutesToSeconds(value.minutes),
    }
  }

  function childName(id: number): string {
    return children.find((c) => c.id === id)?.name ?? `child ${id}`
  }

  function serviceName(id: string): string {
    return services?.find((s) => s.id === id)?.name ?? id
  }

  /** PUT the whole list; the response is server truth (fresh ids). */
  async function put(next: ButtonInput[]): Promise<boolean> {
    return run(async () => {
      buttons = await saveButtons(next)
    })
  }

  function resetForm() {
    editing = null
    label = ''
    value = { child_id: null, services: [], minutes: 60 }
  }

  async function save(e: SubmitEvent) {
    e.preventDefault()
    const next = buttons.map(toInput)
    if (editing === null) next.push(draft())
    else next[editing] = draft()
    if (await put(next)) resetForm()
  }

  function edit(i: number) {
    const b = buttons[i]
    editing = i
    label = b.label
    value = { child_id: b.child_id, services: [...b.services], minutes: secondsToMinutes(b.duration) }
  }

  function del(i: number) {
    void put(buttons.filter((_, j) => j !== i).map(toInput))
  }
</script>

<main class="buttons">
  <h1>Buttons</h1>
  <button type="button" onclick={onBack}>Back</button>

  {#if error}
    <p class="error" role="alert" data-error={error.code}>{error.text}</p>
  {/if}

  {#if buttons.length === 0}
    <p class="none">No buttons yet — add one below and it shows on Home.</p>
  {:else}
    <ul class="list">
      {#each buttons as b, i (b.id)}
        <li data-button={b.id} class:editing={editing === i}>
          <div class="row">
            <strong>{b.label}</strong>
            <span class="meta">{childName(b.child_id)} · {b.services.map(serviceName).join(', ')} · {formatDuration(b.duration)}</span>
          </div>
          <div class="actions">
            <button type="button" onclick={() => edit(i)} disabled={pending}>Edit</button>
            <button type="button" onclick={() => del(i)} disabled={pending}>Delete</button>
          </div>
        </li>
      {/each}
    </ul>
  {/if}

  <form onsubmit={save} data-editing={editing}>
    <h2>{editing === null ? 'New button' : 'Edit button'}</h2>
    <label class="field">
      <span>Label</span>
      <input name="label" type="text" aria-label="Label" maxlength="64" bind:value={label} disabled={pending} />
    </label>
    <GrantForm {children} {services} bind:value bind:valid disabled={pending} />
    <div class="actions">
      <button type="submit" disabled={!canSave}>Save</button>
      {#if editing !== null}
        <button type="button" onclick={resetForm} disabled={pending}>Cancel</button>
      {/if}
    </div>
  </form>
</main>

<style>
  .buttons {
    max-width: 40rem;
    margin: 10vh auto;
    padding: 0 1rem;
  }
  .list {
    list-style: none;
    margin: 1rem 0;
    padding: 0;
    display: grid;
    gap: 0.5rem;
  }
  li {
    display: grid;
    gap: 0.5rem;
    padding: 0.75rem;
    border: 1px solid #ddd;
    border-radius: 0.5rem;
  }
  li.editing {
    border-color: var(--accent-border);
  }
  .row {
    display: grid;
  }
  .meta {
    opacity: 0.75;
    font-size: 0.9rem;
  }
  form {
    display: grid;
    gap: 0.75rem;
    margin-top: 1.5rem;
    padding: 1rem;
    border: 1px solid #ddd;
    border-radius: 0.5rem;
  }
  form h2 {
    margin: 0;
    font-size: 1.1rem;
  }
  .field {
    display: grid;
    gap: 0.25rem;
  }
  .field > span {
    font-size: 0.9rem;
    color: var(--text-h);
  }
  input[type='text'] {
    font: inherit;
    padding: 0.5rem;
    min-height: 2.75rem;
  }
  .actions {
    display: flex;
    gap: 0.5rem;
  }
  .actions button {
    min-height: 2.75rem;
  }
  .none {
    opacity: 0.75;
  }
  .error {
    color: #b00020;
  }
</style>
