<script lang="ts">
  import { onMount } from 'svelte'
  import { ApiError, changePin, exportSetup, importSetup, messageFor, pinStatus, removePin, setPin } from './api'

  // This device's PIN. The first PIN needs only the session; changing or
  // removing one re-checks the AdGuard password (locked pin_change_reauth).
  let { onBack }: { onBack: () => void } = $props()

  const PIN_RE = /^[0-9]{4,6}$/
  const EXPORT_NAME = 'adguard-reward-export.json'
  /** iOS Safari reads the Blob after the click returns, so revoking at once can lose the download. */
  const REVOKE_AFTER_MS = 40_000

  type Problem = { code: string; text: string }

  /** null until GET /api/v1/pin answers. */
  let hasPin = $state<boolean | null>(null)
  let pin = $state('')
  let confirm = $state('')
  let password = $state('')
  let pending = $state(false)
  let error = $state<Problem | null>(null)
  let note = $state<string | null>(null)

  // Backup keeps its own state, so its failures never show in the PIN section.
  let backupPending = $state(false)
  let backupError = $state<Problem | null>(null)
  let backupNote = $state<string | null>(null)
  let missingClients = $state<string[]>([])

  function load() {
    void run(async () => {
      hasPin = (await pinStatus()).has_pin
    })
  }

  onMount(load)

  function failure(err: unknown): Problem {
    return err instanceof ApiError
      ? { code: err.code, text: messageFor(err) }
      : { code: 'network', text: "Can't reach the server — check your connection" }
  }

  /** Run one call; a failure becomes the inline alert and the form keeps what was typed. */
  async function run(fn: () => Promise<void>): Promise<boolean> {
    pending = true
    error = null
    note = null
    try {
      await fn()
      return true
    } catch (err) {
      error = failure(err)
      return false
    } finally {
      pending = false
    }
  }

  function invalid(code: string, text: string) {
    error = { code, text }
    note = null
  }

  /** Client-side checks; a failure never reaches the server. */
  function pinProblem(): string | null {
    if (!PIN_RE.test(pin)) return 'A PIN is 4 to 6 digits'
    if (pin !== confirm) return "The PINs don't match"
    return null
  }

  function clearFields() {
    pin = ''
    confirm = ''
    password = ''
  }

  async function save(e: SubmitEvent) {
    e.preventDefault()
    const problem = pinProblem()
    if (problem !== null) return invalid('invalid_pin', problem)
    const first = hasPin === false
    if (!first && password === '') return invalid('reauth_required', 'Enter your AdGuard password')
    const ok = await run(async () => {
      if (first) await setPin(pin)
      else await changePin(pin, password)
    })
    if (ok) {
      hasPin = true
      clearFields()
      note = first ? 'PIN set' : 'PIN changed'
    }
  }

  async function remove() {
    if (password === '') return invalid('reauth_required', 'Enter your AdGuard password')
    const ok = await run(async () => {
      await removePin(password)
    })
    if (ok) {
      hasPin = false
      clearFields()
      note = 'PIN removed'
    }
  }

  /** Run one Backup call; a failure becomes this section's alert, in the words toProblem picks. */
  async function runBackup(fn: () => Promise<void>, toProblem: (err: unknown) => Problem) {
    backupPending = true
    backupError = null
    backupNote = null
    missingClients = []
    try {
      await fn()
    } catch (err) {
      backupError = toProblem(err)
    } finally {
      backupPending = false
    }
  }

  /** Download through a temporary link: a bare href would skip request()'s 401 handling. */
  function saveFile(file: unknown) {
    const url = URL.createObjectURL(new Blob([JSON.stringify(file, null, 2)], { type: 'application/json' }))
    const link = document.createElement('a')
    link.href = url
    link.download = EXPORT_NAME
    link.click()
    setTimeout(() => URL.revokeObjectURL(url), REVOKE_AFTER_MS)
  }

  function exportFile() {
    void runBackup(async () => {
      saveFile(await exportSetup())
      backupNote = `Downloading ${EXPORT_NAME}`
    }, failure)
  }

  /** A 502 keeps the server's message: messageFor's fixed line would drop the client it names. */
  function importFailure(err: unknown): Problem {
    if (err instanceof ApiError && err.status === 502) {
      return { code: err.code, text: `${err.message} — nothing was imported, it's safe to try again` }
    }
    return failure(err)
  }

  async function chooseFile(e: Event) {
    const input = e.currentTarget as HTMLInputElement
    const file = input.files?.[0]
    // Cleared on every pick: a browser fires no change for the file already selected.
    input.value = ''
    if (!file || backupPending) return
    await runBackup(async () => {
      let text: string
      try {
        text = await file.text()
      } catch {
        backupError = { code: 'unreadable_file', text: "Can't read that file — choose it again" }
        return
      }
      const result = await importSetup(text)
      missingClients = result.missing_clients
      backupNote = result.rules_applied ? 'Setup imported' : 'Setup imported — AdGuard will catch up'
    }, importFailure)
  }
