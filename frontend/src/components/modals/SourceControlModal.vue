<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount, watch } from 'vue'
import { useUiStore } from '../../stores/ui'
import { useSessionsStore } from '../../stores/sessions'
import {
  fetchGitStatus, fetchGitChanges, gitPull, gitPush, gitCommit,
  type GitStatus, type GitFileChange,
} from '../../api'

const ui = useUiStore()
const sessionsStore = useSessionsStore()

const workspace = computed(() => sessionsStore.currentSession?.workspace || '')
const status = ref<GitStatus | null>(null)
const changes = ref<GitFileChange[]>([])
const message = ref('')
const log = ref('')
const busy = ref(false)
const error = ref('')

let pollHandle: number | undefined

async function refresh() {
  if (!workspace.value) return
  try {
    const [s, c] = await Promise.all([
      fetchGitStatus(workspace.value),
      fetchGitChanges(workspace.value),
    ])
    status.value = s
    changes.value = c.changes
  } catch (e: any) {
    error.value = e?.message || String(e)
  }
}

async function commit(andPush = false) {
  if (!message.value.trim()) {
    error.value = 'Commit message is required'
    return
  }
  busy.value = true
  error.value = ''
  try {
    const res = await gitCommit(workspace.value, message.value, andPush)
    log.value = res.output || res.error || ''
    if (res.success) {
      message.value = ''
    } else if (res.error) {
      error.value = res.error
    }
    status.value = res.status
    await refresh()
  } finally {
    busy.value = false
  }
}

async function pull() {
  busy.value = true; error.value = ''
  try {
    const res = await gitPull(workspace.value)
    log.value = res.output || res.error || ''
    status.value = res.status
    if (!res.success && res.error) error.value = res.error
    await refresh()
  } finally { busy.value = false }
}

async function push() {
  busy.value = true; error.value = ''
  try {
    const res = await gitPush(workspace.value)
    log.value = res.output || res.error || ''
    status.value = res.status
    if (!res.success && res.error) error.value = res.error
    await refresh()
  } finally { busy.value = false }
}

const repoName = computed(() => {
  const root = status.value?.root || ''
  if (!root) return ''
  const parts = root.split('/').filter(Boolean)
  return parts[parts.length - 1] || root
})

const stagedCount = computed(() => changes.value.filter(c => c.staged).length)
const unstagedCount = computed(() => changes.value.filter(c => !c.staged).length)

function statusLabel(s: string) {
  switch (s) {
    case 'M': return 'Modified'
    case 'A': return 'Added'
    case 'D': return 'Deleted'
    case 'R': return 'Renamed'
    case 'C': return 'Copied'
    case 'U': return 'Conflict'
    case '?': return 'Untracked'
    case 'I': return 'Ignored'
    default:  return s
  }
}

onMounted(() => {
  refresh()
  pollHandle = window.setInterval(refresh, 5000)
})
onBeforeUnmount(() => {
  if (pollHandle) clearInterval(pollHandle)
})
watch(workspace, () => refresh())
</script>

