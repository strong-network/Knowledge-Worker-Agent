// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import {
  fetchProviders,
  isBackendUnavailableError,
  type ModelProvider,
  type ProviderRegistry,
} from '../api'

// useProvidersStore is the single source of truth for which model providers
// this workspace offers and which of them are usable right now.
//
// It replaces useOpencodeAuthStore and useVertexAuthStore, which between them
// hard-coded the assumption that there are exactly two providers. Anything
// derived from "is the assistant connected" — the sign-in banner, the Files and
// Plan widgets, the Settings menu badge — reads this store, so a workspace
// running on a config-declared provider is not permanently told it is
// disconnected.
//
// The built-in providers still have their own login flows and their own
// endpoints; only the *state* is unified here. `byId` is what those flows call
// to find out whether they succeeded.
export const useProvidersStore = defineStore('providers', () => {
  const registry = ref<ProviderRegistry>({ mode: 'builtin', providers: [], presets: {} })
  const loaded = ref(false)
  let timer: number | undefined

  const providers = computed(() => registry.value.providers)
  const mode = computed(() => registry.value.mode)
  const fallbackReason = computed(() => registry.value.fallbackReason || '')

  // The server-resolved Default/Thinking nominations. Empty in built-in mode,
  // where the Claude heuristic in utils/models.ts takes over.
  const presets = computed(() => registry.value.presets || {})

  function byId(id: string): ModelProvider | undefined {
    return registry.value.providers.find(p => p.id === id)
  }

  function isAuthenticated(id: string): boolean {
    return !!byId(id)?.authenticated
  }

  const authenticated = computed(() => providers.value.filter(p => p.authenticated))

  // Providers we hold credentials for but which did not answer when probed.
  const unreachable = computed(() =>
    providers.value.filter(p => p.authenticated && p.reachability === 'unreachable'),
  )

  // Signed in AND able to answer. "Connected" has to mean the second thing too:
  // a workspace whose only provider is blocked by an org network policy is
  // signed in, has an empty model picker, and can run nothing.
  const usable = computed(() =>
    providers.value.filter(p => p.authenticated && p.reachability !== 'unreachable'),
  )
  const anyAuthenticated = computed(() => usable.value.length > 0)

  // True when there is nothing the user could do about being disconnected:
  // every provider on offer is administrator-provisioned and none is ready.
  // The banner uses this to avoid telling someone to "sign in" to a provider
  // that has no sign-in.
  const allManagedAndPending = computed(
    () =>
      providers.value.length > 0 &&
      !anyAuthenticated.value &&
      providers.value.every(p => p.authType === 'managed'),
  )

  // True when the workspace was pointed at central config but no provider
  // resolved at all — a misconfiguration, not a sign-in problem.
  const noProvidersConfigured = computed(() => loaded.value && providers.value.length === 0)

  // Display name for a provider id, for labelling models in the picker. Falls
  // back to the id so an id that is somehow not in the registry still reads as
  // something rather than blank.
  function labelFor(id: string): string {
    return byId(id)?.label || id
  }

  async function refresh() {
    try {
      registry.value = await fetchProviders()
    } catch (err) {
      // A backend that is still booting is not the same as "nothing is
      // connected" — keep the last known state so the banner does not flash on
      // every restart.
      if (isBackendUnavailableError(err)) return
      registry.value = { mode: 'builtin', providers: [], presets: {} }
    } finally {
      loaded.value = true
    }
  }

  function start(intervalMs = 15000) {
    if (timer) return
    refresh()
    timer = window.setInterval(refresh, intervalMs)
    if (typeof window !== 'undefined') {
      window.addEventListener('focus', refresh)
      document.addEventListener('visibilitychange', () => {
        if (!document.hidden) refresh()
      })
    }
  }

  function stop() {
    if (timer) {
      clearInterval(timer)
      timer = undefined
    }
  }

  return {
    registry,
    loaded,
    providers,
    mode,
    fallbackReason,
    presets,
    authenticated,
    usable,
    unreachable,
    anyAuthenticated,
    allManagedAndPending,
    noProvidersConfigured,
    byId,
    isAuthenticated,
    labelFor,
    refresh,
    start,
    stop,
  }
})
