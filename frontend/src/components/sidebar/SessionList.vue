<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, nextTick } from 'vue'
import { useSessionsStore } from '../../stores/sessions'
import { useChatStore } from '../../stores/chat'
import { useUiStore } from '../../stores/ui'
import { formatRelativeDate } from '../../utils/time'
import { confirmChatDelete } from '../../utils/confirmDelete'
import { loadFlag, saveFlag, loadOverride, saveOverride } from '../../utils/sectionState'
import { useShareStore } from '../../stores/share'
import { useMe } from '../../composables/useMe'
import ActionMenu, { type MenuEntry } from '../ActionMenu.vue'
import TimerIcon from '../TimerIcon.vue'
import { useNow } from '../../composables/useNow'
import { timeLeft, timeLeftLong } from '../../utils/temporary'
import type { Session } from '../../api'

const sessionsStore = useSessionsStore()
const chatStore = useChatStore()
const ui = useUiStore()
const share = useShareStore()
const { me } = useMe()

// Chat sharing: the Share action is absent, not disabled, where sharing cannot work.
const sharingAvailable = computed(() => me.value.sharing_available === true)

// A turn this tab is not showing still counts: a guest's, in a chat the owner
// does not have open.
function isWorking(id: string): boolean {
  return chatStore.isStreamingSession(id) || !!share.activity.get(id)?.streaming
}
function needsYou(id: string): boolean {
  return id !== sessionsStore.currentSessionId && !!share.activity.get(id)?.waiting
}
function openShare(id: string) {
  contextMenuId.value = null
  share.openModal(id)
}

const SHARED_KEY = 'sharedOpen'
const sharedOverride = ref<boolean | null>(loadOverride(SHARED_KEY))

// Temporary chats: their own group, each row with the time it has left.
const TEMPORARY_KEY = 'temporaryOpen'
const temporaryOverride = ref<boolean | null>(loadOverride(TEMPORARY_KEY))
const now = useNow()
const left = (s: Session) => timeLeft(s.expires_at ?? now.value, now.value)
const leftLong = (s: Session) => timeLeftLong(s.expires_at ?? now.value, now.value)
async function keepChat(id: string) {
  contextMenuId.value = null
  await sessionsStore.setTemporary(id, false)
}

// Open a chat from the sidebar list. Switching the session alone isn't enough
// when a Project view is open: App.vue renders ProjectView while
// ui.activeView === 'project', so we must also flip back to the chat view or
// the click appears to do nothing.
function openChat(id: string) {
  sessionsStore.switchSession(id)
  ui.showChat()
}
const contextMenuId = ref<string | null>(null)
const renameId = ref<string | null>(null)
const renameValue = ref('')

// ── Collapsible history sections ──
// Manual toggles are tracked as explicit overrides (null = follow adaptive
// default). Adaptive default: while the user has no starred chats, Recent is
// expanded and Starred collapsed; once they star their first chat, Starred
// expands and Recent collapses. A search query force-expands both so hits are
// visible regardless of collapse state.
//
// The override is what gets persisted, not the resolved open/closed state. A
// user who has never touched these carets keeps following the adaptive default
// as their starred list fills up; one who has set them keeps their choice. Had
// we stored the resolved value instead, the first visit would freeze whatever
// the default happened to be that day into a permanent decision.
const STARRED_KEY = 'starredOpen'
const RECENT_KEY = 'recentOpen'
const starredOverride = ref<boolean | null>(loadOverride(STARRED_KEY))
const recentOverride = ref<boolean | null>(loadOverride(RECENT_KEY))

const searching = computed(() => !!sessionsStore.searchQuery.trim())

// ── "Chat sessions" umbrella section ──
// Mirrors the Projects / Scheduled tasks sections: one collapsible top-level
// group whose state is remembered across reloads. Starred / Pinned / Recent
// are its subsections. A search query force-expands it.
const SECTION_COLLAPSED_KEY = 'chatSessionsCollapsed'
const sectionCollapsed = ref(loadFlag(SECTION_COLLAPSED_KEY, false))
const sectionOpen = computed(() => searching.value || !sectionCollapsed.value)

function toggleSection() {
  sectionCollapsed.value = !sectionCollapsed.value
  saveFlag(SECTION_COLLAPSED_KEY, sectionCollapsed.value)
}

