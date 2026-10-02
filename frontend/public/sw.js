// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

/* Knowledge Worker Agent service worker.
 *
 * Versioning: the version is read from the registration query string
 * (?v=...), supplied by main.ts at registration time. Changing the version
 * forces a fresh install + activate cycle and clears the previous cache.
 *
 * Strategy:
 *   - /api/*, /events, /healthz       → network-only (never cached; SSE etc.)
 *   - Vite hashed assets (/assets/*)  → cache-first (immutable)
 *   - Static assets (icons, manifest) → stale-while-revalidate
 *   - Navigation requests             → network-first, fall back to cached '/'
 */

const VERSION = 'kwa-' + (new URL(self.location.href).searchParams.get('v') || 'dev');
const APP_SHELL = ['/', '/manifest.webmanifest', '/icon.svg', '/icon-192.png'];

self.addEventListener('install', (event) => {
  // Cache the app shell so the page boots offline. Failures (e.g. the user
  // is offline on first install) shouldn't block activation.
  event.waitUntil(
    caches.open(VERSION).then((cache) => cache.addAll(APP_SHELL).catch(() => {}))
  );
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  // Drop stale caches from prior versions.
  event.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(keys.filter((k) => k !== VERSION).map((k) => caches.delete(k)))
    ).then(() => self.clients.claim())
  );
});

function isNetworkOnly(url) {
  const p = url.pathname;
  return (
    p.startsWith('/api/') ||
    p.startsWith('/events') ||
    p === '/healthz' ||
    p.includes('/stream')
  );
}

function isImmutableAsset(url) {
  // Vite emits hashed filenames under /assets/.
  return url.pathname.startsWith('/assets/');
}

function isStaticAsset(url) {
  return (
    url.pathname === '/manifest.webmanifest' ||
    url.pathname === '/icon.svg' ||
    url.pathname === '/apple-touch-icon.png' ||
    /\/icon-\d+(-maskable)?\.png$/.test(url.pathname)
  );
}

async function networkFirst(req) {
  try {
    const res = await fetch(req);
    // no-store marks a page that mustn't stand in for the app, such as the
    // status page shown while files move.
    if (res && res.ok && !/no-store/.test(res.headers.get('Cache-Control') || '')) {
      const cache = await caches.open(VERSION);
      cache.put(req, res.clone()).catch(() => {});
    }
    return res;
  } catch (err) {
    // Fall back to the cached SPA shell so the app boots offline; runtime
    // queries to /api/* will fail at fetch time which the app already handles.
    const fallback = await caches.match('/');
    if (fallback) return fallback;
    throw err;
  }
}

async function cacheFirst(req) {
  const cached = await caches.match(req);
  if (cached) return cached;
  const res = await fetch(req);
  if (res && res.ok) {
    const cache = await caches.open(VERSION);
    cache.put(req, res.clone()).catch(() => {});
  }
  return res;
}

async function staleWhileRevalidate(req) {
  const cache = await caches.open(VERSION);
  const cached = await cache.match(req);
  const networkPromise = fetch(req)
    .then((res) => {
      if (res && res.ok) cache.put(req, res.clone()).catch(() => {});
      return res;
    })
    .catch(() => cached);
  return cached || networkPromise;
}

self.addEventListener('fetch', (event) => {
  const req = event.request;
  if (req.method !== 'GET') return;

  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;
  if (isNetworkOnly(url)) return;

  if (req.mode === 'navigate' || req.destination === 'document') {
    event.respondWith(networkFirst(req));
    return;
  }

  if (isImmutableAsset(url)) {
    event.respondWith(cacheFirst(req));
    return;
  }

  if (isStaticAsset(url)) {
    event.respondWith(staleWhileRevalidate(req));
    return;
  }

  // For anything else (rare), prefer network with cache fallback.
  event.respondWith(networkFirst(req));
});

// Allow the page to trigger an immediate activation after a manual reload.
self.addEventListener('message', (event) => {
  if (event.data && event.data.type === 'SKIP_WAITING') {
    self.skipWaiting();
  }
});
