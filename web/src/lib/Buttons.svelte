<script lang="ts">
  import { onMount } from 'svelte'
  import {
    ApiError,
    iconUrl,
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
  // The form's icon: "" is Default (the first service's), else a catalogue id.
  let icon = $state('')
  // A stored icon the catalogue no longer has, which the form shows as
  // Default; null when there is nothing to explain.
  let formLost = $state<string | null>(null)
  // The one row asking "Delete <label>?", by full-list index like `editing`.
  let confirming = $state<number | null>(null)

  const canSave = $derived(!pending && label.trim() !== '' && valid)

  type Row = { b: Button; i: number }
  /**
   * One group per child, in child order like Home; a button whose child is
   * unknown still gets a group after them so it can be edited or deleted.
   * Each row keeps `i`, its index in the full list, which is what a PUT orders by.
   */
  const groups = $derived.by(() => {
    const ids = children.map((c) => c.id)
    for (const b of buttons) if (!ids.includes(b.child_id)) ids.push(b.child_id)
    const rows: Row[] = buttons.map((b, i) => ({ b, i }))
    return ids
      .map((child_id) => ({ child_id, rows: rows.filter((r) => r.b.child_id === child_id) }))
      .filter((g) => g.rows.length > 0)
  })

  async function load() {
    const [b, c] = await Promise.all([listButtons(), listChildren()])
    // A reloaded list may be in any order, so a delete question asked by
    // index no longer names a known button.
    confirming = null
    buttons = b
    children = c
    try {
      services = await listServices()
    } catch {
      services = null
    }
    // A catalogue that arrives without the form's icon would make Save a 422:
    // fall back to Default, and say so.
    const lost = lostIcon(icon)
    if (lost !== null) {
      formLost = lost
      icon = ''
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

  /**
   * `id` when the loaded catalogue lacks it, else null. With the catalogue
   * unreachable nothing counts as lost: a stored icon is kept as-is
   * (locked icon_fallback).
   */
  function lostIcon(id: string | undefined): string | null {
    if (services === null || !id) return null
    return services.some((s) => s.id === id) ? null : id
  }

  /** A stored button as a PUT sends it; a lost icon goes as "" so the list stays saveable. */
  function toInput(b: Button): ButtonInput {
    const kept = lostIcon(b.icon) === null ? (b.icon ?? '') : ''
    return { label: b.label, child_id: b.child_id, services: b.services, duration: b.duration, icon: kept }
  }

  function draft(): ButtonInput {
    return {
      label: label.trim(),
      child_id: value.child_id ?? 0,
      services: value.services,
      duration: minutesToSeconds(value.minutes),
      icon,
    }
  }

  function childName(id: number): string {
    return children.find((c) => c.id === id)?.name ?? `child ${id}`
  }

  function serviceName(id: string): string {
    return services?.find((s) => s.id === id)?.name ?? id
  }

  /**
   * PUT the whole list; the response is server truth (fresh ids). `onSaved`
   * re-points index state in the same step the new list lands, so no render
   * shows it on the wrong row.
   */
  async function put(next: ButtonInput[], onSaved?: () => void): Promise<boolean> {
    return run(async () => {
      buttons = await saveButtons(next)
      onSaved?.()
    })
  }

  function resetForm() {
    editing = null
    label = ''
    value = { child_id: null, services: [], minutes: 60 }
    icon = ''
    formLost = null
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
    formLost = lostIcon(b.icon)
    icon = formLost === null ? (b.icon ?? '') : ''
  }

  /** What the preview shows: the chosen icon, or for Default the first service's. */
  const preview = $derived.by(() => {
    const id = icon !== '' ? icon : value.services[0]
    return services?.find((s) => s.id === id) ?? null
  })

  /**
   * Swap the row at full-list index `i` with the row at `j` (its same-child
   * neighbour) and PUT. The open edit and an open delete confirmation are
   * indices too, so they follow the button they were on — but only once the
   * server has taken the new order.
   */
  async function move(i: number, j: number) {
    const next = buttons.map(toInput)
    ;[next[i], next[j]] = [next[j], next[i]]
    const swap = (at: number | null) => (at === i ? j : at === j ? i : at)
    await put(next, () => {
      editing = swap(editing)
      confirming = swap(confirming)
    })
  }

  /** Confirm the delete asked on row `i`: PUT the list without it. */
  async function del(i: number) {
    await put(
      buttons.filter((_, j) => j !== i).map(toInput),
      () => {
        confirming = null
        // Rows after the deleted one shift up a place; the open edit goes with them.
        if (editing === i) resetForm()
        else if (editing !== null && editing > i) editing -= 1
      },
    )
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
    {#each groups as g (g.child_id)}
      <section class="group" data-child={g.child_id}>
        <h2>{childName(g.child_id)}</h2>
        <ul class="list">
          {#each g.rows as { b, i }, k (b.id)}
            {@const up = k > 0 ? g.rows[k - 1].i : null}
            {@const down = k < g.rows.length - 1 ? g.rows[k + 1].i : null}
            <li data-button={b.id} class:editing={editing === i}>
              <div class="row">
                <strong>{b.label}</strong>
                <span class="meta">{childName(b.child_id)} · {b.services.map(serviceName).join(', ')} · {formatDuration(b.duration)}</span>
                {#if lostIcon(b.icon) !== null}
                  <span class="hint" data-lost-icon>Icon “{b.icon}” is no longer available — using the default</span>
                {/if}
              </div>
              {#if confirming === i}
                <div class="actions confirm" role="group" aria-label={`Delete ${b.label}?`}>
                  <span>Delete {b.label}?</span>
                  <button type="button" class="danger" onclick={() => del(i)} disabled={pending}>Confirm</button>
                  <button type="button" onclick={() => (confirming = null)} disabled={pending}>Cancel</button>
                </div>
              {:else}
                <div class="actions">
                  <button type="button" aria-label={`Move ${b.label} up`} onclick={() => up !== null && move(i, up)} disabled={pending || up === null}>
                    Up
                  </button>
                  <button type="button" aria-label={`Move ${b.label} down`} onclick={() => down !== null && move(i, down)} disabled={pending || down === null}>
                    Down
                  </button>
                  <button type="button" onclick={() => edit(i)} disabled={pending}>Edit</button>
                  <button type="button" onclick={() => (confirming = i)} disabled={pending}>Delete</button>
                </div>
              {/if}
            </li>
          {/each}
        </ul>
      </section>
    {/each}
  {/if}

  <form onsubmit={save} data-editing={editing}>
    <h2>{editing === null ? 'New button' : 'Edit button'}</h2>
    <label class="field">
      <span>Label</span>
      <input name="label" type="text" aria-label="Label" maxlength="64" bind:value={label} disabled={pending} />
    </label>
    <GrantForm {children} {services} bind:value bind:valid disabled={pending} />
    <div class="field">
      <label for="button-icon">Icon</label>
      <div class="icon">
        <select id="button-icon" name="icon" bind:value={icon} disabled={pending || services === null}>
          <option value="">Default (first service)</option>
          {#if services === null}
            <!-- Catalogue unreachable: the stored choice is kept, shown by id. -->
            {#if icon !== ''}
              <option value={icon}>{icon}</option>
            {/if}
          {:else}
            {#each services as s (s.id)}
              <option value={s.id}>{s.name}</option>
            {/each}
          {/if}
        </select>
        {#if preview}
          <img alt="" src={iconUrl(preview.icon)} width="24" height="24" data-icon-preview={preview.id} />
        {/if}
      </div>
      {#if formLost !== null && icon === ''}
        <p class="hint" data-lost-icon>Icon “{formLost}” is no longer available — using the default</p>
      {/if}
    </div>
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
  .group h2 {
    margin: 1.25rem 0 0;
    font-size: 1.1rem;
  }
  .list {
    list-style: none;
    margin: 0.5rem 0 1rem;
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
  .field > span,
  .field > label {
    font-size: 0.9rem;
    color: var(--text-h);
  }
  .icon {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .icon select {
    flex: 1;
    min-width: 0;
    font: inherit;
    padding: 0.5rem;
    min-height: 2.75rem;
  }
  .hint {
    margin: 0;
    opacity: 0.75;
    font-size: 0.9rem;
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
  .confirm {
    align-items: center;
    flex-wrap: wrap;
  }
  .confirm .danger {
    color: #b00020;
  }
  .none {
    opacity: 0.75;
  }
  .error {
    color: #b00020;
  }
</style>
