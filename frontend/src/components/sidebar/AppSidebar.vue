<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, watch, onMounted, onBeforeUnmount, nextTick } from 'vue'
import { useSessionsStore } from '../../stores/sessions'
import { useUiStore, SIDEBAR_MIN } from '../../stores/ui'
import { useProjectsStore } from '../../stores/projects'
import { useScheduledTasksStore } from '../../stores/scheduledTasks'
import SessionList from './SessionList.vue'
import ProjectsSection from './ProjectsSection.vue'
import ScheduledTasksSection from './ScheduledTasksSection.vue'
import ArtifactsSection from './ArtifactsSection.vue'
import SettingsMenu from './SettingsMenu.vue'

const sessionsStore = useSessionsStore()
const ui = useUiStore()
const projectsStore = useProjectsStore()
const scheduledTasks = useScheduledTasksStore()

const settingsOpen = ref(false)

onMounted(async () => {
  projectsStore.loadProjects()
  scheduledTasks.loadTasks()
})

function closeSidebarOnMobile() {
  if (window.matchMedia('(max-width: 768px)').matches) {
    ui.closeSidebar()
  }
}

async function openNewChat() {
  closeSidebarOnMobile()
  // Starting a chat commits to it, so end any glance. Explicit because the
  // reuse path below can leave the current session unchanged, which the
  // navigation watcher would not see.
  endPeek()
  // Always land on the chat surface, even if a Project view is currently open
  // (App.vue renders ProjectView while ui.activeView === 'project').
  ui.showChat()
  // New chat lands directly on the prompt-first surface — no config
  // modal. The backend auto-creates a per-chat workspace folder. Reuse the
  // current session if it's already an empty "New chat" so repeated clicks
  // don't pile up blank sessions.
  const cur = sessionsStore.currentSession
  const curEmpty =
    cur &&
    (cur.name === 'New chat' || cur.name === 'New Chat' || !cur.name) &&
    sessionsStore.currentMessages.length === 0
  if (curEmpty) {
    focusComposer()
    return
  }
  try {
    await sessionsStore.createSession({ name: 'New chat' })
  } catch (e) {
    console.error('Failed to create session:', e)
    ;(window as any).showToast?.('Could not start a new chat')
    return
  }
  focusComposer()
}

function openSearch() {
  closeSidebarOnMobile()
  endPeek()
  ui.openSearch()
  nextTick(() => document.querySelector<HTMLInputElement>('#search-input')?.focus())
}

function focusComposer() {
  nextTick(() => {
    const el = document.querySelector('#prompt-input') as HTMLTextAreaElement | null
    el?.focus()
  })
}

function toggleSettings() {
  settingsOpen.value = !settingsOpen.value
}

// ── Hover peek ──
// Collapsing costs you the chat list, and reaching it back via the expand
// button means two clicks (expand, then collapse again) for a one-second
// glance. Hovering the rail floats the full sidebar over the content instead:
// a look, not a state change. It overlays rather than pushing the layout, so
// brushing past the screen edge can't shove the conversation sideways.
const peeking = ref(false)
let peekTimer: ReturnType<typeof setTimeout> | null = null

function clearPeekTimer() {
  if (peekTimer) { clearTimeout(peekTimer); peekTimer = null }
}

// A finger can enter but never leave, which would strand the panel over the
// content with no way to dismiss it, so peek is for pointing devices only.
// This asks what the input actually is rather than what the device supports:
// `(hover: hover)` reports false on touch laptops, which do have a mouse.
function onDockEnter(e: PointerEvent) {
  if (!ui.sidebarCollapsed || e.pointerType === 'touch' || resizing.value) return
  clearPeekTimer()
  // A pointer crossing the rail on its way somewhere else shouldn't fling the
  // panel out, so wait long enough to tell a pass-through from an intent.
  peekTimer = setTimeout(() => { peeking.value = true }, 180)
}

function onDockLeave() {
  clearPeekTimer()
  peekTimer = setTimeout(() => { peeking.value = false }, 120)
}

// The header button sits in the same place in both states, but the useful
// action differs: while peeking the sidebar is already open in front of you,
// so the button pins it rather than collapsing what you are looking at.
function onSidebarButton() {
  clearPeekTimer()
  if (ui.sidebarCollapsed) {
    peeking.value = false
    ui.setSidebarCollapsed(false)
  } else {
    ui.setSidebarCollapsed(true)
  }
}

