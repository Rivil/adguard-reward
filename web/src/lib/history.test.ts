// Stryker disable all: test sources are not mutation targets
import { describe, expect, it } from 'vitest'
import type { HistoryGrant, HistoryService } from './api'
import {
  appendPage,
  dayBounds,
  endTimeOf,
  filterParams,
  formatTime,
  hasFilters,
  parseFilters,
  removedText,
  statusLabel,
  type HistoryFilters,
} from './history'

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

  it('keeps every field of a row that carries more than a plain grant', () => {
    const row = (id: number, name: string) => ({ ...grant(id), child: { id: 1, name } })
    const out = appendPage([row(9, 'Ada')], [row(9, 'Other'), row(8, 'Ben')])
    expect(out.map((g) => g.child.name)).toEqual(['Ada', 'Ben'])
  })
})

const NONE: HistoryFilters = { service: null, status: null, from: null, to: null }

describe('test time zone', () => {
  it('runs in America/Havana, not whatever zone the host is in', () => {
    expect(new Date('2026-01-15T12:00:00Z').getTimezoneOffset()).toBe(300)
    expect(new Date('2026-07-15T12:00:00Z').getTimezoneOffset()).toBe(240)
    // New York reads the same offsets; only here does this midnight not exist.
    expect(new Date(2026, 2, 8).getHours()).toBe(1)
  })
})

describe('dayBounds', () => {
  it('spans a 23-hour day whose midnight the clocks skip', () => {
    expect(dayBounds('2026-03-08', '2026-03-08')).toEqual({
      from: '2026-03-08T05:00:00.000Z',
      until: '2026-03-09T04:00:00.000Z',
    })
  })

  it('spans a 25-hour day whose midnight the clocks repeat', () => {
    expect(dayBounds('2026-11-01', '2026-11-01')).toEqual({
      from: '2026-11-01T04:00:00.000Z',
      until: '2026-11-02T05:00:00.000Z',
    })
  })

  it('reads a day in the local zone, not UTC', () => {
    expect(dayBounds('2026-03-01', null).from).toBe('2026-03-01T05:00:00.000Z')
  })

  it('crosses the end of a month and a year', () => {
    expect(dayBounds(null, '2026-12-31').until).toBe('2027-01-01T05:00:00.000Z')
  })

  it('invents no bound the parent did not set', () => {
    expect(dayBounds('2026-03-01', null)).toEqual({ from: '2026-03-01T05:00:00.000Z' })
    expect(dayBounds(null, '2026-03-01')).toEqual({ until: '2026-03-02T05:00:00.000Z' })
    expect(dayBounds(null, null)).toEqual({})
  })
})

describe('parseFilters', () => {
  it('reads every filter, with or without the leading ?', () => {
    const want = { service: 'custom:5', status: 'expired', from: '2026-03-01', to: '2026-03-31' }
    const query = 'status=expired&to=2026-03-31&from=2026-03-01&service=custom:5&child=3'
    expect(parseFilters(query)).toEqual(want)
    expect(parseFilters('?' + query)).toEqual(want)
  })

  it('reads every end status', () => {
    for (const s of ['ongoing', 'expired', 'ended_early', 'permanent_block', 'ended_unknown']) {
      expect(parseFilters(`status=${s}`).status).toBe(s)
    }
  })

  it('reads no filters from an empty query', () => {
    expect(parseFilters('')).toEqual(NONE)
  })

  it.each([
    'status=bogus',
    'status=constructor',
    'status=',
    'service=',
    'from=2026-02-31',
    'from=2026-13-01',
    'from=2026-00-10',
    'from=2026-01-00',
    'to=yesterday',
    'to=2026-3-1',
    'to=2026-03-01T00:00:00Z',
    'from=',
  ])('treats %s as no filter', (query) => {
    expect(parseFilters(query)).toEqual(NONE)
  })

  it('accepts a leap day only in a leap year', () => {
    expect(parseFilters('from=2028-02-29').from).toBe('2028-02-29')
    expect(parseFilters('from=2026-02-29').from).toBeNull()
  })

  it.each(['0099-01-01', '0202-06-15', '0999-12-31', '9999-12-31'])('drops the out-of-range day %s', (day) => {
    expect(parseFilters(`from=${day}&to=${day}`)).toEqual(NONE)
  })

  it('keeps the first and last years in range', () => {
    expect(parseFilters('from=1000-01-01&to=9998-12-31')).toEqual({ ...NONE, from: '1000-01-01', to: '9998-12-31' })
  })

  it('never lets a day in range serialise past what Go reads', () => {
    expect(dayBounds(null, '9998-12-31').until).toMatch(/^9999-01-01T/)
  })
})

describe('filterParams', () => {
  const values: HistoryFilters[] = [
    { service: 'custom:5', status: 'expired', from: '2026-03-01', to: '2026-03-31' },
    { service: 'a&b=c +', status: 'ended_unknown', from: '2026-11-01', to: '2026-11-02' },
  ]

  it('survives a round trip for every subset of the filters', () => {
    const keys = ['service', 'status', 'from', 'to'] as const
    for (const full of values) {
      for (let bits = 0; bits < 16; bits++) {
        const f = { ...NONE }
        keys.forEach((k, i) => {
          if (bits & (1 << i)) Object.assign(f, { [k]: full[k] })
        })
        expect(parseFilters(filterParams(f).toString())).toEqual(f)
      }
    }
  })

  it('writes one order whatever order the filters went in', () => {
    const reversed: HistoryFilters = { to: '2026-03-31', from: '2026-03-01', status: 'expired', service: 'youtube' }
    expect(filterParams(reversed).toString()).toBe('service=youtube&status=expired&from=2026-03-01&to=2026-03-31')
  })

  it('writes nothing for no filters', () => {
    expect(filterParams(NONE).toString()).toBe('')
  })
})

describe('hasFilters', () => {
  it('is false with no filter and true with any single one', () => {
    expect(hasFilters(NONE)).toBe(false)
    expect(hasFilters({ ...NONE, service: 'youtube' })).toBe(true)
    expect(hasFilters({ ...NONE, status: 'expired' })).toBe(true)
    expect(hasFilters({ ...NONE, from: '2026-03-01' })).toBe(true)
    expect(hasFilters({ ...NONE, to: '2026-03-01' })).toBe(true)
  })
})
