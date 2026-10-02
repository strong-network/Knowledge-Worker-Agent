<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// Root of a coworker's view of a shared chat. Served only by the guest
// listener at /s/<id>: the chat pane and nothing else. Like DocWindowApp it
// skips the chat app's bootstrap, and it talks only to /guest/* routes.
import { ref, computed, nextTick, onMounted, onBeforeUnmount } from 'vue'
import MessageBubble from './components/chat/MessageBubble.vue'
import GoalsPanelView from './components/chat/GoalsPanelView.vue'
import FileBrowser from './components/chat/FileBrowser.vue'
import PermissionCard from './components/chat/PermissionCard.vue'
import type { ToolCall, PermissionRequest } from './stores/chat'
import type { PermissionResponse } from './api'
import { guestFileApi } from './api/files'
import { TurnFollower, type TurnStart } from './utils/turnFollower'

interface GuestMessage {
  key: number
  role: 'user' | 'assistant'
  content: string
  authorId?: string
  authorName?: string
  turnId?: string
  createdAt?: string
  streaming?: boolean
  stepCount?: number
  toolCalls?: ToolCall[]
}

type Phase = 'loading' | 'missing' | 'gate' | 'chat' | 'ended'

const sid = decodeURIComponent(window.location.pathname.split('/')[2] || '')
const phase = ref<Phase>('loading')
const title = ref('')
const ownerName = ref('')
const me = ref<{ id: string; name: string } | null>(null)
const nameInput = ref('')
const gateError = ref('')
const messages = ref<GuestMessage[]>([])
const draft = ref('')
const notice = ref('')
const waiting = ref<{ reason: string; text?: string } | null>(null)
const endedReason = ref('')
const listEl = ref<HTMLElement | null>(null)
// What the owner lets people in this chat do; the owner can
// change either while the page is open, and share_options says so.
const allowPermissions = ref(false)
const allowFiles = ref(false)
const permission = ref<PermissionRequest | null>(null)
const permissionTry = ref(0)
const files = guestFileApi(sid)
const fileBrowser = ref<InstanceType<typeof FileBrowser> | null>(null)
const attachInput = ref<HTMLInputElement | null>(null)
const attachments = ref<Array<{ key: number; name: string; path: string; uploading: boolean }>>([])

let nextKey = 1
let es: EventSource | null = null
let reconnectTimer: number | undefined
let live: GuestMessage | null = null

const ownerLabel = computed(() => ownerName.value || 'The owner')
const liveCalls = computed(() => messages.value.find(m => m.streaming)?.toolCalls || [])
const heading = computed(() => title.value || 'Shared chat')

function authorLabel(m: GuestMessage): string {
  if (m.role !== 'user') return ''
  if (!m.authorId) return ownerLabel.value
  if (me.value && m.authorId === me.value.id) return 'You'
  return m.authorName || 'Guest'
}

async function getJSON(path: string): Promise<{ status: number; body: any }> {
  const res = await fetch(path, { headers: { Accept: 'application/json' } })
  let body: any = null
  try { body = await res.json() } catch { /* not JSON */ }
  return { status: res.status, body }
}

async function postJSON(path: string, payload: unknown): Promise<{ status: number; body: any }> {
  const res = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  })
  let body: any = null
  try { body = await res.json() } catch { /* not JSON */ }
  return { status: res.status, body }
}

function scrollToEnd() {
  nextTick(() => {
    const el = listEl.value
    if (el) el.scrollTop = el.scrollHeight
  })
}

async function loadMeta(): Promise<boolean> {
  const { status, body } = await getJSON(`/guest/api/sessions/${encodeURIComponent(sid)}`)
  if (status !== 200) return false
  title.value = body?.title || ''
  ownerName.value = body?.owner_name || ''
  applyOptions(body)
  document.title = `${heading.value} — shared chat`
  return true
}

