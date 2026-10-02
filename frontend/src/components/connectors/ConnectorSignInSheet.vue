<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// Sign in to an MCP connector.
//
// The design assumes the browser's loopback redirect returns to the app by
// itself, with pasting the URL as a rare fallback. That is backwards here.
// `opencode mcp auth` listens on 127.0.0.1 *on the server*; the user's browser
// is on a different machine and can never reach it. So the browser always lands
// on a page that won't load, and the user always pastes the address back —
// there is no automatic path to fall back from.
//
// Hence one working state with all three steps visible at once, and the "your
// browser will show an error" line stated up front rather than apologised for
// afterwards. Hiding step 3 until something "fails" would recreate exactly the
// dead end it exists to prevent.
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  cancelMcpAuth, fetchMcpAuthInfo, fetchMcpServerStatus, startMcpAuth,
  submitMcpAuthCallback, type McpAuthInfo, type McpServer,
} from '../../api'
import { displayName } from '../../utils/connectorStatus'
import { useModalDialog } from '../../composables/useModalDialog'

const props = defineProps<{ server: McpServer }>()
const emit = defineEmits<{ (e: 'close', signedIn: boolean): void }>()

const name = computed(() => props.server.name)
const title = computed(() => displayName(props.server))

const info = ref<McpAuthInfo | null>(null)
const startError = ref('')
const pasted = ref('')
const pasteProblem = ref('')
const submitting = ref(false)
// Whether we have handed a callback URL to the server at all, and what its
// listener said. Only interesting when something went wrong, so it lives in the
// technical details rather than on screen.
const delivered = ref('')
const confirming = ref(false)
const confirmed = ref<boolean | null>(null)
let poll: number | undefined

const url = computed(() => info.value?.verify_url || '')
const done = computed(() => !!info.value?.done)
const succeeded = computed(() => done.value && !!info.value?.success && confirmed.value !== false)
const failed = computed(() => !!startError.value || (done.value && !succeeded.value))

type Phase = 'starting' | 'working' | 'connected' | 'failed'
const phase = computed<Phase>(() => {
  if (failed.value) return 'failed'
  if (succeeded.value) return 'connected'
  return url.value ? 'working' : 'starting'
})

// Layered over the connectors modal, so it takes the keyboard while it is up.
// Escape is held back for the same reason this sheet has no click-outside-to-
// close: a sign-in is running behind it, and a stray keystroke should not
// abandon it halfway. Cancel is deliberate, reachable, and does it properly.
// Once the flow has settled there is nothing left to lose, so Escape closes.
const sheet = ref<HTMLElement | null>(null)
useModalDialog(() => sheet.value, {
  onClose: () => close(),
  canCloseOnEscape: () => phase.value === 'connected' || phase.value === 'failed',
})

// What went wrong, in words. The server's own messages are already written for
// a person — "Turn X on first, then sign in" — so they are preferred over
// anything generic we could substitute.
const failureText = computed(() => {
  if (startError.value) return startError.value
  if (confirmed.value === false) {
    return `${title.value} reported the sign-in as complete, but it still isn't connected. `
      + 'That usually means the account you approved with does not have access.'
  }
  const e = (info.value?.error || '').trim()
  if (!e) return 'The sign-in did not complete.'
  if (e === 'cancelled by user') return 'The sign-in was cancelled.'
  return e
})

// This sheet advances on a 1s poll, not on a click: `starting → working` and
// `working → connected` both happen with no user action and without moving
// focus, so a live region is not a nicety here, it is the only channel. The
// failure panel is role="alert" and announces itself, so this deliberately
// stays quiet on failure rather than saying it twice.
const liveStatus = computed(() => {
  if (confirming.value) return `Checking that ${title.value} is connected…`
  switch (phase.value) {
    case 'connected':
      return `Signed in to ${title.value}. The assistant can use it now.`
    case 'working':
      return 'Sign-in page ready. Open it, sign in, then paste the address you land on.'
    default:
      return ''
  }
})

