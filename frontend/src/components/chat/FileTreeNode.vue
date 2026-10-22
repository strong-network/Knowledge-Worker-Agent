<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// Recursive file-tree node for the WorkspaceFilePanel. Renders a single row for
// `entry` and, when the folder is expanded (tracked in the shared `expanded`
// map, keyed by full path), recurses into its children at `depth + 1`. This is
// what makes the tree work at arbitrary depth — the parent panel only owns the
// state and actions; rendering nests through this component.
import { computed, nextTick, ref, watch } from 'vue'
import type { FileEntry } from '../../api'
import type { FileSharing } from '../../api/files'
import ActionMenu, { type MenuEntry } from '../ActionMenu.vue'

const props = defineProps<{
  entry: FileEntry
  // Siblings of `entry` within its own folder — used for rename-collision checks.
  siblings: FileEntry[]
  // Nesting depth (0 = top level), for left indentation.
  depth: number
  // Shared state owned by WorkspaceFilePanel.
  expanded: Record<string, FileEntry[]>
  renamingPath: string
  // False for a guest of a shared chat: no rename, delete or moving.
  canModify: boolean
  canDuplicate: boolean
  // Offer to open a file in a window of its own.
  canPopOut: boolean
  // The file being dragged, and the folder it would be dropped into.
  dragPath: string
  dropTarget: string
  sharing?: FileSharing
  // Actions (stable references passed down from the panel).
  isChanged: (path: string) => boolean
  iconFor: (entry: FileEntry) => string
  openFile: (entry: FileEntry) => void
  startRename: (entry: FileEntry) => void
  commitRename: (entry: FileEntry, siblings: FileEntry[], refocus?: boolean) => void
  cancelRename: (refocus?: boolean) => void
  deleteEntry: (entry: FileEntry) => void
  downloadEntry: (entry: FileEntry) => void
  duplicateEntry: (entry: FileEntry) => void
  copyEntry: (entry: FileEntry) => void
  popOutEntry: (entry: FileEntry) => void
  dragStart: (entry: FileEntry, e: DragEvent) => void
  dragOver: (entry: FileEntry, e: DragEvent) => void
  dropOn: (e: DragEvent) => void
  dragEnd: () => void
}>()

// Two-way binding for the in-place rename input, threaded through the recursion.
const renameValue = defineModel<string>('renameValue', { required: true })

// Base indent per level (matches the previous fixed 20px for level 1, but now
// scales with depth). Level 0 rows get no extra indent.
const INDENT_PER_LEVEL = 14

const marker = computed(() => (props.entry.is_dir ? null : props.sharing?.marker(props.entry.path) ?? null))
const shareable = computed(() =>
  (!!props.sharing?.share || !!props.sharing?.shareBlocked) && !!props.sharing?.canShare?.(props.entry),
)
const fixing = computed(() => !props.entry.is_dir && !!props.sharing?.fixing?.(props.entry.path))

// Files whose contents can be copied as text.
const TEXT_EXTS = new Set([
  'md', 'markdown', 'mdx', 'txt', 'csv', 'tsv', 'json', 'jsonl', 'yaml', 'yml', 'toml', 'ini', 'xml',
  'html', 'htm', 'css', 'js', 'mjs', 'cjs', 'ts', 'tsx', 'jsx', 'vue', 'py', 'go', 'java', 'kt', 'c',
  'h', 'cpp', 'cs', 'rs', 'rb', 'php', 'sh', 'bash', 'ps1', 'sql', 'log', 'env', 'tex', 'rst',
])
const isText = computed(() => {
  const name = props.entry.name
  const dot = name.lastIndexOf('.')
  return dot <= 0 || TEXT_EXTS.has(name.slice(dot + 1).toLowerCase())
})

// The row's ⋯ menu.
const actionsName = computed(() => `Actions for "${props.entry.name}"`)
const items = computed<MenuEntry[]>(() => {
  const e = props.entry
  const list: MenuEntry[] = []
  if (!e.is_dir && props.canPopOut) list.push({ label: 'Open in new window', action: () => props.popOutEntry(e) })
  if (!e.is_dir && isText.value) list.push({ label: 'Copy contents', action: () => props.copyEntry(e) })
  list.push({ label: e.is_dir ? 'Download as .zip' : 'Download', action: () => props.downloadEntry(e) })
  if (shareable.value) {
    const sharing = props.sharing!
    list.push({
      label: marker.value ? 'Change sharing' : 'Share',
      unavailable: sharing.share ? undefined : sharing.shareBlocked,
      action: () => sharing.share?.(e),
    })
  }
  if (props.canModify) {
    list.push({ label: 'Rename', action: () => props.startRename(e) })
    if (props.canDuplicate && !e.is_dir) list.push({ label: 'Duplicate', action: () => props.duplicateEntry(e) })
    list.push('divider', { label: 'Delete', danger: true, action: () => props.deleteEntry(e) })
  }
  return list
})

// The name field takes focus when a rename starts, with the name selected
// up to its extension.
const renameInput = ref<HTMLInputElement | null>(null)
watch(() => props.renamingPath === props.entry.path, async (renaming) => {
  if (!renaming) return
  await nextTick()
  const input = renameInput.value
  if (!input) return
  input.focus()
  const dot = props.entry.is_dir ? -1 : input.value.lastIndexOf('.')
  input.setSelectionRange(0, dot > 0 ? dot : input.value.length)
}, { immediate: true })
</script>

