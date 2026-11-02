/// <reference types="vitest/config" />
// Stryker disable all: build config, not runtime code
import { realpathSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { defineConfig, searchForWorkspaceRoot, type Plugin } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import { svelteTesting } from '@testing-library/svelte/vite'
import { PUBLIC_SHELL } from './src/lib/sw-routing.ts'

const here = (rel: string) => fileURLToPath(new URL(rel, import.meta.url))

const PRECACHE_TOKEN = 'self.__PRECACHE__'

/**
 * Fills the service worker's precache list with every emitted chunk and
 * asset plus the public shell files, and refuses a worker that is not a
 * classic script: an `import` in sw.js means rollup split a shared chunk
 * out of it, which a service worker cannot load.
 */
function swPrecache(): Plugin {
  return {
    name: 'adguard-reward:sw-precache',
    apply: 'build',
    generateBundle(_, bundle) {
      const sw = bundle['sw.js']
      if (!sw || sw.type !== 'chunk') throw new Error('sw-precache: no sw.js chunk in the bundle')
      if (!sw.code.includes(PRECACHE_TOKEN)) throw new Error(`sw-precache: ${PRECACHE_TOKEN} not found in sw.js`)
      const emitted = Object.keys(bundle)
        .filter((f) => f !== 'sw.js' && f !== 'index.html')
        .map((f) => '/' + f)
      const precache = [...new Set([...PUBLIC_SHELL, ...emitted])]
      sw.code = sw.code.replaceAll(PRECACHE_TOKEN, JSON.stringify(precache))
      if (/\b(import|export)\s/.test(sw.code)) {
        throw new Error('sw-precache: sw.js is not a classic script (contains import/export); compile it standalone')
      }
    },
  }
}

// https://vite.dev/config/
export default defineConfig({
  plugins: [svelte(), svelteTesting(), swPrecache()],
  build: {
    rollupOptions: {
      // The worker is a second entry, emitted unhashed at /sw.js so the
      // browser's update check always finds it at the registered URL.
      input: { main: here('index.html'), sw: here('src/sw.ts') },
      output: {
        entryFileNames: (chunk) => (chunk.name === 'sw' ? 'sw.js' : 'assets/[name]-[hash].js'),
      },
    },
  },
  test: {
    // The fetch layer is plain TS and runs under node; component tests opt
    // into jsdom per file with `// @vitest-environment jsdom`.
    environment: 'node',
  },
  server: {
    fs: {
      // Stryker runs vitest from a sandbox copy whose node_modules is a
      // symlink back here; @testing-library/svelte is noExternal, so its
      // setup file is served by absolute (real) path and must be allowed.
      allow: [
        searchForWorkspaceRoot(process.cwd()),
        realpathSync(fileURLToPath(new URL('node_modules', import.meta.url))),
      ],
    },
    // In dev the Go API runs separately (`make dev`); proxy API calls to it.
    proxy: {
      '/api': 'http://127.0.0.1:8080',
      '/healthz': 'http://127.0.0.1:8080',
    },
  },
})
