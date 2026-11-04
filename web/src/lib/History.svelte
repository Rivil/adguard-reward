<script lang="ts">
  import { ApiError, fetchHistory, messageFor, type HistoryGrant } from './api'
  import { appendPage, endTimeOf, formatTime, removedText, statusLabel } from './history'

  // One child's grant history (locked history_location): newest first, 50 a
  // page, with load-more paging further back (locked retention). A missing,
  // invalid, unknown or deleted child shows "No such child" with the back button.
  let { child, onBack }: { child: number | null; onBack: () => void } = $props()

  type Failure = { code: string; text: string }

  let childName = $state<string | null>(null)
  let grants = $state<HistoryGrant[]>([])
  let nextBefore = $state<number | null>(null)
  let loaded = $state(false)
  let notFound = $state(false)
  /** The first page failed for a reason other than an unknown child. */
  let loadError = $state<Failure | null>(null)
  let loadingMore = $state(false)
  /** A further page failed; the rows already shown stay. */
  let moreError = $state<Failure | null>(null)

  /** Bumped per load so a response for an earlier child never lands. */
  let generation = 0

  function failureOf(err: unknown): Failure {
    return err instanceof ApiError
      ? { code: err.code, text: messageFor(err) }
      : { code: 'network', text: "Can't reach the server — check your connection" }
  }

  async function load(id: number | null) {
    const gen = ++generation
    childName = null
    grants = []
    nextBefore = null
    loaded = false
    notFound = false
    loadError = null
    loadingMore = false
    moreError = null
    if (id === null) {
      notFound = true
      return
    }
    try {
      const page = await fetchHistory(id)
      if (gen !== generation) return
      childName = page.child.name
      grants = page.grants
      nextBefore = page.next_before
      loaded = true
    } catch (err) {
      if (gen !== generation) return
      if (err instanceof ApiError && err.status === 404) notFound = true
      else loadError = failureOf(err)
    }
  }

  async function loadMore() {
    const id = child
    const before = nextBefore
    if (loadingMore || id === null || before === null) return
    const gen = generation
    loadingMore = true
    moreError = null
    try {
      const page = await fetchHistory(id, before)
      if (gen !== generation) return
      grants = appendPage(grants, page.grants)
      nextBefore = page.next_before
    } catch (err) {
      if (gen !== generation) return
      moreError = failureOf(err)
    } finally {
      if (gen === generation) loadingMore = false
    }
  }

  $effect(() => {
    void load(child)
  })
</script>

<main class="history">
  <h1>{childName === null ? 'History' : `${childName} — history`}</h1>
  <button type="button" onclick={onBack}>Back</button>

  {#if notFound}
    <p class="none" data-not-found>No such child</p>
  {:else if loadError}
    <p class="error" role="alert" data-error={loadError.code}>{loadError.text}</p>
    <button type="button" onclick={() => load(child)}>Retry</button>
  {:else if loaded}
    {#if grants.length === 0}
      <p class="none" data-empty>No grants yet</p>
    {:else}
      <ul class="list">
        {#each grants as g (g.id)}
          {@const ended = endTimeOf(g)}
          <li data-grant={g.id}>
            <div class="row">
              <span class="status" data-status={g.status}>{statusLabel(g.status)}</span>
              <span class="meta">
                <time data-started datetime={g.started_at}>{formatTime(g.started_at)}</time>
                {#if ended !== null}
                  – <time data-ended datetime={ended}>{formatTime(ended)}</time>
                {:else if g.status !== 'ongoing'}
                  – <span data-ended>—</span>
                {/if}
              </span>
            </div>
            {#if g.services.length === 0}
              <p class="meta" data-no-services>—</p>
            {:else}
              <ul class="services">
                {#each g.services as s (s.id)}
                  {@const removed = removedText(s)}
                  <li data-service={s.id} class:removed={removed !== null}>
                    <span class="name">{s.name}</span>
                    {#if removed !== null}
                      <span class="meta" data-removed>{removed}</span>
                    {/if}
                  </li>
                {/each}
              </ul>
            {/if}
            {#if g.extensions.length > 0}
              <ol class="extensions">
                {#each g.extensions as x, i (i)}
                  <li data-extension>
                    Extended {formatTime(x.at)}: {formatTime(x.old_ends_at)} → {formatTime(x.new_ends_at)}
                  </li>
                {/each}
              </ol>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
    {#if moreError}
      <p class="error" role="alert" data-more-error={moreError.code}>{moreError.text}</p>
    {/if}
    {#if nextBefore !== null}
      <button type="button" onclick={loadMore} disabled={loadingMore}>
        {moreError ? 'Retry' : 'Load more'}
      </button>
    {/if}
  {:else}
    <p class="meta" role="status">Loading…</p>
  {/if}
</main>

<style>
  .history {
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
  .list > li {
    display: grid;
    gap: 0.5rem;
    padding: 0.75rem;
    border: 1px solid var(--border);
    border-radius: 0.5rem;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.5rem;
  }
  .status {
    font-weight: 600;
  }
  .meta {
    font-size: 0.875rem;
    opacity: 0.75;
  }
  .services,
  .extensions {
    margin: 0;
    padding-left: 1.25rem;
    display: grid;
    gap: 0.25rem;
  }
  .services li.removed .name {
    text-decoration: line-through;
  }
  .services li .meta {
    margin-left: 0.5rem;
  }
  .extensions {
    font-size: 0.875rem;
  }
</style>
