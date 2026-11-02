<script lang="ts">
  import { onMount } from 'svelte'
  import { ApiError, applyMigration, messageFor, migrationDismissed, migrationOffer, type MigrationOffer } from './api'

  let { onMigrated }: { onMigrated: () => void } = $props()

  let offer = $state<MigrationOffer | null>(null)
  let pending = $state(false)
  let error = $state<{ code: string; text: string } | null>(null)

  onMount(async () => {
    try {
      offer = await migrationOffer()
    } catch {
      // The home page's own error state covers AdGuard being down; the
      // banner simply stays hidden.
      offer = null
    }
  })

  // The condition is the state: shown whenever a mapped client still uses
  // the global list and the parent has not said "not now" this login.
  // Nothing is persisted (locked: migration_offer_ux).
  let visible = $derived(offer !== null && offer.clients.length > 0 && !$migrationDismissed)

  async function migrate() {
    pending = true
    error = null
    try {
      await applyMigration()
      offer = null
      onMigrated()
    } catch (err) {
      error =
        err instanceof ApiError
          ? { code: err.code, text: messageFor(err) }
          : { code: 'network', text: "Can't reach the server — check your connection" }
    } finally {
      pending = false
    }
  }

  function notNow() {
    migrationDismissed.set(true)
  }
</script>

{#if visible && offer}
  <aside role="status" data-migration class="banner">
    <p>
      Some of your children's devices still use AdGuard Home's global blocked-services list. Move them to their
      own lists so grants can be per device:
    </p>
    <ul>
      {#each offer.clients as c (c.name)}
        <li>
          {c.name} ({c.child.name})
          {#if c.gains.length > 0}
            gains {c.gains.join(', ')}
          {:else}
            keeps its list
          {/if}
        </li>
      {/each}
    </ul>
    {#if error}
      <p class="error" role="alert" data-error={error.code}>{error.text}</p>
    {/if}
    <div class="actions">
      <button type="button" onclick={migrate} disabled={pending}>Migrate</button>
      <button type="button" onclick={notNow} disabled={pending}>Not now</button>
    </div>
  </aside>
{/if}

<style>
  .banner {
    margin: 1rem 0;
    padding: 0.75rem 1rem;
    border: 1px solid #e0c060;
    border-radius: 0.5rem;
    background: #fff8e1;
  }
  .banner p {
    margin: 0 0 0.5rem;
  }
  .actions {
    display: flex;
    gap: 0.5rem;
  }
  .error {
    color: #b00020;
  }
</style>