// Navigating away is what ends a glance: the panel would otherwise be left
// covering the very thing the user just chose. This watches navigation rather
// than clicks, because "clicked inside the sidebar" is not the same event —
// expanding a group, starring or renaming a chat all happen in place and must
// leave the panel alone. It also covers projects and scheduled tasks, not just
// chats.
function endPeek() {
  clearPeekTimer()
  peeking.value = false
}
watch(
  () => [sessionsStore.currentSessionId, ui.activeView, ui.activeProjectId].join('|'),
  endPeek,
)

onBeforeUnmount(clearPeekTimer)

// ── Resizing ──
// Dragging the right edge sets the width, between SIDEBAR_MIN and a third of
// the window. Dragging well past the minimum collapses the bar to the rail,
// live, so dragging back out reopens it within the same gesture.
const COLLAPSE_BELOW = 150
const resizing = ref(false)
function startResize(e: PointerEvent) {
  if (e.button !== 0) return
  e.preventDefault()
  const startX = e.clientX
  const startW = document.getElementById('sidebar')?.getBoundingClientRect().width ?? ui.sidebarWidth
  const handle = e.currentTarget as HTMLElement | null
  try { handle?.setPointerCapture(e.pointerId) } catch { /* not all pointers capture */ }
  resizing.value = true
  function onMove(ev: PointerEvent) {
    const want = startW + (ev.clientX - startX)
    if (want < COLLAPSE_BELOW) {
      // Reopening restores the width from before this drag, not the minimum.
      if (!ui.sidebarCollapsed) { ui.setSidebarWidth(startW); ui.setSidebarCollapsed(true) }
      return
    }
    if (ui.sidebarCollapsed) ui.setSidebarCollapsed(false)
    ui.setSidebarWidth(Math.max(SIDEBAR_MIN, want))
  }
  function onUp(ev: PointerEvent) {
    try { handle?.releasePointerCapture(ev.pointerId) } catch { /* handle gone after a collapse */ }
    window.removeEventListener('pointermove', onMove)
    window.removeEventListener('pointerup', onUp)
    window.removeEventListener('pointercancel', onUp)
    document.body.style.cursor = ''
    document.body.style.userSelect = ''
    resizing.value = false
  }
  window.addEventListener('pointermove', onMove)
  window.addEventListener('pointerup', onUp)
  window.addEventListener('pointercancel', onUp)
  document.body.style.cursor = 'col-resize'
  document.body.style.userSelect = 'none'
}
</script>

