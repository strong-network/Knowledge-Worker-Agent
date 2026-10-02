// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Pop-out document window.
//
// On a small screen, reading a document in the right-hand Files column while
// chatting squeezes both. This moves the document into a *second browser
// window* so each can be maximised, and the user alternates between them.
//
// The second window is the same SPA booted with `?doc=<path>` — `main.ts`
// branches on that parameter and mounts DocWindowApp instead of the chat app.
// No server route is needed: `/?doc=…` already serves index.html, whereas a
// path like `/viewer` would 404 (the Go handler has no SPA fallback).
//
// The two windows coordinate over a BroadcastChannel rather than `window.opener`
// so the link survives a reload of either side.

import { ref, readonly } from 'vue'

const CHANNEL = 'kwa-doc-window'

// A fixed window name makes the browser reuse the existing window instead of
// spawning a new one for every file — verified: window.open with the same name
// returns the same Window object.
const WINDOW_NAME = 'kwa-document-window'

/** Messages exchanged between the chat window and the document window. */
export type DocWindowMessage =
  /** Document window → chat: "I'm here", sent on boot and in reply to `ping`. */
  | { type: 'hello'; path: string }
  /** Document window → chat: "I'm closing." */
  | { type: 'bye' }
  /** Chat → document window: "Are you there?" (sent when the chat window loads). */
  | { type: 'ping' }
  /** Chat → document window: show this file. */
  | { type: 'open'; path: string }
  /** Chat → document window: the workspace changed on disk, re-read the file. */
  | { type: 'refresh' }

// Module-level singletons: every component that calls useDocWindow() observes
// the same window, and only one channel is opened per browser window.
const isOpen = ref(false)
const currentPath = ref('')
let handle: Window | null = null
let channel: BroadcastChannel | null = null

/** BroadcastChannel is unavailable in non-browser contexts (SSR, old Safari). */
export const supported = typeof window !== 'undefined' && typeof BroadcastChannel !== 'undefined'

function chatChannel(): BroadcastChannel | null {
  if (!supported) return null
  if (channel) return channel
  channel = new BroadcastChannel(CHANNEL)
  channel.onmessage = (ev: MessageEvent<DocWindowMessage>) => {
    const msg = ev.data
    if (msg?.type === 'hello') {
      isOpen.value = true
      currentPath.value = msg.path
    } else if (msg?.type === 'bye') {
      isOpen.value = false
      currentPath.value = ''
      handle = null
    }
  }
  // A reload of the chat window loses the Window handle while the document
  // window keeps running. Ask whoever is out there to re-announce itself.
  channel.postMessage({ type: 'ping' } satisfies DocWindowMessage)
  return channel
}

function alive(): boolean {
  // `closed` is readable across windows regardless of origin, and is the only
  // reliable signal once a window is gone: a closed window never sends `bye`
  // if it was killed rather than navigated away.
  return !!handle && !handle.closed
}

/**
 * Show `path` in the document window, opening the window if needed.
 *
 * Must be called from a user gesture: browsers block `window.open` otherwise
 * (verified — the call silently returns null without one).
 */
function openDocument(path: string) {
  if (!path) return
  const ch = chatChannel()
  if (alive() && ch) {
    ch.postMessage({ type: 'open', path } satisfies DocWindowMessage)
    currentPath.value = path
    handle!.focus()
    return
  }
  // Size it to most of the screen: the whole point is a document that isn't
  // squeezed. Height/width only — where the window lands is the OS's business.
  const width = Math.min(1200, Math.round(window.screen.availWidth * 0.9))
  const height = Math.round(window.screen.availHeight * 0.9)
  const url = `${window.location.origin}/?doc=${encodeURIComponent(path)}`
  handle = window.open(url, WINDOW_NAME, `popup,width=${width},height=${height}`)
  if (!handle) {
    ;(window as any).showToast?.('Allow pop-ups for this site to open documents in a window')
    return
  }
  // Optimistic: the window says `hello` once it boots, but the panel should
  // switch to "send it over there" behaviour immediately.
  isOpen.value = true
  currentPath.value = path
  handle.focus()
}

/** Tell the document window that files changed on disk (agent wrote something). */
function notifyRefresh() {
  if (!isOpen.value) return
  chatChannel()?.postMessage({ type: 'refresh' } satisfies DocWindowMessage)
}

/**
 * Chat-window side of the link: reactive state plus the two actions the file
 * panel needs.
 */
export function useDocWindow() {
  chatChannel()
  return {
    isOpen: readonly(isOpen),
    currentPath: readonly(currentPath),
    openDocument,
    notifyRefresh,
  }
}

/**
 * Document-window side of the link. Announces itself, answers pings, and says
 * goodbye on unload so the chat window stops routing files here.
 *
 * Returns a teardown function.
 */
export function connectDocWindow(handlers: {
  currentPath: () => string
  onOpen: (path: string) => void
  onRefresh: () => void
}): () => void {
  if (!supported) return () => {}
  const ch = new BroadcastChannel(CHANNEL)
  const hello = () =>
    ch.postMessage({ type: 'hello', path: handlers.currentPath() } satisfies DocWindowMessage)

  ch.onmessage = (ev: MessageEvent<DocWindowMessage>) => {
    const msg = ev.data
    if (msg?.type === 'ping') hello()
    else if (msg?.type === 'open') handlers.onOpen(msg.path)
    else if (msg?.type === 'refresh') handlers.onRefresh()
  }
  hello()

  const bye = () => ch.postMessage({ type: 'bye' } satisfies DocWindowMessage)
  // `pagehide` fires in cases `beforeunload` does not (notably iOS/bfcache),
  // and both are cheap, so listen for both rather than picking wrong.
  window.addEventListener('beforeunload', bye)
  window.addEventListener('pagehide', bye)

  return () => {
    bye()
    window.removeEventListener('beforeunload', bye)
    window.removeEventListener('pagehide', bye)
    ch.close()
  }
}
