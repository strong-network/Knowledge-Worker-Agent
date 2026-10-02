<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// Recursive file-tree node for the WorkspaceFilePanel. Renders a single row for
// `entry` and, when the folder is expanded (tracked in the shared `expanded`
// map, keyed by full path), recurses into its children at `depth + 1`. This is
// what makes the tree work at arbitrary depth — the parent panel only owns the
// state and actions; rendering nests through this component.
import type { FileEntry } from '../../api'

defineProps<{
  entry: FileEntry
  // Siblings of `entry` within its own folder — used for rename-collision checks.
  siblings: FileEntry[]
  // Nesting depth (0 = top level), for left indentation.
  depth: number
  // Shared state owned by WorkspaceFilePanel.
  expanded: Record<string, FileEntry[]>
  renamingPath: string
  // False for a guest of a shared chat: no rename or delete.
  canModify: boolean
  // Actions (stable references passed down from the panel).
  isChanged: (path: string) => boolean
  iconFor: (entry: FileEntry) => string
  openFile: (entry: FileEntry) => void
  startRename: (entry: FileEntry) => void
  commitRename: (entry: FileEntry, siblings: FileEntry[]) => void
  cancelRename: () => void
  deleteEntry: (entry: FileEntry) => void
  downloadEntry: (entry: FileEntry) => void
}>()

// Two-way binding for the in-place rename input, threaded through the recursion.
const renameValue = defineModel<string>('renameValue', { required: true })

// Base indent per level (matches the previous fixed 20px for level 1, but now
// scales with depth). Level 0 rows get no extra indent.
const INDENT_PER_LEVEL = 14
</script>

<template>
  <div
    class="fp-row"
    :class="{ dir: entry.is_dir }"
    :style="{ paddingLeft: 6 + depth * INDENT_PER_LEVEL + 'px' }"
    @click="openFile(entry)"
  >
    <span v-if="entry.is_dir" class="fp-caret" :class="{ open: !!expanded[entry.path] }" aria-hidden="true">›</span>
    <span v-else class="fp-caret-spacer"></span>
    <span class="fp-icon">{{ iconFor(entry) }}</span>
    <input
      v-if="renamingPath === entry.path"
      class="fp-rename"
      v-model="renameValue"
      @click.stop
      @keydown.enter.stop.prevent="commitRename(entry, siblings)"
      @keydown.esc.stop.prevent="cancelRename"
      @blur="commitRename(entry, siblings)"
      autofocus spellcheck="false"
    />
    <span v-else class="fp-name">{{ entry.name }}</span>
    <span v-if="isChanged(entry.path)" class="fp-dot" title="New or modified" aria-hidden="true"></span>
    <span class="fp-row-actions" @click.stop>
      <button @click="downloadEntry(entry)" :title="entry.is_dir ? 'Download as .zip' : 'Download'">⬇</button>
      <template v-if="canModify">
        <button @click="startRename(entry)" title="Rename">✎</button>
        <button class="danger" @click="deleteEntry(entry)" title="Delete">🗑</button>
      </template>
    </span>
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
</template>

<style scoped>
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
</style>