const starredOpen = computed(() => {
  if (searching.value) return true
  if (starredOverride.value !== null) return starredOverride.value
  return sessionsStore.hasFavorites // expanded once the user has favorites
})
const recentOpen = computed(() => {
  if (searching.value) return true
  if (recentOverride.value !== null) return recentOverride.value
  return !sessionsStore.hasFavorites // collapses after first star
})

const sharedOpen = computed(() => searching.value || sharedOverride.value !== false)
function toggleShared() {
  sharedOverride.value = !sharedOpen.value
  saveOverride(SHARED_KEY, sharedOverride.value)
}

const temporaryOpen = computed(() => searching.value || temporaryOverride.value !== false)
function toggleTemporary() {
  temporaryOverride.value = !temporaryOpen.value
  saveOverride(TEMPORARY_KEY, temporaryOverride.value)
}

function toggleStarred() {
  starredOverride.value = !starredOpen.value
  saveOverride(STARRED_KEY, starredOverride.value)
}
function toggleRecent() {
  recentOverride.value = !recentOpen.value
  saveOverride(RECENT_KEY, recentOverride.value)
}

function onContextMenu(e: MouseEvent, id: string) {
  e.preventDefault()
  contextMenuId.value = id
}

function rowEl(id: string | undefined): HTMLElement | null {
  return id ? document.querySelector<HTMLElement>(`#sessions-list .session-item[data-id="${CSS.escape(id)}"]`) : null
}
function focusRowMenu(id: string | undefined): boolean {
  const button = rowEl(id)?.querySelector<HTMLElement>('.am-trigger')
  button?.focus()
  return !!button
}

async function startRename(id: string, currentName: string) {
  renameId.value = id
  renameValue.value = currentName
  contextMenuId.value = null
  await nextTick()
  const input = rowEl(id)?.querySelector<HTMLInputElement>('input')
  input?.focus()
  input?.select()
}

// Enter saves and Escape cancels, and both hand focus back to the row's ⋯. A
// click elsewhere saves and leaves focus where it went. A blank or unchanged
// name keeps the old one.
async function submitRename(refocus = false) {
  const id = renameId.value
  if (!id) return
  const name = renameValue.value.trim()
  const before = sessionsStore.sessions.find(s => s.id === id)?.name
  renameId.value = null
  if (refocus) nextTick(() => focusRowMenu(id))
  if (name && name !== before) await sessionsStore.renameSession(id, name)
}
function cancelRename() {
  const id = renameId.value
  renameId.value = null
  if (id) nextTick(() => focusRowMenu(id))
}

async function deleteSession(id: string) {
  const session = sessionsStore.sessions.find(s => s.id === id)
  const { ok, deleteNotes } = confirmChatDelete(session)
  contextMenuId.value = null
  if (!ok) return
  // Focus goes to the next chat in the list, else the one before, else New chat.
  const rows = Array.from(document.querySelectorAll<HTMLElement>('#sessions-list .session-item[data-id]'))
    .filter(el => el.offsetParent)
  const at = rows.findIndex(el => el.dataset.id === id)
  const near = at < 0 ? undefined : (rows[at + 1] ?? rows[at - 1])?.dataset.id
  await sessionsStore.deleteSession(id, deleteNotes)
  await nextTick()
  if (!focusRowMenu(near)) document.getElementById('new-chat-btn')?.focus()
}

// Star, Keep chat and Make temporary move the row to another group, where
// it's drawn anew: focus follows it rather than dropping to the page.
async function thenRefocus(id: string, done: Promise<unknown>) {
  await done
  await nextTick()
  if (!document.activeElement || document.activeElement === document.body) focusRowMenu(id)
}

const actionsFor = (s: Session) => `Actions for "${s.name || 'Untitled'}"`
// A row's ⋯ menu. A temporary chat can only be kept, renamed or deleted.
function rowItems(s: Session): MenuEntry[] {
  const del: MenuEntry = { label: 'Delete', danger: true, action: () => deleteSession(s.id) }
  const rename: MenuEntry = { label: 'Rename', action: () => startRename(s.id, s.name) }
  if (s.temporary) return [{ label: 'Keep chat', action: () => thenRefocus(s.id, keepChat(s.id)) }, rename, 'divider', del]
  const items: MenuEntry[] = [
    { label: s.favorite ? 'Remove star' : 'Star', action: () => thenRefocus(s.id, toggleFavorite(s.id, !!s.favorite)) },
    rename,
  ]
  if (sharingAvailable.value) items.push({ label: 'Share', action: () => openShare(s.id) })
  if (!s.deprecated) {
    items.push({
      label: 'Make temporary',
      unavailable: s.shared ? 'Stop sharing first. Shared chats are always saved.' : undefined,
      action: () => thenRefocus(s.id, sessionsStore.setTemporary(s.id, true)),
    })
  }
  items.push('divider', del)
  return items
}

