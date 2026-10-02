<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref } from 'vue'
import { useProjectsStore } from '../../stores/projects'
import { useSessionsStore } from '../../stores/sessions'
import { useUiStore } from '../../stores/ui'
import { formatDisplayPath } from '../../utils/paths'
import { loadFlag, saveFlag } from '../../utils/sectionState'
import { confirmChatDelete } from '../../utils/confirmDelete'

const projectsStore = useProjectsStore()
const sessionsStore = useSessionsStore()
const ui = useUiStore()

// Section collapse is remembered across reloads (default expanded), matching
// the Scheduled tasks and Chat sessions sections.
const COLLAPSED_KEY = 'projectsCollapsed'
const collapsed = ref(loadFlag(COLLAPSED_KEY, false))

function toggleCollapsed() {
  collapsed.value = !collapsed.value
  saveFlag(COLLAPSED_KEY, collapsed.value)
}
const renameId = ref<string | null>(null)
const renameValue = ref('')
const chatRenameId = ref<string | null>(null)
const chatRenameValue = ref('')

function openCreate() {
  ui.openModal('create-project')
}

function openProject(id: string) {
  ui.openProject(id)
  closeSidebarOnMobile()
}

async function toggleProject(id: string, e: Event) {
  e.stopPropagation()
  await projectsStore.toggleExpanded(id)
}

async function newChatIn(projectId: string) {
  try {
    const sid = await projectsStore.newChat(projectId)
    await sessionsStore.loadSessions()
    await sessionsStore.switchSession(sid)
    ui.showChat()
    closeSidebarOnMobile()
  } catch (e) {
    console.error(e)
    ;(window as any).showToast?.('Could not start a project chat')
  }
}

function openChat(id: string) {
  sessionsStore.switchSession(id)
  ui.showChat()
  closeSidebarOnMobile()
}

async function toggleStar(projectId: string, sessionId: string, current: boolean, e: Event) {
  e.stopPropagation()
  await projectsStore.setStar(projectId, sessionId, !current)
}

// ── project rename / delete ──
function startRename(id: string, name: string, e: Event) {
  e.stopPropagation()
  renameId.value = id
  renameValue.value = name
}
async function commitRename(id: string) {
  const name = renameValue.value.trim()
  renameId.value = null
  if (name) {
    try { await projectsStore.renameProject(id, name) } catch (e) { console.error(e) }
  }
}
async function removeProject(id: string, name: string, e: Event) {
  e.stopPropagation()
  if (!confirm(`Delete project "${name}"? Its chats will become loose chats. The project's files are left on disk.`)) return
  try { await projectsStore.deleteProject(id, false) } catch (e) { console.error(e) }
}

// ── project chat rename / delete ──
function startChatRename(id: string, name: string, e: Event) {
  e.stopPropagation()
  chatRenameId.value = id
  chatRenameValue.value = name
}
async function commitChatRename(projectId: string, id: string) {
  const name = chatRenameValue.value.trim()
  chatRenameId.value = null
  if (name) {
    try {
      await sessionsStore.renameSession(id, name)
      await projectsStore.loadProjectChats(projectId)
    } catch (e) { console.error(e) }
  }
}
async function removeChat(projectId: string, id: string, e: Event) {
  e.stopPropagation()
  const { ok, deleteNotes } = confirmChatDelete(
    sessionsStore.sessions.find(s => s.id === id),
    'Delete this chat? The shared project workspace and its files are not affected — only the conversation is removed.',
  )
  if (!ok) return
  try {
    await sessionsStore.deleteSession(id, deleteNotes)
    await Promise.all([projectsStore.loadProjectChats(projectId), projectsStore.refreshProject(projectId)])
  } catch (e) { console.error(e) }
}

function closeSidebarOnMobile() {
  if (window.matchMedia('(max-width: 768px)').matches) ui.closeSidebar()
}
</script>