async function loadHistory(): Promise<boolean> {
  const { status, body } = await getJSON(`/guest/api/sessions/${encodeURIComponent(sid)}/history`)
  if (status === 401) { phase.value = 'gate'; return false }
  if (status !== 200 || !Array.isArray(body)) return false
  // Tool calls are not stored: only this page saw them, so they are carried
  // over by turn. A reply still streaming in keeps its bubble.
  const calls = new Map<string, ToolCall[]>()
  for (const m of messages.value) if (m.role === 'assistant' && m.turnId && m.toolCalls?.length) calls.set(m.turnId, m.toolCalls)
  const fresh: GuestMessage[] = body.map((m: any) => ({
    key: nextKey++,
    role: m.role === 'assistant' ? 'assistant' : 'user',
    content: m.content || '',
    authorId: m.author_id || undefined,
    authorName: m.author_name || undefined,
    turnId: m.turn_id || undefined,
    createdAt: m.created_at,
    toolCalls: (m.turn_id && calls.get(m.turn_id)) || undefined,
  }))
  const id = live?.turnId
  if (live && id && !fresh.some(m => m.role === 'assistant' && m.turnId === id)) {
    const asked = messages.value.find(m => m.role === 'user' && m.turnId === id)
    if (asked && !fresh.some(m => m.role === 'user' && m.turnId === id)) fresh.push(asked)
    fresh.push(live)
  }
  messages.value = fresh
  if (live) live = messages.value.find(m => m === live || (m.streaming && m.turnId === id)) || null
  scrollToEnd()
  return true
}

function upsertTool(callId: string, patch: Partial<ToolCall> & { name?: string }) {
  if (!live) return
  const calls = (live.toolCalls ||= [])
  const found = calls.find(c => c.callId === callId)
  if (found) Object.assign(found, patch)
  else calls.push({ name: patch.name || 'tool', callId, status: 'running', ...patch })
}

function end(reason: string) {
  endedReason.value = reason
  phase.value = 'ended'
  es?.close()
  es = null
  follower.reset()
}

function beginTurn(d: TurnStart) {
  const reply: GuestMessage = { key: nextKey++, role: 'assistant', content: '', turnId: d.turn_id, streaming: true, stepCount: 0, toolCalls: [] }
  // A turn running when history loaded already has its prompt on screen.
  const at = d.turn_id ? messages.value.findIndex(m => m.role === 'user' && m.turnId === d.turn_id) : -1
  if (at >= 0) {
    messages.value.splice(at + 1, 0, reply)
  } else {
    if (d.prompt) {
      messages.value.push({ key: nextKey++, role: 'user', content: d.prompt, turnId: d.turn_id, authorId: d.author_id || undefined, authorName: d.author_name || undefined })
    }
    messages.value.push(reply)
  }
  live = messages.value.find(m => m.key === reply.key) || null
  waiting.value = null
  scrollToEnd()
}

function turnFrame(event: string, d: any) {
  switch (event) {
    case 'chunk': if (live) { live.content += d.text || ''; scrollToEnd() } break
    case 'step': if (live) live.stepCount = (live.stepCount || 0) + 1; break
    case 'tool_call': upsertTool(d.call_id, { name: d.tool, args: d.args, status: 'running' }); break
    case 'tool_done':
      upsertTool(d.call_id, { name: d.tool, status: d.success === false ? 'error' : 'done' })
      if (allowFiles.value && FILE_TOOLS.test(d.tool || '')) fileBrowser.value?.refresh()
      break
    case 'tool_blocked': upsertTool(d.call_id, { name: d.tool, status: 'blocked' }); break
    case 'question': waiting.value = { reason: 'question', text: d.question || '' }; break
    case 'waiting_on_owner': waiting.value = { reason: d.reason || 'approval' }; break
    case 'permission': showPermission(d); break
    case 'permission_answered':
      if (permission.value?.permissionId === d.permission_id) permission.value = null
      if (waiting.value?.reason === 'approval') waiting.value = null
      break
    case 'error': if (live) live.content += `${live.content ? '\n\n' : ''}_${d.message || 'The assistant ran into a problem.'}_`; break
    case 'title': if (d.title) { title.value = d.title; document.title = `${heading.value} — shared chat` } break
  }
}

// Every turn the page shows arrives through this, in order. A turn already in
// history — finished while the page was connecting — is skipped by its id.
const follower = new TurnFollower({
  sync: async () => { await loadHistory() },
  decide: (d) => (d.turn_id && messages.value.some(m => m.role === 'assistant' && m.turnId === d.turn_id) ? 'skip' : 'render'),
  begin: beginTurn,
  frame: turnFrame,
  end: () => {
    if (live) live.streaming = false
    live = null
    waiting.value = null
    permission.value = null
    if (allowFiles.value) fileBrowser.value?.refresh()
  },
  closed: () => end('sharing_ended'),
})

