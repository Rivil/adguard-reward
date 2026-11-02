// @vitest-environment jsdom
// Stryker disable all: test sources are not mutation targets
import { render } from '@testing-library/svelte'
import { describe, expect, it } from 'vitest'
import ServiceIcon from './ServiceIcon.svelte'

function avatar(container: HTMLElement): HTMLElement | null {
  return container.querySelector('[data-letter-avatar]')
}

describe('ServiceIcon', () => {
  it('letter avatar', () => {
    const { container } = render(ServiceIcon, { name: 'school portal', icon: '' })
    expect(avatar(container)?.textContent).toBe('S')
    expect(container.querySelector('img')).toBeNull()
  })

  it('trims before taking the letter', () => {
    const { container } = render(ServiceIcon, { name: '  ünï ', icon: '' })
    expect(avatar(container)?.textContent).toBe('Ü')
  })

  it('a blank name shows ?', () => {
    for (const name of ['  ', '']) {
      const { container, unmount } = render(ServiceIcon, { name, icon: '' })
      expect(avatar(container)?.textContent).toBe('?')
      expect(container.querySelector('img')).toBeNull()
      unmount()
    }
  })

  it('a leading emoji renders whole, not half a surrogate pair', () => {
    const { container, unmount } = render(ServiceIcon, { name: '🎮 Games', icon: '' })
    expect(avatar(container)?.textContent).toBe('🎮')
    unmount()
    // A multi-code-point grapheme (family emoji, ZWJ sequence) stays whole too.
    const fam = '👨‍👩‍👧'
    const { container: c2 } = render(ServiceIcon, { name: fam + ' time', icon: '' })
    expect(avatar(c2)?.textContent).toBe(fam)
  })

  it('a non-empty icon renders the <img>', () => {
    const { container } = render(ServiceIcon, { name: 'YouTube', icon: 'PHN2Zy8+', size: 28 })
    const img = container.querySelector('img')
    expect(img).not.toBeNull()
    expect(img?.getAttribute('src')).toBe('data:image/svg+xml;base64,PHN2Zy8+')
    expect(img?.getAttribute('width')).toBe('28')
    expect(avatar(container)).toBeNull()
  })

  it('sizes the avatar', () => {
    const { container } = render(ServiceIcon, { name: 'Portal', icon: '', size: 28 })
    const a = avatar(container)
    expect(a?.style.width).toBe('28px')
    expect(a?.style.height).toBe('28px')
  })
})
