<script lang="ts">
  import { onMount } from 'svelte'
  import {
    ApiError,
    createChild,
    deleteChild,
    listChildren,
    listClients,
    messageFor,
    updateChild,
    type Child,
    type ClientView,
  } from './api'

  let { onBack }: { onBack: () => void } = $props()

  let children = $state<Child[]>([])
  let clients = $state<ClientView[]>([])
  // Pending edits per child; rebuilt from the server on every load so the
  // page never shows optimistic state.
  let drafts = $state<Record<number, { name: string; clients: string[] }>>({})
  let newName = $state('')
  let pending = $state(false)
  let error = $state<{ code: string; text: string } | null>(null)

  async function load() {
    const [c, cl] = await Promise.all([listChildren(), listClients()])
    const next: typeof drafts = {}
    for (const child of c) next[child.id] = { name: child.name, clients: [...child.clients] }
    drafts = next
    children = c
    clients = cl
  }

  onMount(() => {
    run(load)
  })

  /** Run a mutation; any failure becomes the alert and the page reloads from the server. */
  async function run(fn: () => Promise<void>, reloadOnError = true) {
    pending = true
    error = null
    try {
      await fn()
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
    } finally {
      pending = false
    }
  }

  function known(name: string): boolean {
    return clients.some((c) => c.name === name)
  }

  function toggle(id: number, name: string, checked: boolean) {
    const d = drafts[id]
    if (checked && !d.clients.includes(name)) d.clients = [...d.clients, name]
    if (!checked) d.clients = d.clients.filter((c) => c !== name)
  }

  function remove(id: number, name: string) {
    drafts[id].clients = drafts[id].clients.filter((c) => c !== name)
  }

  function save(id: number) {
    const d = drafts[id]
    run(async () => {
      await updateChild(id, d.name, d.clients)
      await load()
    })
  }

  function del(id: number) {
    run(async () => {
      await deleteChild(id)
      await load()
    })
  }

  function add(e: SubmitEvent) {
    e.preventDefault()
    const name = newName
    run(async () => {
      await createChild(name, [])
      newName = ''
      await load()
    })
  }
</script>

<main class="children">
  <h1>Children</h1>
  <button type="button" onclick={onBack}>Back</button>

  {#if error}
    <p class="error" role="alert" data-error={error.code}>{error.text}</p>
  {/if}

  {#each children as child (child.id)}
    <form
      data-child={child.id}
      onsubmit={(e) => {
        e.preventDefault()
        save(child.id)
      }}
    >
      <input aria-label="Name" type="text" required bind:value={drafts[child.id].name} disabled={pending} />
      <fieldset>
        <legend>Devices</legend>
        {#each clients as c (c.name)}
          {@const other = c.child !== null && c.child.id !== child.id ? c.child : null}
          <label>
            <input
              type="checkbox"
              checked={drafts[child.id].clients.includes(c.name)}
              disabled={pending || other !== null}
              onchange={(e) => toggle(child.id, c.name, e.currentTarget.checked)}
            />
            {c.name}{#if other}
              <span class="assigned"> — assigned to {other.name}</span>
            {/if}
          </label>
        {/each}
        {#each drafts[child.id].clients.filter((n) => !known(n)) as name (name)}
          <p class="missing">
            <s data-missing>{name}</s>
            <span class="hint">not in AdGuard Home</span>
            <button type="button" onclick={() => remove(child.id, name)} disabled={pending}>Remove</button>
          </p>
        {/each}
      </fieldset>
      <div class="actions">
        <button type="submit" disabled={pending}>Save</button>
        <button type="button" onclick={() => del(child.id)} disabled={pending}>Delete</button>
      </div>
    </form>
  {/each}

  <form class="add" onsubmit={add}>
    <label>
      New child
      <input type="text" required bind:value={newName} disabled={pending} />
    </label>
    <button type="submit" disabled={pending}>Add child</button>
  </form>
</main>

<style>
  .children {
    max-width: 40rem;
    margin: 10vh auto;
    padding: 0 1rem;
  }
  form {
    display: grid;
    gap: 0.5rem;
    margin-top: 1.5rem;
    padding: 1rem;
    border: 1px solid #ddd;
    border-radius: 0.5rem;
  }
  fieldset {
    display: grid;
    gap: 0.25rem;
    border: 0;
    padding: 0;
    margin: 0;
  }
  input[type='text'] {
    font: inherit;
    padding: 0.5rem;
  }
  .actions {
    display: flex;
    gap: 0.5rem;
  }
  .assigned,
  .hint {
    color: #666;
    font-size: 0.9em;
  }
  .missing {
    margin: 0;
    display: flex;
    gap: 0.5rem;
    align-items: center;
  }
  .error {
    color: #b00020;
  }
</style>
