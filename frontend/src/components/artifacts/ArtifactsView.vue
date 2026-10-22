<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { nextTick, onMounted, ref } from 'vue'
import { useArtifactsStore } from '../../stores/artifacts'
import { useSessionsStore } from '../../stores/sessions'
import { useUiStore } from '../../stores/ui'
import { repairArtifact, stopArtifact, type Artifact } from '../../api'
import { audienceLabel, STATUS_LABEL } from '../../utils/artifacts'
import { PASTE_LINK_HINT } from '../../utils/shareLinks'
import { formatAbsoluteTime, formatRelativeTime } from '../../utils/time'

// Every shared file, across chats. Loading the list reconciles it with the
// platform, so opening the page shows sharing removed outside the app.
const artifacts = useArtifactsStore()
const sessionsStore = useSessionsStore()
const ui = useUiStore()
const busyId = ref('')

// The report skill makes the file shareable; the prompt only has to ask for one.
const TRY_PROMPT = 'Build an interactive dashboard of … as a single file I can share with my coworkers.'

const toast = (m: string) => (window as any).showToast?.(m)
const message = (e: unknown) => (e instanceof Error ? e.message : String(e))

onMounted(() => { void artifacts.load() })

function iconFor(name: string): string {
  const e = name.split('.').pop()?.toLowerCase() || ''
  if (['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg'].includes(e)) return '🖼️'
  if (['md', 'markdown'].includes(e)) return '📝'
  if (e === 'pdf') return '📕'
  return '📄'
}

function chatName(a: Artifact): string {
  return sessionsStore.sessions.find(s => s.id === a.session_id)?.name || a.chat_label || 'Untitled chat'
}

async function openChat(a: Artifact) {
  try {
    await sessionsStore.switchSession(a.session_id)
    ui.showChat()
  } catch {
    toast("Couldn't open the chat. It may have been deleted.")
  }
}

async function copyLink(a: Artifact) {
  try {
    await navigator.clipboard.writeText(a.url)
    ;(window as any).showToast?.(`Link copied. ${PASTE_LINK_HINT}`, 8000)
  } catch {
    toast(`Couldn't copy the link: ${a.url}`)
  }
}

async function stop(a: Artifact) {
  if (!confirm(`Stop sharing ${a.name}?\n\nEveryone you shared it with loses access immediately.`)) return
  busyId.value = a.id
  try {
    await stopArtifact(a.id)
    artifacts.drop(a.id)
    toast(`Stopped sharing ${a.name}`)
  } catch (e) {
    toast(message(e))
  } finally {
    busyId.value = ''
  }
}

// A removed app comes back shared with no one, so the dialog opens to choose.
async function repair(a: Artifact) {
  busyId.value = a.id
  try {
    artifacts.put(await repairArtifact(a.id))
    toast(`Repaired ${a.name}`)
    if (a.status === 'removed') artifacts.openShare(a.session_id, a.path)
  } catch (e) {
    toast(message(e))
  } finally {
    busyId.value = ''
  }
}

// A chat with the example prompt in its composer, not sent: the current one
// when it's an untouched new chat.
async function tryIt() {
  ui.showChat()
  const cur = sessionsStore.currentSession
  const untouched = cur && (!cur.name || /^new chat$/i.test(cur.name)) &&
    sessionsStore.currentMessages.length === 0 && !sessionsStore.currentDraft
  if (!untouched) {
    try {
      await sessionsStore.createSession({ name: 'New chat' })
    } catch {
      toast('Could not start a new chat')
      return
    }
  }
  const sid = sessionsStore.currentSessionId
  if (!sid) return
  sessionsStore.setDraft(sid, TRY_PROMPT)
  await nextTick()
  const input = document.querySelector('#prompt-input') as HTMLTextAreaElement | null
  if (!input) return
  input.focus()
  const gap = TRY_PROMPT.indexOf('…')
  input.setSelectionRange(gap, gap + 1)
}
</script>

<template>
  <div id="artifacts-view">
    <div class="av-header">
      <h1>Artifacts</h1>
      <p class="av-sub">Files from your chats that you share with coworkers. They open while your workspace is running.</p>
    </div>

    <div class="av-body">
      <p v-if="artifacts.loaded && !artifacts.enabled" class="av-alert" role="status">
        Sharing files is turned off in this workspace.{{ artifacts.problem ? ` ${artifacts.problem}` : '' }}
      </p>
      <p v-else-if="artifacts.problem" class="av-alert" role="status">{{ artifacts.problem }}</p>

      <div v-if="!artifacts.loaded" class="av-muted">Loading…</div>

      <div v-else-if="!artifacts.list.length" class="av-guide">
        <h2>Share what you build</h2>
        <p>
          An artifact is a report, a dashboard or a small tool from one of your chats that you share with
          coworkers. They open it in their browser and always see the latest version.
        </p>
        <ol>
          <li>Ask the assistant for what you need. It knows how to build it so it's ready to share.</li>
          <li>In the chat's <strong>Files</strong>, point to the file and select <strong>Share</strong>.</li>
          <li>Choose who can open it, and send them the link.</li>
        </ol>
        <button class="btn btn-primary" @click="tryIt">Try it</button>
        <p class="av-note">
          A shared file shows only what's inside it, so the assistant puts everything a report needs into the
          file itself: its data, charts and styling. If a file you share loads something from the internet or
          from other files, the assistant can fix it for you.
        </p>
      </div>

      <ul v-else class="av-list" aria-label="Shared files">
        <li v-for="a in artifacts.list" :key="a.id" class="av-row" :class="{ attention: a.status !== 'shared' }">
          <span class="av-icon" aria-hidden="true">{{ iconFor(a.name) }}</span>
          <div class="av-info">
            <div class="av-title">
              <span class="av-name" :title="a.path">{{ a.name }}</span>
              <span class="av-status" :class="a.status">{{ STATUS_LABEL[a.status] }}</span>
            </div>
            <div class="av-meta">
              <button v-if="a.session_id" class="av-chat" @click="openChat(a)">{{ chatName(a) }}</button>
              <span v-else>Its chat was deleted</span>
              <template v-if="a.audience"><span aria-hidden="true">·</span><span>{{ audienceLabel(a.audience) }}</span></template>
              <template v-if="a.updated">
                <span aria-hidden="true">·</span>
                <span :title="formatAbsoluteTime(a.updated)">Updated {{ formatRelativeTime(a.updated).replace(/^Just now$/, 'just now') }}</span>
              </template>
            </div>
            <p v-if="a.problem" class="av-problem">{{ a.problem }}</p>
          </div>
          <div class="av-actions">
            <button
              v-if="a.status === 'removed' || a.status === 'unavailable'"
              class="av-btn primary"
              :disabled="busyId === a.id"
              @click="repair(a)"
            >
              Repair
            </button>
            <button class="av-btn" @click="copyLink(a)">Copy link</button>
            <a class="av-btn" :href="a.url" target="_blank" rel="noopener noreferrer">Open</a>
            <button class="av-btn" @click="artifacts.openShare(a.session_id, a.path)">Change sharing</button>
            <button class="av-btn danger" :disabled="busyId === a.id" @click="stop(a)">Stop sharing</button>
          </div>
        </li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
