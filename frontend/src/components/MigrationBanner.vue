<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { fetchBackendStatus, type MigrationInfo } from '../api'

// A database an older version left at the old place since the files moved
// into one folder: chats made there aren't shown here.
const info = ref<MigrationInfo | null>(null)
const dismissed = ref(false)

onMounted(async () => {
  try {
    const s = await fetchBackendStatus()
    info.value = s.migration?.state === 'stray' ? s.migration : null
  } catch { /* the app shows its own message when the backend is down */ }
})
</script>

<template>
  <div v-if="info && !dismissed" class="migration-banner" role="status">
    <div class="mb-msg">
      <strong>Some chats aren't shown</strong>
      <span>A database has appeared at {{ info.path }} since your files moved, so an older version of Knowledge Worker Agent probably ran. Chats made there aren't shown here.</span>
    </div>
    <button class="mb-close" aria-label="Dismiss" @click="dismissed = true">×</button>
  </div>
</template>

<style scoped>
.migration-banner {
  display: flex; align-items: flex-start; gap: 12px; flex-shrink: 0;
  padding: 10px calc(16px + var(--titlebar-right)) 10px calc(16px + var(--header-inset-left, 0px));
  background: rgba(255, 193, 7, 0.10);
  border-bottom: 1px solid rgba(255, 193, 7, 0.35);
  color: var(--text);
}
.mb-msg { display: flex; flex-direction: column; gap: 2px; flex: 1; min-width: 0; }
.mb-msg strong { font-size: 13px; }
.mb-msg span { font-size: 12px; color: var(--text2); overflow-wrap: anywhere; }
.mb-close {
  background: none; border: none; color: var(--text2); cursor: pointer;
  font-size: 18px; line-height: 1; padding: 0 4px;
}
.mb-close:hover { color: var(--text); }
</style>
