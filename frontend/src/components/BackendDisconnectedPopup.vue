<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { checkBackendReady } from '../api'

const props = defineProps<{
  error?: string
}>()

let pollHandle: number | undefined
const lastError = ref(props.error || '')

watch(() => props.error, (value) => {
  if (value) lastError.value = value
})

async function reloadIfReady() {
  try {
    await checkBackendReady()
    window.location.reload()
  } catch (err) {
    lastError.value = err instanceof Error && err.message
      ? err.message
      : 'Backend is not reachable'
  }
}

function reloadNow() {
  window.location.reload()
}

onMounted(() => {
  pollHandle = window.setInterval(reloadIfReady, 2000)
})

onBeforeUnmount(() => {
  if (pollHandle) {
    window.clearInterval(pollHandle)
    pollHandle = undefined
  }
})
</script>

<template>
  <div class="disconnect-popup" role="alertdialog" aria-modal="true" aria-labelledby="disconnect-title">
    <div class="disconnect-card">
      <div class="disconnect-icon" aria-hidden="true">🔌</div>
      <div class="disconnect-copy">
        <h2 id="disconnect-title">You are disconnected</h2>
        <p>The backend went offline. Keep this tab open; Knowledge Worker Agent will reload when the backend is ready again.</p>
        <code v-if="lastError" class="disconnect-error">{{ lastError }}</code>
      </div>
      <div class="disconnect-actions">
        <div class="disconnect-status">
          <span class="disconnect-spinner" aria-hidden="true" />
          Reconnecting
        </div>
        <button class="disconnect-btn" @click="reloadNow">Reload now</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.disconnect-popup {
  position: fixed;
  inset: 0;
  z-index: 10000;
  display: flex;
  align-items: flex-start;
  justify-content: center;
  padding: 20px;
  pointer-events: none;
  background: rgba(0, 0, 0, .18);
  backdrop-filter: blur(1px);
}
.disconnect-card {
  width: min(620px, 100%);
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  gap: 14px;
  align-items: center;
  padding: 16px;
  border: 1px solid var(--border);
  border-radius: 14px;
  background: var(--surface);
  color: var(--text);
  box-shadow: 0 18px 60px rgba(0,0,0,.28);
  pointer-events: auto;
}
.disconnect-icon { font-size: 28px; }
.disconnect-copy h2 {
  margin: 0 0 4px;
  font-size: 16px;
}
.disconnect-copy p {
  margin: 0;
  color: var(--text2);
  font-size: 13px;
  line-height: 1.45;
}
.disconnect-error {
  display: block;
  margin-top: 8px;
  padding: 7px 8px;
  border-radius: 7px;
  background: var(--code-bg);
  color: var(--text2);
  white-space: pre-wrap;
  word-break: break-word;
  font-size: 11px;
}
.disconnect-actions {
  display: flex;
  align-items: center;
  gap: 10px;
}
.disconnect-status {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  color: var(--text2);
  font-size: 12px;
  white-space: nowrap;
}
.disconnect-spinner {
  width: 13px;
  height: 13px;
  border: 2px solid var(--border);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: disconnect-spin .8s linear infinite;
}
.disconnect-btn {
  padding: 8px 12px;
  border: 0;
  border-radius: 8px;
  background: var(--accent);
  color: #fff;
  font-weight: 600;
  cursor: pointer;
}
.disconnect-btn:hover { background: var(--accent-h); }

@keyframes disconnect-spin { to { transform: rotate(360deg); } }

@media (max-width: 640px) {
  .disconnect-card {
    grid-template-columns: auto minmax(0, 1fr);
  }
  .disconnect-actions {
    grid-column: 1 / -1;
    justify-content: space-between;
  }
}
</style>
