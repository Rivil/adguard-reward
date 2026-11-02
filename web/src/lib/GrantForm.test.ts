// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { fireEvent, render, screen } from '@testing-library/svelte'
import { describe, expect, it } from 'vitest'
import GrantForm, { type GrantFormValue } from './GrantForm.svelte'
import type { Child, Service } from './api'

const ada: Child = { id: 1, name: 'Ada', clients: ['Kid phone'] }
const ben: Child = { id: 2, name: 'Ben', clients: [] }
const youtube: Service = { id: 'youtube', name: 'YouTube', icon: 'PHN2Zy8+' }
const tiktok: Service = { id: 'tiktok', name: 'TikTok', icon: 'PHN2Zy8+' }

function mount(props: Partial<{ children: Child[]; services: Service[] | null; value: GrantFormValue; disabled: boolean }> = {}) {
  return render(GrantForm, { children: [ada, ben], services: [youtube, tiktok], ...props })
}

/** The bound state, mirrored on the fieldset. */
function value(): GrantFormValue {
  return JSON.parse(fieldset().dataset.value ?? '{}') as GrantFormValue
}

function valid(): boolean {
  return fieldset().dataset.valid === 'true'
}

function fieldset(): HTMLFieldSetElement {
  const el = document.querySelector('fieldset.grant-form')
  if (!el) throw new Error('no grant form')
  return el as HTMLFieldSetElement
}

function select(): HTMLSelectElement {
  return screen.getByLabelText('Child') as HTMLSelectElement
}

function minutes(): HTMLInputElement {
  return screen.getByLabelText('Minutes') as HTMLInputElement
}

function checkbox(label: string): HTMLInputElement {
  for (const l of document.querySelectorAll('fieldset.grant-form label')) {
    if (l.textContent?.replace(/\s+/g, ' ').trim().startsWith(label)) {
      const cb = l.querySelector('input[type="checkbox"]')
      if (cb) return cb as HTMLInputElement
    }
  }
  throw new Error(`no checkbox labelled ${label}`)
}

async function pickChild(id: number | '') {
  await fireEvent.change(select(), { target: { value: id === '' ? '' : String(id) } })
}

async function typeMinutes(v: string) {
  await fireEvent.input(minutes(), { target: { value: v } })
}

describe('GrantForm shared hint', () => {
  const kidPhone = (c: Child): Child => ({ ...c, clients: ['Kid phone'] })
  const cleo: Child = { id: 3, name: 'Cleo', clients: ['Kid tablet'] }

  it('hintFollowsChild', async () => {
    const { rerender } = mount({ children: [ada, kidPhone(ben), cleo] })
    expect(screen.queryByRole('note')).toBeNull()

    await pickChild(1)
    expect(screen.getByRole('note').textContent).toContain('Kid phone (shared with Ben)')
    await pickChild(3)
    expect(screen.queryByRole('note')).toBeNull()
    await pickChild('')
    expect(screen.queryByRole('note')).toBeNull()

    // Ada stays selected while the children prop changes; the hint follows it.
    const v: GrantFormValue = { child_id: 1, services: [], minutes: 60 }
    await rerender({ children: [ada, kidPhone(ben), cleo], value: v })
    expect(screen.getByRole('note').textContent).toContain('Kid phone (shared with Ben)')
    await rerender({ children: [ada, ben, cleo], value: v })
    expect(value().child_id).toBe(1)
    expect(screen.queryByRole('note')).toBeNull()
    await rerender({ children: [ada, kidPhone(ben), kidPhone(cleo)], value: v })
    expect(value().child_id).toBe(1)
    expect(screen.getByRole('note').textContent).toContain('Kid phone (shared with Ben, Cleo)')
  })

  it('hintPassive', async () => {
    mount({ children: [ada, kidPhone(ben)] })
    await pickChild(1)
    await fireEvent.click(checkbox('YouTube'))
    await typeMinutes('30')
    const note = screen.getByRole('note')
    expect(valid()).toBe(true)
    expect(note.querySelector('button, input, select')).toBeNull()
  })
})

