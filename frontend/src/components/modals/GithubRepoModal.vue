<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, watch, nextTick, onMounted, onBeforeUnmount } from 'vue'
import { useSessionsStore } from '../../stores/sessions'
import { useUiStore } from '../../stores/ui'
import { cloneRepo, discoverRepos } from '../../api'
import type { RepoInfo } from '../../api'
import { formatDisplayPath } from '../../utils/paths'

// "Connect a repository" — clone one by its URL, or pick one of the local git
// repositories already under the workspace (folders containing a .git
// directory). Either way the chat starts in that repository.
const sessionsStore = useSessionsStore()
const ui = useUiStore()

const repos = ref<RepoInfo[]>([])
const loading = ref(true)
const busy = ref(false)
const error = ref('')
const url = ref('')
const cloning = ref(false)
const urlInput = ref<HTMLInputElement | null>(null)
const blockedPanel = ref<HTMLElement | null>(null)

// The workspace refused the host before cloning: no token deployed for it, or
// it may only clone repositories added to the workspace. Both are fixed in
// SecurSpaces, not here, so the panel says where and offers a retry.
const blocked = ref<{ reason: 'no_token' | 'restricted', host: string, tokenUrl: string } | null>(null)
watch(url, () => { blocked.value = null })

const PROVIDERS: Record<string, string> = {
  'github.com': 'GitHub', 'gitlab.com': 'GitLab', 'bitbucket.org': 'Bitbucket', 'dev.azure.com': 'Azure DevOps',
}
const provider = computed(() => blocked.value ? (PROVIDERS[blocked.value.host] || blocked.value.host) : '')

// A clone can outlast the user's patience. Closing the modal doesn't stop it
// on the server, but it must not start a chat the user walked away from.
let open = true
onBeforeUnmount(() => { open = false })

async function load() {
  loading.value = true
  error.value = ''
  try {
    repos.value = await discoverRepos()
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not scan for repositories'
  } finally {
    loading.value = false
  }
}
onMounted(load)

async function startChat(name: string, workspace: string) {
  // Explicit workspace (the repo folder) suppresses the auto per-chat folder.
  await sessionsStore.createSession({ name: name || 'New chat', workspace })
  ui.closeModal()
}

async function pick(repo: RepoInfo) {
  if (busy.value || cloning.value) return
  busy.value = true
  error.value = ''
  try {
    await startChat(repo.name, repo.path)
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not start chat'
  } finally {
    busy.value = false
  }
}

async function clone() {
  const u = url.value.trim()
  if (!u || busy.value || cloning.value) return
  cloning.value = true
  error.value = ''
  try {
    const res = await cloneRepo(u)
    if (!open) return
    // Kept during a retry, so Try again doesn't vanish from under the pointer.
    blocked.value = res.blocked
      ? { reason: res.blocked, host: res.host || '', tokenUrl: res.token_url || '' }
      : null
    if (blocked.value) return
    if (!res.success || !res.workspace) {
      error.value = res.error || 'Could not clone the repository'
      return
    }
    await startChat(res.workspace.split('/').pop() || '', res.workspace)
  } catch (e) {
    if (open) error.value = e instanceof Error ? e.message : 'Could not clone the repository'
  } finally {
    cloning.value = false
    // Disabling the field and button while cloning dropped focus to the page.
    if (open && (blocked.value || error.value)) {
      void nextTick(() => (blockedPanel.value?.querySelector<HTMLElement>('a, button') || urlInput.value)?.focus())
    }
  }
}
</script>

