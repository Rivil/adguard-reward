/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
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
    // In dev the Go API runs separately (`make dev`); proxy API calls to it.
    proxy: {
      '/api': 'http://127.0.0.1:8080',
      '/healthz': 'http://127.0.0.1:8080',
    },
  },
})
