<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useUiStore } from '../../stores/ui'
import { useSessionsStore } from '../../stores/sessions'
import { useShareStore } from '../../stores/share'
import {
  getShare, startShare, stopShare, getAudience, putAudience, setShareOptions,
  type ShareState, type AudienceState, type ShareAudience,
} from '../../api'
import { formatRelativeTime } from '../../utils/time'
import { formatDisplayPath } from '../../utils/paths'
import { confirmStopSharing } from '../../utils/confirmDelete'
import MemberPicker from './MemberPicker.vue'

// Sharing one chat. The audience belongs to the Workspace App, so it
// covers every shared chat; the modal says so rather than implying a per-chat
// list the platform does not have.
const ui = useUiStore()
const sessionsStore = useSessionsStore()
const share = useShareStore()

const sid = computed(() => share.modalSessionId || '')
const session = computed(() => sessionsStore.sessions.find(s => s.id === sid.value))
const st = ref<ShareState | null>(null)
const aud = ref<AudienceState | null>(null)
const loading = ref(true)
const busy = ref(false)
const error = ref('')
// audienceError is a failed load, which leaves nothing to edit; saveError a
// failed change, shown beside the controls.
const audienceError = ref('')
const saveError = ref('')
const choice = ref<'project' | 'members'>('members')
const picked = ref<number[]>([])
const announcement = ref('')
const copied = ref(false)
const linkInput = ref<HTMLInputElement | null>(null)

const message = (e: unknown) => (e instanceof Error ? e.message : String(e))

function announce(text: string) {
  // Clearing first makes a repeated message be read again.
  announcement.value = ''
  setTimeout(() => { announcement.value = text }, 50)
}

async function load() {
  try {
    st.value = await getShare(sid.value)
    error.value = ''
  } catch (e) {
    error.value = message(e)
  } finally {
    loading.value = false
  }
}

function adopt(a: AudienceState) {
  aud.value = a
  choice.value = a.audience.project ? 'project' : 'members'
  picked.value = [...a.audience.user_ids]
}

async function loadAudience() {
  try {
    adopt(await getAudience())
    audienceError.value = ''
  } catch (e) {
    audienceError.value = message(e)
  }
}

// Keep the sidebar row in step with what the server now says.
function syncRow() {
  const s = session.value
  if (s && st.value) {
    s.shared = st.value.shared
    s.guest_count = st.value.participants.length
    s.present = st.value.present
  }
  void share.poll()
}

async function start() {
  busy.value = true
  error.value = ''
  try {
    // The app is created with the saved audience, so a pick made just before
    // must be saved first.
    if (saving) await saving
    st.value = await startShare(sid.value)
    syncRow()
    share.start()
    announce('Sharing started. Copy the link to send it.')
  } catch (e) {
    error.value = message(e)
  } finally {
    busy.value = false
  }
}

async function stop() {
  if (!confirmStopSharing(st.value?.participants.length || 0)) return
  busy.value = true
  error.value = ''
  try {
    const next = await stopShare(sid.value)
    // The last stop forgets the audience, so reload it before Share chat
    // shows again: otherwise the old people sit beside it until it arrives.
    await loadAudience()
    st.value = next
    syncRow()
    announce('Sharing stopped. Everyone you shared this chat with has lost access.')
  } catch (e) {
    error.value = message(e)
  } finally {
    busy.value = false
  }
}

async function copyLink() {
  const url = st.value?.share_url
  if (!url) return
  try {
    await navigator.clipboard.writeText(url)
    copied.value = true
    announce('Link copied.')
    setTimeout(() => { copied.value = false }, 2000)
  } catch {
    // No clipboard access (an insecure context): leave the link selected.
    linkInput.value?.select()
    announce('Select the link and copy it.')
  }
}

// Changes save as they are made. One save at a time, and one more afterwards
// if anything changed meanwhile, so quick picks never land out of order.
let saving: Promise<void> | null = null
let again = false

function persist(): Promise<void> {
  if (saving) {
    again = true
    return saving
  }
  saving = (async () => {
    try {
      do {
        again = false
        const next: ShareAudience = { user_ids: [...picked.value], project: choice.value === 'project' }
        aud.value = await putAudience(next)
      } while (again)
      saveError.value = ''
      announce('Saved. This applies to every chat you share.')
    } catch (e) {
      const why = message(e)
      await loadAudience()
      saveError.value = why
    } finally {
      saving = null
    }
  })()
  return saving
}

function choose(c: 'project' | 'members') {
  choice.value = c
  void persist()
}