const FRAMES = ['ready', 'turn_start', 'chunk', 'step', 'tool_call', 'tool_done', 'tool_blocked', 'question', 'waiting_on_owner', 'permission', 'permission_answered', 'error', 'done', 'title', 'share_options', 'sharing_ended', 'session_deleted']

function openEvents() {
  es?.close()
  follower.reset()
  const source = new EventSource(`/guest/api/sessions/${encodeURIComponent(sid)}/events`)
  es = source
  for (const type of FRAMES) {
    source.addEventListener(type, (e: Event) => {
      // EventSource fires its own dataless `error` on every dropped connection;
      // only the server's frames carry data.
      if (es !== source || !(e instanceof MessageEvent) || typeof e.data !== 'string') return
      let d: any = {}
      try { d = JSON.parse(e.data) } catch { /* ignore */ }
      // A title belongs to the chat, not the turn: keep it even from a turn
      // the page skips because history already has it.
      if (type === 'title') turnFrame(type, d)
      if (type === 'share_options') { applyOptions(d); return }
      follower.push(type, d)
    })
  }

  // Reconnect by hand: EventSource's own retry would reconnect without the
  // meta check that notices sharing has ended.
  source.onerror = () => {
    if (phase.value === 'ended' || es !== source) return
    source.close()
    es = null
    follower.reset()
    window.clearTimeout(reconnectTimer)
    reconnectTimer = window.setTimeout(reconnect, 3000)
  }
}

async function reconnect() {
  if (phase.value !== 'chat') return
  if (!(await loadMeta())) { end('sharing_ended'); return }
  if (await loadHistory()) openEvents()
  else if (phase.value === 'chat') reconnectTimer = window.setTimeout(reconnect, 5000)
}

async function startChat() {
  phase.value = 'chat'
  if (await loadHistory()) openEvents()
}

async function submitName() {
  gateError.value = ''
  const name = nameInput.value.trim()
  if (!name) { gateError.value = 'Enter the name others should see.'; return }
  const { status, body } = await postJSON('/guest/identify', { name })
  if (status !== 200 || !body?.identified) {
    gateError.value = status === 403
      ? 'This page must be opened from the link you were sent.'
      : (body?.error || 'That did not work. Try again.')
    return
  }
  me.value = { id: body.id, name: body.name }
  await startChat()
}

function changeName() {
  nameInput.value = me.value?.name || ''
  es?.close()
  es = null
  follower.reset()
  phase.value = 'gate'
}

async function send() {
  const text = draft.value.trim()
  const attached = attachments.value.filter(a => a.path)
  if (!text && !attached.length) return
  if (attachments.value.some(a => a.uploading)) { notice.value = 'Wait for the upload to finish.'; return }
  // The assistant works in the chat's folder, so a folder-relative path is
  // one it can open.
  const refs = attached.map(a => `[Attached: ${a.path}]`).join('\n')
  const prompt = refs ? (text ? `${text}\n\n${refs}` : refs) : text
  notice.value = ''
  const { status, body } = await postJSON('/guest/api/chat', { session_id: sid, prompt })
  if (status === 202) {
    draft.value = ''
    attachments.value = []
    if (body?.status === 'queued') notice.value = 'Queued. It will run after the current reply.'
    return
  }
  if (status === 401) { changeName(); return }
  if (status === 404) { end('sharing_ended'); return }
  notice.value = status === 503
    ? 'The assistant is starting up. Try again in a moment.'
    : (body?.error || 'Your message could not be sent.')
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
    e.preventDefault()
    send()
  }
}

const waitingText = computed(() => {
  const w = waiting.value
  if (!w) return ''
  if (w.reason === 'question') return `The assistant asked a question. Waiting for ${ownerLabel.value} to answer.`
  if (w.reason === 'connector') return `Waiting for ${ownerLabel.value} to sign in to a connector.`
  return `Waiting for ${ownerLabel.value} to approve the next step.`
})

