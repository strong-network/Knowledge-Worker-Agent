<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useUiStore } from '../../stores/ui'
import { useProvidersStore } from '../../stores/providers'
import { useSessionsStore } from '../../stores/sessions'
import {
  cancelChatGPTLogin,
  fetchChatGPTLoginStatus,
  sendChatGPTCallback,
  startChatGPTLogin,
  type ChatGPTLoginInfo,
} from '../../api'

const ui = useUiStore()
const providers = useProvidersStore()
const sessions = useSessionsStore()

const info = ref<ChatGPTLoginInfo | null>(null)
const error = ref('')
const pasted = ref('')
const sending = ref(false)
const copied = ref(false)
const status = ref<'starting' | 'pending' | 'complete' | 'failed' | 'expired'>('starting')
let pollHandle: number | undefined
let closing = false

async function begin() {
  error.value = ''
  status.value = 'starting'
  try {
    const started = await startChatGPTLogin()
    if (started.error) {
      error.value = started.error
      status.value = 'failed'
      return
    }
    if (!started.attempt_id || !started.verify_url) {
      error.value = 'OpenCode returned an incomplete sign-in request.'
      status.value = 'failed'
      return
    }
    info.value = started
    status.value = 'pending'
    pollHandle = window.setInterval(poll, 1000)
  } catch {
    error.value = 'Couldn\'t start ChatGPT sign-in. Try again.'
    status.value = 'failed'
  }
}

async function poll() {
  const attemptID = info.value?.attempt_id
  if (!attemptID || closing) return
  try {
    const result = await fetchChatGPTLoginStatus(attemptID)
    if (result.error) {
      error.value = 'OpenCode could not confirm the sign-in status. Retry the connection.'
      status.value = 'failed'
      stopPolling()
      return
    }
    status.value = result.status
    if (result.status === 'complete') {
      stopPolling()
      // OpenCode has saved the OAuth credential. Wait for the provider list
      // to reflect that authoritative state before showing a connected result.
      for (let i = 0; i < 8; i++) {
        await providers.refresh()
        if (providers.isAuthenticated('openai')) break
        await new Promise(resolve => setTimeout(resolve, 350))
      }
      try { await sessions.loadModels() } catch { /* model list refresh can retry later */ }
      if (!providers.isAuthenticated('openai')) {
        error.value = 'Sign-in finished, but the ChatGPT connection isn\'t confirmed yet. Close this and check Accounts & connections in a moment.'
      }
      return
    }
    if (result.status === 'failed' || result.status === 'expired') stopPolling()
  } catch {
    error.value = 'OpenCode sign-in status is temporarily unavailable.'
  }
}

function stopPolling() {
  if (pollHandle !== undefined) {
    window.clearInterval(pollHandle)
    pollHandle = undefined
  }
}

async function submitCallback() {
  const attemptID = info.value?.attempt_id
  if (!attemptID || !pasted.value.trim() || sending.value) return
  sending.value = true
  error.value = ''
  try {
    const result = await sendChatGPTCallback(attemptID, pasted.value.trim())
    if (result.error) {
      error.value = result.error
      return
    }
    await poll()
  } catch {
    error.value = 'Couldn\'t send the address. Try again.'
  } finally {
    sending.value = false
  }
}

// Copying lets the user sign in from another browser, such as the one where
// they're already signed in to ChatGPT.
async function copyVerify() {
  const url = info.value?.verify_url
  if (!url) return
  try {
    await navigator.clipboard.writeText(url)
    copied.value = true
    window.setTimeout(() => { copied.value = false }, 2000)
  } catch {
    error.value = 'Couldn\'t copy the link. Select it below and copy it yourself.'
  }
}

function openVerify() {
  const url = info.value?.verify_url
  if (url) window.open(url, '_blank', 'noopener,noreferrer')
}

async function retry() {
  stopPolling()
  const attemptID = info.value?.attempt_id
  if (attemptID) {
    try { await cancelChatGPTLogin(attemptID) } catch { /* best effort */ }
  }
  info.value = null
  await begin()
}

async function cancel() {
  closing = true
  stopPolling()
  const attemptID = info.value?.attempt_id
  if (attemptID && status.value === 'pending') {
    try { await cancelChatGPTLogin(attemptID) } catch { /* best effort */ }
  }
  ui.closeModal()
  await providers.refresh()
}

function close() {
  const attemptID = info.value?.attempt_id
  if (attemptID && status.value === 'pending') {
    void cancelChatGPTLogin(attemptID)
  }
  stopPolling()
  ui.closeModal()
  void providers.refresh()
}

onMounted(begin)
onBeforeUnmount(() => {
  stopPolling()
  const attemptID = info.value?.attempt_id
  if (attemptID && status.value === 'pending') void cancelChatGPTLogin(attemptID)
})
</script>