<template>
  <div class="projects-section">
    <!-- PROJECTS header: no item count, compact "New project" affordance. -->
    <div class="ph-header">
      <button class="ph-toggle" :aria-expanded="!collapsed" @click="toggleCollapsed">
        <span class="ph-caret" :class="{ open: !collapsed }" aria-hidden="true">›</span>
        <span class="ph-label">Projects</span>
      </button>
      <button class="ph-new" @click="openCreate" title="New project" aria-label="New project">
        <svg viewBox="0 0 16 16" fill="currentColor" width="13" height="13">
          <path d="M8 1.5a.5.5 0 0 1 .5.5v5.5H14a.5.5 0 0 1 0 1H8.5V14a.5.5 0 0 1-1 0V8.5H2a.5.5 0 0 1 0-1h5.5V2a.5.5 0 0 1 .5-.5z"/>
        </svg>
      </button>
    </div>

    <template v-if="!collapsed">
      <div v-if="!projectsStore.projects.length" class="ph-empty">No projects yet</div>

      <template v-for="p in projectsStore.sortedProjects" :key="p.id">
        <div class="project-row" @click="openProject(p.id)">
          <button class="pr-caret" :class="{ open: projectsStore.expanded[p.id] }"
                  @click="toggleProject(p.id, $event)" :aria-label="projectsStore.expanded[p.id] ? 'Collapse' : 'Expand'">›</button>
          <span class="pr-icon" aria-hidden="true">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" width="15" height="15">
              <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
          </span>
          <input
            v-if="renameId === p.id"
            v-model="renameValue"
            class="pr-rename"
            @click.stop
            @keydown.enter.stop.prevent="commitRename(p.id)"
            @keydown.esc.stop.prevent="renameId = null"
            @blur="commitRename(p.id)"
            autofocus spellcheck="false"
          />
          <span v-else class="pr-name">{{ p.name }}</span>
          <span v-if="p.repo_url" class="pr-repo" title="Repo-backed workspace" aria-hidden="true">⎇</span>
          <span class="pr-actions" @click.stop>
            <button @click="startRename(p.id, p.name, $event)" title="Rename project">✎</button>
            <button class="danger" @click="removeProject(p.id, p.name, $event)" title="Delete project">🗑</button>
          </span>
        </div>

        <!-- Expanded: the project's chats (starred first) + inline New chat -->
        <template v-if="projectsStore.expanded[p.id]">
          <div
            v-for="c in projectsStore.chatsFor(p.id)"
            :key="c.id"
            class="project-chat"
            :class="{ active: sessionsStore.currentSessionId === c.id && ui.activeView === 'chat' }"
            @click="openChat(c.id)"
          >
            <button
              class="pc-star"
              :class="{ on: c.project_starred }"
              @click="toggleStar(p.id, c.id, !!c.project_starred, $event)"
              :title="c.project_starred ? 'Unstar' : 'Star'"
              aria-label="Toggle star"
            >{{ c.project_starred ? '★' : '☆' }}</button>
            <input
              v-if="chatRenameId === c.id"
              v-model="chatRenameValue"
              class="pc-rename"
              @click.stop
              @keydown.enter.stop.prevent="commitChatRename(p.id, c.id)"
              @keydown.esc.stop.prevent="chatRenameId = null"
              @blur="commitChatRename(p.id, c.id)"
              autofocus spellcheck="false"
            />
            <span v-else class="pc-name">{{ c.name || 'Untitled chat' }}</span>
            <span class="pc-actions" @click.stop>
              <button @click="startChatRename(c.id, c.name, $event)" title="Rename chat">✎</button>
              <button class="danger" @click="removeChat(p.id, c.id, $event)" title="Delete chat">🗑</button>
            </span>
          </div>
          <div v-if="!projectsStore.chatsFor(p.id).length" class="pc-empty">No chats yet</div>
          <button class="pc-new" @click.stop="newChatIn(p.id)">
            <svg viewBox="0 0 16 16" fill="currentColor" width="11" height="11">
              <path d="M8 1.5a.5.5 0 0 1 .5.5v5.5H14a.5.5 0 0 1 0 1H8.5V14a.5.5 0 0 1-1 0V8.5H2a.5.5 0 0 1 0-1h5.5V2a.5.5 0 0 1 .5-.5z"/>
            </svg>
            New chat
          </button>
        </template>
      </template>
    </template>
  </div>