async function togglePin(id: string, pinned: boolean) {
  await sessionsStore.pinSession(id, !pinned)
  contextMenuId.value = null
}

async function toggleFavorite(id: string, favorite: boolean) {
  await sessionsStore.favoriteSession(id, !favorite)
  contextMenuId.value = null
}
</script>

<template>
  <div id="sessions-list">
    <!-- Umbrella section (Chat sessions) — mirrors Projects / Scheduled tasks. -->
    <div class="ph-header">
      <button class="ph-toggle" :aria-expanded="sectionOpen" @click="toggleSection">
        <span class="ph-caret" :class="{ open: sectionOpen }" aria-hidden="true">›</span>
        <span class="ph-label">Chat sessions</span>
      </button>
    </div>

    <template v-if="sectionOpen">
      <!-- Starred (favorite) sessions -->
      <template v-if="sessionsStore.sortedSessions.favorites.length">
        <button class="group-header" :aria-expanded="starredOpen" @click="toggleStarred">
          <span class="gh-caret" :class="{ open: starredOpen }" aria-hidden="true">›</span>
          <span class="gh-label">Starred</span>
          <span class="gh-star" aria-hidden="true">★</span>
          <span class="gh-count">{{ sessionsStore.sortedSessions.favorites.length }}</span>
        </button>
        <div
          v-show="starredOpen"
          v-for="s in sessionsStore.sortedSessions.favorites"
          :key="s.id"
          class="session-item favorite"
          :class="{ active: s.id === sessionsStore.currentSessionId, deprecated: s.deprecated }"
          @click="openChat(s.id)"
          :data-id="s.id"
          @contextmenu="onContextMenu($event, s.id)"
        >
          <div class="si-body">
            <div class="si-label" v-if="renameId !== s.id"><span v-if="needsYou(s.id)" class="needs-dot" title="Waiting for you"></span><span v-else-if="isWorking(s.id)" class="streaming-dot" title="Working"></span>{{ s.name || "Untitled" }}</div>
            <input
              v-else
              v-model="renameValue"
              class="form-input"
              style="font-size:12px;padding:3px 6px"
              @keydown.enter.prevent="submitRename(true)"
              @keydown.esc="cancelRename"
              @blur="submitRename()"
              autofocus
            />
            <div class="si-meta">
              <span class="dep-pill" v-if="s.deprecated" title="Deprecated GitHub Copilot session (read-only)">Deprecated</span>
              <span v-if="s.shared" class="share-pill" title="Shared with coworkers">Shared</span>
              <span v-if="s.shared && share.present(s.id)" class="here">{{ share.present(s.id) }} here</span>
              <span>{{ formatRelativeDate(s.updated_at) }}</span>
            </div>
          </div>
          <div class="si-actions" v-if="renameId !== s.id">
            <ActionMenu :items="rowItems(s)" :label="actionsFor(s)" :menu-label="actionsFor(s)" />
          </div>
        </div>
      </template>

      <!-- Pinned sessions -->
      <template v-if="sessionsStore.sortedSessions.pinned.length">
        <div class="group-header">
          <span class="gh-caret-spacer" aria-hidden="true"></span>
          <span class="gh-label">Pinned</span>
          <span class="gh-count">{{ sessionsStore.sortedSessions.pinned.length }}</span>
        </div>
        <div
          v-for="s in sessionsStore.sortedSessions.pinned"
          :key="s.id"
          class="session-item pinned"
          :class="{ active: s.id === sessionsStore.currentSessionId, deprecated: s.deprecated }"
          @click="openChat(s.id)"
          :data-id="s.id"
          @contextmenu="onContextMenu($event, s.id)"
        >
          <div class="si-body">
            <div class="si-label" v-if="renameId !== s.id"><span v-if="needsYou(s.id)" class="needs-dot" title="Waiting for you"></span><span v-else-if="isWorking(s.id)" class="streaming-dot" title="Working"></span>{{ s.name || "Untitled" }}</div>
            <input
              v-else
              v-model="renameValue"
              class="form-input"
              style="font-size:12px;padding:3px 6px"
              @keydown.enter.prevent="submitRename(true)"
              @keydown.esc="cancelRename"
              @blur="submitRename()"
              autofocus
            />
            <div class="si-meta">
              <span class="dep-pill" v-if="s.deprecated" title="Deprecated GitHub Copilot session (read-only)">Deprecated</span>
              <span v-if="s.shared" class="share-pill" title="Shared with coworkers">Shared</span>
              <span v-if="s.shared && share.present(s.id)" class="here">{{ share.present(s.id) }} here</span>
              <span>{{ formatRelativeDate(s.updated_at) }}</span>
            </div>
          </div>
          <div class="si-actions" v-if="renameId !== s.id">
            <ActionMenu :items="rowItems(s)" :label="actionsFor(s)" :menu-label="actionsFor(s)" />
          </div>
        </div>
      </template>

      <!-- Shared sessions: what am I sharing -->
      <button
        class="group-header"
        v-if="sessionsStore.sortedSessions.shared.length"
        :aria-expanded="sharedOpen"
        @click="toggleShared"
      >
        <span class="gh-caret" :class="{ open: sharedOpen }" aria-hidden="true">›</span>
        <span class="gh-label">Shared</span>
        <span class="gh-count">{{ sessionsStore.sortedSessions.shared.length }}</span>
      </button>
      <div
        v-show="sharedOpen"
        v-for="s in sessionsStore.sortedSessions.shared"
        :key="s.id"
        class="session-item"
        :class="{ active: s.id === sessionsStore.currentSessionId, deprecated: s.deprecated }"
        @click="openChat(s.id)"
        :data-id="s.id"
        @contextmenu="onContextMenu($event, s.id)"
      >
        <div class="si-body">
          <div class="si-label" v-if="renameId !== s.id"><span v-if="needsYou(s.id)" class="needs-dot" title="Waiting for you"></span><span v-else-if="isWorking(s.id)" class="streaming-dot" title="Working"></span>{{ s.name || "Untitled" }}</div>
          <input
            v-else
            v-model="renameValue"
            class="form-input"
            style="font-size:12px;padding:3px 6px"
            @keydown.enter.prevent="submitRename(true)"
            @keydown.esc="cancelRename"
            @blur="submitRename()"
            autofocus
          />
          <div class="si-meta">
            <span class="dep-pill" v-if="s.deprecated" title="Deprecated GitHub Copilot session (read-only)">Deprecated</span>
            <span v-if="s.shared" class="share-pill" title="Shared with coworkers">Shared</span>
            <span v-if="s.shared && share.present(s.id)" class="here">{{ share.present(s.id) }} here</span>
            <span>{{ formatRelativeDate(s.updated_at) }}</span>
          </div>
        </div>
        <div class="si-actions" v-if="renameId !== s.id">
          <ActionMenu :items="rowItems(s)" :label="actionsFor(s)" :menu-label="actionsFor(s)" />
        </div>
      </div>

      <!-- Temporary chats: not in the history, deleted after 24 hours without a message -->
      <button
        class="group-header"
        v-if="sessionsStore.sortedSessions.temporary.length"
        :aria-expanded="temporaryOpen"
        @click="toggleTemporary"
      >
        <span class="gh-caret" :class="{ open: temporaryOpen }" aria-hidden="true">›</span>
        <span class="gh-label">Temporary</span>
        <span class="gh-count">{{ sessionsStore.sortedSessions.temporary.length }}</span>
      </button>
      <div
        v-show="temporaryOpen"
        v-for="s in sessionsStore.sortedSessions.temporary"
        :key="s.id"
        class="session-item temporary"
        :class="{ active: s.id === sessionsStore.currentSessionId, deprecated: s.deprecated }"
        @click="openChat(s.id)"
        :data-id="s.id"
        @contextmenu="onContextMenu($event, s.id)"
      >
        <div class="si-body">
          <div class="si-label" v-if="renameId !== s.id"><span v-if="needsYou(s.id)" class="needs-dot" title="Waiting for you"></span><span v-else-if="isWorking(s.id)" class="streaming-dot" title="Working"></span>{{ s.name || "Untitled" }}<span class="sr-only">, temporary chat, deleted in {{ leftLong(s) }}</span></div>
          <input
            v-else
            v-model="renameValue"
            class="form-input"
            style="font-size:12px;padding:3px 6px"
            @keydown.enter.prevent="submitRename(true)"
            @keydown.esc="cancelRename"
            @blur="submitRename()"
            autofocus
          />
          <div class="si-meta">
            <span class="temp-left" aria-hidden="true"><TimerIcon :size="11" />{{ left(s) }}</span>
          </div>
        </div>
        <div class="si-actions" v-if="renameId !== s.id">
          <ActionMenu :items="rowItems(s)" :label="actionsFor(s)" :menu-label="actionsFor(s)" />
        </div>
      </div>

      <!-- Regular sessions -->
      <button
        class="group-header"
        v-if="sessionsStore.sortedSessions.unpinned.length"
        :aria-expanded="recentOpen"
        @click="toggleRecent"
      >
        <span class="gh-caret" :class="{ open: recentOpen }" aria-hidden="true">›</span>
        <span class="gh-label">Recent</span>
        <span class="gh-count">{{ sessionsStore.sortedSessions.unpinned.length }}</span>
      </button>
      <div
        v-show="recentOpen"
        v-for="s in sessionsStore.sortedSessions.unpinned"
        :key="s.id"
        class="session-item"
        :class="{ active: s.id === sessionsStore.currentSessionId, deprecated: s.deprecated }"
        @click="openChat(s.id)"
        :data-id="s.id"
        @contextmenu="onContextMenu($event, s.id)"
      >
        <div class="si-body">
          <div class="si-label" v-if="renameId !== s.id"><span v-if="needsYou(s.id)" class="needs-dot" title="Waiting for you"></span><span v-else-if="isWorking(s.id)" class="streaming-dot" title="Working"></span>{{ s.name || "Untitled" }}</div>
          <input
            v-else
            v-model="renameValue"
            class="form-input"
            style="font-size:12px;padding:3px 6px"
            @keydown.enter.prevent="submitRename(true)"
            @keydown.esc="cancelRename"
            @blur="submitRename()"
            autofocus
          />
          <div class="si-meta">
            <span class="dep-pill" v-if="s.deprecated" title="Deprecated GitHub Copilot session (read-only)">Deprecated</span>
            <span v-if="s.shared" class="share-pill" title="Shared with coworkers">Shared</span>
            <span v-if="s.shared && share.present(s.id)" class="here">{{ share.present(s.id) }} here</span>
            <span>{{ formatRelativeDate(s.updated_at) }}</span>
          </div>
        </div>
        <div class="si-actions" v-if="renameId !== s.id">
          <ActionMenu :items="rowItems(s)" :label="actionsFor(s)" :menu-label="actionsFor(s)" />
        </div>
      </div>

      <div v-if="!sessionsStore.sessions.length" class="list-empty">
        No sessions yet
      </div>
      <div v-else-if="sessionsStore.searchHasNoMatches" class="list-empty">
        No matching chats
      </div>
    </template>
  </div>
