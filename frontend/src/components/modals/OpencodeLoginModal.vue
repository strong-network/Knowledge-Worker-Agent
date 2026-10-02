<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, computed } from 'vue'
import { useUiStore } from '../../stores/ui'
import { useProvidersStore } from '../../stores/providers'
import { useSessionsStore } from '../../stores/sessions'
import {
  startOpencodeLogin,
  fetchOpencodeLoginInfo,
  cancelOpencodeLogin,
  type LoginInfo,
} from '../../api'

const ui = useUiStore()
const auth = useProvidersStore()
const sessionsStore = useSessionsStore()

const info = ref<LoginInfo | null>(null)
const error = ref('')
const copyOk = ref(false)
let pollHandle: number | undefined

const code = computed(() => info.value?.user_code || '')
const url = computed(() => info.value?.verify_url || 'https://github.com/login/device')
const running = computed(() => !!info.value?.running)
const done = computed(() => !!info.value?.done)
const success = computed(() => !!info.value?.success)
const failed = computed(() => done.value && !success.value)
const failureText = computed(() => info.value?.error || error.value || 'Unknown error.')

async function begin() {
  error.value = ''
  // Latch onto any in-flight flow first (e.g. modal reopened).
  try {
    const current = await fetchOpencodeLoginInfo()
    if (current && current.running) {
      info.value = current
      pollHandle = window.setInterval(poll, 1000)
      return
    }
  } catch { /* ignore, fall through to start */ }

  let started: LoginInfo | null = null
  try {
    started = await startOpencodeLogin()
  } catch (e: any) {
    error.value = e?.message || String(e)
  }
  if (!started || (started as any).error || (!started.running && !started.done)) {
    try {
      const fallback = await fetchOpencodeLoginInfo()
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
  try { await cancelOpencodeLogin() } catch { /* ignore */ }
  info.value = null
  error.value = ''
  await begin()
}

async function poll() {
  try {
    info.value = await fetchOpencodeLoginInfo()
    if (info.value.done) {
      stopPolling()
      if (info.value.success) {
        // The credential file is written right before the flow exits;
        // refresh a few times in case the first read races the flush.
        for (let i = 0; i < 5; i++) {
          await auth.refresh()
          if (auth.isAuthenticated('github-copilot')) break
          await new Promise(r => setTimeout(r, 300))
        }
        // The server refreshes its model cache after sign-in; pull the
        // (now populated) list into the store so the picker is usable.
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

async function copyCode() {
  if (!code.value) return
  try {
    await navigator.clipboard.writeText(code.value)
    copyOk.value = true
    setTimeout(() => { copyOk.value = false }, 1500)
  } catch { /* ignore */ }
}

function openVerify() {
  window.open(url.value, '_blank', 'noopener')
}

async function cancel() {
  stopPolling()
  try { await cancelOpencodeLogin() } catch { /* ignore */ }
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
      <h2>Sign in</h2>
      <button class="icon-btn" @click="close" aria-label="Close" title="Close">✕</button>
    </div>

    <p class="lm-sub">
      Connect the assistant to your account using the GitHub device flow.
    </p>

    <!-- Loading: started but no code yet -->
    <div v-if="!code && !done" class="lm-loading">
      <span class="lm-spinner" aria-hidden="true" />
      Starting sign-in…
    </div>

    <!-- Code is ready -->
    <div v-if="code && !done" class="lm-step-grid">
      <div class="lm-step">
        <div class="lm-step-num">1</div>
        <div class="lm-step-body">
          <div class="lm-step-title">Copy this one-time code</div>
          <div class="lm-code-row">
            <code class="lm-code">{{ code }}</code>
            <button class="lm-btn ghost" @click="copyCode">{{ copyOk ? '✓ Copied' : 'Copy' }}</button>
          </div>
        </div>
      </div>
      <div class="lm-step">
        <div class="lm-step-num">2</div>
        <div class="lm-step-body">
          <div class="lm-step-title">Open GitHub and paste the code</div>
          <button class="lm-btn primary" @click="openVerify">
            🔗 Open <span class="lm-url">{{ url }}</span>
          </button>
        </div>
      </div>
      <div class="lm-step">
        <div class="lm-step-num">3</div>
        <div class="lm-step-body">
          <div class="lm-step-title">Approve the request</div>
          <div class="lm-waiting">
            <span class="lm-spinner" aria-hidden="true" />
            Waiting for you to approve in the browser…
          </div>
        </div>
      </div>
    </div>

    <!-- Done -->
    <div v-if="done && success" class="lm-success">
      ✅ Connected! The assistant is ready to use.
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
/* Close (✕) button: borderless and subtle (the global .icon-btn is scoped to
   ChatHeader and does not reach here, so without this it falls back to the
   browser's default heavy border). */
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
.lm-code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 22px; letter-spacing: 4px; font-weight: 700;
  padding: 8px 14px;
  background: var(--surface2); color: var(--text);
  border: 1px solid var(--border); border-radius: 8px;
}

.lm-btn {
  padding: 8px 14px;
  border-radius: 6px; border: 1px solid transparent;
  font-size: 13px; font-weight: 600;
  cursor: pointer;
  transition: background .15s, border-color .15s, color .15s;
}
.lm-btn.primary { background: var(--accent, #1f6feb); color: #fff; }
.lm-btn.primary:hover { background: var(--accent-hover, #388bfd); }
.lm-btn.ghost {
  background: var(--surface2); color: var(--text);
  border-color: var(--border);
}
.lm-btn.ghost:hover { background: var(--surface3); border-color: var(--text2); }
.lm-url {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-weight: 400; font-size: 12px; opacity: .9;
}

.lm-waiting {
  display: flex; align-items: center; gap: 8px;
  font-size: 13px; color: var(--text2);
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
</style>