<template>
  <!-- Dock: holds the rail and the full chat bar so a hover that moves from
       one onto the other never leaves the pair and can't flicker. -->
  <div
    id="sidebar-dock"
    :class="{ collapsed: ui.sidebarCollapsed, peeking }"
    :style="{ '--sidebar-width': ui.sidebarWidth + 'px' }"
    @pointerenter="onDockEnter"
    @pointerleave="onDockLeave"
  >
    <!-- Slim rail (whole chat bar collapsed) -->
    <div v-if="ui.sidebarCollapsed" id="sidebar-rail">
      <button class="rail-btn" @click="onSidebarButton" title="Expand sidebar" aria-label="Expand sidebar">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="18" height="18">
          <rect x="3" y="4" width="18" height="16" rx="2"/><path d="M9 4v16" stroke-linecap="round"/>
        </svg>
      </button>
      <button class="rail-btn new" @click="openNewChat" title="New chat" aria-label="New chat">
        <svg viewBox="0 0 16 16" fill="currentColor" width="16" height="16">
          <path d="M8 1.5a.5.5 0 0 1 .5.5v5.5H14a.5.5 0 0 1 0 1H8.5V14a.5.5 0 0 1-1 0V8.5H2a.5.5 0 0 1 0-1h5.5V2a.5.5 0 0 1 .5-.5z"/>
        </svg>
      </button>
      <button class="rail-btn" @click="openSearch" title="Search chats" aria-label="Search chats">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16" aria-hidden="true">
          <circle cx="11" cy="11" r="7"/><path d="M21 21l-4.3-4.3" stroke-linecap="round"/>
        </svg>
      </button>
    </div>

    <!-- Full chat bar. Docked in the layout when expanded; the same element
         floats over the content while peeking, so pinning it is a change of
         position, not of content. -->
    <Transition name="peek">
      <div
        v-if="!ui.sidebarCollapsed || peeking || ui.sidebarOpen"
        id="sidebar"
        :class="{ open: ui.sidebarOpen, peek: ui.sidebarCollapsed }"
      >
        <div id="sidebar-header">
          <div class="brand-title">Knowledge Worker Agent</div>
          <button
            class="collapse-btn"
            @click="onSidebarButton"
            :title="ui.sidebarCollapsed ? 'Keep sidebar open' : 'Collapse sidebar'"
            :aria-label="ui.sidebarCollapsed ? 'Keep sidebar open' : 'Collapse sidebar'"
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="18" height="18">
              <rect x="3" y="4" width="18" height="16" rx="2"/>
              <path d="M9 4v16" stroke-linecap="round"/>
            </svg>
          </button>
        </div>

        <button id="new-chat-btn" @click="openNewChat">
          <svg viewBox="0 0 16 16" fill="currentColor" width="14" height="14">
            <path d="M8 1.5a.5.5 0 0 1 .5.5v5.5H14a.5.5 0 0 1 0 1H8.5V14a.5.5 0 0 1-1 0V8.5H2a.5.5 0 0 1 0-1h5.5V2a.5.5 0 0 1 .5-.5z"/>
          </svg>
          New chat
        </button>

        <button
          id="search-btn"
          class="nav-item"
          :class="{ active: ui.activeView === 'search' }"
          :aria-current="ui.activeView === 'search' ? 'page' : undefined"
          @click="openSearch"
        >
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="15" height="15" aria-hidden="true">
            <circle cx="11" cy="11" r="7"/><path d="M21 21l-4.3-4.3" stroke-linecap="round"/>
          </svg>
          Search chats
        </button>

        <ProjectsSection />

        <ScheduledTasksSection />

        <ArtifactsSection />

        <SessionList />

        <div id="sidebar-footer">
          <SettingsMenu :open="settingsOpen" @close="settingsOpen = false" />
          <button
            class="footer-btn settings-trigger"
            :class="{ active: settingsOpen }"
            @click="toggleSettings"
            title="Settings"
            :aria-expanded="settingsOpen"
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="16" height="16" aria-hidden="true">
              <circle cx="12" cy="12" r="3"/>
              <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09a1.65 1.65 0 0 0-1-1.51 1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09a1.65 1.65 0 0 0 1.51-1 1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
            Settings
            <!-- Workspace hygiene: the review list is always available and never
                 interrupts, so its only ambient signal is this dot. -->
            <span v-if="ui.tidyCount" class="settings-dot" :title="`${ui.tidyCount} chats to review`" />
          </button>
        </div>
      </div>
    </Transition>

    <!-- On the docked bar's right edge; a peeking or collapsed bar has none. -->
    <div
      v-if="!ui.sidebarCollapsed"
      class="sb-resize"
      :class="{ dragging: resizing }"
      role="separator"
      aria-orientation="vertical"
      aria-label="Resize sidebar"
      title="Drag to resize"
      @pointerdown="startResize"
    ></div>
  </div>
</template>

<style scoped>
/* Dock: the layout slot for the chat bar, holding the rail and the full
   sidebar. Sized by whichever is docked, so a peeking panel (which is taken
   out of flow) can float over the content without widening the slot.

   Deliberately has no z-index. `position: relative` alone anchors the peeking
   panel without creating a stacking context, which matters because the mobile
   drawer inside it must out-rank the page overlay (299) that is a sibling of
   this dock. A z-index here would trap the drawer's 300 inside the dock's
   layer and the overlay would paint over it, swallowing every tap. */
#sidebar-dock {
  display: flex;
  position: relative;
  flex-shrink: 0;
}

#sidebar {
  width: var(--sidebar-width, 268px);
  /* Also enforced while dragging; here it holds when the window shrinks. */
  max-width: 33.333vw;
  flex-shrink: 0;
  background: var(--surface);
  border-right: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  overflow: hidden;
  transition: background .2s, border-color .2s;
}

/* Peeking: the same panel, lifted out of the layout and laid over the content
   in the rail's place. Nothing reflows, so a pointer brushing the screen edge
   can't shove the conversation sideways. The shadow is what says "floating,
   still collapsed" rather than "expanded". */
#sidebar.peek {
  position: absolute;
  top: 0; bottom: 0; left: 0;
  /* Above the chat content, below the editor panel and modals (400+). */
  z-index: 250;
  box-shadow: 6px 0 28px rgba(0, 0, 0, .18);
}
.peek-enter-active, .peek-leave-active { transition: transform .18s ease, opacity .18s ease; }
.peek-enter-from, .peek-leave-to { transform: translateX(-12px); opacity: 0; }
@media (prefers-reduced-motion: reduce) {
  .peek-enter-active, .peek-leave-active { transition: none; }
  .peek-enter-from, .peek-leave-to { transform: none; }
}

