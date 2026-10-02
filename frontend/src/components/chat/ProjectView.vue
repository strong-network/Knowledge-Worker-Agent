<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useProjectsStore } from '../../stores/projects'
import { useSessionsStore } from '../../stores/sessions'
import { useUiStore } from '../../stores/ui'
import type { Session } from '../../api'
import { getProjectMcp, type SessionConnector } from '../../api'
import { formatRelativeDate } from '../../utils/time'
import { confirmChatDelete } from '../../utils/confirmDelete'
import { formatDisplayPath } from '../../utils/paths'
import WorkspaceFilePanel from './WorkspaceFilePanel.vue'

const projectsStore = useProjectsStore()
const sessionsStore = useSessionsStore()
const ui = useUiStore()

const project = computed(() => (ui.activeProjectId ? projectsStore.getProject(ui.activeProjectId) : undefined))

// Project-scoped search: filters this project's chats by title only.
const search = ref('')
const chats = computed<Session[]>(() => {
  const all = ui.activeProjectId ? projectsStore.chatsFor(ui.activeProjectId) : []
  const q = search.value.trim().toLowerCase()
  if (!q) return all
  return all.filter(c => (c.name || '').toLowerCase().includes(q))
})
const starredChats = computed(() => chats.value.filter(c => c.project_starred))
const allChats = computed(() => chats.value.filter(c => !c.project_starred))

// Load the project's chats whenever the active project changes. The embedded
// file panel loads the workspace itself (it watches its rootPath).
watch(
  () => ui.activeProjectId,
  async (id) => {
    if (!id) return
    await Promise.all([projectsStore.loadProjectChats(id), projectsStore.refreshProject(id)])
    loadConnectors(id)
  },
  { immediate: true },
)

// Connectors glance: read-only summary of the project's shared
// MCP selection. Editing happens in project settings. "Blocked" = selected but
// not currently connected (error/failed/disconnected).
const connectors = ref<SessionConnector[]>([])
const activeConnectors = computed(() =>
  connectors.value.filter(c => c.selected && (c.status === 'connected' || c.status === 'connecting' || c.status === 'pending')),
)
const blockedConnectors = computed(() =>
  connectors.value.filter(c => c.selected && !(c.status === 'connected' || c.status === 'connecting' || c.status === 'pending')),
)

async function loadConnectors(id: string) {
  try {
    connectors.value = await getProjectMcp(id)
  } catch {
    connectors.value = []
  }
}

function connectorDotClass(status: string): string {
  switch (status) {
    case 'connected': return 'ok'
    case 'connecting':
    case 'pending': return 'pending'
    case 'error':
    case 'failed': return 'err'
    default: return 'off'
  }
}

// ── actions ──
const chatRenameId = ref<string | null>(null)
const chatRenameValue = ref('')

async function newChat() {
  const id = ui.activeProjectId
  if (!id) return
  try {
    const sid = await projectsStore.newChat(id)
    await sessionsStore.loadSessions()
    await sessionsStore.switchSession(sid)
    ui.showChat()
  } catch (e) {
    console.error(e)
    ;(window as any).showToast?.('Could not start a project chat')
  }
}

function openChat(id: string) {
  sessionsStore.switchSession(id)
  ui.showChat()
}

async function toggleStar(id: string, current: boolean, e: Event) {
  e.stopPropagation()
  if (!ui.activeProjectId) return
  await projectsStore.setStar(ui.activeProjectId, id, !current)
}

function startRename(id: string, name: string, e: Event) {
  e.stopPropagation()
  chatRenameId.value = id
  chatRenameValue.value = name
}
async function commitRename(id: string) {
  const name = chatRenameValue.value.trim()
  chatRenameId.value = null
  if (name && ui.activeProjectId) {
    try {
      await sessionsStore.renameSession(id, name)
      await projectsStore.loadProjectChats(ui.activeProjectId)
    } catch (e) { console.error(e) }
  }
}
async function removeChat(id: string, e: Event) {
  e.stopPropagation()
  if (!ui.activeProjectId) return
  const session = sessionsStore.sessions.find(s => s.id === id)
  const { ok, deleteNotes } = confirmChatDelete(
    session,
    'Delete this chat? The shared project workspace and its files are not affected — only the conversation is removed.',
  )
  if (!ok) return
  try {
    await sessionsStore.deleteSession(id, deleteNotes)
    await Promise.all([projectsStore.loadProjectChats(ui.activeProjectId), projectsStore.refreshProject(ui.activeProjectId)])
  } catch (e) { console.error(e) }
}