<template>
  <div class="modal chatgpt-login" role="dialog" aria-modal="true" aria-labelledby="chatgpt-login-title" @click.stop>
    <div class="cg-header">
      <h2 id="chatgpt-login-title">Connect ChatGPT</h2>
      <button class="cg-close" type="button" aria-label="Close" @click="close">✕</button>
    </div>

    <p class="cg-subtitle">
      Sign in with your ChatGPT account. OpenCode handles authorization and keeps the credential in its own store.
    </p>

    <div v-if="status === 'starting'" class="cg-waiting" role="status">
      <span class="cg-spinner" aria-hidden="true" /> Starting secure sign-in…
    </div>

    <div v-else-if="status === 'pending' && info" class="cg-steps">
      <section class="cg-step">
        <span class="cg-number">1</span>
        <div>
          <h3>Sign in to ChatGPT and approve</h3>
          <div class="cg-link-actions">
            <button class="cg-primary" type="button" @click="openVerify">Open ChatGPT sign-in</button>
            <button class="cg-secondary" type="button" @click="copyVerify">{{ copied ? 'Copied' : 'Copy link' }}</button>
          </div>
          <p class="cg-hint">To use another browser, copy the link and open it there.</p>
          <input class="cg-link" type="text" readonly :value="info.verify_url" aria-label="ChatGPT sign-in link" @focus="($event.target as HTMLInputElement).select()" />
        </div>
      </section>
      <section class="cg-step">
        <span class="cg-number">2</span>
        <div>
          <h3>Paste the address you land on</h3>
          <p class="cg-hint">
            After you approve, your browser goes to an address starting with
            <code>http://127.0.0.1</code> and shows that the page can't be reached. That's expected.
            Copy the whole address from the address bar and paste it here.
          </p>
          <form class="cg-paste" @submit.prevent="submitCallback">
            <input v-model="pasted" type="url" placeholder="http://127.0.0.1:…/auth/callback?code=…" aria-label="Address after approving" autocomplete="off" spellcheck="false" />
            <button class="cg-primary" type="submit" :disabled="!pasted.trim() || sending">Connect</button>
          </form>
        </div>
      </section>
      <div v-if="error" class="cg-error" role="alert">{{ error }}</div>
      <div class="cg-waiting" role="status">
        <span class="cg-spinner" aria-hidden="true" /> Waiting for approval…
      </div>
    </div>

    <div v-else-if="status === 'complete' && providers.isAuthenticated('openai')" class="cg-success" role="status">
      ChatGPT is connected, and its models are now available.
    </div>

    <div v-else-if="status === 'expired'" class="cg-error" role="status">
      This sign-in expired. Start a new connection to try again.
    </div>

    <div v-else-if="status === 'failed' || (status === 'complete' && error)" class="cg-error" role="alert">
      {{ error || 'ChatGPT sign-in did not complete.' }}
    </div>

    <div class="cg-actions">
      <button v-if="status === 'pending'" class="cg-secondary" type="button" @click="cancel">Cancel</button>
      <button v-if="status === 'failed' || status === 'expired'" class="cg-primary" type="button" @click="retry">Try again</button>
      <button v-else-if="status !== 'pending' && status !== 'starting'" class="cg-secondary" type="button" @click="close">Close</button>
    </div>
  </div>
</template>

<style scoped>
.chatgpt-login { width: min(520px, 95vw); padding: 20px; background: var(--surface); border-radius: 12px; }
.cg-header { display: flex; align-items: center; justify-content: space-between; }
.cg-header h2 { margin: 0; font-size: 17px; }
.cg-close { border: 0; background: transparent; color: var(--text2); cursor: pointer; font-size: 16px; }
.cg-subtitle { color: var(--text2); line-height: 1.5; margin: 12px 0 20px; }
.cg-link-actions { display: flex; gap: 8px; flex-wrap: wrap; }
.cg-link { width: 100%; box-sizing: border-box; margin-top: 6px; padding: 6px 8px; border: 1px solid var(--border); border-radius: 6px; background: var(--bg); color: var(--text2); font: inherit; font-size: 12px; }
.cg-paste { display: flex; gap: 8px; margin-top: 8px; }
.cg-paste input { flex: 1; min-width: 0; padding: 6px 8px; border: 1px solid var(--border); border-radius: 6px; background: var(--bg); color: var(--text); font: inherit; font-size: 12px; }
.cg-steps { display: flex; flex-direction: column; gap: 18px; }
.cg-step { display: flex; gap: 12px; align-items: flex-start; }
.cg-number { width: 25px; height: 25px; flex: 0 0 25px; display: grid; place-items: center; border-radius: 50%; background: var(--accent); color: white; font-weight: 700; }
.cg-step h3 { margin: 0 0 8px; font-size: 14px; }
.cg-primary, .cg-secondary { border: 1px solid var(--border); border-radius: 7px; padding: 8px 12px; cursor: pointer; font-weight: 600; }
.cg-primary { color: white; background: var(--accent); border-color: var(--accent); }
.cg-secondary { color: var(--text); background: var(--surface2); }
.cg-hint { color: var(--text2); font-size: 12px; line-height: 1.5; }
.cg-waiting { display: flex; align-items: center; gap: 9px; color: var(--text2); padding: 12px 0; }
.cg-spinner { width: 15px; height: 15px; border: 2px solid var(--border); border-top-color: var(--accent); border-radius: 50%; animation: spin .8s linear infinite; }
.cg-success, .cg-error { margin: 16px 0; padding: 12px; border-radius: 8px; }
.cg-success { color: #2da44e; background: rgba(45,164,78,.12); }
.cg-error { color: #cf222e; background: rgba(207,34,46,.10); }
.cg-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 16px; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
