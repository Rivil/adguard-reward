// Stryker disable all: test sources are not mutation targets
import { describe, expect, it, vi } from 'vitest'
import { ApiError, type Grant, type GrantCreated } from './api'
import { acceptOffer, runTap, type Offer, type TapDeps, type TapSpec } from './tap'

const ENDS = '2026-09-21T11:00:00Z'

function grant(id: number, child_id: number, services: string[]): Grant {
  return { id, child_id, services, clients: ['Kid phone'], started_at: '2026-09-21T10:00:00Z', ends_at: ENDS }
}

const b1: TapSpec = { key: 'button:1', child_id: 1, services: ['youtube', 'tiktok'], duration: 3600, clients: ['Kid phone'] }

type Fake = TapDeps & {
  createGrant: ReturnType<typeof vi.fn<TapDeps['createGrant']>>
  extendGrant: ReturnType<typeof vi.fn<TapDeps['extendGrant']>>
  refresh: ReturnType<typeof vi.fn<TapDeps['refresh']>>
  merge: ReturnType<typeof vi.fn<TapDeps['merge']>>
}

function fake(list: Grant[] = []): Fake {
  return {
    createGrant: vi.fn<TapDeps['createGrant']>(async () => ({ id: 9, ends_at: ENDS, applied: true, failed: [] })),
    extendGrant: vi.fn<TapDeps['extendGrant']>(async (id) => ({ id, ends_at: '2026-09-21T12:00:00Z' })),
    refresh: vi.fn<TapDeps['refresh']>(async () => {}),
    merge: vi.fn<TapDeps['merge']>(),
    grants: () => list,
  }
}

const created = (over: Partial<GrantCreated> = {}): GrantCreated => ({ id: 9, ends_at: ENDS, applied: true, failed: [], ...over })
const conflict = (grantId?: number) => new ApiError(409, 'conflict', 'youtube is already granted by grant 5', undefined, grantId)

describe('runTap', () => {
  it('posts once and merges the 201', async () => {
    const d = fake()
    let release!: (g: GrantCreated) => void
    d.createGrant.mockImplementationOnce(() => new Promise<GrantCreated>((r) => (release = r)))

    const first = runTap(b1, d)
    expect(d.createGrant).toHaveBeenCalledTimes(1)
    expect(await runTap(b1, d)).toEqual({ kind: 'ignored' })
    expect(d.createGrant).toHaveBeenCalledTimes(1)

    release(created())
    const out = await first
    expect(d.createGrant).toHaveBeenCalledExactlyOnceWith(1, ['youtube', 'tiktok'], 3600)
    expect(out.kind).toBe('applied')
    const merged = d.merge.mock.calls[0][0]
    expect(merged).toEqual({
      id: 9,
      child_id: 1,
      services: ['youtube', 'tiktok'],
      clients: ['Kid phone'],
      started_at: '2026-09-21T10:00:00.000Z',
      ends_at: ENDS,
    })
    expect(d.refresh).toHaveBeenCalledTimes(1)
    expect(d.merge.mock.invocationCallOrder[0]).toBeLessThan(d.refresh.mock.invocationCallOrder[0])

    // The key is free again once the first tap settled.
    await runTap(b1, d)
    expect(d.createGrant).toHaveBeenCalledTimes(2)
  })

  it('partial names the client', async () => {
    const d = fake()
    d.createGrant.mockResolvedValueOnce(created({ applied: false, failed: ['Kid tablet'] }))
    const out = await runTap(b1, d)
    expect(out.kind).toBe('partial')
    if (out.kind === 'partial') {
      expect(out.failed).toEqual(['Kid tablet'])
      expect(out.grant.id).toBe(9)
    }
    expect(d.merge).toHaveBeenCalledTimes(1)
    expect(d.refresh).toHaveBeenCalledTimes(1)

    // An envelope that omits `failed` still reports partial, with nobody named.
    const bare = fake()
    bare.createGrant.mockResolvedValueOnce({ id: 9, ends_at: ENDS, applied: false } as GrantCreated)
    const none = await runTap(b1, bare)
    expect(none).toMatchObject({ kind: 'partial', failed: [] })
  })

  it('errors carry code and message', async () => {
    const cases: Array<[unknown, string, RegExp]> = [
      [new TypeError('Failed to fetch'), 'network', /reach the server/],
      [new ApiError(422, 'unprocessable', 'unknown service "gone"'), 'unprocessable', /gone/],
      [new ApiError(502, 'adguard_unavailable', 'upstream'), 'adguard_unavailable', /AdGuard Home/],
    ]
    for (const [err, code, message] of cases) {
      const d = fake()
      d.createGrant.mockRejectedValueOnce(err)
      const out = await runTap(b1, d)
      expect(out).toMatchObject({ kind: 'error', code })
      if (out.kind === 'error') expect(out.message).toMatch(message)
      expect(d.merge).not.toHaveBeenCalled()
      expect(d.refresh).not.toHaveBeenCalled()
    }
  })

  it('409 refreshes and computes targets', async () => {
    const A = grant(5, 1, ['youtube'])
    const d = fake([A])
    d.createGrant.mockRejectedValueOnce(conflict(5))
    const out = await runTap(b1, d)
    expect(d.refresh).toHaveBeenCalledTimes(1)
    expect(out).toEqual({ kind: 'offer', targets: [A], remaining: ['tiktok'] })

    const stale = fake([])
    stale.createGrant.mockRejectedValueOnce(conflict(5))
    stale.refresh.mockRejectedValueOnce(new TypeError('Failed to fetch'))
    const fallback = await runTap(b1, stale)
    // The stub carries nothing but the id the envelope named: no services
    // (so nothing counts as already covered), no clients, no timestamps.
    expect(fallback).toEqual({
      kind: 'offer',
      targets: [{ id: 5, child_id: 1, services: [], clients: [], started_at: '', ends_at: '' }],
      remaining: ['youtube', 'tiktok'],
    })

    const noId = fake([])
    noId.createGrant.mockRejectedValueOnce(conflict(undefined))
    expect((await runTap(b1, noId)).kind).toBe('error')
  })
})

