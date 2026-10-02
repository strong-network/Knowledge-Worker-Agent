<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, computed } from 'vue'
import { useUiStore } from '../../stores/ui'
import { useProvidersStore } from '../../stores/providers'
import { useSessionsStore } from '../../stores/sessions'
import {
  startVertexLogin,
  fetchVertexLoginInfo,
  submitVertexLoginCode,
  cancelVertexLogin,
  type VertexLoginInfo,
} from '../../api'

const ui = useUiStore()
const auth = useProvidersStore()
const sessionsStore = useSessionsStore()

const info = ref<VertexLoginInfo | null>(null)
const error = ref('')
const codeInput = ref('')
const submitting = ref(false)
let pollHandle: number | undefined

const url = computed(() => info.value?.verify_url || '')
const awaitingCode = computed(() => !!info.value?.awaiting_code)
const running = computed(() => !!info.value?.running)
const done = computed(() => !!info.value?.done)
const success = computed(() => !!info.value?.success)
const failed = computed(() => done.value && !success.value)
const failureText = computed(() => info.value?.error || error.value || 'Unknown error.')

async function begin() {
  error.value = ''
  // Latch onto any in-flight flow first (e.g. modal reopened).
  try {
    const current = await fetchVertexLoginInfo()
    if (current && current.running) {
      info.value = current
      pollHandle = window.setInterval(poll, 1000)
      return
    }
  } catch { /* ignore, fall through to start */ }

  let started: VertexLoginInfo | null = null
  try {
    started = await startVertexLogin()
  } catch (e: any) {
    error.value = e?.message || String(e)
  }
  if (!started || (started as any).error || (!started.running && !started.done)) {
    try {
      const fallback = await fetchVertexLoginInfo()
      info.value = fallback
      if (started && (started as any).error) {
        error.value = (started as any).error
      }
      if (!fallback.running && !fallback.done) {
        if (!error.value) {
          error.value = 'Could not start sign-in. Try cancelling and reopening the dialog.'
        }
        return
      }
    } catch (e: any) {
      error.value = e?.message || String(e)
      return
    }
  } else {
    info.value = started
  }
  pollHandle = window.setInterval(poll, 1000)
}

async function retry() {
  stopPolling()
  try { await cancelVertexLogin() } catch { /* ignore */ }
  info.value = null
  error.value = ''
  codeInput.value = ''
  await begin()
}

async function poll() {
  try {
    info.value = await fetchVertexLoginInfo()
    if (info.value.done) {
      stopPolling()
      if (info.value.success) {
        for (let i = 0; i < 5; i++) {
          await auth.refresh()
          if (auth.isAuthenticated('google-vertex')) break
          await new Promise(r => setTimeout(r, 300))
        }
        try { await sessionsStore.loadModels() } catch { /* ignore */ }
      }
    }
  } catch (e: any) {
    error.value = e?.message || String(e)
  }
}

function stopPolling() {
  if (pollHandle) {
    clearInterval(pollHandle)
    pollHandle = undefined
  }
}

function openVerify() {
  if (url.value) window.open(url.value, '_blank', 'noopener')
}

async function submitCode() {
  const c = codeInput.value.trim()
  if (!c || submitting.value) return
  submitting.value = true
  error.value = ''
  try {
    info.value = await submitVertexLoginCode(c)
    if (info.value && (info.value as any).error) {
      error.value = (info.value as any).error
    }
  } catch (e: any) {
    error.value = e?.message || String(e)
  } finally {
    submitting.value = false
  }
}

async function cancel() {
  stopPolling()
  try { await cancelVertexLogin() } catch { /* ignore */ }
  ui.closeModal()
  auth.refresh()
}

function close() {
  stopPolling()
  ui.closeModal()
  auth.refresh()
}

onMounted(begin)
onBeforeUnmount(stopPolling)
</script>

<template>
  <div class="modal login-modal" @click.stop>
    <div class="lm-header">
      <h2>Sign in with Google Cloud</h2>
      <button class="icon-btn" @click="close" aria-label="Close" title="Close">✕</button>
    </div>

    <p class="lm-sub">
      Connect the assistant to Anthropic Claude on Google Cloud Vertex AI using
      your Google account (Application Default Credentials).
    </p>

    <!-- Loading: started but no URL yet -->
    <div v-if="!url && !done" class="lm-loading">
      <span class="lm-spinner" aria-hidden="true" />
      Starting sign-in…
    </div>

    <!-- URL ready + code entry -->
    <div v-if="url && !done" class="lm-step-grid">
      <div class="lm-step">
        <div class="lm-step-num">1</div>
        <div class="lm-step-body">
          <div class="lm-step-title">Open the Google consent page and sign in</div>
          <button class="lm-btn primary" @click="openVerify">
            🔗 Open Google sign-in
          </button>
          <div class="lm-url-row">
            <code class="lm-url">{{ url }}</code>
          </div>
        </div>
      </div>
      <div class="lm-step">
        <div class="lm-step-num">2</div>
        <div class="lm-step-body">
          <div class="lm-step-title">Paste the verification code from your browser</div>
          <form class="lm-code-row" @submit.prevent="submitCode">
            <input
              v-model="codeInput"
              class="lm-input"
              type="text"
              autocomplete="off"
              spellcheck="false"
              placeholder="Verification code"
              :disabled="!awaitingCode || submitting"
            />
            <button
              class="lm-btn primary"
              type="submit"
              :disabled="!awaitingCode || submitting || !codeInput.trim()"
            >
              {{ submitting ? 'Verifying…' : 'Submit' }}
            </button>
          </form>
          <div v-if="!awaitingCode" class="lm-waiting">
            <span class="lm-spinner" aria-hidden="true" />
            Preparing…
          </div>
        </div>
      </div>
    </div>

    <!-- Done -->
    <div v-if="done && success" class="lm-success">
      ✅ Connected! Vertex AI is ready to use.
    </div>
    <div v-if="done && !success" class="lm-error">
      ❌ Sign-in failed.<br>
      <small>{{ failureText }}</small>
    </div>
    <div v-if="error" class="lm-error">{{ error }}</div>

    <details v-if="info?.output" class="lm-log">
      <summary>Show CLI output</summary>
      <pre>{{ info.output }}</pre>
    </details>

    <div class="lm-actions">
      <button v-if="(error || failed) && !running" class="lm-btn ghost" @click="retry">Retry</button>
      <button v-if="running" class="lm-btn ghost" @click="cancel">Cancel</button>
      <button v-else class="lm-btn primary" @click="close">{{ success ? 'Done' : 'Close' }}</button>
    </div>
  </div>