function stopPolling() {
  if (poll !== undefined) {
    clearInterval(poll)
    poll = undefined
  }
}

async function begin() {
  startError.value = ''
  confirmed.value = null
  try {
    // Latch onto a flow already running for this connector rather than starting
    // a second one: only one auth flow runs at a time server-side, so starting
    // again would collapse onto the same process and just look confusing.
    const current = await fetchMcpAuthInfo(name.value).catch(() => null)
    info.value = current?.running && current.server === name.value
      ? current
      : await startMcpAuth(name.value)
  } catch (e: any) {
    startError.value = e?.message || String(e)
    return
  }
  if (info.value?.done) return void finish()
  poll = window.setInterval(tick, 1000)
}

async function tick() {
  try {
    info.value = await fetchMcpAuthInfo(name.value)
  } catch {
    return /* a dropped poll is not a failure; the next one will tell us */
  }
  if (info.value.done) {
    stopPolling()
    void finish()
  }
}

// Confirm with the status endpoint before claiming success. The flow's own
// `success` flag is opencode's word for it; this is a second, independent read,
// forced live because the cached one can still be answering from before the
// token existed.
async function finish() {
  if (!info.value?.success) return
  confirming.value = true
  try {
    const s = await fetchMcpServerStatus(name.value, { fresh: true })
    confirmed.value = s.authenticated
  } catch {
    // Don't manufacture a failure out of a failed confirmation — the flow said
    // it worked, and we have no better evidence.
    confirmed.value = null
  } finally {
    confirming.value = false
  }
}

function openVerify() {
  if (url.value) window.open(url.value, '_blank', 'noopener')
}

// Mirrors DeliverCallback's rules so the user gets a specific correction
// instead of a round trip and a generic rejection. The backend still enforces
// them — this is a courtesy, not the gate.
function problemWith(raw: string): string {
  const v = raw.trim()
  if (!v) return 'Paste the address your browser was redirected to.'
  let u: URL
  try {
    u = new URL(v)
  } catch {
    return "That doesn't look like a web address. Copy the whole thing from your browser's address bar."
  }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') {
    return 'That address should start with http:// or https://.'
  }
  const host = u.hostname.toLowerCase()
  const loopback = host === 'localhost' || host === '::1' || /^127\./.test(host)
  if (!loopback) {
    return 'That is a different address. The one you need starts with http://127.0.0.1 — '
      + "it's in the address bar of the tab that failed to load."
  }
  if (!u.pathname.includes('/oauth/callback')) {
    return 'That looks like a different page. The address you need is the one your browser '
      + 'landed on after you chose Allow.'
  }
  if (!u.searchParams.get('code')) {
    return 'That address is missing the authorization code. Copy the whole thing, including '
      + 'everything after the question mark.'
  }
  return ''
}

function onPasteInput() {
  pasteProblem.value = ''
}

async function submit() {
  if (submitting.value) return
  const problem = problemWith(pasted.value)
  if (problem) {
    pasteProblem.value = problem
    return
  }
  submitting.value = true
  pasteProblem.value = ''
  try {
    const res = await submitMcpAuthCallback(name.value, pasted.value.trim())
    delivered.value = res.ok
      ? `yes (HTTP ${res.status_code ?? '200'})`
      : `rejected — ${res.error || `HTTP ${res.status_code ?? '?'}`}`
    if (res.info) info.value = res.info
    if (!res.ok) {
      // Two different rejections arrive here. With `error`, the server refused
      // the address itself. Without one, it was delivered and the provider
      // turned the code down — and that case must not pass silently, which is
      // what guarding on `res.error` alone would do. Either way the flow is
      // still running, so this belongs against the field, not in a dead end.
      pasteProblem.value = res.error
        || `${title.value} wouldn't accept that address. An authorization code can only be `
          + 'used once and expires quickly — open the sign-in page again and paste the new address.'
      return
    }
    if (info.value?.done) {
      stopPolling()
      await finish()
    }
  } catch (e: any) {
    pasteProblem.value = e?.message || String(e)
  } finally {
    submitting.value = false
  }
}