<template>
  <div class="modal repo-picker" @click.stop style="width:480px">
    <h2>Connect a repository</h2>
    <p class="modal-subtitle">Clone a repository by its URL, or pick one that's already in your workspace.</p>

    <form class="rp-clone" @submit.prevent="clone">
      <label class="form-label" for="rp-url">Repository URL</label>
      <div class="rp-clone-row">
        <input
          id="rp-url"
          ref="urlInput"
          v-model="url"
          class="form-input"
          placeholder="https://github.com/owner/repo.git"
          spellcheck="false"
          autocomplete="off"
          :disabled="cloning"
        />
        <button type="submit" class="btn btn-primary" :disabled="!url.trim() || cloning || busy">
          {{ cloning ? 'Cloning…' : 'Clone' }}
        </button>
      </div>
      <p class="form-hint">Use the HTTPS address. Only the latest commit is downloaded.</p>
    </form>

    <div v-if="blocked" ref="blockedPanel" class="rp-blocked" role="alert">
      <template v-if="blocked.reason === 'no_token'">
        <strong>Connect {{ provider }} to SecurSpaces first</strong>
        <p>
          Your workspace has no {{ provider }} token, so it can't clone from {{ blocked.host }}.
          Connect {{ provider }} in SecurSpaces under Profile → Integrations, then try again.
        </p>
      </template>
      <template v-else>
        <strong>This workspace can't clone from {{ blocked.host }}</strong>
        <p>
          It can only clone the {{ blocked.host }} repositories added to it. Add this repository to the
          workspace in SecurSpaces, then try again.
        </p>
      </template>
      <div class="rp-blocked-actions">
        <a
          v-if="blocked.reason === 'no_token' && blocked.tokenUrl"
          class="btn btn-primary"
          :href="blocked.tokenUrl"
          target="_blank"
          rel="noopener noreferrer"
        >Open SecurSpaces integrations<span aria-hidden="true">↗</span><span class="sr-only"> (opens in a new tab)</span></a>
        <button type="button" class="btn btn-ghost" :disabled="cloning" @click="clone">
          {{ cloning ? 'Trying…' : 'Try again' }}
        </button>
      </div>
    </div>

    <h3 class="rp-section">In your workspace</h3>

    <div v-if="loading" class="rp-state">
      <span class="rp-spinner" aria-hidden="true" />
      Scanning for repositories…
    </div>

    <div v-else-if="repos.length" class="rp-list">
      <button
        v-for="repo in repos"
        :key="repo.path"
        class="rp-item"
        :disabled="busy || cloning"
        @click="pick(repo)"
        :title="repo.path"
      >
        <span class="rp-ico" aria-hidden="true">
          <svg viewBox="0 0 24 24" fill="currentColor" width="18" height="18"><path d="M12 2C6.48 2 2 6.58 2 12.25c0 4.53 2.87 8.37 6.84 9.73.5.1.68-.22.68-.49 0-.24-.01-.87-.01-1.71-2.78.62-3.37-1.37-3.37-1.37-.45-1.18-1.11-1.49-1.11-1.49-.91-.64.07-.63.07-.63 1 .07 1.53 1.06 1.53 1.06.89 1.56 2.34 1.11 2.91.85.09-.66.35-1.11.63-1.37-2.22-.26-4.55-1.14-4.55-5.07 0-1.12.39-2.03 1.03-2.75-.1-.26-.45-1.3.1-2.71 0 0 .84-.28 2.75 1.05a9.3 9.3 0 0 1 5 0c1.91-1.33 2.75-1.05 2.75-1.05.55 1.41.2 2.45.1 2.71.64.72 1.03 1.63 1.03 2.75 0 3.94-2.34 4.81-4.57 5.06.36.32.68.94.68 1.9 0 1.37-.01 2.48-.01 2.82 0 .27.18.6.69.49A10.02 10.02 0 0 0 22 12.25C22 6.58 17.52 2 12 2z"/></svg>
        </span>
        <span class="rp-text">
          <span class="rp-name">
            {{ repo.name }}
            <span v-if="repo.branch" class="rp-branch">⎇ {{ repo.branch }}</span>
          </span>
          <span class="rp-path">{{ formatDisplayPath(repo.path) }}</span>
        </span>
        <span class="rp-arrow" aria-hidden="true">›</span>
      </button>
    </div>

    <div v-else class="rp-empty">
      <div class="rp-empty-icon">🔍</div>
      <div class="rp-empty-title">No local repositories found</div>
      <div class="rp-empty-sub">Clone one with its URL above.</div>
    </div>

    <p v-if="error" class="rp-error" role="alert">{{ error }}</p>

    <div class="modal-footer">
      <button class="btn btn-ghost" @click="ui.closeModal()" :disabled="busy">Cancel</button>
      <button class="btn btn-ghost" @click="load" :disabled="loading || busy || cloning" title="Re-scan">
        {{ loading ? 'Scanning…' : 'Rescan' }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.rp-clone { margin-bottom: 18px; }
.rp-clone-row { display: flex; gap: 8px; }
.rp-clone-row .form-input { flex: 1; min-width: 0; }
.rp-clone-row .btn { flex-shrink: 0; }

.rp-blocked {
  margin: -6px 0 18px; padding: 11px 13px; border-radius: 0 8px 8px 0;
  border-left: 3px solid var(--orange);
  background: color-mix(in srgb, var(--orange) 10%, transparent);
  font-size: 12px; line-height: 1.5; color: var(--text);
}
.rp-blocked strong { display: block; font-size: 13px; margin-bottom: 3px; }
.rp-blocked p { margin: 0 0 10px; }
.rp-blocked-actions { display: flex; gap: 8px; flex-wrap: wrap; }
.rp-blocked-actions a.btn { text-decoration: none; display: inline-flex; align-items: center; gap: 5px; }

.rp-section {
  font-size: 11px; font-weight: 700; letter-spacing: .06em; text-transform: uppercase;
  color: var(--text2); margin: 0 0 8px;
}

.rp-state {
  display: flex; align-items: center; justify-content: center; gap: 10px;
  padding: 28px 16px; color: var(--text2); font-size: 13px;
  border: 1px solid var(--border); border-radius: 8px;
}
.rp-spinner {
  display: inline-block; width: 16px; height: 16px;
  border: 2px solid var(--border); border-top-color: var(--accent);
  border-radius: 50%; animation: rp-spin .8s linear infinite;
}
@keyframes rp-spin { to { transform: rotate(360deg); } }

.rp-list {
  border: 1px solid var(--border); border-radius: 10px; overflow: hidden;
  max-height: 340px; overflow-y: auto;
}
.rp-item {
  display: flex; align-items: center; gap: 12px; width: 100%;
  padding: 11px 12px; background: var(--surface); border: none;
  border-bottom: 1px solid var(--border); cursor: pointer;
  text-align: left; font-family: inherit; transition: background .12s;
}
.rp-item:last-child { border-bottom: none; }
.rp-item:hover:not(:disabled) { background: var(--surface2); }
.rp-item:disabled { opacity: .6; cursor: default; }
.rp-ico { color: var(--text); flex-shrink: 0; display: inline-flex; }
.rp-text { display: flex; flex-direction: column; gap: 2px; flex: 1; min-width: 0; }
.rp-name {
  font-size: 14px; font-weight: 600; color: var(--text);
  display: flex; align-items: center; gap: 8px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.rp-branch {
  font-size: 11px; font-weight: 500; color: var(--text2);
  background: var(--surface2); border: 1px solid var(--border); border-radius: 999px;
  padding: 1px 7px; flex-shrink: 0;
}
.rp-path {
  font-size: 12px; color: var(--text2); font-family: var(--mono);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.rp-arrow { color: var(--text3); font-size: 18px; flex-shrink: 0; }

.rp-empty {
  display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: 6px; padding: 28px 16px; text-align: center;
  border: 1px solid var(--border); border-radius: 8px;
}
.rp-empty-icon { font-size: 32px; }
.rp-empty-title { font-size: 14px; font-weight: 600; color: var(--text); }
.rp-empty-sub { font-size: 12px; color: var(--text2); }
.rp-error { color: var(--red); font-size: 12px; margin-top: 10px; }
</style>
