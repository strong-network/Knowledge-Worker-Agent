<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// The owner's Files widget: FileBrowser fed from the owner's stores (a guest's
// page feeds it from props). This wrapper decides when the widget shows (it appears once the chat's
// folder holds work), when it refreshes (the chat's tool calls and turns, and
// attachments), and where a document pops out to.
import { ref, computed, watch, onBeforeUnmount } from 'vue'
import { useUiStore } from '../../stores/ui'
import { useSessionsStore } from '../../stores/sessions'
import { useChatStore } from '../../stores/chat'
import { useUploadsStore } from '../../stores/uploads'
import type { FileEntry } from '../../api'
import { ownerFileApi } from '../../api/files'
import FileBrowser from './FileBrowser.vue'
import { useDocWindow } from '../../composables/useDocWindow'

const ui = useUiStore()
const sessionsStore = useSessionsStore()
const chatStore = useChatStore()
const uploadsStore = useUploadsStore()

// Props: by default the panel is the chat-docked Files widget fixed to the
// current chat's workspace. `rootPath` overrides that root (the project
// view mounts it on the project's shared workspace). `embedded` renders it as
// a plain in-place panel — no collapse rail, always shown — for the project
// view where it's the primary file browser rather than a dockable sidebar.
const props = withDefaults(defineProps<{ rootPath?: string; embedded?: boolean }>(), {
  rootPath: '',
  embedded: false,
})

// ── Workspace root (the current chat's workspace, or an explicit override) ──
const workspace = computed(() => props.rootPath || sessionsStore.currentSession?.workspace || '')
const browser = ref<InstanceType<typeof FileBrowser> | null>(null)

// The pop-out document window. While it is open it becomes the reading
// surface: the panel stays on the file list and sends clicks over there.
const docWindow = useDocWindow()
function openElsewhere(path: string): boolean {
  if (!docWindow.isOpen.value) return false
  docWindow.openDocument(path)
  return true
}

// Embedded (project-view) panels are never collapsed — they are the primary
// file browser, not a dockable sidebar.
const collapsed = computed(() => !props.embedded && ui.filePanelCollapsed)

// Opening a file turns this widget from a file list into a reading surface, so
// the column takes its wider reading width; closing hands the space back to
// the chat rather than leaving it beside an empty expanse. The embedded
// project-view panel is sized by its host, so it stays out of this entirely.
function onOpenChange(open: boolean) {
  if (!props.embedded) ui.setFilePanelReading(open)
}

// Body collapse (matches the Plan & progress widget): collapses the file list
// in place while keeping the "Files" header visible, toggled by a rotating
// chevron. Persisted so it survives reloads like other panel prefs.
const bodyCollapsed = ref(ui.filePanelBodyCollapsed)
watch(bodyCollapsed, (v) => ui.setFilePanelBodyCollapsed(v))

// ── Is there anything here worth showing? ──
// A brand-new chat workspace is not empty on disk: the platform scaffolds
// inputs/, working/ and .system/ before the first turn. Those are the
// container, not its contents, so "no entries" is the wrong test — an untouched
// workspace would show a widget listing three empty folders.
const SCAFFOLD_DIRS = ['inputs', 'working', '.system']
// Hidden, platform-managed, and always non-empty (it holds metadata), so it can
// never be evidence of the user's work.
const SYSTEM_DIR = '.system'

const revealed = computed(() => props.embedded || ui.filesRevealed(workspace.value))

async function detectContent(entries: FileEntry[]) {
  // One-way, so once shown there is nothing left to detect and no reason to
  // keep probing.
  if (revealed.value) return
  const root = workspace.value
  if (!root) return

  if (entries.some(e => !(e.is_dir && SCAFFOLD_DIRS.includes(e.name)))) {
    ui.revealFiles(root)
    return
  }

  // The root holds nothing but the scaffold. That is not the same as an empty
  // workspace: the folder convention sends scratch to working/ and source
  // material to inputs/, so a turn's real output can sit one level down while
  // the root still looks untouched. Look inside before declaring it empty.
  const probes = entries.filter(e => e.is_dir && e.name !== SYSTEM_DIR)
  if (!probes.length) return
  const listings = await Promise.all(
    probes.map(d => ownerFileApi.browse(d.path).catch(() => [] as FileEntry[])),
  )
  // The workspace may have changed while those were in flight.
  if (revealed.value || workspace.value !== root) return
  if (listings.some(l => l.length > 0)) ui.revealFiles(root)
}

const refresh = () => browser.value?.refresh()

