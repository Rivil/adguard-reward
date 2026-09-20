/// <reference types="vitest/config" />
// Stryker disable all: build config, not runtime code
import { realpathSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { defineConfig, searchForWorkspaceRoot } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import { svelteTesting } from '@testing-library/svelte/vite'

// https://vite.dev/config/
export default defineConfig({
  plugins: [svelte(), svelteTesting()],
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
