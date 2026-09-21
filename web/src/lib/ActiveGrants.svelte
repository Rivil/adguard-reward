<script lang="ts">
  import { onDestroy } from 'svelte'
  import { ApiError, endGrant, extendGrant, messageFor, type Grant } from './api'
  import { activeGrants, refresh } from './activeGrants'
  import { formatCountdown, formatDuration, ownDuration, remainingSeconds } from './grants'

  let { childNames, serviceNames }: { childNames: Map<number, string>; serviceNames: Record<string, string> } = $props()

  // One clock for the whole list; every countdown is recomputed from its
  // ends_at on each tick, never decremented, so a backgrounded tab that
  // skipped ticks still shows the truth when it wakes.
  let now = $state(Date.now())
  const clock = setInterval(() => {
    now = Date.now()
  }, 1000)
  onDestroy(() => clearInterval(clock))

  let busy = $state<Record<number, boolean>>({})
  let errors = $state<Record<number, string>>({})
  // Ids whose countdown reached zero and already asked the server once; the
  // entry leaves only when the server list no longer has it.
  const asked = new Set<number>()

  $effect(() => {
    const present = new Set<number>()
    for (const g of $activeGrants.grants) {
      present.add(g.id)
      if (remainingSeconds(g.ends_at, now) === 0 && !asked.has(g.id)) {
        asked.add(g.id)
        void refresh()
      }
    }
    for (const id of asked) if (!present.has(id)) asked.delete(id)
  })

  function childName(id: number): string {
    return childNames.get(id) ?? `child ${id}`
  }

  function serviceList(g: Grant): string {
    return g.services.map((s) => serviceNames[s] ?? s).join(', ')
  }

  /** Run one row's request; a 404 means the grant is already gone, so just re-sync. */
  async function run(id: number, fn: () => Promise<unknown>) {
    busy[id] = true
    delete errors[id]
    try {
      await fn()
      await refresh()
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        await refresh()
      } else {
        errors[id] = err instanceof ApiError ? rowMessage(err) : "Can't reach the server — check your connection"
      }
    } finally {
      busy[id] = false
    }
  }

  // A 502 here carries the client names AdGuard would not re-block, which
  // beats the generic can't-reach line.
  function rowMessage(err: ApiError): string {
    return err.code === 'adguard_unavailable' && err.message !== '' ? err.message : messageFor(err)
  }

  function extend(g: Grant) {
    void run(g.id, () => extendGrant(g.id, ownDuration(g)))
  }

  function end(g: Grant) {
    void run(g.id, () => endGrant(g.id))
  }
</script>

<section class="active" aria-label="Active grants">
  <h2>
    Unlocked now
    {#if $activeGrants.unreachable}
      <span class="badge" data-badge="unreachable">Can't reach server</span>
    {/if}
  </h2>
  {#if $activeGrants.grants.length === 0}
    <p class="none">Nothing is unlocked right now</p>
  {:else}
    <ul>
      {#each $activeGrants.grants as g (g.id)}
        <li data-grant={g.id}>
          <div class="who">
            <strong>{childName(g.child_id)}</strong>
            <span class="services">{serviceList(g)}</span>
          </div>
          <time data-countdown datetime={g.ends_at}>{formatCountdown(remainingSeconds(g.ends_at, now))}</time>
          <div class="actions">
            <button type="button" onclick={() => extend(g)} disabled={busy[g.id] === true}>
              Extend {formatDuration(ownDuration(g))}
            </button>
            <button type="button" onclick={() => end(g)} disabled={busy[g.id] === true}>End</button>
          </div>
          {#if errors[g.id]}
            <p class="error" role="alert">{errors[g.id]}</p>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  h2 {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.5rem;
  }
  li {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 0.25rem 0.75rem;
    align-items: center;
    padding: 0.75rem;
    border: 1px solid var(--accent-border);
    border-radius: 0.5rem;
    background: var(--accent-bg);
  }
  .who {
    display: grid;
  }
  .services {
    opacity: 0.8;
    font-size: 0.9rem;
  }
  time {
    font-variant-numeric: tabular-nums;
    font-size: 1.5rem;
    font-weight: 600;
  }
  .actions {
    grid-column: 1 / -1;
    display: flex;
    gap: 0.5rem;
  }
  .actions button {
    flex: 1;
    min-height: 2.75rem;
  }
  .badge {
    font-size: 0.75rem;
    font-weight: normal;
    padding: 0.1rem 0.4rem;
    border-radius: 0.25rem;
    background: #b00020;
    color: #fff;
  }
  .none {
    opacity: 0.75;
  }
  .error {
    grid-column: 1 / -1;
    margin: 0;
    color: #b00020;
  }
</style>
