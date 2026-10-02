<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, watch, nextTick, onMounted } from 'vue'
import { useSessionsStore } from '../../stores/sessions'
import { useChatStore } from '../../stores/chat'
import { useUiStore } from '../../stores/ui'
import { useProjectsStore } from '../../stores/projects'
import { useGitSync } from '../../composables/useGitSync'
import { fetchMe } from '../../api'
import { useMe } from '../../composables/useMe'
import { useShareStore } from '../../stores/share'

const sessionsStore = useSessionsStore()
const chatStore = useChatStore()
const ui = useUiStore()
const projectsStore = useProjectsStore()
const share = useShareStore()

// Chat sharing: who is here, live, while the open chat is shared.
const hereCount = computed(() => (currentSession.value?.shared ? share.present(currentSession.value.id) : 0))

const editingTitle = ref(false)
const titleInput = ref('')
const titleInputEl = ref<HTMLInputElement | null>(null)

const currentSession = computed(() => sessionsStore.currentSession)
const sessionTitle = computed(() => currentSession.value?.name || 'Untitled chat')
const workspacePath = computed(() => currentSession.value?.workspace || '')

// Projects: the project this chat belongs to (if any), for the "In project" pill.
const currentProject = computed(() =>
  sessionsStore.currentProjectId ? projectsStore.getProject(sessionsStore.currentProjectId) : undefined,
)

// Only project chats get the periodic remote refresh + git suggestions.
// A project chat's workspace is the shared, repo-backed project space.
const inProjectChat = computed(() => !!currentProject.value)
const git = useGitSync(workspacePath, {
  fetchIntervalMs: 60000,
  fetchEnabled: () => inProjectChat.value,
})
const showGitLog = ref(false)
watch(() => git.log.value, (v) => { if (v) showGitLog.value = true })

// ── Git suggestions: nudge the user to commit uncommitted work and to
// pull when the shared repo has moved ahead. Only for project chats whose
// workspace is a git repo with a remote. Each is independently dismissible;
// dismissal resets when the underlying condition clears so it can resurface.
const commitDismissed = ref(false)
const pullDismissed = ref(false)

const suggestCommit = computed(
  () => inProjectChat.value && !!git.status.value?.is_repo && !!git.status.value?.dirty && !commitDismissed.value,
)
const suggestPull = computed(
  () =>
    inProjectChat.value &&
    !!git.status.value?.is_repo &&
    !!git.status.value?.has_remote &&
    (git.status.value?.behind || 0) > 0 &&
    !pullDismissed.value,
)
const behindCount = computed(() => git.status.value?.behind || 0)

// Reset a dismissal once its condition no longer holds, so a fresh occurrence
// re-surfaces the suggestion.
watch(() => git.status.value?.dirty, (d) => { if (!d) commitDismissed.value = false })
watch(() => git.status.value?.behind, (b) => { if (!b) pullDismissed.value = false })

function openSourceControl() {
  ui.openModal('source-control')
}
async function doPull() {
  await git.pull()
}

function openSettings() {
  ui.openModal('session-settings')
}

async function startEditTitle() {
  if (!currentSession.value) return
  titleInput.value = currentSession.value.name || ''
  editingTitle.value = true
  await nextTick()
  titleInputEl.value?.focus()
  titleInputEl.value?.select()
}

async function commitTitle() {
  if (!editingTitle.value) return
  editingTitle.value = false
  const newName = titleInput.value.trim()
  const session = currentSession.value
  if (!session || !newName || newName === session.name) return
  await sessionsStore.renameSession(session.id, newName)
}

function cancelEditTitle() {
  editingTitle.value = false
}

// Reset edit state if session changes
watch(() => sessionsStore.currentSessionId, () => { editingTitle.value = false })