// What people in this chat may do. Each switch saves as it is
// flipped; on a failure the modal re-reads, so the switch shows what holds.
const toggling = ref(false)
async function setOption(key: 'allow_permissions' | 'allow_files', on: boolean) {
  toggling.value = true
  error.value = ''
  try {
    st.value = await setShareOptions(sid.value, { [key]: on })
    const what = key === 'allow_files' ? 'work with files' : "approve the assistant's actions"
    announce(on ? `People in this chat can now ${what}.` : `People in this chat can no longer ${what}.`)
  } catch (e) {
    const why = message(e)
    await load()
    error.value = why
  } finally {
    toggling.value = false
  }
}

function pick(ids: number[]) {
  picked.value = ids
  void persist()
}

const presentCount = computed(() => share.present(sid.value))
const hereLabel = computed(() => {
  const n = st.value?.present || 0
  if (!n) return 'No one is here now'
  return `${n} ${n === 1 ? 'person' : 'people'} here now`
})

const summary = computed(() => {
  if (!st.value) return ''
  const opened = st.value.participants.length
  const openedText = opened ? ` · opened by ${opened} ${opened === 1 ? 'person' : 'people'}` : ''
  return (st.value.shared ? 'Shared' : 'Not shared') + openedText
})

// The count changes as people come and go; re-read who, and say so.
watch(presentCount, (now, before) => {
  if (!st.value?.shared) return
  void load()
  if (now > before) announce(now === 1 ? 'Someone joined this chat.' : `${now} people are here now.`)
})

onMounted(() => {
  void load()
  void loadAudience()
})
</script>