<template>
  <div class="modal sc-modal" @click.stop>
    <div class="sc-titlebar">
      <h2>Source Control</h2>
      <button class="icon-btn" @click="ui.closeModal()" aria-label="Close" title="Close">✕</button>
    </div>

    <div v-if="!status?.is_repo" class="sc-empty">
      <p>The current workspace <code>{{ workspace || '(none)' }}</code> isn't a git repository.</p>
    </div>

    <template v-else>
      <!-- Repository row, à la VS Code -->
      <div class="sc-section-title">REPOSITORIES</div>
      <div class="sc-repo-row">
        <span class="sc-repo-icon" aria-hidden="true">🗂</span>
        <span class="sc-repo-name" :title="status.root">{{ repoName }}</span>
        <span class="sc-spacer" />
        <span class="sc-branch" :title="status.upstream ? 'Tracking ' + status.upstream : 'No upstream'">
          ⎇ {{ status.branch || 'detached' }}
        </span>
        <button class="sc-icon-btn" :disabled="busy || !status.has_remote" @click="pull()" title="Pull (git pull --ff-only)" aria-label="Pull">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="17" height="17"><path d="M12 4v12M6 10l6 6 6-6M5 20h14" stroke-linecap="round" stroke-linejoin="round"/></svg>
        </button>
        <button class="sc-icon-btn" :disabled="busy || !status.has_remote" @click="push()" title="Push (git push)" aria-label="Push">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="17" height="17"><path d="M12 20V8M6 14l6-6 6 6M5 4h14" stroke-linecap="round" stroke-linejoin="round"/></svg>
        </button>
        <button class="sc-icon-btn" :disabled="busy" @click="refresh()" title="Refresh" aria-label="Refresh">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="17" height="17"><path d="M21 12a9 9 0 1 1-2.64-6.36M21 3v6h-6" stroke-linecap="round" stroke-linejoin="round"/></svg>
        </button>
      </div>

      <!-- Commit form -->
      <div class="sc-section-title">CHANGES <span class="sc-count">{{ changes.length }}</span></div>
      <div class="sc-commit-box">
        <textarea
          v-model="message"
          class="sc-message"
          rows="2"
          :placeholder="`Message (commit on &quot;${status.branch || 'HEAD'}&quot;)`"
          :disabled="busy"
        />
        <div class="sc-commit-actions">
          <button class="sc-commit-btn" :disabled="busy || !message.trim() || !changes.length" @click="commit(false)">
            ✓ Commit
          </button>
          <button class="sc-commit-btn split" :disabled="busy || !message.trim() || !changes.length || !status.has_remote" @click="commit(true)" title="Commit & Push">
            ⇡ Commit &amp; Push
          </button>
        </div>
        <p class="sc-hint">All tracked &amp; untracked changes are staged before commit (<code>git add -A</code>).</p>
      </div>

      <!-- File list -->
      <div v-if="changes.length === 0" class="sc-empty-changes">
        Working tree clean. Nothing to commit.
      </div>
      <div v-else class="sc-files">
        <div v-if="stagedCount" class="sc-files-group">
          <div class="sc-files-header">Staged Changes <span class="sc-count">{{ stagedCount }}</span></div>
          <div v-for="f in changes.filter(c => c.staged)" :key="'s-' + f.path" class="sc-file" :title="statusLabel(f.status) + ': ' + f.path">
            <span class="sc-file-status" :class="'st-' + f.status">{{ f.status }}</span>
            <span class="sc-file-path">{{ f.old_path ? f.old_path + ' → ' + f.path : f.path }}</span>
          </div>
        </div>
        <div v-if="unstagedCount" class="sc-files-group">
          <div class="sc-files-header">Changes <span class="sc-count">{{ unstagedCount }}</span></div>
          <div v-for="f in changes.filter(c => !c.staged)" :key="'u-' + f.path" class="sc-file" :title="statusLabel(f.status) + ': ' + f.path">
            <span class="sc-file-status" :class="'st-' + f.status">{{ f.status }}</span>
            <span class="sc-file-path">{{ f.path }}</span>
          </div>
        </div>
      </div>

      <div v-if="error" class="sc-error">{{ error }}</div>
      <details v-if="log" class="sc-log">
        <summary>Last operation output</summary>
        <pre>{{ log }}</pre>
      </details>
    </template>
  </div>
</template>

<style scoped>
.sc-modal {
  width: min(720px, 95vw);
  max-height: 90vh;
  overflow-y: auto;
  padding: 0;
  background: var(--surface);
  border-radius: 12px;
}
.sc-titlebar {
  display: flex; align-items: center; justify-content: space-between;
  padding: 14px 18px 8px;
  border-bottom: 1px solid var(--border);
}
.sc-titlebar h2 { margin: 0; font-size: 14px; letter-spacing: .04em; text-transform: uppercase; color: var(--text2); }

.sc-section-title {
  font-size: 11px; letter-spacing: .08em; font-weight: 700;
  color: var(--text2); text-transform: uppercase;
  padding: 12px 18px 4px;
  display: flex; align-items: center; gap: 8px;
}
.sc-count {
  display: inline-block; min-width: 20px; padding: 0 6px;
  font-size: 11px; line-height: 16px; text-align: center;
  background: var(--surface3); color: var(--text2);
  border-radius: 999px; font-weight: 600;
}

