<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { fetchBackendStatus, type LowDiskInfo } from '../api'
import { useUiStore } from '../stores/ui'

// Shown while less than 5% of the workspace's disk is free.
const ui = useUiStore()
const disk = ref<LowDiskInfo | null>(null)
const dismissed = ref(false)
let timer: number | undefined

async function refresh() {
  try {
    disk.value = (await fetchBackendStatus()).disk ?? null
  } catch { /* the app shows its own message when the backend is down */ }
}

onMounted(() => {
  refresh()
  timer = window.setInterval(refresh, 60000)
})
onBeforeUnmount(() => window.clearInterval(timer))
</script>

<template>
  <div v-if="disk && !dismissed" class="disk-banner" role="status">
    <div class="db-icon" aria-hidden="true">⚠</div>
    <div class="db-msg">
      <strong>Your workspace is almost out of disk space</strong>
      <span>{{ disk.free }} of {{ disk.total }} is free ({{ disk.percent_free }}%). When it's full, the agent can't save files, and chats can fail.</span>
    </div>
    <button class="db-btn" @click="ui.openModal('disk-space')">Get more space</button>
    <button class="db-close" aria-label="Dismiss" @click="dismissed = true">×</button>
  </div>
</template>

<style scoped>
.disk-banner {
  display: flex; align-items: center; gap: 12px; flex-shrink: 0;
  padding: 10px 16px;
  background: rgba(255, 193, 7, 0.10);
  border-bottom: 1px solid rgba(255, 193, 7, 0.35);
  color: var(--text);
}
.db-icon { font-size: 18px; color: var(--orange); }
.db-msg { display: flex; flex-direction: column; gap: 2px; flex: 1; min-width: 0; }
.db-msg strong { font-size: 13px; }
.db-msg span { font-size: 12px; color: var(--text2); }
.db-btn {
  padding: 7px 14px; flex-shrink: 0;
  background: var(--accent, #1f6feb); color: #fff;
  border: none; border-radius: 6px;
  font: inherit; font-size: 13px; font-weight: 600;
  cursor: pointer; transition: background .15s;
}
.db-btn:hover { background: var(--accent-hover, #388bfd); }
.db-close {
  background: none; border: none; color: var(--text2); cursor: pointer;
  font-size: 18px; line-height: 1; padding: 0 4px;
}
.db-close:hover { color: var(--text); }
@media (max-width: 768px) {
  .disk-banner { flex-wrap: wrap; }
  .db-msg { flex-basis: calc(100% - 60px); }
  .db-btn { width: 100%; order: 3; }
}
</style>
