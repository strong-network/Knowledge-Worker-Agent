<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { computed } from 'vue'
import { useUiStore } from '../../stores/ui'
import { useOnboardingStore, WHATS_NEW } from '../../stores/onboarding'
import { displayName } from '../../utils/connectorStatus'
import { CONNECTORS_COST, CONNECTORS_EXPLAINED } from '../../utils/connectorHelp'
import OnboardingItems from './OnboardingItems.vue'
import InfoIcon from '../InfoIcon.vue'

// The model providers and connectors the administrator added that the user
// hasn't seen or set up. Close marks them seen; Remind me later waits for the
// next start.
const ui = useUiStore()
const onboarding = useOnboardingStore()

const providerItems = computed(() =>
  onboarding.shownProviders.map(p => ({
    key: p.id,
    name: p.label,
    description: p.description,
    done: p.authenticated,
  })),
)
const connectorItems = computed(() =>
  onboarding.shownConnectors.map(d => ({
    key: d.server.name,
    name: displayName(d.server),
    description: d.server.description,
    done: onboarding.connectorSetUp(d),
  })),
)
</script>

<template>
  <div class="modal news-modal" @click.stop>
    <div class="news-head">
      <h2>New in your workspace</h2>
      <button class="news-x" aria-label="Close" title="Close" @click="ui.closeModal()">✕</button>
    </div>
    <p class="modal-subtitle">Your administrator made these available to you.</p>

    <section v-if="providerItems.length" class="news-section">
      <h3>Model providers</h3>
      <OnboardingItems label="New model providers" :items="providerItems" />
      <button
        v-if="providerItems.some(i => !i.done)"
        class="btn btn-ghost"
        @click="ui.openModal('accounts', { returnTo: WHATS_NEW })"
      >Set up a model</button>
    </section>

    <section v-if="connectorItems.length" class="news-section">
      <h3>Connectors</h3>
      <p class="news-text">{{ CONNECTORS_EXPLAINED }}</p>
      <OnboardingItems label="New connectors" :items="connectorItems" />
      <p class="news-note"><InfoIcon class="news-note-icon" /><span>{{ CONNECTORS_COST }}</span></p>
      <button
        v-if="connectorItems.some(i => !i.done)"
        class="btn btn-ghost"
        @click="ui.openModal('mcp-servers', { returnTo: WHATS_NEW })"
      >Open connectors</button>
    </section>

    <div class="modal-footer">
      <button class="btn btn-secondary" @click="onboarding.remindLater()">Remind me later</button>
      <button class="btn btn-primary" @click="ui.closeModal()">Close</button>
    </div>
  </div>
</template>

<style scoped>
.news-modal { width: 520px; }
.news-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.news-x {
  border: none; background: none; color: var(--text2); cursor: pointer;
  font-size: 15px; line-height: 1; padding: 4px 6px; border-radius: 6px;
}
.news-x:hover { color: var(--text); background: var(--hover); }
.news-section { display: flex; flex-direction: column; align-items: flex-start; gap: 8px; margin-bottom: 16px; }
.news-section h3 { margin: 0; font-size: 14px; font-weight: 650; }
.news-text { margin: 0; font-size: 13px; line-height: 1.5; color: var(--text2); }
.news-note { margin: 0; display: flex; gap: 6px; font-size: 12px; line-height: 1.45; color: var(--text2); }
.news-note-icon { flex: none; margin-top: 2px; }
.news-section :deep(.ob-items) { align-self: stretch; }
</style>