/* Laid out like the Scheduled tasks view. */
#artifacts-view { flex: 1; display: flex; flex-direction: column; overflow: hidden; min-width: 0; }
.av-header { padding: 20px 24px 16px; border-bottom: 1px solid var(--border); }
.av-header h1 { font-size: 20px; font-weight: 700; color: var(--text); margin: 0; }
.av-sub { font-size: 13px; color: var(--text2); margin: 4px 0 0; max-width: 860px; line-height: 1.4; }

.av-body { flex: 1; overflow-y: auto; padding: 18px 24px 32px; }
.av-muted { font-size: 13px; color: var(--text2); }
.av-alert {
  max-width: 860px; font-size: 12.5px; margin: 0 0 14px; padding: 8px 12px; border-radius: 8px;
  border: 1px solid var(--border); background: color-mix(in srgb, var(--orange) 12%, transparent); color: var(--text);
}

.av-guide { max-width: 620px; margin: 24px auto; font-size: 14px; line-height: 1.55; color: var(--text2); }
.av-guide h2 { font-size: 18px; font-weight: 700; color: var(--text); margin: 0 0 10px; }
.av-guide p { margin: 0 0 12px; }
.av-guide ol { margin: 0 0 12px; padding-left: 22px; }
.av-guide li { margin-bottom: 4px; }
.av-guide strong { color: var(--text); }
.av-guide .av-note { margin: 20px 0 0; font-size: 12.5px; color: var(--text3); }

.av-list { list-style: none; margin: 0; padding: 0; max-width: 980px; }
.av-row {
  display: flex; align-items: flex-start; gap: 12px;
  padding: 12px 14px; margin-bottom: 10px;
  border: 1px solid var(--border); border-radius: 12px; background: var(--surface);
}
.av-row.attention { border-left: 3px solid var(--orange); }
.av-icon { font-size: 18px; flex-shrink: 0; line-height: 1.4; }
.av-info { flex: 1; min-width: 0; }
.av-title { display: flex; align-items: center; gap: 8px; min-width: 0; }
.av-name { font-size: 15px; font-weight: 600; color: var(--text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.av-status {
  flex-shrink: 0; font-size: 10.5px; font-weight: 700; text-transform: uppercase; letter-spacing: .04em;
  border-radius: 5px; padding: 1px 6px; color: var(--green); border: 1px solid currentColor;
}
.av-status:not(.shared) { color: var(--orange); }
.av-meta { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; font-size: 12px; color: var(--text3); margin-top: 3px; }
.av-chat {
  background: none; border: none; padding: 0; font: inherit; color: var(--accent); cursor: pointer;
  max-width: 260px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.av-chat:hover { text-decoration: underline; }
.av-problem { font-size: 12.5px; color: var(--text); margin: 6px 0 0; line-height: 1.4; }

.av-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 6px; flex-shrink: 0; }
.av-btn {
  display: inline-flex; align-items: center;
  padding: 5px 10px; font-size: 12px; font-weight: 500; font-family: inherit; text-decoration: none;
  border: 1px solid var(--border); border-radius: 7px; background: none; color: var(--text2);
  cursor: pointer; transition: all .12s; white-space: nowrap;
}
.av-btn:hover:not(:disabled) { color: var(--text); border-color: var(--text3); background: var(--surface2); }
.av-btn:disabled { opacity: .5; cursor: default; }
.av-btn.primary { background: var(--accent); border-color: transparent; color: #fff; font-weight: 600; }
.av-btn.primary:hover:not(:disabled) { background: var(--accent-h); color: #fff; }
.av-btn.danger:hover:not(:disabled) { color: var(--red); border-color: var(--red); background: rgba(218,63,63,.1); }

.btn {
  padding: 8px 14px; font-size: 13px; font-weight: 600; font-family: inherit;
  border-radius: 8px; cursor: pointer; border: 1px solid transparent;
}
.btn-primary { background: var(--accent); color: #fff; }
.btn-primary:hover { background: var(--accent-h); }

@media (max-width: 760px) {
  .av-row { flex-wrap: wrap; }
  .av-actions { justify-content: flex-start; }
}
</style>