</template>

<style scoped>
.projects-section { border-bottom: 1px solid var(--border); padding-bottom: 6px; flex-shrink: 0; max-height: 45vh; overflow-y: auto; }
.ph-header { display: flex; align-items: center; padding: 10px 12px 4px; gap: 4px; }
.ph-toggle {
  display: flex; align-items: center; gap: 6px; flex: 1;
  font-size: 11px; font-weight: 600; color: var(--text3); text-transform: uppercase;
  letter-spacing: .06em; background: none; border: none; cursor: pointer;
  font-family: inherit; text-align: left; padding: 0;
}
.ph-toggle:hover { color: var(--text2); }
.ph-caret {
  display: inline-flex; align-items: center; justify-content: center;
  width: 12px; font-size: 14px; color: var(--text3); transition: transform .12s; text-transform: none;
}
.ph-caret.open { transform: rotate(90deg); }
.ph-label { flex: 1; }
.ph-new {
  width: 22px; height: 22px; display: flex; align-items: center; justify-content: center;
  background: none; border: none; color: var(--text2); border-radius: 5px; cursor: pointer;
  transition: background .12s, color .12s;
}
.ph-new:hover { background: var(--hover); color: var(--text); }
.ph-empty, .pc-empty { padding: 4px 14px 6px 30px; font-size: 12px; color: var(--text3); }
.pc-empty { padding: 4px 14px 4px 34px; }

.project-row {
  display: flex; align-items: center; gap: 5px; padding: 7px 10px 7px 8px;
  border-radius: 7px; cursor: pointer; font-size: 13px; color: var(--text2);
  margin: 0 6px 1px; position: relative; transition: background .1s, color .1s;
}
.project-row:hover { background: var(--hover); color: var(--text); }
.pr-caret {
  width: 14px; flex-shrink: 0; background: none; border: none; cursor: pointer;
  color: var(--text3); font-size: 14px; transition: transform .12s; padding: 0;
}
.pr-caret.open { transform: rotate(90deg); }
.pr-icon { color: var(--accent); flex-shrink: 0; display: inline-flex; }
.pr-name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
.pr-repo { color: var(--text3); font-size: 12px; flex-shrink: 0; }
.pr-rename, .pc-rename {
  flex: 1; min-width: 0; padding: 2px 5px; font-size: 12px;
  border: 1px solid var(--accent); border-radius: 4px; background: var(--surface);
  color: var(--text); font-family: inherit;
}
.pr-actions, .pc-actions { display: none; flex-shrink: 0; gap: 1px; }
.project-row:hover .pr-actions, .project-chat:hover .pc-actions { display: inline-flex; }
.pr-actions button, .pc-actions button {
  padding: 1px 5px; font-size: 13px; border: none; border-radius: 4px;
  background: none; color: var(--text2); cursor: pointer;
}
.pr-actions button:hover, .pc-actions button:hover { background: var(--surface3); color: var(--text); }
.pr-actions button.danger:hover, .pc-actions button.danger:hover { background: rgba(218,63,63,.14); color: var(--red); }

.project-chat {
  display: flex; align-items: center; gap: 6px; padding: 6px 10px 6px 30px;
  border-radius: 7px; cursor: pointer; font-size: 12.5px; color: var(--text2);
  margin: 0 6px 1px; transition: background .1s, color .1s;
}
.project-chat:hover { background: var(--hover); color: var(--text); }
.project-chat.active { background: var(--hover); color: var(--text); }
.pc-star {
  flex-shrink: 0; background: none; border: none; cursor: pointer;
  color: var(--text3); font-size: 15px; padding: 0; line-height: 1;
}
.pc-star.on { color: #facc15; }
.pc-name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 500; }
.pc-new {
  display: flex; align-items: center; gap: 6px; margin: 2px 6px 4px 30px;
  padding: 5px 8px; background: none; border: 1px dashed var(--border); border-radius: 7px;
  color: var(--text2); font-size: 12px; cursor: pointer; font-family: inherit;
  transition: all .12s;
}
.pc-new:hover { color: var(--text); border-color: var(--text3); background: var(--hover); }
</style>