// ── Live refresh: watch chat tool completions + stream end ──
const FILE_TOOLS = /file|edit|write|patch|create|bash|shell|terminal|command/i
watch(
  () => chatStore.toolCalls.filter(t => t.status !== 'running').map(t => `${t.callId}:${t.status}`).join('|'),
  (now, before) => {
    if (now === before) return
    const lastFew = chatStore.toolCalls.slice(-6)
    if (lastFew.some(t => t.status !== 'running' && FILE_TOOLS.test(t.name))) refresh()
  },
)
watch(() => chatStore.streaming, (isStreaming, was) => {
  if (was && !isStreaming) refresh()
})

// Also poll while streaming so writes appear promptly even if a tool name
// doesn't match the heuristic.
let pollTimer: ReturnType<typeof setInterval> | null = null
watch(() => chatStore.streaming, (isStreaming) => {
  if (isStreaming && !pollTimer) {
    pollTimer = setInterval(refresh, 2500)
  } else if (!isStreaming && pollTimer) {
    clearInterval(pollTimer); pollTimer = null
  }
})

// Attaching a file puts it in the workspace without going through a turn, so
// nothing else would refresh the list — and while the widget is hidden, that
// upload is exactly the thing that should bring it back.
watch(() => uploadsStore.attachedFiles.length, (now, before) => {
  if (now > (before || 0)) refresh()
})

// Asking for files on mobile is explicit intent, so honour it even if the
// workspace is still empty — otherwise the button opens an empty overlay.
watch(() => ui.mobileFilesOpen, (open) => {
  if (open && workspace.value) ui.revealFiles(workspace.value)
})

onBeforeUnmount(() => {
  if (pollTimer) clearInterval(pollTimer)
  // Don't strand the column at reading width once this panel is gone.
  if (!props.embedded) ui.setFilePanelReading(false)
})
</script>

<template>
  <!-- Collapsed: a compact card in the shared right-side widget column
       (matches the Plan & progress card); a click/chevron expands the panel. -->
  <div v-if="revealed && collapsed" class="fp-rail" @click="ui.toggleFilePanelCollapsed()" role="button" aria-label="Show files">
    <span class="fp-rail-icon" aria-hidden="true">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="15" height="15">
        <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" stroke-linecap="round" stroke-linejoin="round"/>
      </svg>
    </span>
    <span class="fp-rail-label">Files</span>
    <button class="fp-rail-expand" @click.stop="ui.toggleFilePanelCollapsed()" title="Show files" aria-label="Show files">
      <svg viewBox="0 0 16 16" fill="currentColor" width="12" height="12"><path d="M6.22 3.22a.75.75 0 0 1 1.06 0l4.25 4.25a.75.75 0 0 1 0 1.06l-4.25 4.25a.75.75 0 0 1-1.06-1.06L9.94 8 6.22 4.28a.75.75 0 0 1 0-1.06z"/></svg>
    </button>
  </div>

  <FileBrowser
    ref="browser"
    :api="ownerFileApi"
    :root="workspace"
    :shown="revealed && !collapsed"
    :embedded="embedded"
    can-pop-out
    :open-elsewhere="openElsewhere"
    v-model:body-collapsed="bodyCollapsed"
    :class="{ 'mobile-open': ui.mobileFilesOpen }"
    @open-change="onOpenChange"
    @listed="detectContent"
    @refreshed="docWindow.notifyRefresh()"
    @pop-out="docWindow.openDocument($event)"
  />
</template>

<style scoped>
/* Collapsed Files card — mirrors the Plan & progress header card so the two
   floating panels share a consistent design in the right-side stack. */
.fp-rail {
  width: 100%;
  display: flex; align-items: center; gap: 8px;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 12px;
  background: var(--surface2);
  box-shadow: 0 4px 16px rgba(0,0,0,.08);
  flex-shrink: 0;
  cursor: pointer; transition: background .15s, border-color .15s;
}
.fp-rail:hover { background: var(--surface3); }
.fp-rail-icon { color: var(--accent); display: inline-flex; align-items: center; justify-content: center; flex-shrink: 0; }
.fp-rail-label { font-size: 13px; color: var(--text); font-weight: 700; flex: 1; }
.fp-rail-expand {
  width: 22px; height: 22px; display: flex; align-items: center; justify-content: center;
  background: none; border: none; color: var(--text2); border-radius: 5px; cursor: pointer;
  transition: background .12s, color .12s;
}
.fp-rail-expand:hover { background: var(--surface); color: var(--text); }

@media (max-width: 768px) {
  /* Right-docked widget targets desktop widths; hide on mobile. */
  .fp-rail { display: none; }
}
</style>