<template>
  <div
    class="modal share-modal"
    role="dialog"
    aria-modal="true"
    aria-labelledby="share-modal-title"
    @click.stop
  >
    <header class="share-head">
      <div class="share-head-text">
        <h2 id="share-modal-title">Share chat</h2>
        <p>{{ session?.name || 'Untitled chat' }}</p>
      </div>
    </header>

    <div class="share-body">
      <p v-if="loading" class="share-muted">Loading…</p>
      <template v-else>
        <p v-if="error && error !== audienceError" class="share-alert" role="alert">{{ error }}</p>

        <section v-if="st?.shared" class="share-section" aria-labelledby="share-link-h">
          <h3 id="share-link-h">Link</h3>
          <div v-if="st.share_url" class="share-link">
            <input
              ref="linkInput"
              class="form-input"
              readonly
              :value="st.share_url"
              aria-label="Link to this chat"
              @focus="($event.target as HTMLInputElement).select()"
            />
            <button class="share-btn-ghost" @click="copyLink">{{ copied ? 'Copied' : 'Copy link' }}</button>
          </div>
          <p v-if="st.share_url && !st.online" class="share-alert">
            The link won't open yet: the workspace app for shared chats is not online.
          </p>
          <p v-if="st.problem" class="share-alert">{{ st.problem }}</p>
        </section>

        <section class="share-section" aria-labelledby="share-aud-h">
          <h3 id="share-aud-h">People who can open your shared chats</h3>
          <p class="share-hint">This applies to every chat you share. Each person also needs a chat's link to open it.</p>
          <p v-if="audienceError" class="share-alert">{{ audienceError }}</p>
          <div v-else-if="aud" class="share-audience">
            <div class="share-choice" role="radiogroup" aria-labelledby="share-aud-h">
              <label class="share-radio">
                <input type="radio" name="share-audience" value="project" :checked="choice === 'project'" @change="choose('project')" />
                Everyone in the project
              </label>
              <label class="share-radio">
                <input type="radio" name="share-audience" value="members" :checked="choice === 'members'" @change="choose('members')" />
                Select project members
              </label>
            </div>
            <template v-if="choice === 'members'">
              <p v-if="!aud.members.length" class="share-muted">No one else is a member of this project.</p>
              <template v-else>
                <MemberPicker :members="aud.members" :selected="picked" label="Choose project members" @change="pick" />
                <p v-if="!picked.length" class="share-hint">No one is chosen yet, so no one can open your shared chats.</p>
              </template>
            </template>
            <p v-if="saveError" class="share-alert" role="alert">{{ saveError }}</p>
          </div>
          <p v-else class="share-muted">Loading…</p>
        </section>

        <section v-if="st && (st.shared || st.participants.length)" class="share-section" aria-labelledby="share-people-h">
          <h3 id="share-people-h">{{ st.shared ? hereLabel : 'People who opened this chat' }}</h3>
          <div v-if="st.participants.length" class="share-rows">
            <div
              v-for="p in st.participants"
              :key="p.id"
              class="share-row person"
              :class="{ on: p.present }"
            >
              <span class="share-row-main">{{ p.name }}</span>
              <span class="share-row-meta">{{ p.present ? 'Here now' : `Last here ${formatRelativeTime(p.last_seen).replace(/^Just now$/, 'just now')}` }}</span>
            </div>
          </div>
          <p v-else class="share-muted">No one has opened this chat yet.</p>
        </section>

        <section v-if="st?.shared" class="share-section" aria-labelledby="share-allow-h">
          <h3 id="share-allow-h">What people in this chat can do</h3>
          <div class="share-rows">
            <div class="share-row option" :class="{ on: st.allow_permissions }">
              <span class="share-row-main">
                <span class="share-option-name">Approve the assistant's actions</span>
                <span class="share-option-hint">
                  They see what the assistant wants to do, and can allow it once or deny it.
                  When this is off, only you can.
                </span>
              </span>
              <label class="share-switch">
                <input
                  type="checkbox"
                  role="switch"
                  :checked="st.allow_permissions"
                  :aria-checked="st.allow_permissions"
                  :disabled="toggling"
                  aria-label="Let people in this chat approve the assistant's actions"
                  @change="setOption('allow_permissions', ($event.target as HTMLInputElement).checked)"
                />
                <span class="share-track" aria-hidden="true"></span>
              </label>
            </div>
            <div class="share-row option" :class="{ on: st.allow_files }">
              <span class="share-row-main">
                <span class="share-option-name">Work with files</span>
                <span v-if="st.files_folder" class="share-option-hint">
                  They can open, download, edit and add files in
                  <code>{{ formatDisplayPath(st.files_folder) }}</code>, but not delete, rename or move them.
                </span>
                <span v-else class="share-option-hint">{{ st.files_unavailable }}</span>
              </span>
              <label class="share-switch">
                <input
                  type="checkbox"
                  role="switch"
                  :checked="st.allow_files"
                  :aria-checked="st.allow_files"
                  :disabled="toggling || (!st.allow_files && !!st.files_unavailable)"
                  aria-label="Let people in this chat work with files"
                  @change="setOption('allow_files', ($event.target as HTMLInputElement).checked)"
                />
                <span class="share-track" aria-hidden="true"></span>
              </label>
            </div>
          </div>
        </section>

        <div class="share-notice">
          Anyone who can open your shared chats can send messages to this chat. The
          assistant runs in <strong>your</strong> workspace, with <strong>your</strong>
          credentials, and can read and change <strong>your</strong> files. It will also
          use your model allowance. Share only with people you would trust to sit at
          your keyboard.
        </div>
      </template>
    </div>

    <p class="sr-only" role="status" aria-live="polite">{{ announcement }}</p>

    <footer class="share-foot">
      <span class="share-summary">{{ summary }}</span>
      <div class="share-foot-actions">
        <button class="share-btn-ghost" @click="ui.closeModal()">Close</button>
        <template v-if="st">
          <button v-if="!st.shared" class="share-btn" :disabled="busy" @click="start">
            {{ busy ? 'Sharing…' : 'Share chat' }}
          </button>
          <button v-else class="share-btn-danger" :disabled="busy" @click="stop">
            {{ busy ? 'Stopping…' : 'Stop sharing' }}
          </button>
        </template>
      </div>
    </footer>
  </div>
</template>

<style scoped>
/* One column, so the width of AccountsModal rather than the catalogues'.
   The shared .modal rule scrolls its whole body; this one scrolls the content
   pane, so the header and footer stay put. */