// Tool names that usually mean the folder changed; the list refreshes after them.
const FILE_TOOLS = /file|edit|write|patch|create|bash|shell|terminal|command/i

function applyOptions(d: any) {
  const had = allowPermissions.value
  allowPermissions.value = !!d?.allow_permissions
  allowFiles.value = !!d?.allow_files
  if (!allowPermissions.value && permission.value) {
    permission.value = null
    waiting.value = { reason: 'approval' }
  } else if (allowPermissions.value && !had && waiting.value?.reason === 'approval') {
    // The request was shown to this page as "waiting on the owner"; fetch it.
    void fetchPending()
  }
  if (!allowFiles.value) attachments.value = []
}

async function fetchPending() {
  const { status, body } = await getJSON(`/guest/api/sessions/${encodeURIComponent(sid)}/permission`)
  if (status === 200 && body?.permission_id) showPermission(body)
}

function showPermission(d: any) {
  permission.value = {
    permissionId: d.permission_id || '',
    permission: d.permission || '',
    patterns: Array.isArray(d.patterns) ? d.patterns : undefined,
  }
  waiting.value = null
  scrollToEnd()
}

async function respond(response: PermissionResponse) {
  const p = permission.value
  if (!p) return
  notice.value = ''
  const { status, body } = await postJSON(`/guest/api/sessions/${encodeURIComponent(sid)}/permission`, { permission_id: p.permissionId, response })
  if (status === 200) { permission.value = null; return }
  if (status === 403) {
    allowPermissions.value = false
    permission.value = null
    waiting.value = { reason: 'approval' }
  } else if (status === 404) {
    permission.value = null
  } else {
    permissionTry.value++ // offer the buttons again
  }
  notice.value = body?.error || 'Your answer could not be sent.'
}

function pickAttachments() { attachInput.value?.click() }

async function attach(e: Event) {
  const input = e.target as HTMLInputElement
  const picked = Array.from(input.files || [])
  input.value = ''
  for (const file of picked) {
    attachments.value.push({ key: nextKey++, name: file.name, path: '', uploading: true })
    const item = attachments.value[attachments.value.length - 1]
    try {
      const res = await files.upload([file], 'upload')
      item.path = res.uploaded[0]?.path || ''
      if (!item.path) throw new Error('The file could not be uploaded.')
    } catch (err) {
      attachments.value = attachments.value.filter(a => a !== item)
      notice.value = err instanceof Error ? err.message : 'The file could not be uploaded.'
    } finally {
      item.uploading = false
    }
  }
  fileBrowser.value?.refresh()
}

function unattach(key: number) {
  attachments.value = attachments.value.filter(a => a.key !== key)
}

onMounted(async () => {
  // The file panel reports through a toast in the owner's app; here the
  // notice line under the composer says it.
  ;(window as any).showToast = (msg: string) => { notice.value = msg }
  if (!sid || !(await loadMeta())) { phase.value = 'missing'; return }
  const { body } = await getJSON('/guest/api/identity')
  if (body?.identified) {
    me.value = { id: body.id, name: body.name }
    await startChat()
  } else {
    phase.value = 'gate'
  }
})

onBeforeUnmount(() => {
  window.clearTimeout(reconnectTimer)
  es?.close()
})
</script>

