// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// The setup guide and what's new.
//
// Once per page load, after the app has started, this decides whether to show
// one of them: the setup guide while no model works, otherwise what's new when
// the administrator added model providers or connectors the user hasn't seen
// and hasn't set up. Each shows at most once per start of Knowledge Worker
// Agent; the server keeps what was closed, hidden and seen.
//
// A modal records its close when it goes away for good. Opening Accounts or
// Connectors from it is not a close: those hand back to it (ui.openModal's
// returnTo).

import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'
import {
  closeSetupGuide, fetchOnboarding, markOnboardingSeen, remindWhatsNewLater,
  type ModelProvider, type OnboardingState,
} from '../api'
import { useProvidersStore } from './providers'
import { useConnectorsStore, type DecoratedConnector } from './connectors'
import { useUiStore } from './ui'

export const GUIDE = 'setup-guide'
export const WHATS_NEW = 'whats-new'

// A modal left open past this after the app started isn't followed by either:
// popping up minutes later, when the user closes something else, would be a
// surprise.
const WAIT_FOR_OTHER_MODAL_MS = 60_000

function key(name: string): string {
  return name.trim().toLowerCase()
}

export const useOnboardingStore = defineStore('onboarding', () => {
  const providers = useProvidersStore()
  const connectors = useConnectorsStore()
  const ui = useUiStore()

  const state = ref<OnboardingState | null>(null)
  // What the open modal lists, fixed when it opens: an item set up meanwhile
  // stays, marked as set up, rather than disappearing.
  const listedConnectors = ref<string[]>([])
  const listedProviders = ref<string[]>([])
  // The guide's checkbox, and what's new's Remind me later, decide what the
  // close records.
  const hideGuide = ref(false)
  let later = false
  let checked = false

  // Connectors Knowledge Worker Agent offers: central ones and its own
  // catalogue's, not recall and Obsidian, and not ones the user added.
  const available = computed(() =>
    connectors.decorated.filter(d => (d.server.provisioned || d.server.builtin) && !d.server.owned),
  )
  const central = computed(() => (providers.mode === 'central' ? providers.providers : []))

  function connectorSetUp(d: DecoratedConnector): boolean {
    return d.status === 'on'
  }

  const connectorsByName = computed(() => {
    const m = new Map<string, DecoratedConnector>()
    for (const d of connectors.decorated) m.set(d.server.name, d)
    return m
  })
  const shownConnectors = computed(() =>
    listedConnectors.value.map(n => connectorsByName.value.get(n)).filter((d): d is DecoratedConnector => !!d),
  )
  const shownProviders = computed(() =>
    listedProviders.value.map(id => providers.byId(id)).filter((p): p is ModelProvider => !!p),
  )

  function guideConnectors(): string[] {
    return available.value.filter(d => !connectorSetUp(d)).map(d => d.server.name)
  }

  // Central items that are new to the user and wait for them. A managed
  // provider that isn't ready is left out: there's nothing the user can do.
  function newItems(seen: OnboardingState) {
    const seenC = new Set(seen.seenConnectors)
    const seenP = new Set(seen.seenProviders)
    return {
      connectors: available.value
        .filter(d => d.server.provisioned && !connectorSetUp(d) && !seenC.has(key(d.server.name)))
        .map(d => d.server.name),
      providers: central.value
        .filter(p => !p.authenticated && p.authType !== 'managed' && !seenP.has(p.id))
        .map(p => p.id),
    }
  }

  // Central items already set up count as seen, so one switched off later
  // isn't announced as new.
  async function markSetUpAsSeen(seen: OnboardingState) {
    const seenC = new Set(seen.seenConnectors)
    const seenP = new Set(seen.seenProviders)
    const c = available.value
      .filter(d => d.server.provisioned && connectorSetUp(d) && !seenC.has(key(d.server.name)))
      .map(d => d.server.name)
    const p = central.value.filter(x => x.authenticated && !seenP.has(x.id)).map(x => x.id)
    if (!c.length && !p.length) return
    try {
      state.value = await markOnboardingSeen(c, p)
    } catch { /* told again next time, which is harmless */ }
  }

  function openWhenFree(id: string) {
    const started = Date.now()
    const open = () => {
      hideGuide.value = false
      later = false
      ui.openModal(id)
    }
    if (!ui.activeModal) return open()
    const stop = watch(() => ui.activeModal, now => {
      if (now) return
      stop()
      if (Date.now() - started < WAIT_FOR_OTHER_MODAL_MS) open()
    })
  }

  // check runs once per page load, after the app has started.
  async function check() {
    if (checked) return
    checked = true
    try {
      state.value = await fetchOnboarding()
      await Promise.all([connectors.ensureLoaded(), providers.loaded ? null : providers.refresh()])
    } catch {
      return
    }
    const s = state.value
    if (!s) return

    if (providers.usable.length === 0 && !s.guideHidden && !s.guideClosed) {
      listedConnectors.value = guideConnectors()
      listedProviders.value = []
      openWhenFree(GUIDE)
      return
    }

    await markSetUpAsSeen(s)
    if (s.whatsNewLater) return
    const items = newItems(state.value ?? s)
    if (!items.connectors.length && !items.providers.length) return
    listedConnectors.value = items.connectors
    listedProviders.value = items.providers
    openWhenFree(WHATS_NEW)
  }

  // Leaving a modal for good records it; going to Accounts or Connectors from
  // it doesn't, since those come back.
  watch(() => ui.activeModal, (now, before) => {
    if (now !== null) return
    if (before === GUIDE) void recordGuideClosed()
    else if (before === WHATS_NEW) void recordWhatsNewClosed()
  })

  async function recordGuideClosed() {
    // Everything the guide pointed at counts as seen: its connectors, and
    // every central provider, since Accounts lists them all.
    const c = shownConnectors.value.filter(d => d.server.provisioned).map(d => d.server.name)
    const p = central.value.map(x => x.id)
    try {
      state.value = await closeSetupGuide(hideGuide.value)
      if (c.length || p.length) state.value = await markOnboardingSeen(c, p)
    } catch { /* it shows again next time */ }
  }

  async function recordWhatsNewClosed() {
    try {
      state.value = later
        ? await remindWhatsNewLater()
        : await markOnboardingSeen(listedConnectors.value, listedProviders.value)
    } catch { /* it shows again next time */ }
  }

  function remindLater() {
    later = true
    ui.closeModal()
  }

  return {
    state, hideGuide, available, shownConnectors, shownProviders,
    connectorSetUp, check, remindLater,
  }
})
