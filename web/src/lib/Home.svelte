<script lang="ts">
  import { logout } from './api'

  let { username, onLogout }: { username: string; onLogout: () => void } = $props()

  let pending = $state(false)

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
</script>

<main class="home">
  <h1>adguard-reward</h1>
  <p>Signed in as <strong>{username}</strong></p>
  <button type="button" onclick={signOut} disabled={pending}>Log out</button>
</main>

<style>
  .home {
    max-width: 40rem;
    margin: 10vh auto;
    padding: 0 1rem;
  }
</style>
