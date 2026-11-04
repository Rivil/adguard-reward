<script lang="ts">
  import { onMount } from 'svelte'
  import { historyChild, me, navigate, route } from './lib/api'
  import Login from './lib/Login.svelte'
  import Home from './lib/Home.svelte'
  import Children from './lib/Children.svelte'
  import Buttons from './lib/Buttons.svelte'
  import Services from './lib/Services.svelte'
  import History from './lib/History.svelte'

  let username = $state<string | null>(null)
  let ready = $state(false)

  async function refresh() {
    try {
      const m = await me()
      username = m.username
      // A reload on any page stays there, /history keeping its ?child=; only
      // the login page bounces home.
      navigate($route === 'login' ? 'home' : $route, { child: $historyChild })
    } catch {
      // A 401 already routed to login through onUnauthorized; any other
      // failure also lands there rather than on a blank page.
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
</script>

{#if ready}
  {#if $route === 'home' && username !== null}
    <Home {username} onLogout={signedOut} />
  {:else if $route === 'children' && username !== null}
    <Children onBack={() => navigate('home')} />
  {:else if $route === 'buttons' && username !== null}
    <Buttons onBack={() => navigate('home')} />
  {:else if $route === 'services' && username !== null}
    <Services onBack={() => navigate('home')} />
  {:else if $route === 'history' && username !== null}
    <History child={$historyChild} onBack={() => navigate('home')} />
  {:else}
    <Login onSuccess={refresh} />
  {/if}
{/if}
