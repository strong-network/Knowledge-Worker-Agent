<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// A folder's files, and the document open from it: the view the owner's Files
// widget and a guest's shared chat both use. It renders from props
// and talks to files only through `api`, so it knows no stores. When to show
// it and when to refresh it belong to the host.
import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import type { FileEntry } from '../../api'
import type { FileApi } from '../../api/files'
import FileTreeNode from './FileTreeNode.vue'
import DocumentView from './DocumentView.vue'
import { triggerDownload } from '../../utils/download'

const props = withDefaults(defineProps<{
  api: FileApi
  root: string
  // Whether the panel renders. It stays mounted either way, so it keeps its
  // state and still lists the folder for a host deciding whether to show it.
  shown?: boolean
  // A plain in-place panel sized by its host, rather than the chat-docked
  // widget: always expanded, no collapse chevron.
  embedded?: boolean
  // Offer to move the open document into a window of its own.
  canPopOut?: boolean
  // Takes a file to be read somewhere else instead of here, when it returns true.
  openElsewhere?: (path: string) => boolean
}>(), { shown: true, embedded: false, canPopOut: false })

const bodyCollapsed = defineModel<boolean>('bodyCollapsed', { default: false })

const emit = defineEmits<{
  'open-change': [open: boolean]
  listed: [entries: FileEntry[]]
  refreshed: []
  'pop-out': [path: string]
}>()

// Browse state.
const entries = ref<FileEntry[]>([])            // current dir listing (sorted)
const expanded = ref<Record<string, FileEntry[]>>({}) // path -> children (inline expand)
// Change markers. `seen` is what this panel last listed at each path (a file's
// time and size); `changedPaths` is what is flagged until the user looks. A
// folder's listed time is the newest change anywhere under it, so a collapsed
// folder is marked when that passes `folderSince`: when the user last saw
// inside it, or the first listing for one that was already there. A folder with
// no `folderSince` appeared after that, so everything in it is new.
const seen = new Map<string, string>()
const looked = new Set<string>() // folders whose contents have been listed
const changedPaths = ref<Set<string>>(new Set())
const folderTime = ref<Map<string, number>>(new Map())
const folderSince = ref<Map<string, number>>(new Map())
let firstLoad = true
const uploadInput = ref<HTMLInputElement | null>(null)

// Open-file state — when set, the document view replaces the browse panel.
// The document itself (rendering, editing, saving) belongs to DocumentView;
// the panel only decides *which* file is open.
const openPath = ref('')
const openName = ref('')
const dirty = ref(false)
const fullscreen = ref(false)
const docView = ref<InstanceType<typeof DocumentView> | null>(null)

const isOpen = computed(() => !!openPath.value)
const canModify = computed(() => !!props.api.rename && !!props.api.remove)

watch(isOpen, (open) => emit('open-change', open))

const ext = (name: string) => name.split('.').pop()?.toLowerCase() || ''

// ── Loading / sorting ──
function sortEntries(list: FileEntry[]): FileEntry[] {
  // Folders first, then alphabetical (case-insensitive) within each group.
  return [...list].sort((a, b) => {
    if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1
    return a.name.localeCompare(b.name, undefined, { sensitivity: 'base' })
  })
}

// The prefix every path inside dir starts with. A guest's paths are relative,
// with "." for the folder itself.
function inside(dir: string): string {
  if (dir === '.' || dir === '') return ''
  return dir.endsWith('/') ? dir : dir + '/'
}

function timeOf(modTime: string): number {
  return Date.parse(modTime) || 0
}

