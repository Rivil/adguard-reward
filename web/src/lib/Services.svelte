<script lang="ts">
  import { onMount } from 'svelte'
  import {
    ApiError,
    createCustomService,
    deleteCustomService,
    listCustomServices,
    messageFor,
    updateCustomService,
    type CustomService,
  } from './api'
  import ServiceIcon from './ServiceIcon.svelte'

  // Custom services live in Settings next to Buttons (locked management_ui):
  // a name plus plain hostnames, blocked for every child's devices.
  let { onBack }: { onBack: () => void } = $props()

  let services = $state<CustomService[]>([])
  let error = $state<{ code: string; text: string } | null>(null)
  /** Set when the server stored a change but AdGuard has not caught up yet. */
  let note = $state<string | null>(null)
  let pending = $state(false)

  let name = $state('')
  let domainsText = $state('')
  /** Id of the service the form edits; null: the form creates. */
  let editing = $state<number | null>(null)
  /** Id of the service whose delete asks for confirmation. */
  let confirming = $state<number | null>(null)

  /** One domain per line, trimmed, blank lines dropped. */
  function parseDomains(text: string): string[] {
    return text
      .split('\n')
      .map((l) => l.trim())
      .filter((l) => l !== '')
  }

  const domains = $derived(parseDomains(domainsText))
  const canSave = $derived(!pending && name.trim() !== '' && domains.length > 0)

  onMount(() => {
    void run(async () => {
      services = await listCustomServices()
    })
  })

  /** Run one call; a failure becomes the inline alert and the form keeps what was typed. */
  async function run(fn: () => Promise<void>): Promise<boolean> {
    pending = true
    error = null
    note = null
    try {
      await fn()
      return true
    } catch (err) {
      error =
        err instanceof ApiError
          ? { code: err.code, text: messageFor(err) }
          : { code: 'network', text: "Can't reach the server — check your connection" }
      return false
    } finally {
      pending = false
    }
  }

  function afterSave(saved: CustomService) {
    if (!saved.applied) note = `${saved.name} saved — AdGuard will catch up`
    resetForm()
  }

  async function save(e: SubmitEvent) {
    e.preventDefault()
    if (!canSave) return
    const id = editing
    await run(async () => {
      if (id === null) {
        const saved = await createCustomService(name.trim(), domains)
        services = [...services, saved]
        afterSave(saved)
      } else {
        const saved = await updateCustomService(id, name.trim(), domains)
        services = services.map((s) => (s.id === saved.id ? saved : s))
        afterSave(saved)
      }
    })
  }

  function edit(s: CustomService) {
    editing = s.id
    confirming = null
    name = s.name
    domainsText = s.domains.join('\n')
    error = null
  }

  function resetForm() {
    editing = null
    name = ''
    domainsText = ''
  }

  async function del(id: number) {
    await run(async () => {
      await deleteCustomService(id)
      services = services.filter((s) => s.id !== id)
      confirming = null
      if (editing === id) resetForm()
    })
  }
</script>

<main class="services">
  <h1>Services</h1>
  <button type="button" onclick={onBack}>Back</button>
  <p class="hint">Your own services: blocked on every child's devices until a grant unblocks them.</p>

  {#if error}
    <p class="error" role="alert" data-error={error.code}>{error.text}</p>
  {/if}
  {#if note}
    <p class="note" role="status" data-catch-up>{note}</p>
  {/if}

  {#if services.length === 0}
    <p class="none">No custom services yet — add one below.</p>
  {:else}
    <ul class="list">
      {#each services as s (s.id)}
        <li class={{ editing: editing === s.id }} data-service={s.service_id}>
          <div class="row">
            <ServiceIcon name={s.name} icon="" size={28} />
            <div>
              <strong>{s.name}</strong>
              <span class="meta" data-domains>{s.domains.join(', ')}</span>
            </div>
          </div>
          {#if confirming === s.id}
            <div class="actions confirm">
              <span>Delete {s.name}?</span>
              <button type="button" class="danger" onclick={() => del(s.id)} disabled={pending}>Confirm</button>
              <button type="button" onclick={() => (confirming = null)} disabled={pending}>Cancel</button>
            </div>
          {:else}
            <div class="actions">
              <button type="button" onclick={() => edit(s)} disabled={pending}>Edit</button>
              <button type="button" onclick={() => (confirming = s.id)} disabled={pending}>Delete</button>
            </div>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}

  <form onsubmit={save}>
    <h2>{editing === null ? 'New service' : 'Edit service'}</h2>
    <label class="field">
      <span>Name</span>
      <input type="text" name="name" maxlength="64" autocomplete="off" bind:value={name} disabled={pending} />
    </label>
    <label class="field">
      <span>Domains, one per line</span>
      <textarea
        name="domains"
        rows="4"
        autocomplete="off"
        autocapitalize="none"
        spellcheck="false"
        placeholder="portal.example.com"
        bind:value={domainsText}
        disabled={pending}
      ></textarea>
    </label>
    <div class="actions">
      <button type="submit" disabled={!canSave}>Save</button>
      {#if editing !== null}
        <button type="button" onclick={resetForm} disabled={pending}>Cancel</button>
      {/if}
    </div>
  </form>
</main>

<style>
  .services {
    max-width: 40rem;
    margin: 10vh auto;
    padding: 0 1rem;
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
    border: 1px solid var(--border);
    border-radius: 0.5rem;
  }
  li.editing {
    border-color: var(--accent-border);
  }
  .row {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    min-width: 0;
  }
  .row > div {
    display: grid;
    min-width: 0;
  }
  .meta {
    opacity: 0.75;
    font-size: 0.9rem;
    overflow-wrap: anywhere;
  }
  form {
    display: grid;
    gap: 0.75rem;
    margin-top: 1.5rem;
    padding: 1rem;
    border: 1px solid var(--border);
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
  input[type='text'],
  textarea {
    font: inherit;
    padding: 0.5rem;
    min-height: 2.75rem;
  }
  textarea {
    font-family: var(--mono);
    resize: vertical;
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
  .hint,
  .none {
    opacity: 0.75;
  }
  .error {
    color: #b00020;
  }
</style>
