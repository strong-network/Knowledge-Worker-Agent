// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { fetchTidyCount } from '../api'

const FILE_PANEL_WIDTH_KEY = 'filePanelWidth'
const FILE_PANEL_READING_WIDTH_KEY = 'filePanelReadingWidth'

// Workspace file panel bounds.
const FILE_PANEL_MIN = 240
const FILE_PANEL_MAX = 480
const FILE_PANEL_DEFAULT = 300
// Smallest sliver of chat to keep visible when the file panel is dragged as
// wide as possible, so the panel can grow nearly full-width without ever
// hiding the conversation entirely.
const FILE_PANEL_MIN_CHAT_MARGIN = 120
// Opening a file turns the panel from a file list into a reading surface, and
// a list-width column is a miserable place to read a document. Until the user
// picks their own reading width, use this fraction of the window: wide enough
// for prose, narrow enough to leave the conversation usable beside it.
const FILE_PANEL_READING_FRACTION = 0.4

function readNumber(key: string): number | null {
  const raw = localStorage.getItem(key)
  if (!raw) return null
  const n = Number(raw)
  return Number.isFinite(n) && n > 0 ? n : null
}

export const useUiStore = defineStore('ui', () => {
  const theme = ref<'dark' | 'light'>(
    (localStorage.getItem('theme') as 'dark' | 'light') || 'light'
  )
  const sidebarOpen = ref(false)
  const activeModal = ref<string | null>(null)

  // Projects: which top-level view is showing. 'chat' is the normal chat surface;
  // 'project' shows the ProjectView for activeProjectId. This app has no
  // router, so navigation is a simple reactive view switch.
  const activeView = ref<'chat' | 'project' | 'scheduled'>('chat')
  const activeProjectId = ref<string | null>(null)

  // Scheduled tasks: which scheduled task the editor modal is editing (null = creating a
  // new task). Set before opening the 'scheduled-task' modal.
  const editingScheduledTaskId = ref<string | null>(null)

  // Whole chat-bar collapsed to a slim rail. Persisted so it survives
  // reloads. Distinct from `sidebarOpen`, which is the mobile off-canvas drawer.
  //
  // Chats used to collapse this automatically on their first prompt, which wrote
  // '1' here on the user's behalf. Removing that leaves everyone who was ever
  // auto-collapsed pinned to a rail they never chose — indistinguishable, from
  // the outside, from the behaviour still being there. So clear that residue
  // once, then never touch the preference again. A deliberate collapse is worth
  // one re-click; a stuck sidebar is worth a bug report.
  const AUTO_COLLAPSE_CLEARED_KEY = 'sidebarAutoCollapseCleared'
  try {
    if (!localStorage.getItem(AUTO_COLLAPSE_CLEARED_KEY)) {
      localStorage.setItem('sidebarCollapsed', '0')
      localStorage.setItem(AUTO_COLLAPSE_CLEARED_KEY, '1')
    }
  } catch { /* private mode / quota — the default below is already expanded */ }
  const sidebarCollapsed = ref<boolean>(localStorage.getItem('sidebarCollapsed') === '1')

  // Workspaces whose Files widget has earned its place on screen. A new chat
  // has nothing to browse, so the widget stays out of the way until the
  // workspace holds something — see WorkspaceFilePanel for what counts.
  //
  // Keyed by workspace path rather than session id: it is the folder that does
  // or doesn't have content, so chats sharing a project workspace agree, and
  // the folder picker can mark a folder before any session exists for it.
  //
  // One-way. Content can come and go (scratch files get cleaned up mid-turn),
  // and a widget that appeared and then vanished would read as a glitch, so
  // revealing never reverses.
  //
  // Cached in sessionStorage because one reveal cannot be re-derived: a folder
  // the user chose through "Use existing folder" that happens to be empty. There
  // is nothing on disk to detect, so without the cache the widget would appear
  // on pick and then vanish on the next reload. Reveals that came from content
  // would survive on their own; this one needs remembering.
  //
  // sessionStorage rather than localStorage keeps it from outliving the tab, so
  // a workspace that is emptied for real gets a clean answer next visit instead
  // of a permanently stale one.
  const REVEALED_KEY = 'filesRevealedWorkspaces'
  function readRevealed(): Set<string> {
    try {
      const raw = sessionStorage.getItem(REVEALED_KEY)
      const list = raw ? JSON.parse(raw) : []
      return new Set(Array.isArray(list) ? list.filter(x => typeof x === 'string') : [])
    } catch { return new Set() }
  }
  const revealedWorkspaces = ref<Set<string>>(readRevealed())
  function revealFiles(workspace: string) {
    if (!workspace || revealedWorkspaces.value.has(workspace)) return
    const next = new Set(revealedWorkspaces.value)
    next.add(workspace)
    revealedWorkspaces.value = next
    try { sessionStorage.setItem(REVEALED_KEY, JSON.stringify([...next])) } catch { /* quota / private mode */ }
  }
  function filesRevealed(workspace: string): boolean {
    return !!workspace && revealedWorkspaces.value.has(workspace)
  }

  // ── Workspace file panel ──
  // Docked right of the chat. `filePanelCollapsed` is the effective collapsed
  // state; `filePanelManualCollapse` records an explicit user toggle so the
  // empty↔non-empty auto behavior only fires on that transition (not over a
  // manual choice). null = no manual override yet.
  //
  // The panel does two jobs and they want opposite proportions: browsing wants
  // a narrow list beside a wide chat, reading a file wants the reverse. So it
  // keeps two widths and remembers each separately. Switching between them
  // therefore can't destroy the other's setting, and a drag while reading is
  // never mistaken for a new browse preference.
  const filePanelBrowseWidth = ref<number>(readNumber(FILE_PANEL_WIDTH_KEY) ?? FILE_PANEL_DEFAULT)
  const filePanelStoredReadingWidth = ref<number | null>(readNumber(FILE_PANEL_READING_WIDTH_KEY))
  const filePanelReading = ref<boolean>(false)
  const filePanelCollapsed = ref<boolean>(false)
  // In-place collapse of the Files widget body (list hidden, header kept) —
  // mirrors the Plan & progress widget. Persisted.
  const filePanelBodyCollapsed = ref<boolean>(localStorage.getItem('filePanelBodyCollapsed') === '1')
  function setFilePanelBodyCollapsed(v: boolean) {
    filePanelBodyCollapsed.value = v
    localStorage.setItem('filePanelBodyCollapsed', v ? '1' : '0')
  }
  const filePanelManualCollapse = ref<boolean | null>(null)

  function clampFilePanelWidth(px: number): number {
    // Allow resizing all the way: cap only against the current window width
    // (minus a small margin so the chat never fully disappears), not a fixed
    // pixel maximum. Falls back to FILE_PANEL_MAX if the window size is
    // unavailable.
    const viewportMax =
      typeof window !== 'undefined' && window.innerWidth
        ? window.innerWidth - FILE_PANEL_MIN_CHAT_MARGIN
        : FILE_PANEL_MAX
    const upper = Math.max(FILE_PANEL_MIN, viewportMax)
    return Math.max(FILE_PANEL_MIN, Math.min(upper, Math.round(px)))
  }

  // The width in effect right now, picked by which job the panel is doing.
  const filePanelWidth = computed(() => {
    if (!filePanelReading.value) return clampFilePanelWidth(filePanelBrowseWidth.value)
    const want =
      filePanelStoredReadingWidth.value ??
      (typeof window !== 'undefined' && window.innerWidth
        ? window.innerWidth * FILE_PANEL_READING_FRACTION
        : FILE_PANEL_MAX)
    // Never narrower than the browse width. Widening the panel for readability
    // must not take space away from someone who had already dragged it wider.
    return clampFilePanelWidth(Math.max(want, filePanelBrowseWidth.value))
  })

  // A drag updates whichever width is currently on screen, so the user is
  // always adjusting the thing they can see.
  function setFilePanelWidth(px: number) {
    const clamped = clampFilePanelWidth(px)
    if (filePanelReading.value) {
      filePanelStoredReadingWidth.value = clamped
      localStorage.setItem(FILE_PANEL_READING_WIDTH_KEY, String(clamped))
      return
    }
    filePanelBrowseWidth.value = clamped
    localStorage.setItem(FILE_PANEL_WIDTH_KEY, String(clamped))
  }

  // Set while a file is open in the docked panel. Closing the file clears it
  // and the browse width comes back, so the chat reclaims the space instead of
  // being left beside an empty expanse of white.
  function setFilePanelReading(v: boolean) {
    filePanelReading.value = v
  }

  // Mobile/portrait only: the right-hand widget column (which hosts the Files
  // panel, incl. its markdown preview) is hidden below 900px to keep the chat
  // readable. This opens it as a full-screen overlay instead.
  const mobileFilesOpen = ref(false)
  function toggleMobileFiles() {
    mobileFilesOpen.value = !mobileFilesOpen.value
    // Opening from the header implies "show me the files", so make sure a
    // previously collapsed panel doesn't leave the overlay showing just a header.
    if (mobileFilesOpen.value) {
      filePanelCollapsed.value = false
      setFilePanelBodyCollapsed(false)
    }
  }
  function closeMobileFiles() { mobileFilesOpen.value = false }

  // User-initiated collapse/expand of the file panel (records an override).
  function toggleFilePanelCollapsed() {
    filePanelCollapsed.value = !filePanelCollapsed.value
    filePanelManualCollapse.value = filePanelCollapsed.value
  }

  // Auto collapse/expand driven by the workspace empty↔non-empty transition.
  // Respects a prior manual override.
  function autoSetFilePanelCollapsed(collapsed: boolean) {
    if (filePanelManualCollapse.value !== null) return
    filePanelCollapsed.value = collapsed
  }

  // Called on the empty↔non-empty transition to clear stale manual overrides
  // so the panel adapts to the new state.
  function resetFilePanelManual() {
    filePanelManualCollapse.value = null
  }

  function setTheme(t: 'dark' | 'light') {
    theme.value = t
    localStorage.setItem('theme', t)
    document.documentElement.setAttribute('data-theme', t)
  }

  function toggleTheme() {
    setTheme(theme.value === 'dark' ? 'light' : 'dark')
  }

  function initTheme() {
    document.documentElement.setAttribute('data-theme', theme.value)
  }

  function toggleSidebar() {
    sidebarOpen.value = !sidebarOpen.value
  }

  function closeSidebar() {
    sidebarOpen.value = false
  }

  function toggleSidebarCollapsed() {
    sidebarCollapsed.value = !sidebarCollapsed.value
    localStorage.setItem('sidebarCollapsed', sidebarCollapsed.value ? '1' : '0')
  }

  function setSidebarCollapsed(v: boolean) {
    sidebarCollapsed.value = v
    localStorage.setItem('sidebarCollapsed', v ? '1' : '0')
  }

  function openModal(id: string) {
    activeModal.value = id
  }

  function closeModal() {
    activeModal.value = null
  }

  // Workspace hygiene: how many chats are waiting to be reviewed, for the Settings badge.
  // Kept here rather than in the modal so the badge survives the modal being
  // closed, and so a cleanup can update it without reopening anything.
  const tidyCount = ref(0)

  async function refreshTidyCount() {
    try {
      tidyCount.value = await fetchTidyCount()
    } catch {
      // A badge is not worth a console error or a retry loop; showing none is
      // the correct failure.
      tidyCount.value = 0
    }
  }

  // openProject shows the project view; showChat returns to the chat surface.
  function openProject(id: string) {
    activeProjectId.value = id
    activeView.value = 'project'
  }
  function showChat() {
    activeView.value = 'chat'
  }

  // openScheduled shows the scheduled-tasks view.
  function openScheduled() {
    activeView.value = 'scheduled'
  }

  // openScheduledTaskEditor opens the create/edit task modal. Pass a task id to
  // edit, or null to create a new task.
  function openScheduledTaskEditor(id: string | null) {
    editingScheduledTaskId.value = id
    activeModal.value = 'scheduled-task'
  }

  return {
    theme, sidebarOpen, activeModal,
    activeView, activeProjectId, openProject, showChat,
    editingScheduledTaskId, openScheduled, openScheduledTaskEditor,
    sidebarCollapsed,
    filePanelWidth, filePanelCollapsed, filePanelManualCollapse,
    filePanelBodyCollapsed, setFilePanelBodyCollapsed,
    filePanelReading, setFilePanelReading,
    revealFiles, filesRevealed,
    mobileFilesOpen, toggleMobileFiles, closeMobileFiles,
    setFilePanelWidth, toggleFilePanelCollapsed, autoSetFilePanelCollapsed, resetFilePanelManual,
    setTheme, toggleTheme, initTheme, toggleSidebar, closeSidebar,
    toggleSidebarCollapsed, setSidebarCollapsed,
    openModal, closeModal,
    tidyCount, refreshTidyCount,
  }
})