// SecurSpaces workspace info (env-derived) — used to build "Open in VSCode" link.
const { baseDir } = useMe()
const workspaceId = ref('')
const appsDomain = ref('')
onMounted(async () => {
  try {
    const me = await fetchMe()
    workspaceId.value = me.workspace_id || ''
    appsDomain.value = me.apps_domain || ''
  } catch { /* non-fatal */ }
})
const vscodeUrl = computed(() => {
  if (!workspaceId.value || !appsDomain.value) return ''
  const folder = workspacePath.value || baseDir.value
  return `https://vscode-ws-${workspaceId.value}.${appsDomain.value}/?folder=${encodeURIComponent(folder)}`
})
function openInVscode() {
  if (!vscodeUrl.value) return
  window.open(vscodeUrl.value, '_blank', 'noopener,noreferrer')
}
</script>

<template>
  <div id="chat-header">
    <button
      class="hamburger-btn"
      @click="ui.toggleSidebar()"
      :aria-label="ui.sidebarOpen ? 'Close menu' : 'Open menu'"
      title="Menu"
    >
      <span></span><span></span><span></span>
    </button>
    <div class="hdr-main">
      <div class="title-row">
        <input
          v-if="editingTitle"
          ref="titleInputEl"
          v-model="titleInput"
          class="title-input"
          @keydown.enter="commitTitle"
          @keydown.esc="cancelEditTitle"
          @blur="commitTitle"
        />
        <button
          v-else
          class="chat-title"
          :title="sessionTitle + ' — click to rename'"
          @click="startEditTitle"
        >
          {{ sessionTitle }}
          <span class="title-edit-hint">✎</span>
        </button>
        <button
          v-if="currentProject"
          class="in-project-pill"
          @click="ui.openProject(currentProject.id)"
          :title="'In project ' + currentProject.name + ' — open project view'"
        >
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" width="12" height="12" aria-hidden="true">
            <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" stroke-linecap="round" stroke-linejoin="round"/>
          </svg>
          In {{ currentProject.name }}
        </button>
        <div class="hdr-actions">
          <button
            v-if="currentSession?.shared"
            class="here-pill"
            :class="{ someone: hereCount > 0 }"
            @click="share.openModal(currentSession.id)"
            :title="hereCount ? 'People in this chat now. Open sharing' : 'This chat is shared. Open sharing'"
          >
            <span class="here-dot" aria-hidden="true"></span>
            {{ hereCount ? `${hereCount} ${hereCount === 1 ? 'person' : 'people'} here` : 'Shared' }}
          </button>
          <button class="stop-btn" @click="chatStore.stopStreaming()" v-if="chatStore.streaming" title="Stop response">
            <span aria-hidden="true">⏹</span>
            <span>Stop</span>
          </button>
          <button
            class="icon-btn files-btn"
            :class="{ active: ui.mobileFilesOpen }"
            @click="ui.toggleMobileFiles()"
            :title="ui.mobileFilesOpen ? 'Close files' : 'Browse files'"
            :aria-label="ui.mobileFilesOpen ? 'Close files' : 'Browse files'"
            :aria-pressed="ui.mobileFilesOpen"
          >
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
              <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
          </button>
          <button
            v-if="vscodeUrl"
            class="icon-btn vscode-btn"
            @click="openInVscode"
            :title="'Open ' + (workspacePath || baseDir) + ' in VSCode'"
            aria-label="Open in VSCode"
          >
            <svg width="16" height="16" viewBox="0 0 24 24" aria-hidden="true" fill="currentColor">
              <path d="M17.5 2.5 21 4v16l-3.5 1.5L9 13.6 4.5 17 2 15.5V8.5L4.5 7 9 10.4l8.5-7.9zm-.5 4.4L11.7 12l5.3 5.1V6.9zM4.6 12 7 14l2.6-2-2.6-2-2.4 2z"/>
            </svg>
          </button>
          <button class="icon-btn" @click="openSettings" title="Session settings" aria-label="Session settings">⚙</button>
        </div>
      </div>

      <div v-if="git.status.value?.is_repo" class="git-row">
        <button
          class="sc-chip"
          :class="{ dirty: git.status.value.dirty }"
          @click="ui.openModal('source-control')"
          :title="(git.status.value.upstream ? 'Tracking ' + git.status.value.upstream : 'No upstream configured') + ' — click for Source Control'"
        >
          <span class="sc-chip-icon" aria-hidden="true">⎇</span>
          <span class="sc-chip-branch">{{ git.status.value.branch || 'detached' }}</span>
          <span v-if="git.status.value.behind > 0" class="git-count behind" title="Behind upstream">↓{{ git.status.value.behind }}</span>
          <span v-if="git.status.value.ahead > 0" class="git-count ahead" title="Ahead of upstream">↑{{ git.status.value.ahead }}</span>
          <span v-if="git.status.value.dirty" class="git-dot" aria-hidden="true">●</span>
        </button>
        <button class="mini-btn" :disabled="git.busy.value || !git.status.value.has_remote" @click="git.pull()" :title="git.status.value.has_remote ? 'git pull --ff-only' : 'No remote configured'">
          ⬇ Pull
        </button>
        <button class="mini-btn" :disabled="git.busy.value || !git.status.value.has_remote" @click="git.push()" :title="git.status.value.has_remote ? 'git push' : 'No remote configured'">
          ⬆ Push
        </button>
        <button class="mini-btn primary" @click="ui.openModal('source-control')" title="Open Source Control panel">
          ✓ Commit…
        </button>
      </div>
    </div>

    <div v-if="showGitLog && git.log.value" class="git-log-popover" role="status">
      <div class="git-log-header">
        <strong>Git output</strong>
        <button class="icon-btn" @click="showGitLog = false" aria-label="Dismiss" title="Dismiss">✕</button>
      </div>
      <pre class="git-log-body">{{ git.log.value }}</pre>
    </div>

    <!-- Git suggestions (project chats, repo-backed workspace) -->
    <div v-if="suggestPull" class="git-suggest pull" role="status">
      <span class="gs-ico" aria-hidden="true">⬇</span>
      <span class="gs-text">
        The project repo has <strong>{{ behindCount }}</strong> new commit{{ behindCount === 1 ? '' : 's' }} — pull to update your workspace.
      </span>
      <button class="gs-action" :disabled="git.busy.value" @click="doPull">Pull</button>
      <button class="gs-dismiss" @click="pullDismissed = true" aria-label="Dismiss" title="Dismiss">✕</button>
    </div>
    <div v-if="suggestCommit" class="git-suggest commit" role="status">
      <span class="gs-ico" aria-hidden="true">✎</span>
      <span class="gs-text">You have uncommitted changes in this project.</span>
      <button class="gs-action" @click="openSourceControl">Commit…</button>
      <button class="gs-dismiss" @click="commitDismissed = true" aria-label="Dismiss" title="Dismiss">✕</button>
    </div>
  </div>
