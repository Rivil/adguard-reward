// The service worker: precaches the app shell at install, serves it cache
// first, falls back to the cached / when a navigation cannot reach the
// server, and never caches anything at runtime — /api/* in particular goes
// to the network every time (locked: API is never cached).
/// <reference lib="webworker" />
import { cacheName, decide } from './lib/sw-routing'

const sw = self as unknown as ServiceWorkerGlobalScope

// The build replaces this token with the JSON list of emitted assets plus
// PUBLIC_SHELL; the literal must survive minification as written.
const PRECACHE: string[] = (self as unknown as { __PRECACHE__: string[] }).__PRECACHE__
const PRECACHE_SET = new Set(PRECACHE)
const CACHE = cacheName(PRECACHE)

sw.addEventListener('install', (event) => {
  event.waitUntil(
    caches
      .open(CACHE)
      .then((cache) => cache.addAll(PRECACHE))
      .then(() => sw.skipWaiting()),
  )
})

sw.addEventListener('activate', (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((names) => Promise.all(names.filter((n) => n !== CACHE).map((n) => caches.delete(n))))
      .then(() => sw.clients.claim()),
  )
})

sw.addEventListener('fetch', (event) => {
  const { request } = event
  switch (decide({ method: request.method, url: request.url, mode: request.mode, origin: sw.location.origin }, PRECACHE_SET)) {
    case 'shell':
      event.respondWith(fetch(request).catch(() => caches.match('/').then((hit) => hit ?? Response.error())))
      return
    case 'asset':
      event.respondWith(caches.match(request).then((hit) => hit ?? fetch(request)))
      return
    default:
      // bypass: not calling respondWith hands the request to the network untouched.
      return
  }
})