function openSettings() {
  ui.openModal('project-settings')
}

function openSummarize() {
  // ui.activeProjectId is already this project (the project view is open).
  ui.openModal('summarize')
}
</script>

<template>
  <div class="project-view" v-if="project">
    <!-- Large in-view heading (the generic chat header is hidden for this view) -->
    <div class="pv-head">
      <button class="pv-back" @click="ui.showChat()" title="Back to chat" aria-label="Back to chat">
        <svg viewBox="0 0 16 16" fill="currentColor" width="14" height="14"><path d="M10.28 3.22a.75.75 0 0 1 0 1.06L6.56 8l3.72 3.72a.75.75 0 1 1-1.06 1.06L4.97 8.53a.75.75 0 0 1 0-1.06l4.25-4.25a.75.75 0 0 1 1.06 0z"/></svg>
        Back
      </button>
      <div class="pv-title-row">
        <span class="pv-ico" aria-hidden="true">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" width="24" height="24">
            <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" stroke-linecap="round" stroke-linejoin="round"/>
          </svg>
        </span>
        <h1 class="pv-title">{{ project.name }}</h1>
        <span v-if="project.repo_url" class="pv-repo" :title="project.repo_url">⎇ repo</span>
        <div class="pv-head-actions">
          <button class="pv-btn" @click="openSummarize" title="Summarize this project's chats into a file">📝 Summarize</button>
          <button class="pv-btn" @click="openSettings" title="Project settings">⚙ Settings</button>
          <button class="pv-btn primary" @click="newChat">+ New chat</button>
        </div>
      </div>
      <p v-if="project.description" class="pv-desc">{{ project.description }}</p>
      <p class="pv-path">{{ formatDisplayPath(project.workspace_path) }}</p>
    </div>

    <div class="pv-body">
      <div class="pv-main">
        <!-- Project-scoped search (chat titles only) -->
        <div class="pv-search">
          <svg class="pv-search-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="15" height="15" aria-hidden="true">
            <circle cx="11" cy="11" r="7"/><path d="M21 21l-4.3-4.3" stroke-linecap="round"/>
          </svg>
          <input v-model="search" class="pv-search-input" type="text" placeholder="Search this project's chats…" />
          <button v-if="search" class="pv-search-clear" @click="search = ''" title="Clear" aria-label="Clear">✕</button>
        </div>
        <!-- STARRED -->
        <template v-if="starredChats.length">
          <div class="pv-group-title">Starred</div>
          <div class="pv-cards">
            <div v-for="c in starredChats" :key="c.id" class="pv-card" @click="openChat(c.id)">
              <button class="pv-star on" @click="toggleStar(c.id, true, $event)" title="Unstar">★</button>
              <div class="pv-card-body">
                <input
                  v-if="chatRenameId === c.id"
                  v-model="chatRenameValue"
                  class="pv-rename"
                  @click.stop
                  @keydown.enter.stop.prevent="commitRename(c.id)"
                  @keydown.esc.stop.prevent="chatRenameId = null"
                  @blur="commitRename(c.id)"
                  autofocus spellcheck="false"
                />
                <div v-else class="pv-card-name">{{ c.name || 'Untitled chat' }}</div>
                <div class="pv-card-meta">{{ c.messages || 0 }} messages · {{ formatRelativeDate(c.updated_at) }}</div>
              </div>
              <span class="pv-card-actions" @click.stop>
                <button @click="startRename(c.id, c.name, $event)" title="Rename">✎</button>
                <button class="danger" @click="removeChat(c.id, $event)" title="Delete">🗑</button>
              </span>
            </div>
          </div>
        </template>

        <!-- ALL CHATS -->
        <div class="pv-group-title">All chats</div>
        <div v-if="!allChats.length && !starredChats.length" class="pv-empty">
          <template v-if="search.trim()">No chats match “{{ search.trim() }}”.</template>
          <template v-else>No chats yet — start one with <strong>New chat</strong>.</template>
        </div>
        <div class="pv-cards">
          <div v-for="c in allChats" :key="c.id" class="pv-card" @click="openChat(c.id)">
            <button class="pv-star" :class="{ on: c.project_starred }" @click="toggleStar(c.id, !!c.project_starred, $event)" title="Star">
              {{ c.project_starred ? '★' : '☆' }}
            </button>
            <div class="pv-card-body">
              <input
                v-if="chatRenameId === c.id"
                v-model="chatRenameValue"
                class="pv-rename"
                @click.stop
                @keydown.enter.stop.prevent="commitRename(c.id)"
                @keydown.esc.stop.prevent="chatRenameId = null"
                @blur="commitRename(c.id)"
                autofocus spellcheck="false"
              />
              <div v-else class="pv-card-name">{{ c.name || 'Untitled chat' }}</div>
              <div class="pv-card-meta">{{ c.messages || 0 }} messages · {{ formatRelativeDate(c.updated_at) }}</div>
            </div>
            <span class="pv-card-actions" @click.stop>
              <button @click="startRename(c.id, c.name, $event)" title="Rename">✎</button>
              <button class="danger" @click="removeChat(c.id, $event)" title="Delete">🗑</button>
            </span>
          </div>
        </div>
      </div>

      <!-- Shared workspace: full file browser fixed on the project workspace -->
      <div class="pv-files">
        <!-- Connectors glance (read-only; edit in project settings) -->
        <div v-if="connectors.length" class="pv-connectors">
          <div class="pv-connectors-head">
            <span class="pv-files-title">Connectors</span>
            <button class="pv-cn-manage" @click="openSettings" title="Manage in project settings">Manage</button>
          </div>
          <div v-if="activeConnectors.length" class="pv-cn-list">
            <span v-for="c in activeConnectors" :key="c.name" class="pv-cn-chip">
              <span class="pv-cn-dot" :class="connectorDotClass(c.status)" aria-hidden="true"></span>{{ c.name }}
            </span>
          </div>
          <div v-else class="pv-cn-none">No connectors active for this project.</div>
          <div v-if="blockedConnectors.length" class="pv-cn-blocked">
            <span v-for="c in blockedConnectors" :key="c.name" class="pv-cn-chip blocked" :title="`Selected but ${c.status}`">
              <span class="pv-cn-dot err" aria-hidden="true"></span>{{ c.name }}
            </span>
          </div>
        </div>

        <div class="pv-files-head">
          <span class="pv-files-title">Shared workspace</span>
        </div>
        <div class="pv-files-panel">
          <WorkspaceFilePanel :root-path="project.workspace_path" embedded />
        </div>
      </div>
    </div>
  </div>

  <div v-else class="project-view pv-missing">
    <p>This project is no longer available.</p>
    <button class="pv-btn" @click="ui.showChat()">Back to chat</button>
  </div>