<template>
  <div class="guest">
    <header class="guest-head">
      <div class="guest-head-text">
        <h1>{{ heading }}</h1>
        <p v-if="phase !== 'missing' && phase !== 'ended'">{{ ownerLabel }}'s chat, shared with you</p>
      </div>
      <div v-if="me && phase === 'chat'" class="guest-me">
        Chatting as <strong>{{ me.name }}</strong>
        <button type="button" class="guest-link" @click="changeName">Change</button>
      </div>
    </header>

    <main v-if="phase === 'loading'" class="guest-center" aria-busy="true">Loading…</main>

    <main v-else-if="phase === 'missing'" class="guest-center">
      <h2>This chat isn't available</h2>
      <p>It may no longer be shared, or the link may be incomplete. Ask the person who sent it.</p>
    </main>

    <main v-else-if="phase === 'ended'" class="guest-center">
      <h2>Sharing has ended</h2>
      <p>{{ ownerLabel }} is no longer sharing this chat, so you can't see it any more.</p>
    </main>

    <main v-else-if="phase === 'gate'" class="guest-center">
      <form class="guest-gate" @submit.prevent="submitName">
        <h2>What's your name?</h2>
        <p>
          It's shown next to your messages. Anyone in this chat can read what you send,
          and the assistant works in {{ ownerLabel }}'s workspace.
        </p>
        <label for="guest-name">Your name</label>
        <input id="guest-name" v-model="nameInput" maxlength="60" autocomplete="name" autofocus />
        <p v-if="gateError" class="guest-error" role="alert">{{ gateError }}</p>
        <button type="submit" class="guest-btn">Continue</button>
      </form>
    </main>

    <div v-else class="guest-body" :class="{ 'with-files': allowFiles }">
      <div class="guest-chat">
        <div class="guest-plan">
          <GoalsPanelView :live-calls="liveCalls" :messages="messages" />
        </div>
        <main ref="listEl" class="guest-list" aria-live="polite">
          <p v-if="!messages.length" class="guest-empty">No messages yet. Say hello.</p>
          <MessageBubble
            v-for="m in messages"
            :key="m.key"
            :role="m.role"
            :content="m.content"
            :streaming="m.streaming"
            :step-count="m.stepCount"
            :tool-calls="m.toolCalls"
            :created-at="m.createdAt"
            :author-label="authorLabel(m)"
          />
        </main>
        <PermissionCard
          v-if="permission && allowPermissions"
          class="guest-permission"
          :key="`${permission.permissionId}:${permissionTry}`"
          :permission="permission"
          :allow-always="false"
          @respond="respond"
        />
        <p v-else-if="waitingText" class="guest-waiting" role="status">
          {{ waitingText }}
          <span v-if="waiting?.text" class="guest-question">{{ waiting.text }}</span>
        </p>
        <div v-if="attachments.length" class="guest-attachments">
          <span v-for="a in attachments" :key="a.key" class="guest-chip">
            <span class="guest-chip-name">{{ a.name }}</span>
            <span v-if="a.uploading" class="guest-chip-state">Uploading…</span>
            <button type="button" class="guest-chip-remove" :aria-label="`Remove ${a.name}`" @click="unattach(a.key)">×</button>
          </span>
        </div>
        <form class="guest-composer" @submit.prevent="send">
          <template v-if="allowFiles">
            <input ref="attachInput" type="file" multiple class="sr-only" tabindex="-1" aria-hidden="true" @change="attach" />
            <button type="button" class="guest-attach" title="Attach files" aria-label="Attach files" @click="pickAttachments">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="16" height="16" aria-hidden="true"><path d="M21 12.5l-8.5 8.5a5 5 0 0 1-7-7L14 5.5a3.5 3.5 0 0 1 5 5L10.5 19a2 2 0 0 1-3-3L15 8.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
            </button>
          </template>
          <label for="guest-prompt" class="sr-only">Message</label>
          <textarea
            id="guest-prompt"
            v-model="draft"
            rows="2"
            placeholder="Message the assistant…"
            @keydown="onKeydown"
          />
          <button type="submit" class="guest-btn" :disabled="!draft.trim() && !attachments.some(a => a.path)">Send</button>
        </form>
        <p v-if="notice" class="guest-notice" role="status">{{ notice }}</p>
      </div>
      <!-- mobile-open keeps the panel on narrow screens, where the owner's
           docked widget hides; here it is part of the page. -->
      <aside v-if="allowFiles" class="guest-files" aria-label="Files">
        <FileBrowser ref="fileBrowser" class="mobile-open" :api="files" root="." embedded />
      </aside>
    </div>
  </div>
</template>