</template>

<style scoped>
#sessions-list { flex: 1; overflow-y: auto; padding: 4px 6px; }

/* Umbrella "Chat sessions" header — mirrors ProjectsSection / ScheduledTasksSection.
   Those sections have no container padding, so their header sits at 12px. This
   list already pads 6px, hence 6px here to land on the same 12px column. */
.ph-header { display: flex; align-items: center; padding: 10px 6px 4px; gap: 4px; }
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

/* Subsection headers (Starred / Pinned / Recent) are the same level as a
   project row or a scheduled-task row, so they share those metrics: 6px of
   list padding + 8px here = the 14px left offset used by .project-row and
   .task-row. The 12px caret + 6px gap then puts every label at 32px, which is
   where .session-item's text starts. */
.group-header {
  display: flex; align-items: center; gap: 6px; padding: 8px 10px 4px 8px;
  font-size: 11px; font-weight: 600; color: var(--text3); text-transform: uppercase;
  letter-spacing: .06em; user-select: none;
  width: 100%; background: none; border: none; cursor: pointer;
  font-family: inherit; text-align: left;
}
button.group-header:hover { color: var(--text2); }
.group-header .gh-label { flex: 1; }
.group-header .gh-count {
  font-weight: 600; color: var(--text3); letter-spacing: 0;
  font-size: 11px;
}
.group-header .gh-star { color: #facc15; font-size: 12px; }
.group-header .gh-caret {
  display: inline-flex; align-items: center; justify-content: center;
  width: 12px; flex-shrink: 0; font-size: 14px; color: var(--text3); transition: transform .12s;
  text-transform: none;
}
/* Pinned isn't collapsible, so it reserves the caret column to stay aligned. */
.group-header .gh-caret-spacer { width: 12px; flex-shrink: 0; }
.group-header .gh-caret.open { transform: rotate(90deg); }
.needs-dot {
  display: inline-block; width: 7px; height: 7px; border-radius: 50%;
  background: var(--orange); margin-right: 6px; vertical-align: middle;
}
.share-pill {
  font-size: 10px; font-weight: 600; letter-spacing: .02em;
  padding: 0 6px; border-radius: 999px;
  color: var(--accent); background: color-mix(in srgb, var(--accent) 14%, transparent);
}
.here { color: var(--green); }
.temp-left { display: inline-flex; align-items: center; gap: 4px; }
.list-empty {
  padding: 20px; text-align: center; color: var(--text3); font-size: 12px;
}
.session-item {
  padding: 8px 10px 8px 26px;
  border-radius: 7px;
  cursor: pointer;
  font-size: 13px;
  color: var(--text2);
  display: flex;
  align-items: flex-start;
  gap: 7px;
  transition: background .1s, color .1s;
  margin-bottom: 1px;
  position: relative;
}
/* focus-within is grouped with hover so that reaching the row's buttons by
   keyboard lights the row up the same way the pointer does. */
.session-item:hover,
.session-item.active,
.session-item:focus-within { background: var(--hover); color: var(--text); }
.si-body { flex: 1; min-width: 0; }
.si-label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 500; }
/* A row carries only the chat's name and when it was last used. The workspace
   path, agent and mode used to sit here too; they cost three lines per row and
   are all reachable from the session's settings (agent also from the composer),
   so the list now optimises for scanning many chats rather than describing one. */
