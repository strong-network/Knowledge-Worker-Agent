// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// useActivityMonitor reports browser activity to the workspace platform.
//
// It reports activity (typing + clicks):
//   - listens for `keydown` (typing) and `click`, throttled to once per 10s
//   - every 60s, POSTs ACTIVE (if there was any activity in the window) or
//     IDLE to `<base>/strong-network/ping`
//
// The payload and event codes follow the platform's telemetry protocol.
// Config (base URL + session id) is read from a
// `<meta id="strong-network-data" data-settings='{...}'>` tag when present,
// otherwise it falls back to defaults.
//
// Reporting failures are swallowed (a 403
// redirects to `/`) so telemetry never disrupts the app.
//
// The same activity also feeds this app's own server via POST /api/activity,
// which folds it into the workspace heartbeat. Without that, a workspace only
// counts as active while a chat turn is running, so reading or composing a long
// message looks exactly like an idle workspace.

import { onMounted, onBeforeUnmount } from 'vue'
import { reportActivity } from '../api'

// Numeric event codes expected by the platform telemetry endpoint.
export enum CloudEditorEventType {
  TYPING = 1,
  ONLINE = 2,
  CLICK = 3,
  CUT = 4,
  COPY = 5,
  PASTE = 6,
  SUPERVISED_COPY = 7,
  ACTIVE = 8,
  IDLE = 9,
}

interface ActivityOptions {
  session: string
  base: string
  isTerminal: boolean
}

const ACTIVITY_THROTTLE_MS = 10_000
const HEARTBEAT_INTERVAL_MS = 60_000

function generateSessionId(length = 24): string {
  const chars = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz'
  let out = ''
  for (let i = 0; i < length; i++) out += chars[Math.floor(Math.random() * chars.length)]
  return out
}

// Reads config from the strong-network-data meta tag, with
// a generated fallback session so events are always attributable.
function getOptions(): ActivityOptions {
  const opts: ActivityOptions = { session: '', base: '', isTerminal: false }
  try {
    const raw = document.getElementById('strong-network-data')?.getAttribute('data-settings')
    if (raw) {
      const parsed = JSON.parse(raw)
      opts.session = parsed.session ?? ''
      opts.base = parsed.base ?? ''
      opts.isTerminal = !!parsed.isTerminal
    }
  } catch {
    /* fall through to defaults */
  }
  if (!opts.session) opts.session = `web-generated:${generateSessionId()}`
  return opts
}

// IDE type surfaced in the ping: VS Code hosts report 1; other hosts report 0.
function getIdeType(): number {
  return window.location.hostname.includes('vscode') ? 1 : 0
}

// Minimal leading-edge throttle (avoids adding lodash.throttle as a dep).
function throttleLeading(fn: () => void, waitMs: number): () => void {
  let last = 0
  return () => {
    const now = Date.now()
    if (now - last >= waitMs) {
      last = now
      fn()
    }
  }
}

// useActivityMonitor wires up the keydown/click listeners and the 60s
// ACTIVE/IDLE heartbeat for as long as the consuming component is mounted.
export function useActivityMonitor() {
  const options = getOptions()

  let hasTypingActivity = false
  let hasClickActivity = false
  let heartbeatHandle: number | undefined

  function sendPingEvent(type: CloudEditorEventType, data?: string) {
    const url = `${options.base}/strong-network/ping`
    fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        session: options.session,
        event: type,
        data,
        isTerminal: options.isTerminal,
        ideType: getIdeType(),
      }),
      // Let the request finish even if the page is unloading.
      keepalive: true,
    })
      .then(res => {
        if (res.status === 403) window.location.href = '/'
      })
      .catch(() => {
        /* telemetry is best-effort; never disrupt the app */
      })
  }

  const onKeydown = throttleLeading(() => {
    hasTypingActivity = true
  }, ACTIVITY_THROTTLE_MS)

  const onClick = throttleLeading(() => {
    hasClickActivity = true
  }, ACTIVITY_THROTTLE_MS)

  onMounted(() => {
    window.addEventListener('keydown', onKeydown)
    window.addEventListener('click', onClick)

    // Opening the app is itself a sign of use; don't wait a full interval.
    reportActivity()

    heartbeatHandle = window.setInterval(() => {
      const active = hasTypingActivity || hasClickActivity
      sendPingEvent(active ? CloudEditorEventType.ACTIVE : CloudEditorEventType.IDLE)
      if (active) reportActivity()
      hasTypingActivity = false
      hasClickActivity = false
    }, HEARTBEAT_INTERVAL_MS)
  })

  onBeforeUnmount(() => {
    window.removeEventListener('keydown', onKeydown)
    window.removeEventListener('click', onClick)
    if (heartbeatHandle) window.clearInterval(heartbeatHandle)
  })

  return { sendPingEvent }
}