<style scoped>
.guest {
  display: flex; flex-direction: column;
  /* #app is a flex row: without an explicit width this column shrinks to its
     content and its width changes from screen to screen. */
  width: 100%; height: 100vh; max-width: 880px; margin: 0 auto;
  color: var(--text); background: var(--bg);
}
.guest-head {
  display: flex; align-items: flex-start; gap: 16px;
  padding: 20px 20px 16px;
  border-bottom: 1px solid var(--border);
}
.guest-head-text { flex: 1; min-width: 0; }
.guest-head h1 { font-size: 19px; font-weight: 700; margin: 0 0 4px; overflow-wrap: anywhere; }
.guest-head p { font-size: 12px; color: var(--text2); margin: 0; }
.guest-me { font-size: 12px; color: var(--text2); white-space: nowrap; }
.guest-link {
  margin-left: 8px; padding: 0; border: 0; background: none;
  color: var(--accent); font: inherit; cursor: pointer; text-decoration: underline;
}
.guest-center { flex: 1; display: flex; flex-direction: column; justify-content: center; padding: 32px 20px; }
.guest-center h2 { font-size: 17px; margin: 0 0 8px; }
.guest-center p { color: var(--text2); margin: 0 0 16px; max-width: 520px; }
.guest-gate { display: flex; flex-direction: column; gap: 8px; max-width: 420px; }
.guest-gate label { font-size: 12px; font-weight: 600; }
.guest-gate input {
  padding: 8px 12px; font: inherit;
  border: 1px solid var(--border); border-radius: 8px;
  background: var(--surface); color: var(--text);
}
.guest-error { color: var(--red); font-size: 12px; margin: 0; }
.guest-btn {
  align-self: flex-start; padding: 8px 16px; font: inherit; font-weight: 600;
  border: 0; border-radius: 8px; background: var(--accent); color: #fff; cursor: pointer;
}
.guest-btn:disabled { opacity: .5; cursor: default; }
.guest:has(.guest-body.with-files) { max-width: 1320px; }
.guest-body { flex: 1; min-height: 0; display: flex; }
.guest-chat { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.guest-files {
  width: 380px; flex-shrink: 0; min-height: 0;
  display: flex; flex-direction: column;
  border-left: 1px solid var(--border);
}
@media (max-width: 900px) {
  .guest-body.with-files { flex-direction: column; }
  .guest-files { width: auto; height: 45vh; border-left: 0; border-top: 1px solid var(--border); }
}
.guest-list { flex: 1; overflow-y: auto; padding: 16px 20px; }
/* A flex item with auto margins shrinks to its content; the card is as wide as the owner's. */
.guest-permission { width: calc(100% - 48px); box-sizing: border-box; }
.guest-plan { padding: 12px 20px 0; }
.guest-plan:empty { display: none; }
.guest-empty { color: var(--text2); }
.guest-waiting {
  margin: 0 20px; padding: 8px 12px; font-size: 12px;
  border: 1px solid var(--border); border-radius: 8px; background: var(--surface2);
}
.guest-question { display: block; margin-top: 4px; color: var(--text); }
.guest-composer {
  display: flex; gap: 8px; align-items: flex-end;
  padding: 16px 20px; border-top: 1px solid var(--border);
}
.guest-composer textarea {
  flex: 1; resize: vertical; min-height: 44px; max-height: 40vh;
  padding: 8px 12px; font: inherit;
  border: 1px solid var(--border); border-radius: 8px;
  background: var(--surface); color: var(--text);
}
.guest-notice { margin: 0 20px 16px; font-size: 12px; color: var(--text2); }
.guest-attach {
  align-self: center; display: inline-flex; align-items: center; justify-content: center;
  width: 36px; height: 36px; padding: 0; border: 1px solid var(--border); border-radius: 8px;
  background: var(--surface); color: var(--text2); cursor: pointer;
}
.guest-attach:hover { color: var(--text); background: var(--surface2); }
.guest-attachments { display: flex; flex-wrap: wrap; gap: 8px; padding: 12px 20px 0; }
.guest-chip {
  display: inline-flex; align-items: center; gap: 8px; max-width: 280px;
  padding: 4px 4px 4px 12px; font-size: 12px;
  border: 1px solid var(--border); border-radius: 999px; background: var(--surface2);
}
.guest-chip-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.guest-chip-state { color: var(--text2); }
.guest-chip-remove {
  width: 20px; height: 20px; padding: 0; border: 0; border-radius: 50%;
  background: none; color: var(--text2); font-size: 14px; line-height: 1; cursor: pointer;
}
.guest-chip-remove:hover { background: var(--surface3); color: var(--text); }
.sr-only {
  position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px;
  overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; border: 0;
}
</style>