describe('GrantForm', () => {
  it('binds child, services and minutes', async () => {
    mount()
    await pickChild(1)
    await fireEvent.click(checkbox('YouTube'))
    await fireEvent.click(checkbox('TikTok'))
    await typeMinutes('90')
    expect(value()).toEqual({ child_id: 1, services: ['youtube', 'tiktok'], minutes: 90 })
    expect(select().value).toBe('1')
    expect(checkbox('YouTube').checked).toBe(true)
    expect(checkbox('TikTok').checked).toBe(true)

    await fireEvent.click(checkbox('YouTube'))
    expect(value().services).toEqual(['tiktok'])
    expect(checkbox('YouTube').checked).toBe(false)
  })

  it('keeps tick order, not catalogue order', async () => {
    mount()
    await fireEvent.click(checkbox('TikTok'))
    await fireEvent.click(checkbox('YouTube'))
    expect(value().services).toEqual(['tiktok', 'youtube'])
  })

  it('valid tracks the bounds', async () => {
    mount()
    expect(valid()).toBe(false)
    await pickChild(1)
    expect(valid()).toBe(false)
    await fireEvent.click(checkbox('YouTube'))
    expect(valid()).toBe(true)
    await typeMinutes('0')
    expect(valid()).toBe(false)
    await typeMinutes('1')
    expect(valid()).toBe(true)
    await typeMinutes('1440')
    expect(valid()).toBe(true)
    await typeMinutes('1441')
    expect(valid()).toBe(false)
    await typeMinutes('1.5')
    expect(valid()).toBe(false)
    await typeMinutes('60')
    expect(valid()).toBe(true)
    await fireEvent.click(checkbox('YouTube'))
    expect(valid()).toBe(false)
    expect(value().services).toEqual([])
  })

  it('needs a child, not just services', async () => {
    mount()
    await fireEvent.click(checkbox('YouTube'))
    expect(value().services).toEqual(['youtube'])
    expect(valid()).toBe(false)
    await pickChild(1)
    expect(valid()).toBe(true)

    // Back to the placeholder clears the child (null, never 0) and the validity with it.
    await pickChild('')
    expect(value().child_id).toBeNull()
    expect(valid()).toBe(false)
    expect(select().selectedIndex).toBe(0)
  })

  it('one child is preselected', () => {
    const one = mount({ children: [ada] })
    expect(select().value).toBe('1')
    expect(value().child_id).toBe(1)
    one.unmount()

    mount({ children: [ada, ben] })
    expect(select().value).toBe('')
    expect(value().child_id).toBeNull()
  })

  it('placeholder option exists only while no child is chosen', async () => {
    mount()
    // With nothing chosen the placeholder is the selected option, not a blank select.
    expect(select().selectedIndex).toBe(0)
    expect(select().options[0].value).toBe('')
    expect(select().options[0].textContent).toBe('Choose a child…')
    expect(select().options.length).toBe(3)

    await pickChild(2)
    expect(select().selectedIndex).toBe(1)
    expect(select().value).toBe('2')
    expect([...select().options].map((o) => o.value)).toEqual(['1', '2'])
  })

  it('unknown service id is kept and marked', async () => {
    mount({ services: [youtube], value: { child_id: 1, services: ['gone'], minutes: 30 } })
    const row = checkbox('gone').closest('label')!
    expect(checkbox('gone').checked).toBe(true)
    expect(row.textContent).toContain('gone')
    expect(row.textContent).toContain('not in AdGuard Home')
    expect(value().services).toEqual(['gone'])
    expect(valid()).toBe(true)

    await fireEvent.click(checkbox('gone'))
    expect(value().services).toEqual([])
    expect(document.querySelector('[data-missing]')).toBeNull()
  })

  it('disabled greys every control', () => {
    const value = { child_id: 1, services: ['youtube', 'gone'], minutes: 60 }
    const live = mount({ value })
    expect(fieldset().disabled).toBe(false)
    expect(select().disabled).toBe(false)
    expect(minutes().disabled).toBe(false)
    for (const b of document.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')) expect(b.disabled).toBe(false)
    live.unmount()

    mount({ disabled: true, value })
    expect(fieldset().disabled).toBe(true)
    expect(select().disabled).toBe(true)
    expect(minutes().disabled).toBe(true)
    const boxes = document.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')
    expect(boxes.length).toBe(3)
    for (const b of boxes) expect(b.disabled).toBe(true)
  })

  describe('search', () => {
    // id deliberately differs from name so id-matching is distinguishable.
    const roblox: Service = { id: 'rbx', name: 'Roblox', icon: 'PHN2Zy8+' }

    function search(): HTMLInputElement {
      return screen.getByLabelText('Search services') as HTMLInputElement
    }

    async function typeSearch(v: string) {
      await fireEvent.input(search(), { target: { value: v } })
    }

    /** Names of the visible service rows, in render order. */
    function rows(): string[] {
      return [...document.querySelectorAll('ul.services li label')].map((l) => l.textContent!.replace(/\s+/g, ' ').trim())
    }

    it('filters by name, case-insensitively, ignoring surrounding space', async () => {
      mount({ services: [youtube, tiktok, roblox] })
      expect(rows()).toEqual(['YouTube', 'TikTok', 'Roblox'])
      await typeSearch('  TUBE ')
      expect(rows()).toEqual(['YouTube'])
      await typeSearch('o')
      expect(rows()).toEqual(['YouTube', 'TikTok', 'Roblox'])
    })

    it('filters by id', async () => {
      mount({ services: [youtube, tiktok, roblox] })
      await typeSearch('RBX')
      expect(rows()).toEqual(['Roblox'])
    })

    it('never hides a ticked service, and never changes the value', async () => {
      mount({ services: [youtube, tiktok, roblox] })
      await fireEvent.click(checkbox('TikTok'))
      await typeSearch('roblox')
      expect(rows()).toEqual(['TikTok', 'Roblox'])
      expect(checkbox('TikTok').checked).toBe(true)
      expect(value().services).toEqual(['tiktok'])

      // Ticking from a filtered list works; unticking a pinned row drops it from view.
      await fireEvent.click(checkbox('Roblox'))
      expect(value().services).toEqual(['tiktok', 'rbx'])
      await fireEvent.click(checkbox('TikTok'))
      expect(value().services).toEqual(['rbx'])
      expect(rows()).toEqual(['Roblox'])
    })

    it('clear button appears with a query and resets the list', async () => {
      mount({ services: [youtube, tiktok, roblox] })
      expect(screen.queryByLabelText('Clear search')).toBeNull()
      await typeSearch('tik')
      expect(rows()).toEqual(['TikTok'])
      await fireEvent.click(screen.getByLabelText('Clear search'))
      expect(search().value).toBe('')
      expect(rows()).toEqual(['YouTube', 'TikTok', 'Roblox'])
      expect(screen.queryByLabelText('Clear search')).toBeNull()
    })

    it('says when nothing matches, and only then', async () => {
      mount({ services: [youtube, tiktok] })
      expect(document.querySelector('[data-no-match]')).toBeNull()
      await typeSearch('  zzz ')
      expect(rows()).toEqual([])
      expect(document.querySelector('[data-no-match]')?.textContent).toContain('“zzz”')
      await typeSearch('you')
      expect(document.querySelector('[data-no-match]')).toBeNull()
    })

    it('no-match hint stays quiet for an empty catalogue or while an unknown id is listed', async () => {
      const empty = mount({ services: [] })
      expect(document.querySelector('[data-no-match]')).toBeNull()
      empty.unmount()

      mount({ services: [youtube], value: { child_id: 1, services: ['gone'], minutes: 30 } })
      await typeSearch('zzz')
      expect(rows()).toEqual(['gone not in AdGuard Home'])
      expect(document.querySelector('[data-no-match]')).toBeNull()
    })

    it('Enter in the search box does not submit the surrounding form', async () => {
      mount()
      const ev = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
      search().dispatchEvent(ev)
      expect(ev.defaultPrevented).toBe(true)
      const other = new KeyboardEvent('keydown', { key: 'a', bubbles: true, cancelable: true })
      search().dispatchEvent(other)
      expect(other.defaultPrevented).toBe(false)
    })

    it('follows disabled, and is absent without a catalogue', async () => {
      const live = mount()
      expect(search().disabled).toBe(false)
      live.unmount()

      const off = mount({ disabled: true })
      expect(search().disabled).toBe(true)
      off.unmount()

      mount({ services: null })
      expect(screen.queryByLabelText('Search services')).toBeNull()
    })
  })

  it('null catalogue', () => {
    const stored = mount({ services: null, value: { child_id: 1, services: ['youtube', 'tiktok'], minutes: 60 } })
    expect(document.querySelector('[data-unavailable]')).not.toBeNull()
    expect(document.querySelector('input[type="checkbox"]')).toBeNull()
    expect(valid()).toBe(true)
    expect(value().services).toEqual(['youtube', 'tiktok'])
    // The stored ids are named, comma-separated, so the parent sees what a save keeps.
    expect(fieldset().textContent).toContain('Keeps: youtube, tiktok')
    stored.unmount()

    mount({ services: null, value: { child_id: 1, services: [], minutes: 60 } })
    expect(document.querySelector('[data-unavailable]')).not.toBeNull()
    expect(valid()).toBe(false)
    expect(fieldset().textContent).not.toContain('Keeps:')
  })
})

describe('GrantForm custom services', () => {
  const portal: Service = { id: 'custom:1', name: 'school portal', icon: '', custom: true }

  it('custom service row', () => {
    mount({ services: [youtube, portal], value: { child_id: 1, services: ['custom:1'], minutes: 30 } })
    const cb = checkbox('S')
    expect(cb.value).toBe('custom:1')
    expect(cb.checked).toBe(true)
    const row = cb.closest('label') as HTMLElement
    expect(row.querySelector('[data-letter-avatar]')?.textContent).toBe('S')
    expect(row.querySelector('img')).toBeNull()
    expect(row.textContent).toContain('school portal')
    // In the catalogue, so never shown as stale.
    expect(document.querySelector('[data-missing]')).toBeNull()
    expect(document.body.textContent).not.toContain('not in AdGuard Home')
    // Built-ins keep their real icon.
    const yt = checkbox('YouTube').closest('label') as HTMLElement
    expect(yt.querySelector('img')?.getAttribute('src')).toBe('data:image/svg+xml;base64,PHN2Zy8+')
  })

  it('custom service selection', async () => {
    mount({ services: [youtube, portal], value: { child_id: 1, services: [], minutes: 30 } })
    await fireEvent.click(checkbox('S'))
    await fireEvent.click(checkbox('YouTube'))
    expect(value().services).toEqual(['custom:1', 'youtube'])
    expect(valid()).toBe(true)
  })

  it('search finds a custom service by name', async () => {
    mount({ services: [youtube, portal] })
    await fireEvent.input(screen.getByLabelText('Search services'), { target: { value: 'portal' } })
    const rows = [...document.querySelectorAll('ul.services input[type="checkbox"]')].map((c) => (c as HTMLInputElement).value)
    expect(rows).toEqual(['custom:1'])
  })
})
