<script lang="ts">
  import { ApiError, messageFor, renameClient, type ClientView } from './api'

  let {
    clients,
    disabled = false,
    onRenamed,
  }: {
    /** Every persistent client the server lists, assigned or not (locked: rename_scope). */
    clients: ClientView[]
    disabled?: boolean
    /** Called after a successful rename so the page reloads from the server. */
    onRenamed: () => void | Promise<void>
  } = $props()

  let edits = $state<Record<string, string>>({})
  let errors = $state<Record<string, string>>({})
  let pending = $state(false)

  const draftOf = (c: ClientView) => edits[c.name] ?? c.name
  const changed = (c: ClientView) => draftOf(c).trim() !== '' && draftOf(c).trim() !== c.name

  async function save(e: SubmitEvent, c: ClientView) {
    e.preventDefault()
    if (pending || !changed(c)) return
    pending = true
    delete errors[c.name]
    try {
      await renameClient(c.name, draftOf(c).trim())
      delete edits[c.name]
      await onRenamed()
    } catch (err) {
      errors[c.name] =
        err instanceof ApiError ? messageFor(err) : "Can't reach the server — check your connection"
    } finally {
      pending = false
    }
  }
</script>

<section class="names">
  <h2>Devices</h2>
  {#each clients as c (c.name)}
    <form data-client={c.name} onsubmit={(e) => save(e, c)}>
      <label>
        <span class="current">{c.name}{#if c.children?.length}<span class="owner">{` — ${c.children.map((o) => o.name).join(', ')}`}</span>{/if}</span>
        <input
          aria-label="Rename {c.name}"
          type="text"
          value={draftOf(c)}
          oninput={(e) => (edits[c.name] = e.currentTarget.value)}
          disabled={disabled || pending}
        />
      </label>
      {#if errors[c.name]}
        <p class="error" role="alert">{errors[c.name]}</p>
      {/if}
      <button type="submit" disabled={disabled || pending || !changed(c)}>Save</button>
    </form>
  {/each}
</section>

<style>
  .names {
    margin-top: 2rem;
  }
  form {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 0.5rem;
    align-items: end;
    margin-top: 0.75rem;
  }
  label {
    display: grid;
    gap: 0.25rem;
  }
  input[type='text'] {
    font: inherit;
    padding: 0.5rem;
  }
  .owner {
    color: #666;
    font-size: 0.9em;
  }
  .error {
    grid-column: 1 / -1;
    color: #b00020;
    margin: 0;
  }
</style>
