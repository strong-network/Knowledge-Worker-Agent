// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import { ref, computed, type Ref, type ComputedRef } from 'vue'
import { fetchMe, type MeInfo } from '../api'
import { HOME_PATH } from '../utils/paths'

// Identity of the person this workspace belongs to, derived server-side from
// OWNER_FULL_NAME. Several places need it at once (the sidebar headline, the
// new-chat greeting), so the request is shared: module-level state means the
// first caller fetches and everyone else reads the same reactive value.
const me = ref<MeInfo>({ name: '', email: '' })
let inFlight: Promise<void> | null = null

// Only the given name: "What can I help with, Alex?" reads as a greeting,
// the full legal name reads as a form field.
const firstName = computed(() => (me.value.name || '').trim().split(/\s+/)[0] || '')

// The server's base folder; the home folder until the server has answered.
const baseDir = computed(() => me.value.base_dir || HOME_PATH)

export function useMe(): { me: Ref<MeInfo>; firstName: ComputedRef<string>; baseDir: ComputedRef<string> } {
  if (!inFlight) {
    inFlight = fetchMe()
      .then(info => { me.value = info })
      // A failed lookup is not worth surfacing — every consumer already has a
      // sensible unnamed fallback. Clearing the latch lets a later mount retry.
      .catch(() => { inFlight = null })
  }
  return { me, firstName, baseDir }
}

// loadBaseDir waits for the server's answer, so a picker opens in the right
// folder rather than the fallback.
export async function loadBaseDir(): Promise<string> {
  useMe()
  await inFlight
  return baseDir.value
}