</script>

<main class="settings">
  <h1>Settings</h1>
  <button type="button" onclick={onBack}>Back</button>

  <section aria-labelledby="pin-heading">
    <h2 id="pin-heading">Device PIN</h2>
    <p class="hint">
      With a PIN, this device locks after 15 minutes idle and unlocks with the PIN instead of your password.
    </p>
    {#if hasPin !== null}
      <p class="status" data-has-pin={hasPin}>{hasPin ? 'This device has a PIN' : 'No PIN on this device'}</p>
    {/if}
    {#if error}
      <p class="error" role="alert" data-error={error.code}>{error.text}</p>
    {/if}
    {#if note}
      <p class="note" role="status">{note}</p>
    {/if}
    {#if hasPin === null && error && !pending}
      <button type="button" onclick={load}>Try again</button>
    {/if}

    {#if hasPin !== null}
      <form onsubmit={save}>
        <label class="field">
          <span>{hasPin ? 'New PIN' : 'PIN'}</span>
          <input name="pin" type="password" inputmode="numeric" autocomplete="off" maxlength="6" bind:value={pin} disabled={pending} />
        </label>
        <label class="field">
          <span>Confirm PIN</span>
          <input
            name="confirm"
            type="password"
            inputmode="numeric"
            autocomplete="off"
            maxlength="6"
            bind:value={confirm}
            disabled={pending}
          />
        </label>
        {#if hasPin}
          <label class="field">
            <span>AdGuard password</span>
            <input
              name="password"
              type="password"
              autocomplete="current-password"
              bind:value={password}
              disabled={pending}
            />
          </label>
        {/if}
        <div class="actions">
          <button type="submit" disabled={pending}>{hasPin ? 'Change PIN' : 'Set PIN'}</button>
          {#if hasPin}
            <button type="button" class="danger" onclick={remove} disabled={pending}>Remove PIN</button>
          {/if}
        </div>
        {#if hasPin}
          <p class="hint">Changing or removing the PIN needs your AdGuard password.</p>
        {/if}
      </form>
    {/if}
  </section>

  <section aria-labelledby="backup-heading">
    <h2 id="backup-heading">Backup</h2>
    <p class="hint">
      The file holds your children, their clients, buttons, custom services and permanent blocks, and no passwords or
      PINs. Import works only on a fresh install.
    </p>
    {#if backupError}
      <p class="error" role="alert" data-error={backupError.code}>{backupError.text}</p>
    {/if}
    {#if backupNote}
      <div class="note" role="status">
        <p>{backupNote}</p>
        {#if missingClients.length > 0}
          <p>This AdGuard doesn't have these clients. Their mappings are kept and work if the clients appear:</p>
          <ul>
            {#each missingClients as name (name)}
              <li>{name}</li>
            {/each}
          </ul>
        {/if}
      </div>
    {/if}
    <div class="backup">
      <div class="actions">
        <button type="button" onclick={exportFile} disabled={backupPending}>Export</button>
      </div>
      <label class="field">
        <span>Import a file</span>
        <input name="import" type="file" accept=".json,application/json" onchange={chooseFile} disabled={backupPending} />
      </label>
    </div>
  </section>
</main>

<style>
  .settings {
    max-width: 32rem;
    margin: 0 auto;
    padding: 1rem;
  }
  section {
    margin-top: 1rem;
  }
  form,
  .backup {
    display: grid;
    gap: 0.75rem;
  }
  .field {
    display: grid;
    gap: 0.25rem;
  }
  input {
    font: inherit;
    padding: 0.5rem;
  }
  .actions {
    display: flex;
    gap: 0.5rem;
  }
  .danger {
    color: #b00020;
  }
  .error {
    color: #b00020;
  }
  .hint {
    color: #555;
  }
</style>