// markListing flags what changed in one folder's listing: a file that is new or
// whose time or size moved, and a folder that is new. The first root listing is
// the baseline. The first look into a folder that was already there flags only
// what is newer than the user's last look into it, not all of it; after that,
// whatever wasn't listed before is new. The open file is never flagged: the
// user is looking at it.
function markListing(dir: string, list: FileEntry[]) {
  const isRoot = dir === props.root
  const baseline = isRoot && firstLoad
  const since = isRoot ? undefined : folderSince.value.get(dir)
  const byTime = !looked.has(dir) && since !== undefined
  looked.add(dir)
  const changed = new Set(changedPaths.value)
  const prefix = inside(dir)
  const listed = new Set(list.map(e => e.path))
  // Drop flags for what is gone, and for anything under a folder that is.
  for (const p of changed) {
    if (!p.startsWith(prefix)) continue
    const rest = p.slice(prefix.length)
    const i = rest.indexOf('/')
    if (!listed.has(prefix + (i < 0 ? rest : rest.slice(0, i)))) changed.delete(p)
  }
  for (const e of list) {
    const t = timeOf(e.mod_time)
    const stamp = e.is_dir ? '' : `${e.mod_time}|${e.size}`
    const before = seen.get(e.path)
    if (e.is_dir) {
      folderTime.value.set(e.path, t)
      // A folder seen for the first time counts from the same point as the
      // folder around it. One that is new has no such point: all of it is new.
      if (before === undefined && !folderSince.value.has(e.path)) {
        if (baseline) folderSince.value.set(e.path, t)
        else if (byTime) folderSince.value.set(e.path, since)
      }
    }
    if (!baseline && e.path !== openPath.value) {
      if (before === undefined) {
        if (!byTime || t > since) changed.add(e.path)
      } else if (before !== stamp) {
        changed.add(e.path)
      }
    }
    seen.set(e.path, stamp)
  }
  if (baseline) firstLoad = false
  changedPaths.value = changed
}

async function loadRoot() {
  const root = props.root
  if (!root) { entries.value = []; return }
  try {
    const list = await props.api.browse(root)
    // The root changed while this was in flight: this listing is not its.
    if (root !== props.root) return
    entries.value = sortEntries(list)
    markListing(root, entries.value)
    emit('listed', entries.value)
  } catch (e) {
    console.error('file panel: browse failed', e)
    entries.value = []
  }
}

async function refreshExpanded() {
  for (const key of Object.keys(expanded.value)) {
    try {
      const list = sortEntries(await props.api.browse(key))
      if (!expanded.value[key]) continue // collapsed meanwhile
      expanded.value[key] = list
      markListing(key, list)
    } catch { delete expanded.value[key] }
  }
}

async function fullRefresh() {
  await loadRoot()
  await refreshExpanded()
  // The file open in the editor may have changed on disk; DocumentView decides
  // whether to take the new bytes (it won't clobber unsaved edits).
  docView.value?.reload()
  emit('refreshed')
}

// ── Inline folder expand/collapse ──
async function toggleDir(entry: FileEntry) {
  if (expanded.value[entry.path]) {
    delete expanded.value[entry.path]
    // The user has seen inside it up to now.
    folderSince.value.set(entry.path, folderTime.value.get(entry.path) ?? 0)
    return
  }
  try {
    const list = sortEntries(await props.api.browse(entry.path))
    expanded.value[entry.path] = list
    // What is new inside is now marked on its own entries.
    changedPaths.value.delete(entry.path)
    markListing(entry.path, list)
  } catch (e) {
    ;(window as any).showToast?.('Could not open folder')
  }
}

// ── Open / edit files ──

function openFile(entry: FileEntry) {
  if (entry.is_dir) { toggleDir(entry); return }
  // Clear the change marker once viewed.
  changedPaths.value.delete(entry.path)

  if (props.openElsewhere?.(entry.path)) return
  if (dirty.value && openPath.value) {
    if (!confirm(`Unsaved changes in ${openName.value}. Discard?`)) return
  }
  openPath.value = entry.path
  openName.value = entry.name
}

function closeFile(): boolean {
  if (dirty.value && !confirm('Discard unsaved changes?')) return false
  openPath.value = ''
  openName.value = ''
  dirty.value = false
  fullscreen.value = false
  return true
}

// Must stay on the click path — browsers block window.open without a user
// gesture — and honours the unsaved-changes prompt: the buffer doesn't travel
// to the new window, so leaving it behind unsaved would silently lose it.
function popOut() {
  const path = openPath.value
  if (!path) return
  if (!closeFile()) return
  emit('pop-out', path)
}

function toggleFullscreen() { fullscreen.value = !fullscreen.value }

// ── Browse actions ──
function triggerUpload() { uploadInput.value?.click() }
async function onUpload(e: Event) {
  const input = e.target as HTMLInputElement
  if (!input.files || input.files.length === 0) return
  // Block name collisions (no silent overwrite) — check against current dir.
  const existing = new Set(entries.value.map(x => x.name))
  const clashes = Array.from(input.files).map(f => f.name).filter(n => existing.has(n))
  if (clashes.length) {
    ;(window as any).showToast?.(`Already exists: ${clashes.join(', ')}`)
    input.value = ''
    return
  }
  try {
    await props.api.upload(input.files, props.root)
    ;(window as any).showToast?.('Uploaded')
    await fullRefresh()
  } catch (err) {
    ;(window as any).showToast?.(err instanceof Error ? err.message : 'Upload failed')
  }
  input.value = ''
}

