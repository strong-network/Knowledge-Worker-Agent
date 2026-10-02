// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import { ref, onMounted, onUnmounted } from 'vue'

/**
 * A shared clock for relative timestamps ("2 minutes ago").
 *
 * Without this, a relative time is only recalculated when something else
 * happens to re-render the component, so an idle chat would sit on a stale
 * "Just now" indefinitely.
 *
 * One interval is shared by every subscriber rather than one per message, and
 * it is stopped once the last subscriber unmounts.
 */
const TICK_MS = 30_000

const now = ref(Date.now())
let timer: ReturnType<typeof setInterval> | undefined
let subscribers = 0

export function useNow() {
  onMounted(() => {
    subscribers++
    if (!timer) {
      // Re-sync immediately: the interval may have been stopped for a while,
      // so `now` can be arbitrarily stale by the time someone subscribes again.
      now.value = Date.now()
      timer = setInterval(() => { now.value = Date.now() }, TICK_MS)
    }
  })

  onUnmounted(() => {
    subscribers--
    if (subscribers <= 0 && timer) {
      clearInterval(timer)
      timer = undefined
      subscribers = 0
    }
  })

  return now
}
