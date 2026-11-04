<script lang="ts">
  import type { BlockedView, Service } from './api'
  import ServiceIcon from './ServiceIcon.svelte'
  import { pickerOptions } from './toggles'

  // "Block a service…" at the foot of a child's view (locked block_picker):
  // a searchable list over the catalogue, built-in and custom, without the
  // services already blocked. Picking one hands it to onpick once; the list
  // stays disabled until onpick settles and closes when it resolves.
  let {
    catalogue,
    view,
    onpick,
  }: {
    catalogue: Service[]
    view: BlockedView
    onpick: (s: Service) => void | Promise<void>
  } = $props()

  let open = $state(false)
  let query = $state('')
  let pending = $state(false)

  let options = $derived(pickerOptions(catalogue, view, query))

  function toggleOpen() {
    open = !open
    query = ''
  }

  async function pick(s: Service) {
    if (pending) return
    pending = true
    try {
      await onpick(s)
      open = false
      query = ''
    } catch {
      // The caller reports the failure; the list stays open for a retry.
    } finally {
      pending = false
    }
  }
</script>

<div class="block-picker">
  <button type="button" class="block-picker-toggle" aria-expanded={open} onclick={toggleOpen} disabled={pending}
    >Block a service…</button
  >
  {#if open}
    <div class="block-picker-panel">
      <input
        type="search"
        aria-label="Search services to block"
        placeholder="Search services…"
        autocomplete="off"
        bind:value={query}
        disabled={pending}
      />
      {#if options.length === 0}
        <p class="block-picker-empty" role="status">No service matches.</p>
      {:else}
        <ul aria-label="Services to block">
          {#each options as s (s.id)}
            <li>
              <button type="button" data-service={s.id} disabled={pending} onclick={() => pick(s)}>
                <ServiceIcon name={s.name} icon={s.icon} />
                <span>{s.name}</span>
              </button>
            </li>
          {/each}
        </ul>
      {/if}
    </div>
  {/if}
</div>

<style>
  .block-picker {
    margin-top: 1rem;
  }
  .block-picker-panel {
    margin-top: 0.5rem;
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 0.5rem;
  }
  .block-picker-panel input {
    width: 100%;
    box-sizing: border-box;
  }
  ul {
    list-style: none;
    margin: 0.5rem 0 0;
    padding: 0;
    max-height: 50vh;
    overflow-y: auto;
  }
  li button {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    width: 100%;
    text-align: left;
    padding: 0.4rem 0.25rem;
  }
  .block-picker-empty {
    margin: 0.5rem 0 0;
    color: var(--text);
  }
</style>
