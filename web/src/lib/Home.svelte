<script lang="ts">
  import { onDestroy, onMount } from 'svelte'
  import {
    ApiError,
    childBlocked,
    createGrant,
    extendGrant,
    listButtons,
    listChildren,
    listServices,
    logout,
    messageFor,
    navigate,
    toggleService,
    type BlockedView,
    type Button,
    type Child,
    type Service,
    type ToggleResult,
    type ToggleState,
  } from './api'
  import { merge, refresh, snapshot, start } from './activeGrants'
  import ActiveGrants from './ActiveGrants.svelte'
  import BlockPicker from './BlockPicker.svelte'
  import GrantForm, { type GrantFormValue } from './GrantForm.svelte'
  import MigrationBanner from './MigrationBanner.svelte'
  import ServiceIcon from './ServiceIcon.svelte'
  import { formatDuration, minutesToSeconds, sharedDevices, sharedText } from './grants'
  import { acceptOffer, runTap, type Offer, type Outcome, type TapDeps, type TapSpec } from './tap'
  import { controlFor, openOn, shown } from './toggles'

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
  // Per-child post-tap notice naming the shared devices the grant also
  // unblocked; beside the outcome line, never instead of it.
  let sharedNotes = $state<Record<number, string>>({})
  let inflight = $state<Record<string, boolean>>({})
  let offer = $state<{ offer: Offer; spec: TapSpec; childId: number } | null>(null)

  // Blocked-services disclosures: fetched on first open only, each with its
  // own error line so AdGuard being down never blocks a tap.
  let blocked = $state<Record<number, { view: BlockedView | null; error: string | null }>>({})
  let opened = $state<Record<number, boolean>>({})

  // Permanent toggles, per child: whether one is out (one at a time, so a
  // double tap is one POST and refetches never overlap), and its outcome
  // lines inside that child's blocked view.
  let toggling = $state<Record<number, boolean>>({})
  let toggleNotes = $state<Record<number, { code: string; text: string } | null>>({})
  let toggleShared = $state<Record<number, string>>({})

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

  // The button's chosen icon while the catalogue still has it, else the
  // first service's (locked icon_fallback): a catalogue change never blanks
  // a button that still has a known service. A custom service comes back
  // too; ServiceIcon draws its letter avatar rather than an empty <img>.
  function iconFor(b: Button): Service | null {
    const chosen = b.icon ? services?.find((s) => s.id === b.icon) : undefined
    return chosen ?? services?.find((s) => s.id === b.services[0]) ?? null
  }

  function buttonsFor(childId: number): Button[] {
    return buttons.filter((b) => b.child_id === childId)
  }

  // ---- taps ----------------------------------------------------------------

  function apply(out: Outcome, spec: TapSpec, childId: number) {
    if ((out.kind === 'applied' || out.kind === 'partial') && (out.grant.shared ?? []).length > 0) {
      sharedNotes[childId] = sharedText(out.grant.shared ?? [])
    }
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
    sharedNotes[childId] = ''
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

  async function loadBlocked(id: number) {
    try {
      const view = await childBlocked(id)
      blocked[id] = { view, error: null }
    } catch (err) {
      blocked[id] = { view: null, error: err instanceof ApiError ? err.code : 'network' }
    }
  }

  async function openBlocked(id: number) {
    if (blocked[id]?.view || loadingBlocked.has(id)) return
    loadingBlocked.add(id)
    try {
      await loadBlocked(id)
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

  // ---- permanent toggles ---------------------------------------------------

  /**
   * Permanently block or unblock one service on every client of the child
   * (locked shared_client_scope). A call while that child's last toggle is
   * still out is dropped. Once the server answers, the child's blocked view
   * and the active grants are fetched again — a block can trim or end a
   * grant (locked block_during_grant) — and the controls stay locked until
   * both land. Resolves true when the server stored the intent.
   */
  async function setToggle(childId: number, s: { id: string; name: string }, state: ToggleState): Promise<boolean> {
    if (toggling[childId]) return false
    toggling[childId] = true
    toggleNotes[childId] = null
    toggleShared[childId] = ''
    try {
      let r: ToggleResult
      try {
        r = await toggleService(childId, s.id, state)
      } catch (err) {
        toggleNotes[childId] =
          err instanceof ApiError ? { code: err.code, text: messageFor(err) } : { code: 'network', text: "Can't reach the server — check your connection" }
        return false
      }
      const done = state === 'blocked' ? 'Blocked' : 'Unblocked'
      // The intent is stored either way (locked toggle_partial_apply); name
      // the clients AdGuard refused.
      if (!r.applied) toggleNotes[childId] = { code: 'partial', text: `${done} ${s.name}, except on ${r.failed.join(', ')}` }
      if (r.shared.length > 0) toggleShared[childId] = `Also ${done.toLowerCase()} on ${sharedText(r.shared)}`
      // fresh: a poll already out may predate the toggle and its grant trim.
      await Promise.all([loadBlocked(childId), refresh({ fresh: true })])
      return true
    } finally {
      toggling[childId] = false
    }
  }

  function rowToggle(childId: number, s: { id: string; name: string }, state: ToggleState) {
    void setToggle(childId, s, state)
  }

  // The picker closes only once a block is stored. A failed pick (its alert
  // says why) or one dropped while another toggle is out throws, so the list
  // stays open for another try.
  async function pickBlock(childId: number, s: Service) {
    if (!(await setToggle(childId, s, 'blocked'))) throw new Error('not blocked')
  }
</script>

<main class="home">
  <h1>adguard-reward</h1>
  <p>Signed in as <strong>{username}</strong></p>
  <nav>
    <button type="button" onclick={() => navigate('children')}>Children</button>
    <button type="button" onclick={() => navigate('buttons')}>Buttons</button>
    <button type="button" onclick={() => navigate('services')}>Services</button>
    <button type="button" onclick={() => navigate('history')}>History</button>
    <button type="button" onclick={() => navigate('settings')}>Settings</button>
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
      {@const shared = sharedDevices(child.id, children)}
      {@const hintId = `shared-hint-${child.id}`}
      <section data-child={child.id}>
        <h2>{child.name}</h2>
        <!-- Its own route (locked history_location); Home stays the tap surface. -->
        <p class="history-link">
          <button
            type="button"
            class="link"
            data-history={child.id}
            aria-label={`History for ${child.name}`}
            onclick={() => navigate('history', { child: child.id })}>History</button
          >
        </p>
        {#if own.length === 0}
          <p class="none">
            No buttons yet —
            <button type="button" class="link" onclick={() => navigate('buttons')}>set some up on Buttons</button>
          </p>
        {:else}
          <div class="tiles">
            {#each own as b (b.id)}
              {@const icon = iconFor(b)}
              <button
                type="button"
                class="tile"
                data-button={b.id}
                aria-describedby={shared.length > 0 ? hintId : undefined}
                onclick={() => tapButton(child, b)}
                disabled={inflight[`button:${b.id}`] === true}
              >
                {#if icon}
                  <ServiceIcon name={icon.name} icon={icon.icon} size={28} />
                {/if}
                <span class="label">{b.label}</span>
                <span class="duration">{formatDuration(b.duration)}</span>
              </button>
            {/each}
          </div>
          {#if shared.length > 0}
            <!-- Passive (locked shared_notice): no control, the tap stays one tap. -->
            <p class="hint" role="note" id={hintId} data-shared-hint>Also unblocks on {sharedText(shared)}</p>
          {/if}
        {/if}
        {#if notes[child.id]}
          <p class="error" role="alert" data-error={notes[child.id]?.code}>{notes[child.id]?.text}</p>
        {/if}
        {#if sharedNotes[child.id]}
          <p class="notice" role="status" data-shared-notice>Also unblocked on {sharedNotes[child.id]}</p>
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
                  {(c.shared_with ?? []).length > 0 ? sharedText([{ client: c.name, others: c.shared_with ?? [] }]) : c.name}
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
                  {@const on = openOn(s)}
                  <li data-state={s.state} data-row={s.id}>
                    <ServiceIcon name={s.name} icon={s.icon} size={20} />
                    <span class="name"
                      >{s.name}{#if on.length > 0}
                        <span class="partial"> — unblocked on {on.join(', ')}</span>
                      {:else if s.state === 'unblocked'}
                        <!-- This child's own permanent unblock: listed so it can be reversed (c-5). -->
                        <span class="partial"> — unblocked</span>
                      {/if}</span
                    >
                    <span class="controls">
                      {#each controlFor(s) as c (c)}
                        <button
                          type="button"
                          data-control={c}
                          aria-label={`${c === 'unblock' ? 'Unblock' : 'Block'} ${s.name}`}
                          disabled={toggling[child.id] === true}
                          onclick={() => rowToggle(child.id, s, c === 'unblock' ? 'unblocked' : 'blocked')}
                          >{c === 'unblock' ? 'Unblock' : 'Block'}</button
                        >
                      {/each}
                    </span>
                  </li>
                {/each}
              </ul>
            {/if}
            {#if toggleNotes[child.id]}
              <p class="error" role="alert" data-error={toggleNotes[child.id]?.code} data-toggle-error>{toggleNotes[child.id]?.text}</p>
            {/if}
            {#if toggleShared[child.id]}
              <p class="notice" role="status" data-toggle-shared>{toggleShared[child.id]}</p>
            {/if}
            {#if services !== null}
              <!-- The catalogue feeds the picker; without it there is nothing to pick from. -->
              <BlockPicker catalogue={services} view={v} onpick={(s) => pickBlock(child.id, s)} />
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
  .history-link {
    margin: 0 0 0.5rem;
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
  .services .controls {
    display: flex;
    gap: 0.25rem;
    margin-left: auto;
  }
  .services .controls button {
    min-height: 2.75rem;
  }
  .partial,
  .none,
  .hint,
  .notice {
    color: #666;
  }
  .error {
    color: #b00020;
  }
</style>
