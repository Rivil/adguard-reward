// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen } from '@testing-library/svelte'
import { afterEach, describe, expect, it, vi } from 'vitest'
import PinPad from './PinPad.svelte'

type Call = { method: string; url: string; init: RequestInit }

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function stubUnlock(answer: () => Response | Promise<Response>): Call[] {
  const calls: Call[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit) => {
      const method = init?.method ?? 'GET'
      calls.push({ method, url, init })
      if (method !== 'POST' || url !== '/api/v1/unlock') throw new Error('unexpected fetch ' + method + ' ' + url)
      return answer()
    }),
  )
  return calls
}

function setup() {
  const onUnlocked = vi.fn()
  const onRevoked = vi.fn()
  render(PinPad, { onUnlocked, onRevoked })
  return { onUnlocked, onRevoked }
}

const pinInput = () => screen.getByLabelText('PIN') as HTMLInputElement
const unlockButton = () => screen.getByRole('button', { name: 'Unlock' }) as HTMLButtonElement

async function tap(...digits: string[]) {
  for (const d of digits) await fireEvent.click(screen.getByRole('button', { name: d }))
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('PinPad', () => {
  it('tapping 1,2,3,4 and submitting POSTs exactly {pin:"1234"}', async () => {
    const calls = stubUnlock(() => new Response(null, { status: 204 }))
    const { onUnlocked, onRevoked } = setup()

    await tap('1', '2', '3', '4')
    expect(pinInput().value).toBe('1234')
    await fireEvent.click(unlockButton())

    await vi.waitFor(() => expect(onUnlocked).toHaveBeenCalledTimes(1))
    expect(calls).toHaveLength(1)
    expect(calls[0].init.body).toBe('{"pin":"1234"}')
    expect((calls[0].init.headers as Record<string, string>)['X-Requested-With']).toBe('adguard-reward')
    expect(onRevoked).not.toHaveBeenCalled()
  })

  it('a wrong PIN shows attempts left, keeps the pad and clears the input', async () => {
    stubUnlock(() => json(401, { error: 'bad_pin', message: 'wrong pin', attempts_remaining: 4 }))
    const { onUnlocked, onRevoked } = setup()

    await tap('9', '9', '9', '9')
    await fireEvent.click(unlockButton())

    expect((await screen.findByRole('alert')).textContent).toContain('4 attempts left')
    expect(screen.getByRole('dialog')).toBeTruthy()
    expect(pinInput().value).toBe('')
    expect(unlockButton().disabled).toBe(true)
    expect(onUnlocked).not.toHaveBeenCalled()
    expect(onRevoked).not.toHaveBeenCalled()
  })

  it('the last wrong PIN before revocation reads "1 attempt left"', async () => {
    stubUnlock(() => json(401, { error: 'bad_pin', message: 'wrong pin', attempts_remaining: 1 }))
    setup()
    await tap('9', '9', '9', '9')
    await fireEvent.click(unlockButton())
    expect((await screen.findByRole('alert')).textContent).toContain('1 attempt left')
  })

  it.each(['device_revoked', 'unauthorized'])('401 %s from unlock calls onRevoked', async (code) => {
    stubUnlock(() => json(401, { error: code, message: 'nope' }))
    const { onUnlocked, onRevoked } = setup()
    await tap('1', '2', '3', '4')
    await fireEvent.click(unlockButton())
    await vi.waitFor(() => expect(onRevoked).toHaveBeenCalledTimes(1))
    expect(onUnlocked).not.toHaveBeenCalled()
  })

  it('a server error shows a message and stays on the pad', async () => {
    stubUnlock(() => json(500, { error: 'internal', message: 'boom' }))
    const { onUnlocked, onRevoked } = setup()
    await tap('1', '2', '3', '4')
    await fireEvent.click(unlockButton())
    expect((await screen.findByRole('alert')).getAttribute('data-error')).toBe('internal')
    expect(screen.getByRole('dialog')).toBeTruthy()
    expect(onUnlocked).not.toHaveBeenCalled()
    expect(onRevoked).not.toHaveBeenCalled()
  })

  it('submit is disabled below 4 digits and non-digits never reach the input', async () => {
    const calls = stubUnlock(() => new Response(null, { status: 204 }))
    setup()

    expect(unlockButton().disabled).toBe(true)
    await tap('1', '2', '3')
    expect(unlockButton().disabled).toBe(true)
    await fireEvent.submit(unlockButton().form!)
    expect(calls).toHaveLength(0)

    // Typed input: letters, spaces and non-ASCII digits are dropped.
    await fireEvent.input(pinInput(), { target: { value: '12a3' } })
    expect(pinInput().value).toBe('123')
    await fireEvent.input(pinInput(), { target: { value: '1 2٣４' } })
    expect(pinInput().value).toBe('12')
    expect(unlockButton().disabled).toBe(true)

    await fireEvent.input(pinInput(), { target: { value: '1234' } })
    expect(unlockButton().disabled).toBe(false)
  })

  it('caps the PIN at 6 digits and Delete removes the last one', async () => {
    stubUnlock(() => new Response(null, { status: 204 }))
    setup()
    await tap('1', '2', '3', '4', '5', '6', '7')
    expect(pinInput().value).toBe('123456')
    await fireEvent.input(pinInput(), { target: { value: '12345678' } })
    expect(pinInput().value).toBe('123456')
    await fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    expect(pinInput().value).toBe('12345')
  })

  it('has one key per digit and each key types its own digit', async () => {
    stubUnlock(() => new Response(null, { status: 204 }))
    setup()

    const names = screen.getAllByRole('button').map((b) => b.getAttribute('aria-label') ?? b.textContent)
    expect(names).toEqual(['1', '2', '3', '4', '5', '6', '7', '8', '9', '0', 'Delete', 'Unlock'])

    await tap('7', '8', '9', '0')
    expect(pinInput().value).toBe('7890')
    for (let i = 0; i < 4; i++) await fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    expect(pinInput().value).toBe('')
    await tap('1', '2', '3', '4', '5', '6')
    expect(pinInput().value).toBe('123456')
  })

  it('a full 6-digit PIN can be submitted', async () => {
    const calls = stubUnlock(() => new Response(null, { status: 204 }))
    const { onUnlocked } = setup()

    await tap('1', '2', '3', '4', '5', '6')
    expect(unlockButton().disabled).toBe(false)
    await fireEvent.click(unlockButton())

    await vi.waitFor(() => expect(onUnlocked).toHaveBeenCalledTimes(1))
    expect(calls.map((c) => c.init.body)).toEqual(['{"pin":"123456"}'])
  })

  it('Delete is disabled on an empty PIN and enabled once a digit is in', async () => {
    stubUnlock(() => new Response(null, { status: 204 }))
    setup()
    const del = () => screen.getByRole('button', { name: 'Delete' }) as HTMLButtonElement

    expect(del().disabled).toBe(true)
    await tap('5')
    expect(del().disabled).toBe(false)
    await fireEvent.click(del())
    expect(pinInput().value).toBe('')
    expect(del().disabled).toBe(true)
  })

  it('while unlocking: the label reads Unlocking…, the pad is frozen and the PIN kept', async () => {
    let answer!: (r: Response) => void
    const held = new Promise<Response>((resolve) => {
      answer = resolve
    })
    const calls = stubUnlock(() => held)
    const { onUnlocked } = setup()

    await tap('1', '2', '3', '4')
    await fireEvent.click(unlockButton())

    const busy = screen.getByRole('button', { name: 'Unlocking…' }) as HTMLButtonElement
    expect(busy.disabled).toBe(true)
    expect(screen.queryByRole('button', { name: 'Unlock' })).toBeNull()
    expect(pinInput().disabled).toBe(true)
    const del = screen.getByRole('button', { name: 'Delete' }) as HTMLButtonElement
    expect(del.disabled).toBe(true)
    for (const d of ['1', '2', '3', '4', '5', '6', '7', '8', '9', '0']) {
      expect((screen.getByRole('button', { name: d }) as HTMLButtonElement).disabled, d).toBe(true)
    }

    // A click that still reaches Delete or the form changes nothing in flight.
    await fireEvent.click(del)
    expect(pinInput().value).toBe('1234')
    await fireEvent.submit(busy.form!)
    expect(calls).toHaveLength(1)

    answer(new Response(null, { status: 204 }))
    await vi.waitFor(() => expect(onUnlocked).toHaveBeenCalledTimes(1))
    expect(unlockButton().textContent).toBe('Unlock')
    expect(pinInput().disabled).toBe(false)
  })

  it('a retry clears the old error at once and a success empties the PIN', async () => {
    let attempt = 0
    let answer!: (r: Response) => void
    const held = new Promise<Response>((resolve) => {
      answer = resolve
    })
    stubUnlock(() => {
      attempt++
      return attempt === 1 ? json(401, { error: 'bad_pin', message: 'wrong pin', attempts_remaining: 4 }) : held
    })
    const { onUnlocked } = setup()

    await tap('9', '9', '9', '9')
    await fireEvent.click(unlockButton())
    expect((await screen.findByRole('alert')).textContent).toContain('4 attempts left')

    await tap('1', '2', '3', '4')
    await fireEvent.click(unlockButton())
    // In flight: the stale "attempts left" line is already gone.
    expect(screen.getByRole('button', { name: 'Unlocking…' })).toBeTruthy()
    expect(screen.queryByRole('alert')).toBeNull()

    answer(new Response(null, { status: 204 }))
    await vi.waitFor(() => expect(onUnlocked).toHaveBeenCalledTimes(1))
    expect(screen.queryByRole('alert')).toBeNull()
    expect(pinInput().value).toBe('')
  })

  it('a network failure shows the connection message and stays on the pad', async () => {
    stubUnlock(() => Promise.reject(new TypeError('Failed to fetch')))
    const { onUnlocked, onRevoked } = setup()

    await tap('1', '2', '3', '4')
    await fireEvent.click(unlockButton())

    const alert = await screen.findByRole('alert')
    expect(alert.getAttribute('data-error')).toBe('network')
    expect(alert.textContent).toBe("Can't reach the server — check your connection")
    expect(pinInput().value).toBe('')
    expect(unlockButton().textContent).toBe('Unlock')
    expect(onUnlocked).not.toHaveBeenCalled()
    expect(onRevoked).not.toHaveBeenCalled()
  })
})
