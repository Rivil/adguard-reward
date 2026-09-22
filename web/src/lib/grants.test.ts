// Stryker disable all: test sources are not mutation targets
import { describe, expect, it } from 'vitest'
import type { Grant } from './api'
import {
  MAX_DURATION,
  MIN_DURATION,
  formatCountdown,
  formatDuration,
  minutesToSeconds,
  overlapsFor,
  ownDuration,
  remainingSeconds,
  secondsToMinutes,
} from './grants'

const T0 = Date.parse('2026-09-21T10:00:00Z')

function grant(id: number, child_id: number, services: string[], spanSeconds = 3600): Grant {
  return {
    id,
    child_id,
    services,
    clients: ['c'],
    started_at: new Date(T0).toISOString(),
    ends_at: new Date(T0 + spanSeconds * 1000).toISOString(),
  }
}

describe('bounds', () => {
  it('mirror the server', () => {
    expect(MIN_DURATION).toBe(60)
    expect(MAX_DURATION).toBe(86400)
  })
})

describe('remainingSeconds', () => {
  it('rounds up and never goes negative', () => {
    expect(remainingSeconds(new Date(T0 + 90200).toISOString(), T0)).toBe(91)
    expect(remainingSeconds(new Date(T0 + 90000).toISOString(), T0)).toBe(90)
    expect(remainingSeconds(new Date(T0).toISOString(), T0)).toBe(0)
    expect(remainingSeconds(new Date(T0 - 5000).toISOString(), T0)).toBe(0)
  })
})

describe('formatCountdown', () => {
  it('uses h:mm:ss from an hour and m:ss below', () => {
    expect(formatCountdown(3661)).toBe('1:01:01')
    expect(formatCountdown(3600)).toBe('1:00:00')
    expect(formatCountdown(3599)).toBe('59:59')
    expect(formatCountdown(59)).toBe('0:59')
    expect(formatCountdown(600)).toBe('10:00')
    expect(formatCountdown(0)).toBe('0:00')
    expect(formatCountdown(86400)).toBe('24:00:00')
  })

  it('pads below ten only', () => {
    // Exactly 10 is the boundary: two digits already, so no leading zero.
    expect(formatCountdown(610)).toBe('10:10')
    expect(formatCountdown(36610)).toBe('10:10:10')
    expect(formatCountdown(3609)).toBe('1:00:09')
    expect(formatCountdown(9)).toBe('0:09')
  })
})

describe('formatDuration', () => {
  it('names hours and minutes', () => {
    expect(formatDuration(3600)).toBe('1 h')
    expect(formatDuration(1800)).toBe('30 min')
    expect(formatDuration(5400)).toBe('1 h 30 min')
    expect(formatDuration(60)).toBe('1 min')
    expect(formatDuration(86400)).toBe('24 h')
  })
})

describe('ownDuration', () => {
  it('is the grant span clamped to the server bounds', () => {
    expect(ownDuration(grant(1, 1, ['a'], 3600))).toBe(3600)
    expect(ownDuration(grant(1, 1, ['a'], 1800))).toBe(1800)
    expect(ownDuration(grant(1, 1, ['a'], 48 * 3600))).toBe(86400)
    expect(ownDuration(grant(1, 1, ['a'], 10))).toBe(60)
  })
})

describe('overlapsFor', () => {
  const A = grant(1, 1, ['youtube'])
  const B = grant(2, 1, ['tiktok'])
  const C = grant(3, 2, ['youtube'])
  const all = [B, C, A] // deliberately unsorted

  it('groups by child and shared service, id ASC, remaining in input order', () => {
    expect(overlapsFor(1, ['youtube', 'tiktok', 'roblox'], all)).toEqual({ overlapping: [A, B], remaining: ['roblox'] })
    expect(overlapsFor(1, ['roblox'], all)).toEqual({ overlapping: [], remaining: ['roblox'] })
    expect(overlapsFor(2, ['youtube'], all)).toEqual({ overlapping: [C], remaining: [] })
    expect(overlapsFor(1, [], all)).toEqual({ overlapping: [], remaining: [] })
    expect(overlapsFor(1, ['roblox', 'tiktok'], all)).toEqual({ overlapping: [B], remaining: ['roblox'] })
  })

  it('one shared service is enough to overlap', () => {
    // A bundled grant only partly covered by the tap still collides on the
    // shared service; its other services do not have to be in the tap.
    const D = grant(4, 1, ['youtube', 'tiktok'])
    expect(overlapsFor(1, ['youtube', 'roblox'], [D])).toEqual({ overlapping: [D], remaining: ['roblox'] })
    expect(overlapsFor(1, ['roblox'], [D])).toEqual({ overlapping: [], remaining: ['roblox'] })
  })

  it('does not mutate the input list', () => {
    const copy = [...all]
    overlapsFor(1, ['youtube'], all)
    expect(all).toEqual(copy)
  })
})

describe('minutes', () => {
  it('convert without fractions', () => {
    expect(minutesToSeconds(90)).toBe(5400)
    expect(minutesToSeconds(1)).toBe(60)
    expect(secondsToMinutes(5400)).toBe(90)
    expect(secondsToMinutes(90)).toBe(2)
    expect(secondsToMinutes(60)).toBe(1)
  })
})