async function retry() {
  stopPolling()
  try { await cancelMcpAuth(name.value) } catch { /* best effort */ }
  info.value = null
  pasted.value = ''
  pasteProblem.value = ''
  delivered.value = ''
  await begin()
}

async function cancel() {
  stopPolling()
  try { await cancelMcpAuth(name.value) } catch { /* best effort */ }
  emit('close', false)
}

function close() {
  stopPolling()
  emit('close', succeeded.value)
}

// Replaces "Show CLI output". The raw transcript is still there, but under a
// heading that says who it is for and above a button that makes it sendable.
const details = computed(() => [
  ['Method', 'OAuth 2.1 with PKCE, driven by opencode'],
  ['Connector', name.value],
  ['Authorization URL', url.value || '—'],
  ['Callback delivered', delivered.value || 'not yet'],
  ['Started', info.value?.started_at || '—'],
  ['Last error', (info.value?.error || startError.value || '—').trim()],
] as const)

const copied = ref(false)
async function copyDetails() {
  const text = [
    ...details.value.map(([k, v]) => `${k}: ${v}`),
    '',
    'Output:',
    info.value?.output || '(none)',
  ].join('\n')
  try {
    await navigator.clipboard.writeText(text)
    copied.value = true
    window.setTimeout(() => { copied.value = false }, 1500)
  } catch { /* clipboard blocked — the details are on screen to select */ }
}

onMounted(begin)
onBeforeUnmount(stopPolling)
</script>

<template>
  <div ref="sheet" class="si-sheet" role="dialog" aria-modal="true" :aria-label="`Sign in to ${title}`">
    <header class="si-head">
      <h3>Sign in to {{ title }}</h3>
      <p v-if="phase === 'working'">Three steps, in your browser and then back here.</p>
      <p v-else-if="phase === 'starting'">Getting a sign-in link from {{ title }}…</p>
    </header>

    <div class="si-body">
      <div v-if="phase === 'starting'" class="si-starting">
        <div class="si-bar" aria-hidden="true"><span></span></div>
        <p>This usually takes a few seconds.</p>
      </div>

      <ol v-else-if="phase === 'working'" class="si-steps">
        <li>
          <div class="si-step-title">Open the sign-in page</div>
          <button class="si-btn" @click="openVerify">Open sign-in page ↗</button>
          <div class="si-url" :title="url">{{ url }}</div>
        </li>
        <li>
          <div class="si-step-title">Sign in and choose <strong>Allow</strong></div>
          <p class="si-step-text">Use the account that has access to {{ title }}.</p>
        </li>
        <li>
          <div class="si-step-title">Copy the address you land on, and paste it here</div>
          <p class="si-step-text">
            Your browser will land on a page that <strong>won't load</strong>. That's expected —
            the page it's looking for lives on this workspace, not on your computer. Copy the
            whole address anyway and paste it below.
          </p>
          <div class="si-paste">
            <input
              v-model="pasted"
              type="text"
              spellcheck="false"
              aria-label="Address your browser was redirected to"
              placeholder="http://127.0.0.1:…/oauth/callback?code=…"
              @input="onPasteInput"
              @keydown.enter="submit"
            />
            <button class="si-btn" :disabled="submitting || !pasted.trim()" @click="submit">
              {{ submitting ? 'Finishing…' : 'Finish' }}
            </button>
          </div>
          <p v-if="pasteProblem" class="si-paste-problem" role="alert">{{ pasteProblem }}</p>
          <p v-else class="si-step-note">Waiting for you — this page stays open for 10 minutes.</p>
        </li>
      </ol>

      <div v-else-if="phase === 'connected'" class="si-ok">
        <span class="si-tick" aria-hidden="true">✓</span>
        <div>
          <div class="si-ok-title">Signed in to {{ title }}</div>
          <div class="si-ok-sub">The assistant can use it now.</div>
        </div>
      </div>

      <div v-else class="si-failed" role="alert">
        <strong>That didn't work</strong>
        <span>{{ failureText }}</span>
        <!-- The design's reassurance, and it happens to be true: nothing in the
             connector's configuration is touched by a failed sign-in. -->
        <span class="si-failed-calm">
          Nothing changed — {{ title }} is still switched on and waiting.
        </span>
      </div>

      <details class="si-details">
        <summary>Technical details for IT</summary>
        <dl>
          <template v-for="[k, v] in details" :key="k">
            <dt>{{ k }}</dt>
            <dd>{{ v }}</dd>
          </template>
        </dl>
        <pre v-if="info?.output">{{ info.output }}</pre>
        <button class="si-btn-ghost si-copy" @click="copyDetails">
          {{ copied ? 'Copied' : 'Copy details' }}
        </button>
      </details>

      <p class="sr-only" role="status" aria-live="polite">{{ liveStatus }}</p>
    </div>

    <footer class="si-foot">
      <template v-if="phase === 'failed'">
        <button class="si-btn-ghost" @click="close">Close</button>
        <button class="si-btn" @click="retry">Try again</button>
      </template>
      <template v-else-if="phase === 'connected'">
        <button class="si-btn" :disabled="confirming" @click="close">Done</button>
      </template>
      <template v-else>
        <button class="si-btn-ghost" @click="cancel">Cancel</button>
      </template>
    </footer>
  </div>