</template>

<style scoped>
.project-view { flex: 1; display: flex; flex-direction: column; min-height: 0; overflow: hidden; }
.pv-head { padding: 18px 24px 14px; border-bottom: 1px solid var(--border); }
.pv-back {
  display: inline-flex; align-items: center; gap: 5px; background: none; border: none;
  color: var(--text2); font-size: 13px; cursor: pointer; padding: 0; margin-bottom: 12px; font-family: inherit;
}
.pv-back:hover { color: var(--text); }
.pv-title-row { display: flex; align-items: center; gap: 10px; }
.pv-ico { color: var(--accent); flex-shrink: 0; display: inline-flex; }
.pv-title { font-size: 22px; font-weight: 700; color: var(--text); margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pv-repo {
  font-size: 11px; font-weight: 600; color: var(--text2);
  background: var(--surface2); border: 1px solid var(--border); border-radius: 999px; padding: 2px 8px; flex-shrink: 0;
}
.pv-head-actions { margin-left: auto; display: flex; gap: 8px; flex-shrink: 0; }
.pv-btn {
  padding: 7px 12px; font-size: 13px; font-weight: 600; border-radius: 8px;
  border: 1px solid var(--border); background: var(--surface); color: var(--text2); cursor: pointer; font-family: inherit;
}
.pv-btn:hover { color: var(--text); border-color: var(--text3); background: var(--surface2); }
.pv-btn.primary { background: var(--accent); color: #fff; border-color: transparent; }
.pv-btn.primary:hover { background: var(--accent-h); }
.pv-desc { margin: 10px 0 0; font-size: 14px; color: var(--text2); }
.pv-path { margin: 6px 0 0; font-size: 12px; color: var(--text3); font-family: var(--mono); }

.pv-body { flex: 1; display: flex; min-height: 0; overflow: hidden; }
.pv-main { flex: 1; min-width: 0; overflow-y: auto; padding: 16px 24px 24px; }
.pv-search {
  display: flex; align-items: center; gap: 8px; padding: 8px 12px; margin-bottom: 6px;
  border: 1px solid var(--border); border-radius: 10px; background: var(--surface);
}
.pv-search:focus-within { border-color: var(--accent); }
.pv-search-ico { color: var(--text3); flex-shrink: 0; }
.pv-search-input {
  flex: 1; border: none; background: none; outline: none; color: var(--text);
  font-size: 13px; font-family: inherit; min-width: 0;
}
.pv-search-clear { background: none; border: none; color: var(--text3); cursor: pointer; font-size: 12px; }
.pv-search-clear:hover { color: var(--text); }
.pv-group-title {
  font-size: 12px; font-weight: 700; color: var(--text3); text-transform: uppercase;
  letter-spacing: .06em; margin: 12px 0 8px;
}
.pv-cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(240px, 1fr)); gap: 10px; }
.pv-card {
  display: flex; align-items: flex-start; gap: 8px; padding: 12px;
  border: 1px solid var(--border); border-radius: 10px; background: var(--surface);
  cursor: pointer; transition: border-color .12s, background .12s; position: relative;
}
.pv-card:hover { border-color: var(--text3); background: var(--surface2); }
.pv-star {
  flex-shrink: 0; background: none; border: none; cursor: pointer;
  color: var(--text3); font-size: 15px; padding: 0; line-height: 1.2;
}
.pv-star.on { color: #facc15; }
.pv-card-body { flex: 1; min-width: 0; }
.pv-card-name { font-size: 14px; font-weight: 600; color: var(--text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pv-card-meta { font-size: 12px; color: var(--text2); margin-top: 3px; }
.pv-rename {
  width: 100%; padding: 3px 6px; font-size: 13px; border: 1px solid var(--accent);
  border-radius: 5px; background: var(--surface); color: var(--text); font-family: inherit;
}
.pv-card-actions { display: none; flex-shrink: 0; gap: 2px; }
.pv-card:hover .pv-card-actions { display: inline-flex; }
.pv-card-actions button { padding: 2px 6px; font-size: 12px; border: none; border-radius: 4px; background: none; color: var(--text2); cursor: pointer; }
.pv-card-actions button:hover { background: var(--surface3); color: var(--text); }
.pv-card-actions button.danger:hover { background: rgba(218,63,63,.14); color: var(--red); }
.pv-empty { font-size: 13px; color: var(--text3); padding: 8px 2px 12px; }

.pv-files {
  width: 320px; flex-shrink: 0; border-left: 1px solid var(--border);
  display: flex; flex-direction: column; min-height: 0; padding: 14px 0 0;
}
.pv-files-head { display: flex; align-items: center; margin: 0 14px 8px; }
.pv-files-title { flex: 1; font-size: 12px; font-weight: 700; color: var(--text3); text-transform: uppercase; letter-spacing: .06em; }
.pv-files-panel { flex: 1; min-height: 0; display: flex; overflow: hidden; }
.pv-missing { align-items: center; justify-content: center; gap: 12px; color: var(--text2); }

/* Connectors glance (read-only) */
.pv-connectors { margin: 0 14px 14px; padding-bottom: 12px; border-bottom: 1px solid var(--border); }
.pv-connectors-head { display: flex; align-items: center; margin-bottom: 8px; }
.pv-cn-manage {
  border: none; background: none; cursor: pointer; color: var(--accent);
  font: inherit; font-size: 12px; padding: 0;
}
.pv-cn-list, .pv-cn-blocked { display: flex; flex-wrap: wrap; gap: 6px; }
.pv-cn-blocked { margin-top: 6px; }
.pv-cn-none { font-size: 12px; color: var(--text2); }
.pv-cn-chip {
  display: inline-flex; align-items: center; gap: 6px; padding: 3px 8px;
  border-radius: 12px; background: var(--surface2); font-size: 12px; color: var(--text);
}
.pv-cn-chip.blocked { opacity: .8; }
.pv-cn-dot { width: 7px; height: 7px; border-radius: 50%; flex-shrink: 0; background: var(--text2); }
.pv-cn-dot.ok { background: #2ecc71; }
.pv-cn-dot.pending { background: #f1c40f; }
.pv-cn-dot.err { background: #e74c3c; }
.pv-cn-dot.off { background: var(--border); }

@media (max-width: 900px) {
  .pv-files { display: none; }
}
</style>