async function addFolder() {
  const name = window.prompt('Folder name')?.trim()
  if (!name) return
  if (entries.value.some(x => x.name === name)) {
    ;(window as any).showToast?.(`"${name}" already exists`)
    return
  }
  try {
    await props.api.createDirectory(joinPath(props.root, name))
    ;(window as any).showToast?.('Folder created')
    await fullRefresh()
  } catch (err) {
    ;(window as any).showToast?.(err instanceof Error ? err.message : 'Failed to create folder')
  }
}

function downloadEntry(entry: FileEntry) {
  triggerDownload(props.api.downloadUrl(entry.path))
}

const renamingPath = ref('')
const renameValue = ref('')
function startRename(entry: FileEntry) {
  renamingPath.value = entry.path
  renameValue.value = entry.name
}
function cancelRename() { renamingPath.value = ''; renameValue.value = '' }
async function commitRename(entry: FileEntry, siblings: FileEntry[]) {
  const next = renameValue.value.trim()
  if (!next || next === entry.name || !props.api.rename) { cancelRename(); return }
  if (next.includes('/')) { (window as any).showToast?.('Name cannot contain "/"'); return }
  if (siblings.some(x => x.name === next && x.path !== entry.path)) {
    ;(window as any).showToast?.(`"${next}" already exists`)
    return
  }
  const dest = joinPath(dirOf(entry.path), next)
  try {
    await props.api.rename(entry.path, dest)
    cancelRename()
    if (openPath.value === entry.path) { openPath.value = dest; openName.value = next }
    await fullRefresh()
  } catch (err) {
    ;(window as any).showToast?.(err instanceof Error ? err.message : 'Rename failed')
  }
}

async function deleteEntry(entry: FileEntry) {
  if (!props.api.remove) return
  const label = entry.is_dir ? `folder "${entry.name}" and all its contents` : `file "${entry.name}"`
  if (!window.confirm(`Delete ${label}? This cannot be undone.`)) return
  try {
    await props.api.remove(entry.path)
    if (openPath.value === entry.path || openPath.value.startsWith(entry.path + '/')) closeFile()
    delete expanded.value[entry.path]
    await fullRefresh()
  } catch (err) {
    ;(window as any).showToast?.(err instanceof Error ? err.message : 'Delete failed')
  }
}

function joinPath(dir: string, name: string): string {
  if (!dir) return name
  return dir.endsWith('/') ? dir + name : `${dir}/${name}`
}
function dirOf(path: string): string {
  const i = path.lastIndexOf('/')
  return i <= 0 ? '/' : path.slice(0, i)
}

function iconFor(entry: FileEntry): string {
  if (entry.is_dir) return '📁'
  const e = ext(entry.name)
  if (['png','jpg','jpeg','gif','webp','svg','bmp','ico','avif'].includes(e)) return '🖼️'
  if (['md','markdown','txt'].includes(e)) return '📝'
  if (e === 'pdf') return '📕'
  if (['pptx','ppt'].includes(e)) return '📊'
  if (['xlsx','xls','csv'].includes(e)) return '📈'
  if (['docx','doc'].includes(e)) return '📄'
  return '📄'
}
// A file is marked while flagged. A folder is also marked while something under
// it is, or, collapsed, while it holds a change newer than the user's last look.
function isChanged(path: string): boolean {
  if (changedPaths.value.has(path)) return true
  const t = folderTime.value.get(path)
  if (t === undefined) return false
  if (!expanded.value[path]) {
    const since = folderSince.value.get(path)
    if (since === undefined || t > since) return true
  }
  const prefix = inside(path)
  for (const p of changedPaths.value) if (p.startsWith(prefix)) return true
  return false
}

// Reload whenever the root changes (chat switch, or a project's workspace).
watch(() => props.root, () => {
  if (openPath.value && !dirty.value) closeFile()
  expanded.value = {}
  seen.clear()
  looked.clear()
  changedPaths.value = new Set()
  folderTime.value = new Map()
  folderSince.value = new Map()
  firstLoad = true
  loadRoot()
})

// ESC exits full-screen.
function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape' && fullscreen.value) { fullscreen.value = false }
}

onMounted(() => {
  loadRoot()
  window.addEventListener('keydown', onKeydown)
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKeydown)
})

defineExpose({ refresh: fullRefresh })
</script>

