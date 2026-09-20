// Stryker config for `dross verify` (runs here via [mutation.stryker] workdir).
// dross passes --mutate and --reporters itself; the rest lives here.
/** @type {import('@stryker-mutator/api/core').PartialStrykerOptions} */
export default {
  testRunner: 'vitest',
  plugins: ['@stryker-mutator/vitest-runner'],
  vitest: { configFile: 'vite.config.ts' },
  mutate: ['src/**/*.{ts,svelte}', '!src/**/*.test.ts', '!src/vite-env.d.ts'],
  reporters: ['json', 'progress'],
  jsonReporter: { fileName: 'reports/mutation/mutation.json' },
  tempDirName: '.stryker-tmp',
  ignoreStatic: true,
  // More runners race on the sandbox's shared vite deps cache
  // (ENOTEMPTY rename under node_modules/.vite/vitest).
  concurrency: 2,
}
