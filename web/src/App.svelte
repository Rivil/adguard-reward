<script lang="ts">
  import { onMount } from 'svelte'
  import { me, navigate, route } from './lib/api'
  import Login from './lib/Login.svelte'
  import Home from './lib/Home.svelte'

  let username = $state<string | null>(null)
  let ready = $state(false)

  async function refresh() {
    try {
      const m = await me()
      username = m.username
      navigate('home')
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
  {:else}
    <Login onSuccess={refresh} />
  {/if}
{/if}
