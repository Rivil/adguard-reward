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
    expect(await screen.findByRole('alert')).toBeTruthy()
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
})
