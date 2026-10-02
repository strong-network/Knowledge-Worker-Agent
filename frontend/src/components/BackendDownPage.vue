<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref, watch } from 'vue'
import { checkBackendReady } from '../api'

const props = defineProps<{
  initialError?: string
}>()

let pollHandle: number | undefined
const lastError = ref(props.initialError || '')

watch(() => props.initialError, (value) => {
  if (value) lastError.value = value
})

async function checkBackend(): Promise<boolean> {
  try {
    await checkBackendReady()
    lastError.value = ''
    return true
  } catch (err) {
    lastError.value = err instanceof Error && err.message
      ? err.message
      : 'Backend is not reachable'
    return false
  }
}

async function reloadIfReady() {
  if (await checkBackend()) {
    window.location.reload()
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
  <div id="backend-down">
    <div class="bd-card">
      <!-- The app's robot mark, inlined rather than <img src="/icon.svg">:
           this page renders precisely when the backend is unreachable, so a
           fetched icon could fail and show a broken image. Strokes use
           currentColor and the occluding fills use the card's own surface
           colour, so it reads correctly in both themes. -->
      <svg class="bd-icon" viewBox="0 0 227 227" role="img" aria-label="Knowledge Worker Agent">
        <g fill="none" stroke="currentColor" stroke-width="8" stroke-linecap="round" stroke-linejoin="round">
          <circle cx="113.5" cy="113.5" r="96" stroke-width="8.5"/>
          <path d="M113.5 48.5V73.5"/>
          <circle cx="113.5" cy="48.5" r="9.5" fill="currentColor" stroke="none"/>
          <rect x="45" y="104.5" width="20" height="29" rx="7" fill="var(--surface)"/>
          <rect x="162" y="104.5" width="20" height="29" rx="7" fill="var(--surface)"/>
          <rect x="59" y="73.5" width="109" height="89" rx="30" fill="var(--surface)"/>
          <circle cx="90.5" cy="113.5" r="9" fill="currentColor" stroke="none"/>
          <circle cx="136.5" cy="113.5" r="9" fill="currentColor" stroke="none"/>
        </g>
      </svg>
      <h1>Backend is restarting</h1>
      <p>Knowledge Worker Agent cannot reach the local backend right now.</p>
      <p class="bd-sub">This page checks every few seconds and reloads automatically when the backend is available again.</p>
      <div class="bd-status">
        <span class="bd-spinner" aria-hidden="true" />
        Waiting for backend…
      </div>
      <code v-if="lastError" class="bd-error">{{ lastError }}</code>
      <button class="bd-btn" @click="reloadNow">Reload now</button>
    </div>
  </div>
</template>

<style scoped>
#backend-down {
  width: 100%;
  min-height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
  background:
    radial-gradient(circle at top, rgba(3, 169, 241, .14), transparent 36%),
    var(--bg);
}
.bd-card {
  width: min(480px, 100%);
  padding: 32px;
  border: 1px solid var(--border);
  border-radius: 18px;
  background: var(--surface);
  color: var(--text);
  text-align: center;
  box-shadow: 0 24px 70px rgba(0,0,0,.18);
}
.bd-icon { display: block; width: 56px; height: 56px; margin: 0 auto 14px; }
.bd-card h1 { font-size: 22px; margin-bottom: 8px; }
.bd-card p { color: var(--text2); line-height: 1.5; }
.bd-sub { margin-top: 8px; font-size: 13px; }
.bd-status {
  display: inline-flex;
  align-items: center;
  gap: 9px;
  margin-top: 22px;
  padding: 9px 12px;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: var(--surface2);
  color: var(--text);
  font-size: 13px;
}
.bd-spinner {
  width: 14px;
  height: 14px;
  border: 2px solid var(--border);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: bd-spin .8s linear infinite;
}
.bd-error {
  display: block;
  margin-top: 16px;
  padding: 10px;
  border-radius: 8px;
  background: var(--code-bg);
  color: var(--text2);
  white-space: pre-wrap;
  word-break: break-word;
  font-size: 12px;
}
.bd-btn {
  margin-top: 18px;
  padding: 8px 16px;
  border: 0;
  border-radius: 8px;
  background: var(--accent);
  color: #fff;
  font-weight: 600;
  cursor: pointer;
}
.bd-btn:hover { background: var(--accent-h); }
@keyframes bd-spin { to { transform: rotate(360deg); } }
</style>