describe('acceptOffer', () => {
  const spec: TapSpec = { key: 'button:2', child_id: 1, services: ['youtube', 'tiktok', 'roblox'], duration: 3600, clients: ['Kid phone'] }
  const A = grant(3, 1, ['youtube'])
  const B = grant(1, 1, ['tiktok'])

  it('extends each target then creates the rest', async () => {
    const d = fake([A, B])
    const offer: Offer = { targets: [A, B], remaining: ['roblox'] }
    const out = await acceptOffer(offer, spec, d)
    expect(d.extendGrant.mock.calls).toEqual([
      [1, 3600],
      [3, 3600],
    ])
    expect(d.createGrant).toHaveBeenCalledExactlyOnceWith(1, ['roblox'], 3600)
    expect(d.refresh).toHaveBeenCalledTimes(1)
    const order = [...d.extendGrant.mock.invocationCallOrder, ...d.createGrant.mock.invocationCallOrder, ...d.refresh.mock.invocationCallOrder]
    expect([...order].sort((a, b) => a - b)).toEqual(order)
    expect(out.kind).toBe('applied')
    if (out.kind === 'applied') expect(out.grant.id).toBe(9)

    const only = fake([A])
    const single: TapSpec = { ...spec, services: ['youtube'] }
    const res = await acceptOffer({ targets: [A], remaining: [] }, single, only)
    expect(only.extendGrant).toHaveBeenCalledExactlyOnceWith(3, 3600)
    expect(only.createGrant).not.toHaveBeenCalled()
    expect(only.refresh).toHaveBeenCalledTimes(1)
    expect(res.kind).toBe('applied')
    if (res.kind === 'applied') expect(res.grant).toEqual({ ...A, ends_at: '2026-09-21T12:00:00Z' })
    expect(only.merge).toHaveBeenCalledWith({ ...A, ends_at: '2026-09-21T12:00:00Z' })
  })

  it('retries create on 404', async () => {
    const d = fake([A, B])
    d.extendGrant.mockImplementation(async (id) => {
      if (id === 3) throw new ApiError(404, 'not_found', 'no such grant')
      return { id, ends_at: '2026-09-21T12:00:00Z' }
    })
    const out = await acceptOffer({ targets: [A, B], remaining: ['roblox'] }, spec, d)
    expect(d.createGrant).toHaveBeenCalledExactlyOnceWith(1, ['youtube', 'roblox'], 3600)
    expect(out.kind).toBe('applied')

    const again = fake([A, B])
    again.extendGrant.mockImplementation(async (id) => {
      if (id === 3) throw new ApiError(404, 'not_found', 'no such grant')
      return { id, ends_at: '2026-09-21T12:00:00Z' }
    })
    again.createGrant.mockRejectedValueOnce(conflict(7))
    const res = await acceptOffer({ targets: [A, B], remaining: ['roblox'] }, spec, again)
    expect(res.kind).toBe('error')
    expect(again.createGrant).toHaveBeenCalledTimes(1)
    expect(again.extendGrant).toHaveBeenCalledTimes(2)
    expect(again.refresh).not.toHaveBeenCalled()
  })

  it('a non-404 extend failure stops', async () => {
    const d = fake([A, B])
    d.extendGrant.mockRejectedValueOnce(new ApiError(502, 'adguard_unavailable', 'down'))
    const out = await acceptOffer({ targets: [A, B], remaining: ['roblox'] }, spec, d)
    expect(out).toMatchObject({ kind: 'error', code: 'adguard_unavailable' })
    expect(d.extendGrant).toHaveBeenCalledTimes(1)
    expect(d.createGrant).not.toHaveBeenCalled()
  })

  it('is deduped by key', async () => {
    const d = fake([A])
    let release!: (v: { id: number; ends_at: string }) => void
    d.extendGrant.mockImplementationOnce(() => new Promise((r) => (release = r)))
    const first = acceptOffer({ targets: [A], remaining: [] }, { ...spec, services: ['youtube'] }, d)
    expect(await acceptOffer({ targets: [A], remaining: [] }, { ...spec, services: ['youtube'] }, d)).toEqual({ kind: 'ignored' })
    release({ id: 3, ends_at: ENDS })
    expect((await first).kind).toBe('applied')
  })
})
