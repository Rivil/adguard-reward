// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { tick } from 'svelte'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Login from './Login.svelte'

function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  })
}

function mockFetch(...responses: Response[]): void {
  const queue = [...responses]
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => {
      const next = queue.shift()
      if (!next) throw new Error('unexpected fetch ' + url)
      return next
    }),
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
})

/** Render the page, fill both fields and submit; resolves once the fetch settled. */
async function signIn(onSuccess = vi.fn()): Promise<void> {
  render(Login, { onSuccess })
  await fireEvent.input(screen.getByLabelText('Username'), { target: { value: 'mum' } })
  await fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'pw' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
}

async function alertText(): Promise<string> {
  const el = await screen.findByRole('alert')
  return el.textContent ?? ''
}

describe('Login', () => {
  it('fields start empty and idle', () => {
    render(Login, { onSuccess: vi.fn() })
    const username = screen.getByLabelText('Username') as HTMLInputElement
    const password = screen.getByLabelText('Password') as HTMLInputElement
    const button = screen.getByRole('button') as HTMLButtonElement
    expect(username.value).toBe('')
    expect(password.value).toBe('')
    expect(username.disabled).toBe(false)
    expect(password.disabled).toBe(false)
    // Phone keyboards capitalise the first letter of a text field by default;
    // AdGuard usernames are case-sensitive.
    expect(username.getAttribute('autocapitalize')).toBe('none')
    expect(button.disabled).toBe(false)
    expect(button.textContent).toBe('Sign in')
  })

  it('locks the form while signing in', async () => {
    let settle!: (r: Response) => void
    const inFlight = new Promise<Response>((resolve) => {
      settle = resolve
    })
    vi.stubGlobal('fetch', vi.fn(() => inFlight))
    const onSuccess = vi.fn()
    await signIn(onSuccess)
    await tick()

    const username = screen.getByLabelText('Username') as HTMLInputElement
    const password = screen.getByLabelText('Password') as HTMLInputElement
    const button = screen.getByRole('button') as HTMLButtonElement
    expect(username.disabled).toBe(true)
    expect(password.disabled).toBe(true)
    expect(button.disabled).toBe(true)
    expect(button.textContent).toBe('Signing in…')
    expect(onSuccess).not.toHaveBeenCalled()

    settle(new Response(null, { status: 204 }))
    await waitFor(() => expect(button.disabled).toBe(false))
    expect(username.disabled).toBe(false)
    expect(password.disabled).toBe(false)
    expect(button.textContent).toBe('Sign in')
    expect(onSuccess).toHaveBeenCalledExactlyOnceWith('mum')
  })

  it('shows distinct messages for wrong password, AdGuard unreachable and rate limit', async () => {
    // The server's message is deliberately unhelpful; the page shows its own line per code.
    mockFetch(json(401, { error: 'bad_credentials', message: 'nope' }))
    await signIn()
    expect(await alertText()).toBe('Wrong username or password')
    expect(screen.getByRole('alert').getAttribute('data-error')).toBe('bad_credentials')

    mockFetch(json(502, { error: 'adguard_unavailable', message: 'down' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(await alertText()).toBe("Can't reach AdGuard Home — try again in a moment")
    expect(screen.getByRole('alert').getAttribute('data-error')).toBe('adguard_unavailable')

    mockFetch(json(429, { error: 'rate_limited', message: 'slow down' }, { 'Retry-After': '120' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(await alertText()).toBe('Too many attempts — wait 120s')
    expect(screen.getByRole('alert').getAttribute('data-error')).toBe('rate_limited')
  })

  it('shows no message before a failed attempt', () => {
    render(Login, { onSuccess: vi.fn() })
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('calls onSuccess with the username on 204 and shows no alert', async () => {
    mockFetch(new Response(null, { status: 204 }))
    const onSuccess = vi.fn()
    await signIn(onSuccess)
    expect(onSuccess).toHaveBeenCalledExactlyOnceWith('mum')
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('shows a network message when fetch itself fails', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new TypeError('Failed to fetch')
      }),
    )
    await signIn()
    expect(screen.getByRole('alert').getAttribute('data-error')).toBe('network')
    expect(await alertText()).toBe("Can't reach the server — check your connection")
  })
})