.si-meta { font-size: 11px; color: var(--text2); margin-top: 3px; display: flex; gap: 5px; align-items: center; flex-wrap: wrap; }
.dep-pill {
  display: inline-flex; align-items: center;
  padding: 2px 7px; border-radius: 10px; font-size: 10px; font-weight: 600;
  background: rgba(248,81,73,.15); color: #f87171;
  text-transform: uppercase; letter-spacing: .04em;
}
.session-item.deprecated .si-label { opacity: .6; }
/* The ⋯ overlays the end of the row rather than taking a column in its flex
   flow, so the name, the one thing people scan for, gets the full width. It
   shows only while the row is hovered, holds focus or has its menu open.

   pointer-events: none is what makes hiding it safe: opacity: 0 alone still
   hit-tests, so an invisible button would sit over the end of the title in
   every resting row. */
.si-actions {
  position: absolute;
  right: 6px;
  /* Stretched to the row rather than centred on it, so the covered end of the
     name is hidden whole rather than poking out above and below. */
  top: 4px;
  bottom: 4px;
  display: flex;
  align-items: center;
  opacity: 0;
  pointer-events: none;
  transition: opacity .1s;
  background: var(--hover);
  border-radius: 5px;
}
/* Fades the name out beneath the ⋯ instead of cutting it. Sits immediately
   left of it and shares its background. */
.si-actions::before {
  content: '';
  position: absolute;
  right: 100%;
  top: 0;
  bottom: 0;
  width: 24px;
  background: linear-gradient(to right, transparent, var(--hover));
}
.session-item:hover .si-actions,
.session-item:focus-within .si-actions,
.si-actions:has(.am.open) { opacity: 1; pointer-events: auto; }
.session-item:has(.am.open) { background: var(--hover); color: var(--text); }
/* Touch screens have no hover, so the ⋯ always shows there, and the name
   stops short of it instead of fading under it. */
@media (pointer: coarse) {
  .session-item { padding-right: 36px; }
  .si-actions { opacity: 1; pointer-events: auto; background: none; }
  .si-actions::before { display: none; }
}
/* Working indicator on a session row: a plain spinner, matched to the label's
   type size so the title does not shift when it appears. */
.streaming-dot {
  display: inline-block;
  width: 10px; height: 10px;
  margin-right: 6px; vertical-align: -1px;
  border: 2px solid rgba(3,169,241,.25);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: sessionSpin .7s linear infinite;
}
@media (prefers-reduced-motion: reduce) {
  .streaming-dot {
    animation: none;
  }
}
@keyframes sessionSpin {
  to { transform: rotate(360deg); }
}
</style>
