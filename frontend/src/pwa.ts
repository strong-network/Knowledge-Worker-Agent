// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// PWA service-worker registration.
//
// We register sw.js with a query string `?v=<build-id>`. Because Vite ships
// the SPA's hashed JS bundle URL based on file contents, we derive a stable
// per-build identifier from the URL of *this* module's script tag (the
// `?v=...` query the browser is loading right now). When the bundle hash
// changes, the version changes, and the browser treats the SW as new —
// triggering an install/activate cycle that drops the previous cache.

function pickBuildId(): string {
  if (typeof document !== 'undefined') {
    const scripts = Array.from(document.querySelectorAll<HTMLScriptElement>('script[src]'))
    // The Vite-emitted main bundle has a hashed filename like
    // "/assets/index-<hash>.js". Pull the hash out of the first one we find.
    for (const s of scripts) {
      const m = s.src.match(/\/assets\/index-([A-Za-z0-9_-]+)\.js$/)
      if (m) return m[1]
    }
  }
  return 'dev'
}

export function registerServiceWorker(): void {
  if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return
  // Only register on http(s) origins. SW doesn't run on file://.
  if (window.location.protocol !== 'http:' && window.location.protocol !== 'https:') return

  // Wait until the page is loaded so we don't compete with the initial
  // request burst.
  window.addEventListener('load', () => {
    const url = `/sw.js?v=${encodeURIComponent(pickBuildId())}`
    navigator.serviceWorker.register(url, { scope: '/' }).catch((err) => {
      console.warn('[pwa] service worker registration failed:', err)
    })
  })

  // When a new SW takes control after an update, reload once so the user
  // immediately sees the new bundle. Guard with a flag to avoid loops.
  let reloading = false
  navigator.serviceWorker.addEventListener('controllerchange', () => {
    if (reloading) return
    reloading = true
    window.location.reload()
  })
}
