<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { computed } from 'vue'
import { useUiStore } from '../../stores/ui'
import { useProvidersStore } from '../../stores/providers'
import { useConnectorsStore } from '../../stores/connectors'
import { useOnboardingStore, GUIDE } from '../../stores/onboarding'
import { displayName } from '../../utils/connectorStatus'
import { CONNECTORS_COST, CONNECTORS_EXPLAINED } from '../../utils/connectorHelp'
import OnboardingItems from './OnboardingItems.vue'
import InfoIcon from '../InfoIcon.vue'

// The two setup steps for someone who can't use the assistant yet. Each opens
// the existing settings, which hand back here when the user is done.
const ui = useUiStore()
const providers = useProvidersStore()
const connectors = useConnectorsStore()
const onboarding = useOnboardingStore()

const model = computed(() => {
  const ready = providers.usable[0]
  if (ready) return { done: true, text: `${ready.label} is connected.`, action: false }
  if (providers.noProvidersConfigured) {
    return { done: false, text: 'No model provider is assigned to this workspace. Contact your administrator.', action: false }
  }
  if (providers.allManagedAndPending) {
    const names = providers.providers.map(p => p.label).join(', ')
    return {
      done: false,
      text: `Your administrator is still setting up ${names}. The assistant works as soon as that's done.`,
      action: false,
    }
  }
  const blocked = providers.unreachable[0]
  if (blocked) {
    return { done: false, text: `${blocked.label} is signed in, but this workspace can't reach it.`, action: true }
  }
  return { done: false, text: 'The assistant needs a model to answer you.', action: true }
})

const items = computed(() =>
  onboarding.shownConnectors.map(d => ({
    key: d.server.name,
    name: displayName(d.server),
    description: d.server.description,
    done: onboarding.connectorSetUp(d),
  })),
)
// Done once one connector works, as step 1 is once a model does. Recall and
// Obsidian don't count: they work without the user doing anything.
const connected = computed(() =>
  connectors.decorated.filter(d => !d.server.owned && onboarding.connectorSetUp(d)).map(d => displayName(d.server)),
)
const appsDone = computed(() => connected.value.length > 0)
const appsText = computed(() => {
  const names = connected.value
  if (names.length === 1) return `${names[0]} is connected.`
  if (names.length === 2) return `${names[0]} and ${names[1]} are connected.`
  if (names.length > 2) return `${names[0]}, ${names[1]} and ${names.length - 2} more are connected.`
  if (items.value.length) return CONNECTORS_EXPLAINED
  return onboarding.available.length
    ? 'Your available connectors are set up.'
    : 'No connectors are available yet.'
})
</script>

<template>
  <div class="modal guide-modal" data-autofocus @click.stop>
    <div class="guide-head">
      <h2>Set up your assistant</h2>
      <button class="guide-x" aria-label="Close" title="Close" @click="ui.closeModal()">✕</button>
    </div>
    <p class="modal-subtitle">Two steps, and the assistant can answer you and work with your apps.</p>

    <ol class="guide-steps">
      <li class="guide-step" :class="{ done: model.done }">
        <span class="guide-num" aria-hidden="true">{{ model.done ? '✓' : '1' }}</span>
        <div class="guide-body">
          <h3>Connect a model</h3>
          <p class="guide-text">{{ model.text }}</p>
          <button
            v-if="model.action"
            class="btn btn-primary"
            @click="ui.openModal('accounts', { returnTo: GUIDE })"
          >Set up a model</button>
        </div>
      </li>
      <li class="guide-step" :class="{ done: appsDone }">
        <span class="guide-num" aria-hidden="true">{{ appsDone ? '✓' : '2' }}</span>
        <div class="guide-body">
          <h3>Connect your apps</h3>
          <p class="guide-text">{{ appsText }}</p>
          <OnboardingItems v-if="items.length" label="Connectors to set up" :items="items" />
          <p v-if="items.length" class="guide-note"><InfoIcon class="guide-note-icon" /><span>{{ CONNECTORS_COST }}</span></p>
          <button
            v-if="items.length"
            class="btn"
            :class="model.done && !appsDone ? 'btn-primary' : 'btn-ghost'"
            @click="ui.openModal('mcp-servers', { returnTo: GUIDE })"
          >Open connectors</button>
        </div>
      </li>
    </ol>

    <div class="modal-footer guide-footer">
      <label class="guide-hide">
        <input v-model="onboarding.hideGuide" type="checkbox" />
        Don't show this again
      </label>
      <button class="btn btn-secondary" @click="ui.closeModal()">Close</button>
    </div>
  </div>
</template>

<style scoped>
.guide-modal { width: 540px; }
.guide-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.guide-x {
  border: none; background: none; color: var(--text2); cursor: pointer;
  font-size: 15px; line-height: 1; padding: 4px 6px; border-radius: 6px;
}
.guide-x:hover { color: var(--text); background: var(--hover); }
.guide-steps { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 18px; }
.guide-step { display: flex; gap: 12px; }
.guide-num {
  flex: none; width: 26px; height: 26px; border-radius: 50%;
  display: inline-flex; align-items: center; justify-content: center;
  border: 1px solid var(--border); background: var(--surface2);
  font-size: 13px; font-weight: 650; color: var(--text);
}
.guide-step.done .guide-num {
  border-color: var(--green);
  background: color-mix(in srgb, var(--green) 22%, var(--surface));
}
.guide-body { flex: 1; min-width: 0; display: flex; flex-direction: column; align-items: flex-start; gap: 8px; }
.guide-body h3 { margin: 3px 0 0; font-size: 14px; font-weight: 650; }
.guide-text { margin: 0; font-size: 13px; line-height: 1.5; color: var(--text2); }
.guide-note { margin: 0; display: flex; gap: 6px; font-size: 12px; line-height: 1.45; color: var(--text2); }
.guide-note-icon { flex: none; margin-top: 2px; }
.guide-body :deep(.ob-items) { align-self: stretch; }
.guide-footer { justify-content: space-between; align-items: center; }
.guide-hide { display: inline-flex; align-items: center; gap: 8px; font-size: 13px; color: var(--text2); cursor: pointer; }
</style>
