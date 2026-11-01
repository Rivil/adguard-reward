<script lang="ts">
  import { ApiError, createClient, messageFor, type Child, type UnlabelledDevice } from './api'

  let {
    devices,
    children,
    disabled = false,
    onCreated,
  }: {
    /** Rendered in the order given — the server sorts them. */
    devices: UnlabelledDevice[]
    /** Offered in the optional child picker. */
    children: Child[]
    disabled?: boolean
    /** Called after a 201 so the page can reload from the server. */
    onCreated: () => void | Promise<void>
  } = $props()

  // Only what the parent changed is stored; untouched fields read the
  // device's own defaults, so a reload with new devices needs no syncing.
  let edits = $state<Record<string, { name?: string; ids?: string; child?: string }>>({})
  let errors = $state<Record<string, string>>({})
  let pending = $state(false)

  const nameOf = (d: UnlabelledDevice) => edits[d.ip]?.name ?? d.hostname
  const idsOf = (d: UnlabelledDevice) => edits[d.ip]?.ids ?? d.suggested_ids.join(', ')
  const childOf = (d: UnlabelledDevice) => edits[d.ip]?.child ?? ''

  function parseIds(text: string): string[] {
    return text
      .split(',')
      .map((s) => s.trim())
      .filter((s) => s !== '')
  }

  function edit(ip: string, field: 'name' | 'ids' | 'child', value: string) {
    edits[ip] = { ...edits[ip], [field]: value }
  }

  function ready(d: UnlabelledDevice): boolean {
    return nameOf(d).trim() !== '' && parseIds(idsOf(d)).length > 0
  }

  async function save(e: SubmitEvent, d: UnlabelledDevice) {
    e.preventDefault()
    if (pending || !ready(d)) return
    pending = true
    delete errors[d.ip]
    const child = childOf(d)
    try {
      await createClient(nameOf(d).trim(), parseIds(idsOf(d)), child === '' ? null : Number(child))
      delete edits[d.ip]
      await onCreated()
    } catch (err) {
      errors[d.ip] =
        err instanceof ApiError ? messageFor(err) : "Can't reach the server — check your connection"
    } finally {
      pending = false
    }
  }
</script>

<section class="unlabelled">
  <h2>Unlabelled devices</h2>
  {#if devices.length === 0}
    <p class="empty">
      No unlabelled devices. A device must connect to the network and appear in AdGuard Home before it can be named.
    </p>
  {/if}
  {#each devices as d (d.ip)}
    <form data-device={d.ip} onsubmit={(e) => save(e, d)}>
      <p class="seen">{d.ip} · {d.source}</p>
      <input
        aria-label="Device name"
        type="text"
        required
        placeholder="Name this device"
        value={nameOf(d)}
        oninput={(e) => edit(d.ip, 'name', e.currentTarget.value)}
        disabled={disabled || pending}
      />
      <input
        aria-label="Device ids"
        type="text"
        required
        value={idsOf(d)}
        oninput={(e) => edit(d.ip, 'ids', e.currentTarget.value)}
        disabled={disabled || pending}
      />
      <select
        aria-label="Child"
        onchange={(e) => edit(d.ip, 'child', e.currentTarget.value)}
        disabled={disabled || pending}
      >
        <option value="" selected={childOf(d) === ''}>No child</option>
        {#each children as c (c.id)}
          <option value={String(c.id)} selected={childOf(d) === String(c.id)}>{c.name}</option>
        {/each}
      </select>
      {#if errors[d.ip]}
        <p class="error" role="alert">{errors[d.ip]}</p>
      {/if}
      <button type="submit" disabled={disabled || pending || !ready(d)}>Save</button>
    </form>
  {/each}
</section>

<style>
  .unlabelled {
    margin-top: 2rem;
  }
  form {
    display: grid;
    gap: 0.5rem;
    margin-top: 1rem;
    padding: 1rem;
    border: 1px dashed #bbb;
    border-radius: 0.5rem;
  }
  input[type='text'],
  select {
    font: inherit;
    padding: 0.5rem;
  }
  .seen,
  .empty {
    color: #666;
    font-size: 0.9em;
    margin: 0;
  }
  .error {
    color: #b00020;
    margin: 0;
  }
</style>
