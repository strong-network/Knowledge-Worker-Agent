<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useSessionsStore } from '../../stores/sessions'
import { useUiStore } from '../../stores/ui'
import { browseFiles } from '../../api'
import { loadBaseDir } from '../../composables/useMe'

// Focused "Use an existing folder" picker. Browse the workspace and
// pick a folder to use as the chat's working directory — no advanced config.
const sessionsStore = useSessionsStore()
const ui = useUiStore()

const currentPath = ref('')
const dirs = ref<{ name: string; path: string }[]>([])
const creating = ref(false)
const error = ref('')

async function browse(path: string) {
  error.value = ''
  try {
    const entries = await browseFiles(path)
    dirs.value = entries.filter(e => e.is_dir).map(e => ({ name: e.name, path: e.path }))
    currentPath.value = path
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not open folder'
  }
}

function goUp() {
  const parent = currentPath.value.split('/').slice(0, -1).join('/') || '/'
  browse(parent)
}

onMounted(async () => browse(await loadBaseDir()))

async function useFolder() {
  if (!currentPath.value) return
  creating.value = true
  try {
    // Choosing a folder is asking to work with its contents, so the Files
    // widget should be there on arrival rather than waiting for the workspace
    // to prove itself — the folder may legitimately still be empty.
    ui.revealFiles(currentPath.value)
    // Explicit workspace suppresses the auto per-chat folder on the backend.
    await sessionsStore.createSession({ name: 'New chat', workspace: currentPath.value })
    ui.closeModal()
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not start chat'
  } finally {
    creating.value = false
  }
}
</script>

<template>
  <div class="modal folder-picker" @click.stop style="width:480px">
    <h2>Use an existing folder</h2>
    <p class="modal-subtitle">Pick a folder to work in. The chat will operate directly on it.</p>

    <div class="fp-path">
      <span class="fp-path-label">Location</span>
      <code>{{ currentPath }}</code>
    </div>

    <div class="dir-browser">
      <div class="dir-item nav-up" @click="goUp">⬆ ..</div>
      <div
        v-for="d in dirs"
        :key="d.path"
        class="dir-item"
        @click="browse(d.path)"
        :title="d.path"
      >
        <span class="di-ico" aria-hidden="true">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="15" height="15"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" stroke-linecap="round" stroke-linejoin="round"/></svg>
        </span>
        {{ d.name }}
      </div>
      <div v-if="!dirs.length" class="dir-empty">No subfolders here</div>
    </div>

    <p v-if="error" class="fp-error">{{ error }}</p>

    <div class="modal-footer">
      <button class="btn btn-ghost" @click="ui.closeModal()" :disabled="creating">Cancel</button>
      <button class="btn btn-primary" @click="useFolder" :disabled="creating">
        {{ creating ? 'Starting…' : 'Use this folder' }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.fp-path {
  display: flex; align-items: center; gap: 8px; margin-bottom: 10px;
  padding: 8px 11px; background: var(--surface2); border: 1px solid var(--border); border-radius: 8px;
}
.fp-path-label { font-size: 11px; font-weight: 600; color: var(--text2); text-transform: uppercase; letter-spacing: .05em; flex-shrink: 0; }
.fp-path code { font-family: var(--mono); font-size: 12px; color: var(--text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.dir-browser {
  background: var(--bg); border: 1px solid var(--border); border-radius: 8px;
  max-height: 280px; overflow-y: auto;
}
.dir-item {
  padding: 8px 12px; cursor: pointer; font-size: 13px; color: var(--text);
  display: flex; align-items: center; gap: 8px; transition: background .1s;
  border-bottom: 1px solid var(--border);
}
.dir-item:last-child { border-bottom: none; }
.dir-item:hover { background: var(--surface2); }
.dir-item.nav-up { color: var(--accent); font-style: italic; }
.di-ico { color: var(--text2); display: inline-flex; }
.dir-empty { padding: 14px; text-align: center; color: var(--text3); font-size: 12px; }
.fp-error { color: var(--red); font-size: 12px; margin-top: 10px; }
</style>