<template>
  <div
    v-if="shown"
    class="file-panel"
    :class="{ editing: isOpen, fullscreen, embedded, 'body-collapsed': bodyCollapsed && !isOpen }"
  >
    <!-- ── Browse view ── -->
    <template v-if="!isOpen">
      <div class="fp-header" :class="{ clickable: !embedded }" @click="!embedded && (bodyCollapsed = !bodyCollapsed)">
        <span class="fp-title">Files</span>
        <div class="fp-header-actions" @click.stop>
          <button class="fp-ico" @click="triggerUpload" title="Upload file" aria-label="Upload file">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="15" height="15"><path d="M12 16V4M7 9l5-5 5 5M5 20h14" stroke-linecap="round" stroke-linejoin="round"/></svg>
          </button>
          <button class="fp-ico" @click="addFolder" title="New folder" aria-label="New folder">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="15" height="15"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" stroke-linecap="round" stroke-linejoin="round"/><path d="M12 11v5M9.5 13.5h5" stroke-linecap="round"/></svg>
          </button>
          <button class="fp-ico" @click="fullRefresh" title="Refresh" aria-label="Refresh">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="15" height="15"><path d="M21 12a9 9 0 1 1-2.64-6.36M21 3v6h-6" stroke-linecap="round" stroke-linejoin="round"/></svg>
          </button>
          <!-- In-place collapse chevron (matches Plan & progress) -->
          <button
            v-if="!embedded"
            class="fp-ico"
            @click="bodyCollapsed = !bodyCollapsed"
            :title="bodyCollapsed ? 'Expand' : 'Collapse'"
            :aria-label="bodyCollapsed ? 'Expand' : 'Collapse'"
          >
            <svg viewBox="0 0 16 16" fill="currentColor" width="12" height="12" :style="{ transform: bodyCollapsed ? 'rotate(180deg)' : 'none' }">
              <path d="M4.22 9.78a.75.75 0 0 0 1.06 0L8 7.06l2.72 2.72a.75.75 0 1 0 1.06-1.06L8.53 5.47a.75.75 0 0 0-1.06 0L4.22 8.72a.75.75 0 0 0 0 1.06z"/>
            </svg>
          </button>
        </div>
      </div>
      <input ref="uploadInput" type="file" multiple style="display:none" @change="onUpload" />

      <div v-if="!bodyCollapsed" class="fp-list">
        <template v-if="entries.length">
          <FileTreeNode
            v-for="entry in entries"
            :key="entry.path"
            :entry="entry"
            :siblings="entries"
            :depth="0"
            :expanded="expanded"
            :renaming-path="renamingPath"
            :can-modify="canModify"
            v-model:rename-value="renameValue"
            :is-changed="isChanged"
            :icon-for="iconFor"
            :open-file="openFile"
            :start-rename="startRename"
            :commit-rename="commitRename"
            :cancel-rename="cancelRename"
            :delete-entry="deleteEntry"
            :download-entry="downloadEntry"
          />
        </template>
        <div v-else class="fp-empty">No files yet</div>
      </div>
    </template>

    <!-- ── Document view (replaces the browse panel) ── -->
    <template v-else>
      <DocumentView
        ref="docView"
        :path="openPath"
        :name="openName"
        :api="api"
        @update:dirty="dirty = $event"
      >
        <template #leading>
          <button class="fp-ico" @click="closeFile" title="Back to files" aria-label="Back to files">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="15" height="15"><path d="M15 18l-6-6 6-6" stroke-linecap="round" stroke-linejoin="round"/></svg>
          </button>
        </template>
        <template #trailing>
          <button v-if="canPopOut" class="fp-ico" @click="popOut" title="Open in a separate window" aria-label="Open in a separate window">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="15" height="15"><path d="M14 4h6v6M20 4l-8 8M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5" stroke-linecap="round" stroke-linejoin="round"/></svg>
          </button>
          <button class="fp-ico" @click="toggleFullscreen" :title="fullscreen ? 'Exit full screen' : 'Full screen'" aria-label="Full screen">
            <svg v-if="!fullscreen" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="15" height="15"><path d="M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5" stroke-linecap="round" stroke-linejoin="round"/></svg>
            <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="15" height="15"><path d="M9 4v5H4M15 4v5h5M9 20v-5H4M15 20v-5h5" stroke-linecap="round" stroke-linejoin="round"/></svg>
          </button>
          <button class="fp-ico" @click="closeFile" title="Close" aria-label="Close">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="15" height="15"><path d="M6 6l12 12M18 6L6 18" stroke-linecap="round"/></svg>
          </button>
        </template>
      </DocumentView>
    </template>
  </div>