</template>

<style scoped>
.si-sheet {
  width: 560px; max-width: 100%; max-height: 100%;
  display: flex; flex-direction: column;
  background: var(--surface); border: 1px solid var(--border); border-radius: 12px;
  box-shadow: 0 18px 48px rgba(0, 0, 0, .45);
}

.si-head { padding: 18px 20px 12px; border-bottom: 1px solid var(--border); }
.si-head h3 { font-size: 15px; font-weight: 700; margin: 0 0 4px; }
.si-head p { font-size: 12px; color: var(--text2); margin: 0; }

.si-body { padding: 16px 20px; overflow-y: auto; flex: 1; min-height: 0; }

.si-starting { padding: 10px 0 6px; }
.si-starting p { font-size: 12px; color: var(--text2); margin: 10px 0 0; }
.si-bar { height: 4px; border-radius: 3px; background: var(--surface3); overflow: hidden; }
.si-bar span {
  display: block; height: 100%; width: 35%; border-radius: 3px; background: var(--accent);
  animation: si-slide 1.2s ease-in-out infinite;
}
@keyframes si-slide {
  0% { transform: translateX(-100%); }
  100% { transform: translateX(300%); }
}
/* Indeterminate and infinite, so it is exactly the kind of motion this setting
   exists for. Still a bar, still visibly "busy" — it just stops moving. */
@media (prefers-reduced-motion: reduce) {
  .si-bar span { animation: none; width: 100%; opacity: .55; }
}