</template>

<style scoped>
.login-modal {
  width: min(560px, 95vw);
  padding: 18px 20px;
  background: var(--surface);
  border-radius: 12px;
}
.lm-header { display: flex; align-items: center; justify-content: space-between; }
.lm-header h2 { margin: 0; font-size: 16px; }
.icon-btn {
  display: inline-flex; align-items: center; justify-content: center;
  width: 28px; height: 28px; padding: 0;
  border: none; border-radius: 6px;
  background: transparent; color: var(--text2); cursor: pointer;
  font-size: 15px; line-height: 1;
}
.icon-btn:hover { background: var(--surface2); color: var(--text); }
.icon-btn:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }

.lm-loading {
  display: flex; align-items: center; gap: 10px;
  padding: 18px; color: var(--text2); justify-content: center;
}

.lm-step-grid { display: flex; flex-direction: column; gap: 14px; }
.lm-step { display: flex; gap: 12px; align-items: flex-start; }
.lm-step-num {
  width: 26px; height: 26px; border-radius: 50%;
  background: var(--accent, #1f6feb); color: #fff;
  display: inline-flex; align-items: center; justify-content: center;
  font-weight: 700; font-size: 13px; flex-shrink: 0;
}
.lm-step-body { flex: 1; min-width: 0; }
.lm-step-title { font-weight: 600; margin-bottom: 6px; }

.lm-code-row { display: flex; gap: 8px; align-items: center; }
.lm-input {
  flex: 1; min-width: 0;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 14px;
  padding: 8px 12px;
  background: var(--surface2); color: var(--text);
  border: 1px solid var(--border); border-radius: 8px;
}
.lm-input:focus { outline: 2px solid var(--accent); outline-offset: 0; }
.lm-url-row { margin-top: 8px; }

.lm-btn {
  padding: 8px 14px;
  border-radius: 6px; border: 1px solid transparent;
  font-size: 13px; font-weight: 600;
  cursor: pointer;
  transition: background .15s, border-color .15s, color .15s;
}
.lm-btn:disabled { opacity: .5; cursor: not-allowed; }
.lm-btn.primary { background: var(--accent, #1f6feb); color: #fff; }
.lm-btn.primary:hover:not(:disabled) { background: var(--accent-hover, #388bfd); }
.lm-btn.ghost {
  background: var(--surface2); color: var(--text);
  border-color: var(--border);
}
.lm-btn.ghost:hover { background: var(--surface3); border-color: var(--text2); }
.lm-url {
  display: inline-block; word-break: break-all;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-weight: 400; font-size: 11px; opacity: .8;
}

.lm-waiting {
  display: flex; align-items: center; gap: 8px;
  margin-top: 8px; font-size: 13px; color: var(--text2);
}

.lm-success {
  margin: 18px 0;
  padding: 12px 14px;
  background: rgba(45,164,78,.12);
  border: 1px solid rgba(45,164,78,.4);
  color: #2da44e; border-radius: 8px;
  text-align: center; font-weight: 600;
}
.lm-error {
  margin: 12px 0;
  padding: 10px 12px;
  background: rgba(207,34,46,.10);
  border: 1px solid rgba(207,34,46,.35);
  color: #cf222e; border-radius: 6px;
  font-size: 13px;
}

.lm-log {
  margin: 8px 0; background: var(--surface2);
  border: 1px solid var(--border); border-radius: 6px;
}
.lm-log summary { padding: 6px 10px; cursor: pointer; font-size: 12px; color: var(--text2); }
.lm-log pre {
  margin: 0; padding: 8px 10px; border-top: 1px solid var(--border);
  font-size: 11px; line-height: 1.4;
  white-space: pre-wrap; word-break: break-word;
  max-height: 200px; overflow-y: auto;
}

.lm-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 18px; }

.lm-spinner {
  display: inline-block; width: 14px; height: 14px;
  border: 2px solid var(--border);
  border-top-color: var(--accent, #1f6feb);
  border-radius: 50%;
  animation: lm-spin .8s linear infinite;
}
@keyframes lm-spin { to { transform: rotate(360deg); } }
.lm-sub { color: var(--text2); font-size: 13px; margin: 4px 0 14px; }
</style>
