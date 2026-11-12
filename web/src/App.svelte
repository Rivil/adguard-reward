<script lang="ts">
  import { onDestroy, onMount } from 'svelte'
  import { get } from 'svelte/store'
  import { historyChild, historyFilters, locked, me, navigate, route } from './lib/api'
  import { retryUntil, unreachable } from './lib/retry'
  import Login from './lib/Login.svelte'
  import Home from './lib/Home.svelte'
  import Children from './lib/Children.svelte'
  import Buttons from './lib/Buttons.svelte'
  import Services from './lib/Services.svelte'
  import History from './lib/History.svelte'
  import PinPad from './lib/PinPad.svelte'
  import Settings from './lib/Settings.svelte'

  let username = $state<string | null>(null)
  let ready = $state(false)
  // The server could not be reached (network down, tailscale off, or a 5xx
  // from the proxy): the parent is not signed out, so the URL stays put and
  // /me is retried until it answers.
  let offline = $state(false)
  let stopRetry: (() => void) | null = null

  // One /me check. 'unreachable' leaves the page where it is; anything else
  // is a real answer and settles it.
  async function check(): Promise<'ok' | 'unreachable' | 'out'> {
    try {
      const m = await me()
      username = m.username
      offline = false
      // A reload on any page stays there with its URL untouched, so /history
      // keeps its query byte for byte; only the login page bounces home.
      if ($route === 'login') navigate('home')
      return 'ok'
    } catch (err) {
      if (unreachable(err)) return 'unreachable'
      offline = false
      // pin_required set `locked` through onPinRequired: stay on this URL
      // behind the PIN pad. A plain 401 already routed to login through
      // onUnauthorized; any other 4xx also lands there rather than on a
      // blank page.
      if (get(locked)) return 'out'
      username = null
      navigate('login')
      return 'out'
    }
  }

  async function refresh() {
    stopRetry?.()
    stopRetry = null
    if ((await check()) !== 'unreachable') return
    offline = true
    stopRetry = retryUntil(async () => (await check()) !== 'unreachable')
  }

  onMount(async () => {
    await refresh()
    ready = true
  })
  onDestroy(() => stopRetry?.())

  function signedOut() {
    username = null
    navigate('login')
  }

  // While locked the route is not rendered at all, so nothing underneath
  // keeps polling into more 401s; unlocking mounts it afresh and it
  // refetches. refresh() re-reads /me in case the lock came before it did.
  async function unlocked() {
    locked.set(false)
    ready = false
    await refresh()
    ready = true
  }

  function revoked() {
    locked.set(false)
    signedOut()
  }
</script>

{#if $locked}
  <PinPad onUnlocked={unlocked} onRevoked={revoked} />
{:else if offline}
  <main class="offline">
    <p role="alert" data-error="unreachable">Can't reach the server — reconnecting…</p>
    <button type="button" onclick={() => void refresh()}>Try again</button>
  </main>
{:else if ready}
  {#if $route === 'home' && username !== null}
    <Home {username} onLogout={signedOut} />
  {:else if $route === 'children' && username !== null}
    <Children onBack={() => navigate('home')} />
  {:else if $route === 'buttons' && username !== null}
    <Buttons onBack={() => navigate('home')} />
  {:else if $route === 'services' && username !== null}
    <Services onBack={() => navigate('home')} />
  {:else if $route === 'history' && username !== null}
    <History child={$historyChild} filters={$historyFilters} onBack={() => navigate('home')} />
  {:else if $route === 'settings' && username !== null}
    <Settings onBack={() => navigate('home')} />
  {:else}
    <Login onSuccess={refresh} />
  {/if}
{/if}
