// Stryker disable all: test sources are not mutation targets
import { describe, expect, it } from 'vitest'
import type { HistoryGrant, HistoryService } from './api'
import { appendPage, endTimeOf, formatTime, removedText, statusLabel } from './history'

function grant(id: number, over: Partial<HistoryGrant> = {}): HistoryGrant {
  return {
    id,
    status: 'expired',
    started_at: '2026-10-01T10:00:00Z',
    ends_at: '2026-10-01T11:00:00Z',
    ended_at: '2026-10-01T11:00:05Z',
    services: [],
    extensions: [],
    ...over,
  }
}

describe('statusLabel', () => {
  it('labels every end status, never calling a legacy end "Ended early"', () => {
    expect(statusLabel('ongoing')).toBe('Ongoing')
    expect(statusLabel('expired')).toBe('Expired')
    expect(statusLabel('ended_early')).toBe('Ended early')
    expect(statusLabel('permanent_block')).toBe('Ended by permanent block')
    expect(statusLabel('ended_unknown')).toBe('Ended (reason unknown)')
    expect(statusLabel('ended_unknown')).not.toBe(statusLabel('ended_early'))
  })
})

describe('endTimeOf', () => {
  it('returns ended_at, never the scheduled ends_at', () => {
    const g = grant(1, { status: 'ended_early', ends_at: '2026-10-01T11:00:00Z', ended_at: '2026-10-01T10:20:00Z' })
    expect(endTimeOf(g)).toBe('2026-10-01T10:20:00Z')
  })

  it('has no end time for an ongoing grant even when ends_at is in the past', () => {
    const g = grant(1, { status: 'ongoing', ends_at: '2000-01-01T00:00:00Z', ended_at: null })
    expect(endTimeOf(g)).toBeNull()
    expect(endTimeOf({ status: 'ongoing', ended_at: '2026-10-01T10:20:00Z' })).toBeNull()
  })

  it('is null for a finished grant the server sent no ended_at for', () => {
    expect(endTimeOf({ status: 'ended_unknown', ended_at: null })).toBeNull()
  })
})

describe('removedText', () => {
  const live: HistoryService = { id: 'youtube', name: 'YouTube', removed_at: null, removed_reason: null }

  it('is null for a service that stayed on the grant', () => {
    expect(removedText(live)).toBeNull()
  })

  it('marks a trimmed service with its time and reason', () => {
    const at = '2026-10-01T10:30:00Z'
    expect(removedText({ ...live, removed_at: at, removed_reason: 'permanent_block' })).toBe(
      `removed ${formatTime(at)} · permanent block`,
    )
  })

  it('marks a removed service with only its time when the server sent no reason', () => {
    const at = '2026-10-01T10:30:00Z'
    expect(removedText({ ...live, removed_at: at, removed_reason: null })).toBe(`removed ${formatTime(at)}`)
  })
})

describe('formatTime', () => {
  it('passes an unparseable timestamp through rather than "Invalid Date"', () => {
    expect(formatTime('not a time')).toBe('not a time')
    expect(formatTime('2026-10-01T10:30:00Z')).not.toBe('2026-10-01T10:30:00Z')
  })

  it('writes a medium date and a short time in the parent locale', () => {
    const iso = '2026-10-01T10:30:45Z'
    const want = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(iso))
    expect(formatTime(iso)).toBe(want)
    expect(formatTime(iso)).not.toBe(new Date(iso).toLocaleString())
  })
})

describe('appendPage', () => {
  it('appends the older page after the shown one, dropping duplicates by id', () => {
    const shown = [grant(9), grant(8)]
    const out = appendPage(shown, [grant(8, { status: 'ended_early' }), grant(7), grant(7), grant(6)])
    expect(out.map((g) => g.id)).toEqual([9, 8, 7, 6])
    expect(out[1].status).toBe('expired')
    expect(shown.map((g) => g.id)).toEqual([9, 8])
  })
})
