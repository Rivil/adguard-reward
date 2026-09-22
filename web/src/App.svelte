<script lang="ts">
  import { onMount } from 'svelte'
  import { me, navigate, route } from './lib/api'
  import Login from './lib/Login.svelte'
  import Home from './lib/Home.svelte'
  import Children from './lib/Children.svelte'
  import Buttons from './lib/Buttons.svelte'

  let username = $state<string | null>(null)
  let ready = $state(false)

  async function refresh() {
    try {
      const m = await me()
      username = m.username
      // A reload on /children or /buttons stays there; only the login page bounces home.
      navigate($route === 'login' ? 'home' : $route)
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
  {:else}
    <Login onSuccess={refresh} />
  {/if}
{/if}