<template>
  <div
    class="fp-row"
    :class="{ dir: entry.is_dir, dragging: dragPath === entry.path, 'drop-target': entry.is_dir && dropTarget === entry.path }"
    :data-path="entry.path"
    :style="{ paddingLeft: 6 + depth * INDENT_PER_LEVEL + 'px' }"
    :draggable="canModify && !entry.is_dir && renamingPath !== entry.path"
    @click="openFile(entry)"
    @dragstart="dragStart(entry, $event)"
    @dragover="dragOver(entry, $event)"
    @drop="dropOn($event)"
    @dragend="dragEnd"
  >
    <span v-if="entry.is_dir" class="fp-caret" :class="{ open: !!expanded[entry.path] }" aria-hidden="true">›</span>
    <span v-else class="fp-caret-spacer"></span>
    <span class="fp-icon">{{ iconFor(entry) }}</span>
    <input
      v-if="renamingPath === entry.path"
      ref="renameInput"
      class="fp-rename"
      v-model="renameValue"
      aria-label="Name"
      @click.stop
      @keydown.enter.stop.prevent="commitRename(entry, siblings, true)"
      @keydown.esc.stop.prevent="cancelRename(true)"
      @blur="commitRename(entry, siblings)"
      spellcheck="false"
    />
    <span v-else class="fp-name">{{ entry.name }}</span>
    <span v-if="marker" class="fp-shared" :class="{ attention: marker.attention }" role="img" :aria-label="marker.label" :title="marker.label">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="13" height="13" aria-hidden="true"><circle cx="9" cy="8" r="3.2"/><path d="M3.5 19c.6-3.2 2.8-5 5.5-5s4.9 1.8 5.5 5" stroke-linecap="round"/><path d="M15.5 5.2a3 3 0 0 1 0 5.6M17.5 14.4c1.7.6 2.8 2.2 3.1 4.6" stroke-linecap="round"/></svg>
    </span>
    <span v-if="fixing" class="fp-fixing" title="It's being made self-contained in the background.">Fixing</span>
    <span v-if="isChanged(entry.path)" class="fp-dot" title="New or modified" aria-hidden="true"></span>
    <ActionMenu v-if="renamingPath !== entry.path" class="fp-row-menu" :items="items" :label="actionsName" :menu-label="actionsName" />
  </div>

  <!-- Expanded children: recurse to arbitrary depth. -->
  <template v-if="entry.is_dir && expanded[entry.path]">
    <FileTreeNode
      v-for="child in expanded[entry.path]"
      :key="child.path"
      :entry="child"
      :siblings="expanded[entry.path]"
      :depth="depth + 1"
      :expanded="expanded"
      :renaming-path="renamingPath"
      :can-modify="canModify"
      :can-duplicate="canDuplicate"
      :can-pop-out="canPopOut"
      :drag-path="dragPath"
      :drop-target="dropTarget"
      :sharing="sharing"
      v-model:rename-value="renameValue"
      :is-changed="isChanged"
      :icon-for="iconFor"
      :open-file="openFile"
      :start-rename="startRename"
      :commit-rename="commitRename"
      :cancel-rename="cancelRename"
      :delete-entry="deleteEntry"
      :download-entry="downloadEntry"
      :duplicate-entry="duplicateEntry"
      :copy-entry="copyEntry"
      :pop-out-entry="popOutEntry"
      :drag-start="dragStart"
      :drag-over="dragOver"
      :drop-on="dropOn"
      :drag-end="dragEnd"
    />
  </template>
</template>

<style scoped>
/* 3px rather than 6px of padding: the ⋯ is 24px tall, so the row keeps its
   height. */
.fp-row {
  display: flex; align-items: center; gap: 6px; padding: 3px 6px;
  border-radius: 6px; cursor: pointer; font-size: 14px; color: var(--text);
  white-space: nowrap; overflow: hidden; transition: background .1s;
}
.fp-row:hover { background: var(--surface2); }
.fp-row.dragging { opacity: .45; }
.fp-row.drop-target { background: var(--surface2); box-shadow: inset 0 0 0 1.5px var(--accent); }
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
.fp-shared { display: inline-flex; flex-shrink: 0; color: var(--accent); }
.fp-shared.attention { color: var(--orange); }
.fp-fixing {
  flex-shrink: 0; font-size: 10px; font-weight: 700; line-height: 1.4; padding: 0 5px;
  color: var(--orange); border: 1px solid currentColor; border-radius: 4px;
}
.fp-shared svg { display: block; }
.fp-rename {
  flex: 1; min-width: 0; padding: 2px 5px; font-size: 12px;
  border: 1px solid var(--accent); border-radius: 4px;
  background: var(--surface); color: var(--text); font-family: var(--mono);
}
/* The ⋯ keeps its place at the end of the row, so the name doesn't reflow
   when it shows: while the row is hovered, holds focus or has its menu open,
   and always on touch screens, which have no hover. */
.fp-row-menu { margin-left: auto; opacity: 0; pointer-events: none; }
.fp-row:hover .fp-row-menu,
.fp-row:focus-within .fp-row-menu,
.fp-row-menu.open { opacity: 1; pointer-events: auto; }
.fp-row:has(.fp-row-menu.open) { background: var(--surface2); }
@media (pointer: coarse) {
  .fp-row-menu { opacity: 1; pointer-events: auto; }
}
</style>
