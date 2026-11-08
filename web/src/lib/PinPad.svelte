<script lang="ts">
  import { ApiError, messageFor, unlock } from './api'

  // onUnlocked: the server minted a fresh session. onRevoked: this device
  // can no longer unlock (revoked, expired or no device cookie) — the
  // caller falls back to the full login page.
  let { onUnlocked, onRevoked }: { onUnlocked: () => void; onRevoked: () => void } = $props()

  const MIN = 4
  const MAX = 6
  const DIGITS = ['1', '2', '3', '4', '5', '6', '7', '8', '9', '0']

  let pin = $state('')
  let pending = $state(false)
  let error = $state<{ code: string; text: string } | null>(null)

  const canSubmit = $derived(!pending && pin.length >= MIN && pin.length <= MAX)

  /** Keep only ASCII digits, at most MAX of them: a non-digit never lands. */
  function digitsOnly(v: string): string {
    return v.replace(/[^0-9]/g, '').slice(0, MAX)
  }

  function onInput(e: Event & { currentTarget: HTMLInputElement }) {
    const clean = digitsOnly(e.currentTarget.value)
    // Write back so the field never shows what was filtered out.
    e.currentTarget.value = clean
    pin = clean
  }

  function press(d: string) {
    if (pending || pin.length >= MAX) return
    pin += d
  }

  function backspace() {
    if (pending) return
    pin = pin.slice(0, -1)
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    if (!canSubmit) return
    pending = true
    error = null
    const attempt = pin
    try {
      await unlock(attempt)
      pin = ''
      onUnlocked()
    } catch (err) {
      pin = ''
      if (err instanceof ApiError && err.status === 401 && err.code !== 'bad_pin') {
        // device_revoked, or a plain unauthorized (no or unknown device
        // cookie): a PIN can no longer help.
        onRevoked()
        return
      }
      error =
        err instanceof ApiError
          ? { code: err.code, text: messageFor(err) }
          : { code: 'network', text: "Can't reach the server — check your connection" }
    } finally {
      pending = false
    }
  }
</script>

<div class="overlay" role="dialog" aria-modal="true" aria-labelledby="pinpad-title">
  <form class="pad" onsubmit={submit}>
    <h1 id="pinpad-title">Enter PIN</h1>
    <p class="hint">This device is locked. Enter its PIN to carry on.</p>
    <input
      name="pin"
      type="password"
      inputmode="numeric"
      autocomplete="off"
      aria-label="PIN"
      maxlength={MAX}
      value={pin}
      oninput={onInput}
      disabled={pending}
    />
    <div class="keys">
      {#each DIGITS as d (d)}
        <button type="button" class="key" class:zero={d === '0'} onclick={() => press(d)} disabled={pending}>{d}</button>
      {/each}
      <button type="button" class="key" aria-label="Delete" onclick={backspace} disabled={pending || pin.length === 0}
        >⌫</button
      >
    </div>
    {#if error}
      <p class="error" role="alert" data-error={error.code}>{error.text}</p>
    {/if}
    <button type="submit" class="unlock" disabled={!canSubmit}>{pending ? 'Unlocking…' : 'Unlock'}</button>
  </form>
</div>

<style>
  .overlay {
    position: fixed;
    inset: 0;
    z-index: 100;
    display: grid;
    place-items: center;
    background: var(--bg, #fff);
    padding: 1rem;
  }
  .pad {
    display: grid;
    gap: 0.75rem;
    width: min(18rem, 100%);
  }
  h1,
  .hint {
    margin: 0;
    text-align: center;
  }
  input {
    font: inherit;
    font-size: 1.5rem;
    letter-spacing: 0.5rem;
    text-align: center;
    padding: 0.5rem;
  }
  .keys {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 0.5rem;
  }
  .key {
    font: inherit;
    font-size: 1.25rem;
    padding: 0.75rem 0;
  }
  .zero {
    grid-column: 2;
  }
  .unlock {
    font: inherit;
    padding: 0.6rem;
  }
  .error {
    margin: 0;
    color: #b00020;
    text-align: center;
  }
</style>