/* Slim rail */
#sidebar-rail {
  width: 52px; min-width: 52px;
  background: var(--surface);
  border-right: 1px solid var(--border);
  display: flex; flex-direction: column; align-items: center;
  /* 9px top matches the header's padding, so the toggle sits on the header row. */
  gap: 8px; padding: 9px 0 12px;
}
.rail-btn {
  width: 36px; height: 36px;
  display: flex; align-items: center; justify-content: center;
  background: none; border: 1px solid var(--border); color: var(--text2);
  border-radius: 9px; cursor: pointer; transition: all .15s;
}
.rail-btn:hover { color: var(--text); border-color: var(--text2); background: var(--hover); }
.rail-btn.new { background: var(--accent); color: #fff; border-color: transparent; }
.rail-btn.new:hover { background: var(--accent-h); }

/* Lines up with the chat header's bottom rule so the two read as one
   continuous line across the top of the app. Both rows are actually taller
   than their min-height: what makes them match is that padding plus tallest
   child comes to the same 54px either side (here 9 + 36 + 9; there 10 + 34 +
   10), plus a 1px border. Change the button size and the padding has to move
   with it. The old header was 14/12 padding around a 34px avatar, which is
   why it sat 6px proud of the chat header. */
#sidebar-header {
  padding: 9px 8px 9px 16px;
  min-height: 54px;
  border-bottom: 1px solid var(--border);
  display: flex;
  align-items: center;
  gap: 6px;
}
.brand-title {
  flex: 1;
  font-size: 14px; font-weight: 600; line-height: 1.2; min-width: 0;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

/* Ends the header row, at the bar's right edge, where it is easier to spot
   than in the corner. */
.collapse-btn {
  flex-shrink: 0;
  width: 36px; height: 36px;
  display: flex; align-items: center; justify-content: center;
  background: none; border: none; color: var(--text2);
  border-radius: 9px; cursor: pointer; transition: background .15s, color .15s;
}
.collapse-btn:hover { background: var(--hover); color: var(--text); }

#new-chat-btn {
  margin: 10px 12px 8px;
  padding: 10px 12px;
  background: var(--accent);
  color: #fff;
  border: none;
  border-radius: 10px;
  cursor: pointer;
  font-size: 14px;
  font-weight: 600;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  transition: background .15s, transform .1s;
}
#new-chat-btn:hover { background: var(--accent-h); }
#new-chat-btn:active { transform: scale(.98); }

.nav-item {
  display: flex; align-items: center; gap: 9px;
  margin: 2px 12px 4px; padding: 8px 10px;
  background: none; border: none; border-radius: 8px;
  color: var(--text2); font-size: 13px; font-weight: 500; font-family: inherit;
  cursor: pointer; transition: background .12s, color .12s; text-align: left; width: calc(100% - 24px);
}
.nav-item:hover { background: var(--hover); color: var(--text); }
.nav-item.active { background: var(--hover); color: var(--text); }
.nav-item svg { flex-shrink: 0; color: var(--accent); }

#sidebar-footer {
  padding: 10px 12px;
  border-top: 1px solid var(--border);
  position: relative;
}
.footer-btn {
  width: 100%;
  padding: 9px;
  background: none;
  border: 1px solid var(--border);
  color: var(--text2);
  border-radius: 9px;
  cursor: pointer;
  font-size: 13px;
  font-weight: 500;
  transition: all .15s;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
}
.footer-btn:hover, .footer-btn.active { color: var(--text); border-color: var(--text2); background: var(--hover); }
.settings-dot {
  width: 6px; height: 6px; border-radius: 50%;
  background: var(--accent); margin-left: 2px; flex-shrink: 0;
}

/* A 10px grab area centred on the bar's border; only a hairline lights up. */
.sb-resize {
  position: absolute;
  top: 0; bottom: 0; right: -5px;
  width: 10px;
  cursor: col-resize;
  z-index: 20;
  touch-action: none;
}
.sb-resize::after {
  content: '';
  position: absolute;
  top: 0; bottom: 0; left: 4px;
  width: 2px;
  border-radius: 2px;
  background: transparent;
  transition: background .15s;
}
.sb-resize:hover::after, .sb-resize.dragging::after { background: var(--accent); }

@media (max-width: 768px) {
  #sidebar {
    position: fixed; top: 0; left: -100%; bottom: 0; z-index: 300;
    width: min(86vw, 320px); min-width: min(86vw, 320px); max-width: none;
    transition: left .25s ease; box-shadow: none;
  }
  #sidebar.open { left: 0; box-shadow: 4px 0 24px rgba(0,0,0,.4); }
  /* On mobile the off-canvas drawer is the model; hide the slim rail. The
     drawer is also the only sidebar here, so the desktop peek positioning
     must not leak into it. */
  #sidebar-rail { display: none; }
  .sb-resize { display: none; }
  #sidebar.peek { position: fixed; z-index: 300; box-shadow: none; }
}
</style>