.share-modal {
  width: min(760px, 95vw);
  max-height: 90vh;
  padding: 0;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

.share-head {
  display: flex; align-items: flex-start; gap: 16px;
  padding: 20px 20px 16px;
  border-bottom: 1px solid var(--border);
}
.share-head-text { flex: 1; min-width: 0; }
.share-head h2 { font-size: 19px; font-weight: 700; margin: 0 0 4px; }
.share-head p { font-size: 12px; color: var(--text2); margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.share-body { flex: 1; min-height: 0; overflow-y: auto; padding: 16px 20px; display: flex; flex-direction: column; gap: 24px; }

.share-section h3 { font-size: 13px; font-weight: 600; margin: 0 0 8px; }
.share-hint, .share-muted { font-size: 12px; color: var(--text2); margin: 0 0 8px; }
.share-alert {
  font-size: 12px; margin: 8px 0 0; padding: 8px 12px; border-radius: 7px;
  border: 1px solid var(--border);
  background: color-mix(in srgb, var(--orange) 12%, transparent);
  color: var(--text);
}

.share-link { display: flex; gap: 8px; }
.share-link .form-input { flex: 1; min-width: 0; font-size: 12px; }

.share-rows { display: flex; flex-direction: column; gap: 4px; }
/* The left edge carries state, as in #195: full for here now, muted for has visited. */
.share-row {
  display: flex; align-items: center; gap: 8px;
  padding: 8px 12px;
  border: 1px solid var(--border);
  border-left: 3px solid color-mix(in srgb, var(--accent) 32%, transparent);
  border-radius: 7px;
  background: var(--surface2);
  font-size: 13px;
}
.share-row.on { border-left-color: var(--accent); }
.share-row-main { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.share-row-meta { font-size: 12px; color: var(--text2); flex-shrink: 0; }

.share-row.option { align-items: flex-start; gap: 16px; }
.share-row.option .share-row-main { white-space: normal; display: flex; flex-direction: column; gap: 2px; }
.share-option-name { font-weight: 600; }
.share-option-hint { font-size: 12px; color: var(--text2); line-height: 1.45; }
.share-option-hint code { font-size: 11px; overflow-wrap: anywhere; }
/* The Connectors switch (ConnectorRow.vue), so one toggle reads the same everywhere. */
.share-switch { position: relative; display: inline-flex; align-items: center; cursor: pointer; flex-shrink: 0; margin-top: 2px; }
.share-switch input { position: absolute; opacity: 0; width: 0; height: 0; }
.share-track {
  position: relative; display: block; width: 34px; height: 20px; border-radius: 999px;
  background: var(--surface3); border: 1px solid var(--border);
  transition: background .12s, border-color .12s;
}
.share-track::after {
  content: ''; position: absolute; top: 2px; left: 2px; width: 14px; height: 14px;
  border-radius: 50%; background: var(--text3); transition: transform .12s, background .12s;
}
.share-switch input:checked + .share-track { background: var(--green); border-color: var(--green); }
.share-switch input:checked + .share-track::after { transform: translateX(14px); background: #fff; }
.share-switch input:disabled + .share-track { opacity: .55; cursor: default; }
.share-switch input:focus-visible + .share-track { outline: 2px solid var(--accent); outline-offset: 2px; }

.share-notice {
  font-size: 12px; line-height: 1.5; color: var(--text2);
  padding: 12px; border-radius: 7px;
  border: 1px solid var(--border);
  background: var(--surface2);
}
.share-notice strong { color: var(--text); }

.share-foot {
  display: flex; align-items: center; justify-content: space-between; gap: 16px;
  padding: 12px 20px;
  border-top: 1px solid var(--border);
  background: var(--surface2);
}
.share-summary { font-size: 12px; color: var(--text2); }
.share-foot-actions { display: flex; gap: 8px; flex-shrink: 0; }

.share-audience { display: flex; flex-direction: column; gap: 12px; }
.share-choice { display: flex; flex-direction: column; gap: 8px; }
.share-radio { display: flex; align-items: center; gap: 8px; font-size: 13px; cursor: pointer; }
.share-radio input { margin: 0; accent-color: var(--accent); }
.share-audience .share-hint { margin: 0; }

.share-btn {
  background: var(--accent); color: #fff; border: 1px solid transparent;
  padding: 7px 16px; border-radius: 7px; cursor: pointer; font-size: 13px; font-weight: 600;
}
.share-btn:hover { background: var(--accent-h); }
.share-btn-ghost {
  background: var(--surface2); border: 1px solid var(--border); color: var(--text2);
  padding: 7px 16px; border-radius: 7px; cursor: pointer; font-size: 13px; white-space: nowrap;
}
.share-btn-ghost:hover { color: var(--text); background: var(--surface3); }
.share-btn-danger {
  background: var(--surface2); border: 1px solid var(--red); color: var(--red);
  padding: 7px 16px; border-radius: 7px; cursor: pointer; font-size: 13px; font-weight: 600;
}
.share-btn-danger:hover { background: color-mix(in srgb, var(--red) 12%, transparent); }
.share-btn:disabled, .share-btn-ghost:disabled, .share-btn-danger:disabled { opacity: .6; cursor: default; }

@media (max-width: 760px) {
  .share-foot { flex-wrap: wrap; }
}
</style>