.si-steps { list-style: none; counter-reset: si; margin: 0; padding: 0; }
.si-steps > li {
  counter-increment: si; position: relative;
  padding: 0 0 18px 34px; margin: 0;
}
.si-steps > li::before {
  content: counter(si);
  position: absolute; left: 0; top: 0;
  width: 24px; height: 24px; border-radius: 50%;
  display: flex; align-items: center; justify-content: center;
  font-size: 12px; font-weight: 700; color: var(--accent);
  background: color-mix(in srgb, var(--accent) 14%, transparent);
}
.si-steps > li:last-child { padding-bottom: 0; }
.si-step-title { font-size: 13px; font-weight: 600; color: var(--text); margin-bottom: 6px; padding-top: 3px; }
.si-step-text { font-size: 12px; color: var(--text2); line-height: 1.55; margin: 0 0 8px; }
.si-step-note { font-size: 11px; color: var(--text3); margin: 8px 0 0; }
.si-url {
  margin-top: 7px; font-family: var(--mono); font-size: 11px; color: var(--text3);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

.si-paste { display: flex; gap: 8px; align-items: center; }
.si-paste input {
  flex: 1; min-width: 0; box-sizing: border-box;
  background: var(--surface2); border: 1px solid var(--border); color: var(--text);
  border-radius: 7px; padding: 8px 10px; font-size: 12px; font-family: var(--mono); outline: none;
}
.si-paste input:focus { border-color: var(--accent); }
.si-paste-problem {
  font-size: 12px; line-height: 1.5; color: var(--text); margin: 8px 0 0;
  padding: 8px 10px; border-radius: 6px;
  background: color-mix(in srgb, var(--red) 9%, transparent);
  border: 1px solid color-mix(in srgb, var(--red) 35%, transparent);
}

.si-ok {
  display: flex; align-items: center; gap: 12px;
  border: 1px solid color-mix(in srgb, var(--green) 40%, transparent);
  background: color-mix(in srgb, var(--green) 9%, transparent);
  border-radius: 9px; padding: 14px;
}
.si-tick {
  width: 32px; height: 32px; border-radius: 50%; flex-shrink: 0;
  display: flex; align-items: center; justify-content: center;
  font-size: 16px; font-weight: 700; color: #fff; background: var(--green);
}
.si-ok-title { font-size: 13px; font-weight: 600; color: var(--text); }
.si-ok-sub { font-size: 12px; color: var(--text2); margin-top: 1px; }

.si-failed {
  display: flex; flex-direction: column; gap: 4px;
  border: 1px solid color-mix(in srgb, var(--red) 40%, transparent);
  background: color-mix(in srgb, var(--red) 9%, transparent);
  border-radius: 9px; padding: 12px 14px;
}
.si-failed strong { font-size: 13px; color: var(--text); }
.si-failed span { font-size: 12px; color: var(--text2); line-height: 1.55; }
.si-failed-calm { color: var(--text3); }

.si-details { margin-top: 16px; border-top: 1px solid var(--border); padding-top: 10px; }
.si-details summary {
  cursor: pointer; font-size: 12px; color: var(--text2); list-style: none;
}
.si-details summary::before { content: '▸ '; }
.si-details[open] summary::before { content: '▾ '; }
.si-details summary:hover { color: var(--text); }
.si-details dl {
  display: grid; grid-template-columns: 140px minmax(0, 1fr); gap: 4px 12px;
  margin: 10px 0 0; font-size: 11px;
}
.si-details dt { color: var(--text3); }
.si-details dd {
  margin: 0; color: var(--text2); font-family: var(--mono);
  overflow-wrap: anywhere;
}
.si-details pre {
  margin: 10px 0 0; padding: 8px 10px; max-height: 180px; overflow: auto;
  background: var(--surface2); border: 1px solid var(--border); border-radius: 6px;
  font-size: 11px; line-height: 1.45; white-space: pre-wrap; word-break: break-word;
  color: var(--text2);
}
.si-copy { margin-top: 10px; }

.si-foot {
  display: flex; align-items: center; justify-content: flex-end; gap: 8px;
  padding: 12px 20px; border-top: 1px solid var(--border);
  background: var(--surface2); border-radius: 0 0 12px 12px;
}

.si-btn {
  background: var(--accent); color: #fff; border: 1px solid transparent;
  padding: 7px 16px; border-radius: 7px; cursor: pointer; font-size: 13px; font-weight: 600;
}
.si-btn:hover:not(:disabled) { background: var(--accent-h); }
.si-btn:disabled { opacity: .5; cursor: not-allowed; }
.si-btn-ghost {
  background: var(--surface); border: 1px solid var(--border); color: var(--text2);
  padding: 7px 14px; border-radius: 7px; cursor: pointer; font-size: 13px;
}
.si-btn-ghost:hover:not(:disabled) { color: var(--text); background: var(--surface3); }

@media (max-width: 760px) {
  .si-sheet { width: 100%; }
  .si-paste { flex-direction: column; align-items: stretch; }
}
</style>