.sc-repo-row {
  display: flex; align-items: center; gap: 8px;
  padding: 8px 18px; margin: 0 8px;
  border-radius: 6px;
  background: var(--surface2);
}
.sc-repo-icon { font-size: 16px; }
.sc-repo-name { font-weight: 600; }
.sc-spacer { flex: 1; }
.sc-branch {
  font-size: 12.5px; color: var(--text2);
  padding: 3px 10px; border: 1px solid var(--border); border-radius: 999px;
}
.sc-icon-btn {
  width: 34px; height: 34px;
  display: inline-flex; align-items: center; justify-content: center;
  background: var(--surface); color: var(--text2);
  border: 1px solid var(--border);
  border-radius: 8px; cursor: pointer; font-size: 18px; line-height: 1;
  transition: background .12s, color .12s, border-color .12s;
}
.sc-icon-btn:hover:not(:disabled) { background: var(--surface3); color: var(--text); border-color: var(--text3); }
.sc-icon-btn:disabled { opacity: .4; cursor: not-allowed; }

.sc-commit-box { padding: 4px 18px 12px; }
.sc-message {
  width: 100%; resize: vertical; min-height: 56px;
  padding: 8px 10px;
  background: var(--surface2);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
  font-family: inherit; font-size: 13px;
  box-sizing: border-box;
}
.sc-message:focus { outline: none; border-color: var(--accent, #2188ff); }
.sc-commit-actions { display: flex; gap: 8px; margin-top: 8px; }
.sc-commit-btn {
  flex: 1;
  padding: 8px 12px;
  background: var(--accent, #1f6feb);
  color: #fff;
  border: none;
  border-radius: 6px;
  font-weight: 600;
  font-size: 13px;
  cursor: pointer;
  transition: background .15s, opacity .15s;
}
.sc-commit-btn:hover:not(:disabled) { background: var(--accent-hover, #388bfd); }
.sc-commit-btn:disabled { opacity: .45; cursor: not-allowed; }
.sc-commit-btn.split { flex: 0 0 auto; background: var(--surface3); color: var(--text); }
.sc-commit-btn.split:hover:not(:disabled) { background: var(--border); }

.sc-hint { font-size: 11px; color: var(--text2); margin: 6px 0 0; }
.sc-hint code { background: var(--surface3); padding: 0 4px; border-radius: 3px; }

.sc-files { padding: 4px 8px 12px; }
.sc-files-group + .sc-files-group { margin-top: 10px; }
.sc-files-header {
  font-size: 11px; letter-spacing: .06em; font-weight: 700;
  color: var(--text2); text-transform: uppercase;
  padding: 6px 10px;
  display: flex; align-items: center; gap: 8px;
}
.sc-file {
  display: flex; align-items: center; gap: 10px;
  padding: 4px 10px;
  border-radius: 4px;
  font-size: 13px;
  cursor: default;
}
.sc-file:hover { background: var(--surface2); }
.sc-file-status {
  width: 18px; height: 18px;
  display: inline-flex; align-items: center; justify-content: center;
  font-weight: 700; font-size: 11px;
  border-radius: 3px;
  flex-shrink: 0;
}
.sc-file-path {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.st-M { color: #d1a72e; background: rgba(209,167,46,.12); }
.st-A { color: #2da44e; background: rgba(45,164,78,.12); }
.st-D { color: #cf222e; background: rgba(207,34,46,.12); }
.st-R { color: #6f42c1; background: rgba(111,66,193,.12); }
.st-C { color: #6f42c1; background: rgba(111,66,193,.12); }
.st-U { color: #cf222e; background: rgba(207,34,46,.18); }
.st-\? { color: #2da44e; background: rgba(45,164,78,.12); }
.st-I { color: var(--text2); background: var(--surface3); }

.sc-empty, .sc-empty-changes {
  padding: 24px 18px; color: var(--text2); text-align: center;
}
.sc-empty code { background: var(--surface2); padding: 2px 6px; border-radius: 4px; }

.sc-error {
  margin: 8px 18px; padding: 8px 10px;
  background: rgba(207,34,46,.12);
  border: 1px solid rgba(207,34,46,.4);
  color: #cf222e; border-radius: 6px;
  font-size: 12px;
}
.sc-log {
  margin: 0 18px 16px;
  background: var(--surface2);
  border: 1px solid var(--border);
  border-radius: 6px;
}
.sc-log summary {
  padding: 6px 10px;
  cursor: pointer; font-size: 12px; color: var(--text2);
}
.sc-log pre {
  margin: 0; padding: 8px 10px;
  border-top: 1px solid var(--border);
  font-size: 11px; line-height: 1.4;
  white-space: pre-wrap; word-break: break-word;
  max-height: 200px; overflow-y: auto;
}
</style>