</template>

<style scoped>
#chat-header {
  padding: 10px 16px;
  border-bottom: 1px solid var(--border);
  background: var(--surface);
  display: flex;
  align-items: center;
  gap: 14px;
  /* One row now that the mode/model/workspace chips have gone. Sized to the
     34px action buttons plus padding rather than the old two-row 64px. */
  min-height: 54px;
  transition: background .2s, border-color .2s;
  position: relative;
}

/* Git widget */
.git-chip {
  display: inline-flex; align-items: center; gap: 4px;
  padding: 2px 8px; border-radius: 999px;
  border: 1px solid var(--border); background: var(--surface2);
  color: var(--text2); font-size: 11px; font-weight: 600;
  white-space: nowrap; max-width: 140px; overflow: hidden; text-overflow: ellipsis;
}
.git-chip.dirty { color: #b86b00; border-color: #b86b00; }
.git-count { font-weight: 700; font-size: 10px; }
.git-count.ahead  { color: var(--blue); }
.git-count.behind { color: #cf222e; }
.git-dot { color: #b86b00; font-size: 9px; margin-left: 2px; }

.git-suggest {
  display: flex; align-items: center; gap: 8px;
  margin: 8px 0 0; padding: 8px 12px; border-radius: 8px;
  font-size: 13px; color: var(--text);
}
.git-suggest.pull { background: rgba(3,169,241,.1); border: 1px solid rgba(3,169,241,.28); }
.git-suggest.commit { background: rgba(245,158,11,.1); border: 1px solid rgba(245,158,11,.3); }
.git-suggest .gs-ico { flex-shrink: 0; }
.git-suggest.pull .gs-ico { color: var(--accent); }
.git-suggest.commit .gs-ico { color: var(--orange, #f59e0b); }
.git-suggest .gs-text { flex: 1; min-width: 0; }
.git-suggest .gs-text strong { font-weight: 700; }
.git-suggest .gs-action {
  flex-shrink: 0; padding: 5px 12px; border-radius: 7px; font-size: 12px; font-weight: 600;
  border: none; cursor: pointer; color: #fff; font-family: inherit;
}
.git-suggest.pull .gs-action { background: var(--accent); }
.git-suggest.pull .gs-action:hover { background: var(--accent-h); }
.git-suggest.commit .gs-action { background: var(--orange, #f59e0b); }
.git-suggest.commit .gs-action:hover { filter: brightness(1.05); }
.git-suggest .gs-action:disabled { opacity: .6; cursor: default; }
.git-suggest .gs-dismiss {
  flex-shrink: 0; background: none; border: none; color: var(--text3);
  cursor: pointer; font-size: 12px; padding: 2px 4px;
}
.git-suggest .gs-dismiss:hover { color: var(--text); }

.git-log-popover {
  position: absolute;
  top: calc(100% + 4px);
  right: 16px;
  width: min(560px, calc(100% - 32px));
  z-index: 30;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 8px;
  box-shadow: 0 6px 20px rgba(0,0,0,.18);
  overflow: hidden;
}
.git-log-header {
  display: flex; align-items: center; justify-content: space-between;
  padding: 6px 10px;
  border-bottom: 1px solid var(--border);
  background: var(--surface2);
  font-size: 12px;
}
.git-log-body {
  margin: 0; padding: 10px 12px;
  font-size: 11px; line-height: 1.4; color: var(--text2);
  white-space: pre-wrap; max-height: 240px; overflow: auto;
}

/* Hamburger menu — hidden on desktop, shown on mobile */
.hamburger-btn {
  display: none;
  width: 38px; height: 38px;
  flex-direction: column;
  justify-content: center;
  align-items: center;
  gap: 4px;
  background: none;
  border: 1px solid var(--border);
  border-radius: 8px;
  cursor: pointer;
  padding: 0;
  flex-shrink: 0;
  transition: background .15s, border-color .15s;
}
.hamburger-btn:hover { background: var(--surface2); border-color: var(--text2); }
.hamburger-btn:active { background: var(--surface3); }
.hamburger-btn span {
  display: block;
  width: 18px; height: 2px;
  background: var(--text);
  border-radius: 2px;
}
.hdr-main {
  display: flex; flex-direction: column; align-items: flex-start; justify-content: center;
  gap: 5px; min-width: 0; overflow: hidden;
  flex: 1 1 auto;
}
.title-row,
.hdr-actions { display: flex; align-items: center; min-width: 0; }
.title-row { gap: 8px; width: 100%; justify-content: flex-start; }
.hdr-actions { gap: 7px; flex: 0 0 auto; margin-left: auto; }

.git-row {
  display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
  width: 100%;
  padding-top: 4px;
}

.sc-chip {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 3px 10px;
  background: var(--surface2);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 999px;
  font-size: 12px; font-weight: 500;
  cursor: pointer;
  transition: background .15s, border-color .15s, color .15s;
}
.sc-chip:hover { background: var(--surface3); border-color: var(--text2); }
.sc-chip.dirty { color: #b86b00; border-color: #b86b00; }
.sc-chip-icon { font-size: 12px; }
.sc-chip-branch { font-weight: 600; }

.mini-btn {
  display: inline-flex; align-items: center; gap: 4px;
  padding: 3px 9px;
  background: var(--surface2);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 6px;
  font-size: 12px; font-weight: 500;
  cursor: pointer;
  transition: background .15s, border-color .15s, color .15s;
}
.mini-btn:hover:not(:disabled) { background: var(--surface3); border-color: var(--text2); }
.mini-btn:disabled { opacity: .45; cursor: not-allowed; }
.mini-btn.primary {
  background: var(--accent, #1f6feb); color: #fff; border-color: transparent;
}
.mini-btn.primary:hover:not(:disabled) { background: var(--accent-hover, #388bfd); }

.git-tag {
  display: inline-block; padding: 1px 7px; border-radius: 999px;
  font-size: 11px; line-height: 16px;
  background: var(--surface2); color: var(--text2); border: 1px solid var(--border);
}
.git-tag.dirty { color: #b86b00; border-color: #b86b00; }
.git-tag.clean { color: #2da44e; border-color: #2da44e; }

.chat-title {
  background: none; border: none; cursor: pointer;
  font-size: 15px; font-weight: 650; color: var(--text);
  padding: 2px 6px; margin-left: -6px; border-radius: 8px;
  display: inline-flex; align-items: center; gap: 6px;
  min-width: 0; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  line-height: 1.2;
  transition: background .15s;
  flex: 0 1 auto;
}
.chat-title:hover { background: var(--surface2); }
.title-edit-hint {
  font-size: 11px; color: var(--text3); opacity: 0; transition: opacity .15s;
}
.chat-title:hover .title-edit-hint { opacity: 1; }
.title-input {
  font-size: 15px; font-weight: 650; color: var(--text);
  background: var(--surface2); border: 1px solid var(--accent);
  border-radius: 8px; padding: 4px 8px; outline: none;
  min-width: 240px; max-width: min(520px, 100%);
}

.in-project-pill {
  display: inline-flex; align-items: center; gap: 5px; flex-shrink: 0;
  padding: 3px 9px; border-radius: 999px; font-size: 12px; font-weight: 600;
  background: rgba(3,169,241,.1); color: var(--accent);
  border: 1px solid rgba(3,169,241,.25); cursor: pointer; font-family: inherit;
  max-width: 200px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  transition: background .12s;
}
.in-project-pill:hover { background: rgba(3,169,241,.18); }
.icon-btn,
.stop-btn {
  background: none; border: 1px solid var(--border); color: var(--text2);
  border-radius: 8px; cursor: pointer;
  transition: all .15s; display: flex; align-items: center; justify-content: center;
  white-space: nowrap;
}
.icon-btn {
  width: 34px; height: 34px; font-size: 15px;
}
.stop-btn {
  height: 34px; padding: 0 10px; gap: 6px; color: var(--red);
  border-color: rgba(248,81,73,.32); font-size: 12px; font-weight: 600;
}
.icon-btn:hover,
.stop-btn:hover { color: var(--text); border-color: var(--text2); background: var(--surface2); }
.icon-btn.active { background: var(--accent); color: #fff; border-color: var(--accent); }

/* Files shortcut — mobile/portrait only. On desktop the file panel is docked
   next to the chat, so this would be redundant. */
.files-btn { display: none; }

@media (max-width: 900px) {
  .files-btn { display: inline-flex; align-items: center; justify-content: center; }
}
@media (max-width: 768px) {
  #chat-header { padding: 8px 10px; gap: 8px; min-height: 48px; }
  .hamburger-btn { display: inline-flex; }
  .chat-title { font-size: 14px; }
  .title-row { flex-wrap: wrap; gap: 6px; }
  .stop-btn span:last-child { display: none; }
  .stop-btn { width: 34px; padding: 0; justify-content: center; }
  .icon-btn { width: 32px; height: 32px; font-size: 14px; }
  .hdr-actions { margin-left: auto; gap: 5px; }
  .git-row { gap: 6px; padding-top: 2px; }
  .mini-btn { padding: 3px 7px; font-size: 11px; }
}
@media (max-width: 560px) {
  .mini-btn { padding: 3px 6px; font-size: 11px; }
}
.here-pill {
  display: inline-flex; align-items: center; gap: 6px;
  font-size: 12px; padding: 4px 10px; border-radius: 999px; cursor: pointer;
  border: 1px solid var(--border); background: var(--surface2); color: var(--text2);
  font-family: inherit; white-space: nowrap;
}
.here-pill:hover { color: var(--text); background: var(--surface3); }
.here-dot { width: 7px; height: 7px; border-radius: 50%; background: color-mix(in srgb, var(--accent) 32%, transparent); }
.here-pill.someone .here-dot { background: var(--green); }
</style>
