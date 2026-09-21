/// <reference types="node" />
// Stryker disable all: test sources are not mutation targets
import { existsSync, mkdtempSync, readFileSync, readdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { PUBLIC_SHELL, cacheName, decide } from './sw-routing'

const ORIGIN = 'http://localhost'
const webRoot = fileURLToPath(new URL('../..', import.meta.url))

function req(method: string, path: string, mode = 'no-cors', origin = ORIGIN) {
  const url = path.startsWith('http') ? path : ORIGIN + path
  return { method, url, mode, origin }
}

const precache = new Set(['/', '/assets/app-abc.js', '/manifest.webmanifest'])

describe('decide', () => {
  it('API and non-GET always bypass', () => {
    const full = new Set([...precache, '/api/v1/grants', '/api/v1/buttons', '/healthz', '/sw.js'])
    for (const set of [precache, full]) {
      expect(decide(req('GET', '/api/v1/grants'), set)).toBe('bypass')
      expect(decide(req('GET', '/api/v1/buttons'), set)).toBe('bypass')
      expect(decide(req('GET', '/api'), set)).toBe('bypass')
      expect(decide(req('POST', '/'), set)).toBe('bypass')
      expect(decide(req('POST', '/', 'navigate'), set)).toBe('bypass')
      expect(decide(req('GET', '/healthz'), set)).toBe('bypass')
      expect(decide(req('GET', '/sw.js'), set)).toBe('bypass')
      expect(decide(req('GET', 'https://other.example/assets/app-abc.js'), set)).toBe('bypass')
      expect(decide(req('GET', '/api/v1/x', 'navigate'), set)).toBe('bypass')
    }
  })

  it('navigations are shell', () => {
    expect(decide(req('GET', '/', 'navigate'), precache)).toBe('shell')
    expect(decide(req('GET', '/buttons', 'navigate'), precache)).toBe('shell')
    expect(decide(req('GET', '/children/3', 'navigate'), precache)).toBe('shell')
    expect(decide(req('GET', '/children'), precache)).toBe('bypass')
  })

  it('precached assets are cache-first', () => {
    expect(decide(req('GET', '/assets/app-abc.js'), precache)).toBe('asset')
    expect(decide(req('GET', '/assets/other.js'), precache)).toBe('bypass')
    expect(decide(req('GET', '/manifest.webmanifest'), precache)).toBe('asset')
    expect(decide(req('GET', '/assets/app-abc.js?v=1'), precache)).toBe('asset')
  })
})

describe('cacheName', () => {
  it('changes with the list', () => {
    const a = ['/', '/assets/app-abc.js', '/manifest.webmanifest']
    const b = ['/', '/assets/app-def.js', '/manifest.webmanifest']
    expect(cacheName(a)).not.toBe(cacheName(b))
    expect(cacheName(a)).toBe(cacheName([...a].reverse()))
    expect(cacheName(a)).toMatch(/^shell-[0-9a-f]{8}$/)
    expect(cacheName(a)).not.toBe(cacheName(a.slice(1)))
  })
})

describe('PUBLIC_SHELL', () => {
  it('entries exist', () => {
    const files = new Set(readdirSync(join(webRoot, 'public')))
    expect(PUBLIC_SHELL[0]).toBe('/')
    for (const entry of PUBLIC_SHELL.slice(1)) {
      expect(files.has(entry.slice(1)), entry).toBe(true)
    }
  })
})

describe('vite build', () => {
  it('emits sw.js with a precache list', { timeout: 60_000 }, async () => {
    const { build } = await import('vite')
    const outDir = mkdtempSync(join(tmpdir(), 'sw-build-'))
    try {
      await build({
        root: webRoot,
        configFile: join(webRoot, 'vite.config.ts'),
        logLevel: 'silent',
        build: { outDir, emptyOutDir: true },
      })
      const swPath = join(outDir, 'sw.js')
      expect(existsSync(swPath)).toBe(true)
      const code = readFileSync(swPath, 'utf8')
      expect(code).not.toContain('__PRECACHE__')
      expect(code).not.toContain('import ')
      expect(code).not.toContain('export ')
      const m = /\["\/"[^\]]*\]/.exec(code)
      expect(m, 'precache array literal').not.toBeNull()
      const list = JSON.parse(m![0]) as string[]
      expect(list).toEqual(expect.arrayContaining(PUBLIC_SHELL))
      expect(list.some((p) => /^\/assets\/.+\.js$/.test(p))).toBe(true)
      expect(list).not.toContain('/sw.js')
      expect(list).not.toContain('/index.html')
      for (const p of list) {
        if (p === '/') continue
        expect(existsSync(join(outDir, p)), p).toBe(true)
      }
    } finally {
      rmSync(outDir, { recursive: true, force: true })
    }
  })
})
