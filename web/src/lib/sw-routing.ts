// Pure routing for the service worker: which requests it may answer from
// the precache and which it must never touch. No worker globals here so it
// runs under vitest's node environment and can be imported by the build.

/** Files under web/public that make up the app shell; the build appends every emitted asset. */
export const PUBLIC_SHELL = ['/', '/manifest.webmanifest', '/favicon.svg', '/icon-192.png', '/icon-512.png']

export type Decision = 'bypass' | 'shell' | 'asset'

export interface RequestLike {
  method: string
  url: string
  mode: string
  /** The worker's own origin. */
  origin: string
}

/**
 * bypass: the network, untouched — every non-GET, anything cross-origin,
 * /api/*, /healthz and the worker itself. shell: a navigation, network
 * first with the cached / as the offline fallback. asset: a precached file,
 * cache first.
 */
export function decide(req: RequestLike, precache: Set<string>): Decision {
  if (req.method !== 'GET') return 'bypass'
  const url = new URL(req.url)
  if (url.origin !== req.origin) return 'bypass'
  const path = url.pathname
  if (path === '/healthz' || path === '/api' || path.startsWith('/api/') || path === '/sw.js') return 'bypass'
  if (req.mode === 'navigate') return 'shell'
  return precache.has(path) ? 'asset' : 'bypass'
}

/**
 * The cache a precache list lives in: FNV-1a over the sorted list, so a
 * deploy with identical assets reuses the cache and one with any change
 * gets a fresh one that activate prunes the old against.
 */
export function cacheName(precache: Iterable<string>): string {
  const joined = [...precache].sort().join('\n')
  let h = 0x811c9dc5
  for (let i = 0; i < joined.length; i++) {
    h ^= joined.charCodeAt(i)
    h = Math.imul(h, 0x01000193) >>> 0
  }
  return 'shell-' + h.toString(16).padStart(8, '0')
}
