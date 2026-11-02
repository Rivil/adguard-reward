/// <reference types="node" />
// Stryker disable all: test sources are not mutation targets
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

// The shell's static files, read from disk: these are what the Go binary
// embeds and what a phone's installability check inspects.
const root = fileURLToPath(new URL('..', import.meta.url))
const read = (rel: string) => readFileSync(new URL(rel, `file://${root}`))

type Icon = { src: string; sizes: string; type: string; purpose?: string }
type Manifest = {
  name: string
  short_name: string
  id?: string
  start_url: string
  scope: string
  display: string
  theme_color?: string
  icons: Icon[]
}

const manifest = JSON.parse(read('public/manifest.webmanifest').toString()) as Manifest

describe('pwa shell', () => {
  it('manifest is installable', () => {
    expect(manifest.display).toBe('standalone')
    expect(manifest.start_url).toBe('/')
    expect(manifest.scope).toBe('/')
    expect(manifest.name.length).toBeGreaterThan(0)
    expect(manifest.short_name.length).toBeGreaterThan(0)
    const bySize = new Map(manifest.icons.map((i) => [i.sizes, i]))
    for (const size of ['192x192', '512x512']) {
      const icon = bySize.get(size)
      expect(icon, `icon ${size}`).toBeDefined()
      expect(icon!.type).toBe('image/png')
      expect(icon!.src.startsWith('/')).toBe(true)
    }
  })

  it('icons are real PNGs', () => {
    const signature = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])
    for (const icon of manifest.icons) {
      const png = read(`public${icon.src}`)
      expect(png.subarray(0, 8).equals(signature), `${icon.src} signature`).toBe(true)
      const [w, h] = icon.sizes.split('x').map(Number)
      // IHDR follows the signature and the 8-byte chunk header; width and
      // height are its first two big-endian uint32s.
      expect(png.readUInt32BE(16), `${icon.src} width`).toBe(w)
      expect(png.readUInt32BE(20), `${icon.src} height`).toBe(h)
    }
  })

  it('shell links the manifest', () => {
    const html = read('index.html').toString()
    expect(html).toMatch(/<link[^>]*rel="manifest"[^>]*href="\/manifest\.webmanifest"/)
    expect(html).toMatch(/<meta[^>]*name="theme-color"[^>]*content="#[0-9a-fA-F]{6}"/)
    expect(html).toMatch(/<link[^>]*rel="apple-touch-icon"[^>]*href="\/icon-192\.png"/)
    const title = /<title>([^<]*)<\/title>/.exec(html)?.[1]
    expect(title).toBeDefined()
    expect(title).not.toBe('web')
    expect(title!.trim().length).toBeGreaterThan(0)
  })
})
