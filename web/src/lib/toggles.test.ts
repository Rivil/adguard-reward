// Stryker disable all: test sources are not mutation targets
import { describe, expect, it } from 'vitest'
import type { BlockedView, Service, ServiceState, ToggleState, Unblock } from './api'
import { controlFor, openOn, pickerOptions, shown } from './toggles'

function svc(
  id: string,
  state: ServiceState['state'],
  toggle: ToggleState | null = null,
  unblocked_by: Unblock[] = [],
  differs: string[] = [],
): ServiceState {
  return { id, name: id, icon: '', custom: false, state, differs, unblocked_by, toggle }
}

function view(...services: ServiceState[]): BlockedView {
  return { child: { id: 1, name: 'A' }, clients: [], services }
}

const bGrant: Unblock = { client: 'tv', grant_id: 5, child: { id: 2, name: 'B' }, permanent: false }
const bPermanent: Unblock = { client: 'tv', grant_id: 0, child: { id: 2, name: 'B' }, permanent: true }

describe('shown', () => {
  it('shown', () => {
    const toggled = svc('youtube', 'unblocked', 'unblocked')
    const untouched = svc('netflix', 'unblocked')
    expect(shown(view(toggled, untouched))).toEqual([toggled])
  })

  it('keeps blocked, partial, held-open and blocked-toggled rows in order', () => {
    const rows = [
      svc('youtube', 'blocked'),
      svc('tiktok', 'partial', null, [], ['tv']),
      svc('roblox', 'unblocked', null, [bGrant]),
      svc('custom:1', 'unblocked', 'blocked'),
      svc('netflix', 'unblocked'),
    ]
    expect(shown(view(...rows)).map((s) => s.id)).toEqual(['youtube', 'tiktok', 'roblox', 'custom:1'])
  })

  it('treats an absent toggle and unblocked_by like null and []', () => {
    const bare: ServiceState = { id: 'netflix', name: 'Netflix', icon: '', state: 'unblocked', differs: [] }
    expect(shown(view(bare))).toEqual([])
  })
})

describe('openOn', () => {
  it('openOn', () => {
    expect(openOn(svc('youtube', 'unblocked', null, [bPermanent]))).toEqual(['tv by B (permanent)'])
    expect(openOn(svc('youtube', 'unblocked', null, [bGrant]))).toEqual(["tv by B's grant"])
    expect(openOn(svc('youtube', 'unblocked', null, [{ ...bGrant, permanent: undefined }]))).toEqual(["tv by B's grant"])
  })

  it('lists plain partial clients first, then attributions in server order', () => {
    const s = svc('tiktok', 'partial', null, [bPermanent, bGrant], ['kid-phone', 'tv'])
    expect(openOn(s)).toEqual(['kid-phone', 'tv by B (permanent)', "tv by B's grant"])
  })

  it('names a removed child', () => {
    const gone = { id: 9, name: '' }
    expect(openOn(svc('youtube', 'unblocked', null, [{ ...bPermanent, child: gone }]))).toEqual([
      'tv by a removed child (permanent)',
    ])
    expect(openOn(svc('youtube', 'unblocked', null, [{ ...bGrant, child: gone }]))).toEqual([
      "tv by a removed child's grant",
    ])
  })

  it('is empty for a plainly blocked service', () => {
    expect(openOn(svc('youtube', 'blocked'))).toEqual([])
    expect(openOn({ id: 'x', name: 'x', icon: '', state: 'blocked', differs: [] })).toEqual([])
  })
})

describe('controlFor', () => {
  it('controlFor', () => {
    expect(controlFor(svc('youtube', 'blocked'))).toEqual(['unblock'])
    expect(controlFor(svc('youtube', 'partial', null, [], ['tv']))).toEqual(['unblock', 'block'])
    expect(controlFor(svc('youtube', 'unblocked', 'unblocked'))).toEqual(['block'])
    expect(controlFor(svc('youtube', 'unblocked', null, [bGrant]))).toEqual([])
    expect(controlFor(svc('youtube', 'unblocked', null, [bPermanent]))).toEqual([])
  })

  it('controlFor disagree', () => {
    expect(controlFor(svc('youtube', 'blocked', 'unblocked'))).toEqual(['unblock', 'block'])
    expect(controlFor(svc('youtube', 'unblocked', 'blocked'))).toEqual(['unblock'])
    expect(controlFor(svc('youtube', 'blocked', 'blocked'))).toEqual(['unblock'])
    expect(controlFor(svc('youtube', 'partial', 'unblocked'))).toEqual(['unblock', 'block'])
    expect(controlFor(svc('youtube', 'partial', 'blocked'))).toEqual(['unblock', 'block'])
  })
})

describe('pickerOptions', () => {
  const catalogue: Service[] = [
    { id: 'youtube', name: 'YouTube', icon: 'x', custom: false },
    { id: 'tiktok', name: 'TikTok', icon: 'x', custom: false },
    { id: 'netflix', name: 'Netflix', icon: 'x', custom: false },
    { id: 'custom:1', name: 'Portal', icon: '', custom: true },
  ]
  const v = view(svc('youtube', 'blocked'), svc('tiktok', 'partial', null, [], ['tv']))

  it('pickerOptions', () => {
    expect(pickerOptions(catalogue, v, '').map((s) => s.id)).toEqual(['tiktok', 'netflix', 'custom:1'])
    expect(pickerOptions(catalogue, v, 'NET').map((s) => s.id)).toEqual(['netflix'])
    expect(pickerOptions(catalogue, v, 'zzz')).toEqual([])
  })

  it('trims the query and never matches a blocked service by name', () => {
    expect(pickerOptions(catalogue, v, '  portal ').map((s) => s.id)).toEqual(['custom:1'])
    expect(pickerOptions(catalogue, v, 'tube')).toEqual([])
  })

  it('keeps an unblocked or toggled-unblocked service', () => {
    const open = view(svc('youtube', 'unblocked', 'unblocked'), svc('netflix', 'unblocked'))
    expect(pickerOptions(catalogue, open, '').map((s) => s.id)).toEqual(['youtube', 'tiktok', 'netflix', 'custom:1'])
  })
})
