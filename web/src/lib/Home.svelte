<script lang="ts">
  import { onDestroy, onMount } from 'svelte'
  import {
    ApiError,
    childBlocked,
    createGrant,
    extendGrant,
    iconUrl,
    listButtons,
    listChildren,
    listServices,
    logout,
    navigate,
    type BlockedView,
    type Button,
    type Child,
    type Service,
  } from './api'
  import { merge, refresh, snapshot, start } from './activeGrants'
  import ActiveGrants from './ActiveGrants.svelte'
  import GrantForm, { type GrantFormValue } from './GrantForm.svelte'
  import MigrationBanner from './MigrationBanner.svelte'
  import { formatDuration, minutesToSeconds } from './grants'
  import { acceptOffer, runTap, type Offer, type Outcome, type TapDeps, type TapSpec } from './tap'

  let { username, onLogout }: { username: string; onLogout: () => void } = $props()

  let pending = $state(false)
  let children = $state<Child[]>([])
  let buttons = $state<Button[]>([])
  // null until loaded or when the catalogue is unreachable: icons and
  // names fall back, nothing else waits on it.
  let services = $state<Service[] | null>(null)
  let loaded = $state(false)
  // Children or buttons failing to load is the one page-level error; the
  // tap surface cannot exist without them.
  let error = $state<string | null>(null)

  // Per-child outcome line under its buttons, and which tap keys are out.
  let notes = $state<Record<number, { code: string; text: string } | null>>({})
  let inflight = $state<Record<string, boolean>>({})
  let offer = $state<{ offer: Offer; spec: TapSpec; childId: number } | null>(null)

  // Blocked-services disclosures: fetched on first open only, each with its
  // own error line so AdGuard being down never blocks a tap.
  let blocked = $state<Record<number, { view: BlockedView | null; error: string | null }>>({})
  let opened = $state<Record<number, boolean>>({})

  // The ad-hoc form: the same tap path as a button, without one configured.
  let adhoc = $state<GrantFormValue>({ child_id: null, services: [], minutes: 60 })
  let adhocValid = $state(false)

  const deps: TapDeps = { createGrant, extendGrant, refresh, merge, grants: () => snapshot().grants }

  async function load() {
    try {
      const [c, b] = await Promise.all([listChildren(), listButtons()])
      children = c
      buttons = b
      error = null
      loaded = true
    } catch (err) {
      error = err instanceof ApiError ? err.code : 'network'
    }
    try {
      services = await listServices()
    } catch {
      services = null
    }
  }

  let stopPolling: (() => void) | null = null
  onMount(() => {
    void load()
    stopPolling = start()
  })
  onDestroy(() => {
    stopPolling?.()
  })

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

  // ---- names and icons ---------------------------------------------------

  const serviceNames = $derived(Object.fromEntries((services ?? []).map((s) => [s.id, s.name])) as Record<string, string>)
  const childNames = $derived(new Map(children.map((c) => [c.id, c.name])))

  function serviceName(id: string): string {
    return serviceNames[id] ?? id
  }

  function iconFor(b: Button): string | null {
    const first = services?.find((s) => s.id === b.services[0])
    return first ? iconUrl(first.icon) : null
  }

  function buttonsFor(childId: number): Button[] {
    return buttons.filter((b) => b.child_id === childId)
  }

  // ---- taps ----------------------------------------------------------------

  function apply(out: Outcome, spec: TapSpec, childId: number) {
    switch (out.kind) {
      case 'partial':
        notes[childId] = { code: 'partial', text: `Unlocked, except on ${out.failed.join(', ')}` }
        return
      case 'error':
        notes[childId] = { code: out.code, text: out.message }
        return
      case 'offer':
        offer = { offer: out, spec, childId }
        return
      default:
        // applied: the panel already shows the merged entry. ignored: a
        // double tap while the first is out.
        return
    }
  }

  async function tap(spec: TapSpec, childId: number, run: (s: TapSpec, d: TapDeps) => Promise<Outcome> = runTap) {
    inflight[spec.key] = true
    notes[childId] = null
    try {
      apply(await run(spec, deps), spec, childId)
    } finally {
      inflight[spec.key] = false
    }
  }

  function tapButton(child: Child, b: Button) {
    void tap({ key: `button:${b.id}`, child_id: child.id, services: b.services, duration: b.duration, clients: child.clients }, child.id)
  }

  function tapAdhoc(e: SubmitEvent) {
    e.preventDefault()
    const child = children.find((c) => c.id === adhoc.child_id)
    if (!child) return
    void tap(
      { key: 'adhoc', child_id: child.id, services: [...adhoc.services], duration: minutesToSeconds(adhoc.minutes), clients: child.clients },
      child.id,
    )
  }

  function acceptCurrentOffer() {
    const o = offer
    if (!o) return
    offer = null
    void tap(o.spec, o.childId, (s, d) => acceptOffer(o.offer, s, d))
  }

  function offerText(o: { offer: Offer; spec: TapSpec; childId: number }): string {
    const covered = new Set(o.offer.targets.flatMap((g) => g.services))
    const already = o.spec.services.filter((s) => covered.has(s)).map(serviceName)
    const what = already.length > 0 ? already.join(', ') : 'That'
    return `${what} is already unlocked for ${childNames.get(o.childId) ?? 'this child'} — extend by ${formatDuration(o.spec.duration)}?`
  }

  // ---- blocked disclosures -------------------------------------------------

  const loadingBlocked = new Set<number>()

  async function openBlocked(id: number) {
    if (blocked[id]?.view || loadingBlocked.has(id)) return
    loadingBlocked.add(id)
    try {
      const view = await childBlocked(id)
      blocked[id] = { view, error: null }
    } catch (err) {
      blocked[id] = { view: null, error: err instanceof ApiError ? err.code : 'network' }
    } finally {
      loadingBlocked.delete(id)
    }
  }

  function toggled(id: number, open: boolean) {
    opened[id] = open
    if (open) void openBlocked(id)
  }

  function migrated() {
    void load()
    blocked = {}
    for (const [id, open] of Object.entries(opened)) if (open) void openBlocked(Number(id))
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
    <button type="button" onclick={() => navigate('buttons')}>Buttons</button>
    <button type="button" onclick={signOut} disabled={pending}>Log out</button>
  </nav>

  <MigrationBanner onMigrated={migrated} />

  <ActiveGrants {childNames} {serviceNames} />

  {#if offer}
    <div class="offer" role="dialog" aria-modal="true" aria-label="Already unlocked" data-offer>
      <p>{offerText(offer)}</p>
      <div class="actions">
        <button type="button" onclick={acceptCurrentOffer}>Extend</button>
        <button type="button" onclick={() => (offer = null)}>Cancel</button>
      </div>
    </div>
  {/if}

  {#if error !== null}
    <p class="error" role="alert" data-error={error}>Can't reach the server — the buttons can't be shown</p>
  {:else if loaded}
    {#if children.length === 0}
      <p>No children yet</p>
    {/if}
    {#each children as child (child.id)}
      {@const own = buttonsFor(child.id)}
      {@const entry = blocked[child.id]}
      <section data-child={child.id}>
        <h2>{child.name}</h2>
        {#if own.length === 0}
          <p class="none">
            No buttons yet —
            <button type="button" class="link" onclick={() => navigate('buttons')}>set some up on Buttons</button>
          </p>
        {:else}
          <div class="tiles">
            {#each own as b (b.id)}
              {@const icon = iconFor(b)}
              <button type="button" class="tile" data-button={b.id} onclick={() => tapButton(child, b)} disabled={inflight[`button:${b.id}`] === true}>
                {#if icon}
                  <img alt="" src={icon} width="28" height="28" />
                {/if}
                <span class="label">{b.label}</span>
                <span class="duration">{formatDuration(b.duration)}</span>
              </button>
            {/each}
          </div>
        {/if}
        {#if notes[child.id]}
          <p class="error" role="alert" data-error={notes[child.id]?.code}>{notes[child.id]?.text}</p>
        {/if}
        <details data-blocked={child.id} ontoggle={(e) => toggled(child.id, e.currentTarget.open)}>
          <summary>Blocked services</summary>
          {#if entry?.error}
            <p class="error" role="alert" data-error={entry.error}>Can't reach AdGuard Home — try again in a moment</p>
          {:else if entry?.view}
            {@const v = entry.view}
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
          {:else}
            <p class="none">Loading…</p>
          {/if}
        </details>
      </section>
    {/each}

    <details class="adhoc" data-adhoc open={buttons.length === 0}>
      <summary>Unlock something else</summary>
      {#if buttons.length === 0}
        <p class="hint">
          One-tap buttons live on the <button type="button" class="link" onclick={() => navigate('buttons')}>Buttons</button> page; until you add
          some, unlock from here.
        </p>
      {/if}
      <form onsubmit={tapAdhoc}>
        <GrantForm {children} {services} bind:value={adhoc} bind:valid={adhocValid} disabled={inflight.adhoc === true} />
        <button type="submit" disabled={!adhocValid || inflight.adhoc === true}>Unlock</button>
      </form>
    </details>
  {/if}
</main>

<style>
  .home {
    max-width: 40rem;
    margin: 4vh auto;
    padding: 0 1rem;
  }
  nav {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
  }
  nav button,
  form button[type='submit'] {
    min-height: 2.75rem;
  }
  section,
  .adhoc {
    margin-top: 1.5rem;
  }
  .tiles {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(9rem, 1fr));
    gap: 0.5rem;
  }
  .tile {
    display: grid;
    justify-items: center;
    gap: 0.25rem;
    padding: 0.75rem 0.5rem;
    min-height: 5.5rem;
    font: inherit;
  }
  .tile .label {
    font-weight: 600;
  }
  .tile .duration {
    font-size: 0.85rem;
    opacity: 0.75;
  }
  .link {
    background: none;
    border: 0;
    padding: 0;
    font: inherit;
    color: var(--accent);
    text-decoration: underline;
    cursor: pointer;
  }
  details {
    margin-top: 0.75rem;
  }
  summary {
    cursor: pointer;
    min-height: 2.75rem;
    display: flex;
    align-items: center;
  }
  .offer {
    margin-top: 1rem;
    padding: 1rem;
    border: 1px solid var(--accent-border);
    border-radius: 0.5rem;
    background: var(--accent-bg);
  }
  .offer .actions {
    display: flex;
    gap: 0.5rem;
  }
  .offer .actions button {
    flex: 1;
    min-height: 2.75rem;
  }
  form {
    display: grid;
    gap: 0.75rem;
    margin-top: 0.5rem;
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
  .partial,
  .none,
  .hint {
    color: #666;
  }
  .error {
    color: #b00020;
  }
</style>
