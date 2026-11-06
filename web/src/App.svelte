<script lang="ts">
  import { onMount } from 'svelte'
  import { get } from 'svelte/store'
  import { historyChild, historyFilters, locked, me, navigate, route } from './lib/api'
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

  async function refresh() {
    try {
      const m = await me()
      username = m.username
      // A reload on any page stays there with its URL untouched, so /history
      // keeps its query byte for byte; only the login page bounces home.
      if ($route === 'login') navigate('home')
    } catch {
      // pin_required set `locked` through onPinRequired: stay on this URL
      // behind the PIN pad. A plain 401 already routed to login through
      // onUnauthorized; any other failure also lands there rather than on a
      // blank page.
      if (get(locked)) return
      username = null
      navigate('login')
    }
  }

  onMount(async () => {
    await refresh()
    ready = true
  })

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
