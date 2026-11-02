<script lang="ts">
  import { ApiError, login, messageFor } from './api'

  let { onSuccess }: { onSuccess: (username: string) => void } = $props()

  let username = $state('')
  let password = $state('')
  let pending = $state(false)
  let error = $state<{ code: string; text: string } | null>(null)

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    pending = true
    error = null
    try {
      await login(username, password)
      onSuccess(username)
    } catch (err) {
      error =
        err instanceof ApiError
          ? { code: err.code, text: messageFor(err) }
          : { code: 'network', text: "Can't reach the server — check your connection" }
    } finally {
      pending = false
    }
  }
</script>

<main class="login">
  <h1>adguard-reward</h1>
  <p class="hint">Sign in with your AdGuard Home username and password.</p>
  <form onsubmit={submit}>
    <label>
      Username
      <input
        name="username"
        type="text"
        autocomplete="username"
        autocapitalize="none"
        autocorrect="off"
        spellcheck="false"
        required
        bind:value={username}
        disabled={pending}
      />
    </label>
    <label>
      Password
      <input name="password" type="password" autocomplete="current-password" required bind:value={password} disabled={pending} />
    </label>
    {#if error}
      <p class="error" role="alert" data-error={error.code}>{error.text}</p>
    {/if}
    <button type="submit" disabled={pending}>{pending ? 'Signing in…' : 'Sign in'}</button>
  </form>
</main>

<style>
  .login {
    max-width: 22rem;
    margin: 10vh auto;
    padding: 0 1rem;
  }
  form {
    display: grid;
    gap: 0.75rem;
  }
  label {
    display: grid;
    gap: 0.25rem;
  }
  input {
    font: inherit;
    padding: 0.5rem;
  }
  .error {
    margin: 0;
    color: #b00020;
  }
  .hint {
    margin-top: 0;
  }
</style>
