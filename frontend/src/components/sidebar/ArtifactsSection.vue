<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useArtifactsStore } from '../../stores/artifacts'
import { useUiStore } from '../../stores/ui'

// The sidebar's Artifacts node: opens the view of shared files, and counts
// those that need the owner, as Scheduled tasks counts missed runs. A file
// being fixed doesn't.
const artifacts = useArtifactsStore()
const ui = useUiStore()
const needing = computed(() => artifacts.list.filter(a => a.status !== 'shared' && a.status !== 'fixing').length)
const active = computed(() => ui.activeView === 'artifacts')

function open() {
  ui.openArtifacts()
  if (window.matchMedia('(max-width: 768px)').matches) ui.closeSidebar()
}

onMounted(() => {
  if (!artifacts.loaded) void artifacts.load()
})
</script>

<template>
  <div class="art-section">
    <button class="art-node" :class="{ active }" :aria-current="active ? 'page' : undefined" @click="open">
      <svg class="art-node-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="13" height="13" aria-hidden="true">
        <circle cx="9" cy="8" r="3.2"/><path d="M3.5 19c.6-3.2 2.8-5 5.5-5s4.9 1.8 5.5 5" stroke-linecap="round"/>
        <path d="M15.5 5.2a3 3 0 0 1 0 5.6M17.5 14.4c1.7.6 2.8 2.2 3.1 4.6" stroke-linecap="round"/>
      </svg>
      <span class="art-node-label">Artifacts</span>
      <span v-if="needing" class="art-node-badge" :aria-label="`${needing} need${needing === 1 ? 's' : ''} attention`">{{ needing }}</span>
    </button>
  </div>
</template>

<style scoped>
/* Sits between Scheduled tasks and the chat list, in their header's type. */
.art-section { border-bottom: 1px solid var(--border); padding: 4px 0 6px; flex-shrink: 0; }
.art-node {
  display: flex; align-items: center; gap: 6px; width: calc(100% - 12px);
  margin: 0 6px; padding: 6px 6px;
  font-size: 11px; font-weight: 600; color: var(--text3); text-transform: uppercase;
  letter-spacing: .06em; background: none; border: none; border-radius: 6px; cursor: pointer;
  font-family: inherit; text-align: left;
}
.art-node:hover { color: var(--text2); background: var(--hover); }
.art-node.active { color: var(--text); background: var(--hover); }
.art-node-icon { flex-shrink: 0; color: var(--accent); }
.art-node-label { flex: 1; }
.art-node-badge {
  flex-shrink: 0; min-width: 16px; height: 16px; padding: 0 4px; text-transform: none;
  display: inline-flex; align-items: center; justify-content: center;
  font-size: 10px; font-weight: 700; color: #fff; background: var(--orange); border-radius: 999px;
}
</style>