</template>

<style scoped>/* Files widget: a card filling the shared widget column. It grows to take the
   remaining column height (below the Plan card) and scrolls internally. */
.file-panel {
  width: 100%;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 12px;
  box-shadow: 0 4px 16px rgba(0,0,0,.08);
  display: flex; flex-direction: column; position: relative;
  min-height: 0;
  flex: 1 1 auto;
  overflow: hidden;
}
/* Browse mode caps its height so the list scrolls without pushing the column;
   editor mode stretches to fill the column. */
.file-panel:not(.editing) { max-height: 40vh; }
/* Embedded (project view): fill the host container instead of the chat-docked
   card look — no cap, no outer border/shadow (the host provides the frame). */
.file-panel.embedded {
  height: 100%; max-height: none; border: none; border-radius: 0; box-shadow: none;
}
.file-panel.embedded:not(.editing) { max-height: none; }
.file-panel.editing { flex: 1 1 auto; }
.file-panel.fullscreen {
  position: fixed; inset: 0; width: 100vw !important; max-width: 100vw !important;
  height: 100vh; max-height: 100vh; border-radius: 0; z-index: 500;
}

.fp-header {
  display: flex; align-items: center; gap: 6px; padding: 8px 10px;
  border-bottom: 1px solid var(--border); background: var(--surface); flex-shrink: 0;
}
.fp-header.clickable { cursor: pointer; user-select: none; }
/* When collapsed in place, the header has no list below it — drop its divider
   so it reads as a single compact card row (like the Plan widget collapsed). */
.file-panel.body-collapsed { flex: 0 0 auto; }
.file-panel.body-collapsed .fp-header { border-bottom: none; }
.fp-title { font-size: 15px; font-weight: 700; flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.fp-header-actions { display: flex; align-items: center; gap: 2px; }
.fp-ico {
  width: 28px; height: 28px; display: flex; align-items: center; justify-content: center;
  background: none; border: none; color: var(--text2); border-radius: 6px; cursor: pointer;
  transition: all .12s; font-size: 13px;
}
.fp-ico:hover { background: var(--surface2); color: var(--text); }

.fp-list { overflow-y: auto; padding: 4px 4px 8px; }
.fp-row {
  display: flex; align-items: center; gap: 6px; padding: 6px 6px;
  border-radius: 6px; cursor: pointer; font-size: 14px; color: var(--text);
  white-space: nowrap; overflow: hidden; transition: background .1s;
}
.fp-row:hover { background: var(--surface2); }
.fp-caret {
  display: inline-flex; align-items: center; justify-content: center; width: 12px;
  color: var(--text3); font-size: 13px; flex-shrink: 0; transition: transform .12s;
}
.fp-caret.open { transform: rotate(90deg); }
.fp-caret-spacer { width: 12px; flex-shrink: 0; }
.fp-icon { flex-shrink: 0; font-size: 15px; }
.fp-name { overflow: hidden; text-overflow: ellipsis; flex: 1; }
.fp-dot {
  width: 7px; height: 7px; border-radius: 50%; background: var(--green);
  flex-shrink: 0; box-shadow: 0 0 0 2px rgba(110,184,157,.2);
}
.fp-rename {
  flex: 1; min-width: 0; padding: 2px 5px; font-size: 12px;
  border: 1px solid var(--accent); border-radius: 4px;
  background: var(--surface); color: var(--text); font-family: var(--mono);
}
.fp-row-actions { display: none; flex-shrink: 0; gap: 1px; margin-left: auto; }
.fp-row:hover .fp-row-actions { display: inline-flex; }
.fp-row-actions button {
  padding: 1px 5px; font-size: 11px; border: none; border-radius: 4px;
  background: none; color: var(--text2); cursor: pointer;
}
.fp-row-actions button:hover { background: var(--surface3); color: var(--text); }
.fp-row-actions button.danger:hover { background: rgba(218,63,63,.14); color: var(--red); }
.fp-empty { padding: 20px; text-align: center; color: var(--text3); font-size: 13px; }

@media (max-width: 768px) {
  /* Right-docked panel targets desktop widths; hide on mobile. */
  .file-panel { display: none; }
  /* …except when explicitly opened as the mobile Files overlay, where it
     takes the full screen. The host sets mobile-open. */
  .file-panel.mobile-open {
    display: flex;
    width: 100%;
    max-width: 100%;
    height: 100%;
    min-height: 0;
  }
}
</style>
