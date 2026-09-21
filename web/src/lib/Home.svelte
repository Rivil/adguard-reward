<script lang="ts">
  import { onMount } from 'svelte'
  import { ApiError, childBlocked, iconUrl, listChildren, logout, navigate, type BlockedView } from './api'

  let { username, onLogout }: { username: string; onLogout: () => void } = $props()

  let pending = $state(false)
  let views = $state<BlockedView[] | null>(null)
  // A failed fetch replaces the whole list: a stale view of what a child can
  // reach is worse than none.
  let error = $state<string | null>(null)

  async function load() {
    try {
      const children = await listChildren()
      const fresh = await Promise.all(children.map((c) => childBlocked(c.id)))
      error = null
      views = fresh
    } catch (err) {
      views = null
      error = err instanceof ApiError ? err.code : 'network'
    }
  }

  // Every mount re-fetches; nothing is cached across visits.
  onMount(load)

  async function signOut() {
    pending = true
    try {
      await logout()
    } catch {
      // The session may already be gone; either way the page goes to login.
    } finally {
      pending = false
      onLogout()
    }
  }

  function shown(v: BlockedView) {
    return v.services.filter((s) => s.state !== 'unblocked')
  }
</script>

<main class="home">
  <h1>adguard-reward</h1>
  <p>Signed in as <strong>{username}</strong></p>
  <nav>
    <button type="button" onclick={() => navigate('children')}>Children</button>
    <button type="button" onclick={signOut} disabled={pending}>Log out</button>
  </nav>

  {#if error !== null}
    <p class="error" role="alert" data-error={error}>
      Can't reach AdGuard Home — showing nothing rather than a stale list
    </p>
  {:else if views !== null}
    {#if views.length === 0}
      <p>No children yet</p>
    {/if}
    {#each views as v (v.child.id)}
      <section data-child={v.child.id}>
        <h2>{v.child.name}</h2>
        <p class="clients">
          {#each v.clients as c (c.name)}
            <span class="client" class:missing={c.missing}>
              {c.name}
              {#if c.missing}
                <span class="badge" data-badge="missing">missing</span>
              {:else if c.uses_global}
                <span class="badge" data-badge="global">uses global list</span>
              {/if}
            </span>
          {/each}
        </p>
        {#if shown(v).length === 0}
          <p class="none">Nothing is blocked</p>
        {:else}
          <ul class="services">
            {#each shown(v) as s (s.id)}
              <li data-state={s.state}>
                <img alt="" src={iconUrl(s.icon)} width="20" height="20" />
                {s.name}{#if s.state === 'partial'}
                  <span class="partial"> — unblocked on {s.differs.join(', ')}</span>
                {/if}
              </li>
            {/each}
          </ul>
        {/if}
      </section>
    {/each}
  {/if}
</main>

<style>
  .home {
    max-width: 40rem;
    margin: 10vh auto;
    padding: 0 1rem;
  }
  nav {
    display: flex;
    gap: 0.5rem;
  }
  section {
    margin-top: 1.5rem;
  }
  .clients {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    margin: 0.25rem 0;
  }
  .client.missing {
    text-decoration: line-through;
  }
  .badge {
    font-size: 0.75em;
    padding: 0 0.4em;
    border-radius: 0.5em;
    background: #eee;
  }
  .services {
    list-style: none;
    padding: 0;
    margin: 0;
  }
  .services li {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.25rem 0;
  }
  .partial {
    color: #666;
  }
  .none {
    color: #666;
  }
  .error {
    color: #b00020;
  }
</style>
