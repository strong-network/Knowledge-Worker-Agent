<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { onMounted, ref, computed } from 'vue'
import { useUiStore } from '../../stores/ui'
import { useProvidersStore } from '../../stores/providers'
import {
  fetchGitHubPatStatus,
  setGitHubPat,
  clearGitHubPat,
  setProviderKey,
  removeProviderKey,
  signOutProvider,
  probeProvider,
  type GitHubPatStatus,
  type ModelProvider,
} from '../../api'

const ui = useUiStore()
const providers = useProvidersStore()

// Which built-in provider opens which bespoke login modal. Only the built-ins
// have one; a config-declared provider is authenticated with a key entered on
// its own row, so it needs no entry here.
const BUILTIN_LOGIN_MODALS: Record<string, string> = {
  'github-copilot': 'opencode-login',
  'google-vertex': 'vertex-login',
}

// Per-provider icon. Config-declared providers get a neutral glyph rather than
// a per-vendor one — the whole point is that adding a provider needs no source
// change, so it cannot require a new icon to look right.
const BUILTIN_ICONS: Record<string, { glyph: string; cls: string }> = {
  'github-copilot': { glyph: '⌥', cls: 'opencode' },
  'google-vertex': { glyph: '☁', cls: 'vertex' },
}

function iconFor(p: ModelProvider) {
  return BUILTIN_ICONS[p.id] || { glyph: '◈', cls: 'generic' }
}

// A built-in without a known login modal would render a Connect button that
// does nothing, so treat "has a flow" as the condition rather than the flag.
function hasLoginFlow(p: ModelProvider): boolean {
  return p.builtin && !!BUILTIN_LOGIN_MODALS[p.id]
}

function openLogin(p: ModelProvider) {
  const modal = BUILTIN_LOGIN_MODALS[p.id]
  if (modal) ui.openModal(modal)
}

// Status line for a provider row. A managed provider whose secret has not been
// provisioned must NOT read as "not connected" — there is nothing the user can
// do about it, and today's copy would send them looking for a sign-in button
// that does not exist.
function statusFor(p: ModelProvider): { dot: string; text: string } {
  // Being signed in is not the same as being usable: an org network policy can
  // refuse a provider whose credentials are perfectly valid. Reporting that as
  // "Connected" is what let a user pick a model that could only fail on submit.
  if (p.authenticated && p.reachability === 'unreachable') {
    return { dot: 'err', text: 'Signed in, but unreachable' }
  }
  if (p.authenticated) return { dot: 'ok', text: 'Connected' }
  if (p.authType === 'managed') {
    return { dot: 'pending', text: 'Waiting for your administrator' }
  }
  if (p.authType === 'user-key') return { dot: 'off', text: 'No key saved' }
  return { dot: 'off', text: 'Not connected' }
}

// ── Reachability ──
const probing = ref('')
const signingOut = ref('')
const actionError = ref('')

async function retest(p: ModelProvider) {
  if (probing.value) return
  probing.value = p.id
  actionError.value = ''
  try {
    const res = await probeProvider(p.id)
    if (res.error) actionError.value = res.error
    await providers.refresh()
  } catch (e: any) {
    actionError.value = e?.message || String(e)
  } finally {
    probing.value = ''
  }
}

const confirmingSignOut = ref('')

async function doSignOut(p: ModelProvider) {
  if (signingOut.value) return
  signingOut.value = p.id
  actionError.value = ''
  try {
    const res = await signOutProvider(p.id)
    if (res.error) {
      actionError.value = res.error
      return
    }
    confirmingSignOut.value = ''
    await providers.refresh()
  } catch (e: any) {
    actionError.value = e?.message || String(e)
  } finally {
    signingOut.value = ''
  }
}

// ── Per-provider API keys (user-key providers) ──
//
// One row at a time is editable; the id of that row is held here so the key
// field is never rendered for two providers at once (which would make it
// ambiguous which one a typed key belongs to).
const keyEditing = ref('')
const keyValue = ref('')
const keyBusy = ref(false)
const keyError = ref('')

function startKeyEdit(p: ModelProvider) {
  keyEditing.value = p.id
  keyValue.value = ''
  keyError.value = ''
}

function cancelKeyEdit() {
  keyEditing.value = ''
  keyValue.value = ''
  keyError.value = ''
}

async function saveKey(p: ModelProvider) {
  const k = keyValue.value.trim()
  if (!k || keyBusy.value) return
  keyBusy.value = true
  keyError.value = ''
  try {
    const res = await setProviderKey(p.id, k)
    if (res.error) {
      keyError.value = res.error
      return
    }
    cancelKeyEdit()
    await providers.refresh()
  } catch (e: any) {
    keyError.value = e?.message || String(e)
  } finally {
    keyBusy.value = false
  }
}

async function removeKey(p: ModelProvider) {
  if (keyBusy.value) return
  keyBusy.value = true
  keyError.value = ''
  try {
    const res = await removeProviderKey(p.id)
    if (res.error) {
      keyError.value = res.error
      return
    }
    await providers.refresh()
  } catch (e: any) {
    keyError.value = e?.message || String(e)
  } finally {
    keyBusy.value = false
  }
}

// GitHub MCP token state.
const gh = ref<GitHubPatStatus | null>(null)
const ghLoading = ref(false)
const ghEditing = ref(false)
const ghToken = ref('')
const ghBusy = ref(false)
const ghError = ref('')

const ghConnectedForPRs = computed(() => !!gh.value && gh.value.source !== 'none' && gh.value.can_read_prs)
const ghLimited = computed(() => !!gh.value && gh.value.source === 'login')

async function refreshGitHub() {
  ghLoading.value = true
  try {
    gh.value = await fetchGitHubPatStatus()
  } catch (e: any) {
    ghError.value = e?.message || String(e)
  } finally {
    ghLoading.value = false
  }
}

// Refresh auth state whenever this modal is opened so the user sees the
// latest connection state (instead of whatever was cached from the last poll).
onMounted(() => {
  providers.refresh()
  refreshGitHub()
})

function startGitHubEdit() {
  ghToken.value = ''
  ghError.value = ''
  ghEditing.value = true
}

async function saveGitHubPat() {
  const t = ghToken.value.trim()
  if (!t || ghBusy.value) return
  ghBusy.value = true
  ghError.value = ''
  try {
    const res = await setGitHubPat(t)
    if (res.error) {
      ghError.value = res.error
      return
    }
    ghEditing.value = false
    ghToken.value = ''
    await refreshGitHub()
  } catch (e: any) {
    ghError.value = e?.message || String(e)
  } finally {
    ghBusy.value = false
  }
}

async function removeGitHubPat() {
  if (ghBusy.value) return
  ghBusy.value = true
  ghError.value = ''
  try {
    await clearGitHubPat()
    await refreshGitHub()
  } catch (e: any) {
    ghError.value = e?.message || String(e)
  } finally {
    ghBusy.value = false
  }
}
</script>

<template>
  <div class="modal accounts-modal" @click.stop>
    <div class="accounts-header">
      <h2>Accounts &amp; connections</h2>
      <button
        class="accounts-icon-close"
        @click="ui.closeModal()"
        aria-label="Close accounts and connections"
        title="Close"
      >
        ✕
      </button>
    </div>
    <p class="modal-subtitle">
      Sign in to power the assistant and load its models.
    </p>

    <!-- Model providers. Rendered from /api/providers so a provider added to
         the central config repo appears here with no source change. -->
    <div v-if="!providers.loaded" class="account-row">
      <div class="acc-icon generic"><span aria-hidden="true">◈</span></div>
      <div class="acc-info">
        <div class="acc-title">Model providers</div>
        <div class="acc-sub"><span class="dot pending" /> Checking…</div>
      </div>
    </div>

    <div v-else-if="providers.providers.length === 0" class="account-row">
      <div class="acc-icon generic"><span aria-hidden="true">◈</span></div>
      <div class="acc-info">
        <div class="acc-title">No model provider configured</div>
        <div class="acc-sub">
          <span class="dot off" /> This workspace has no provider assigned. Contact your administrator.
        </div>
      </div>
    </div>

    <template v-else v-for="p in providers.providers" :key="p.id">
      <div class="account-row">
        <div class="acc-icon" :class="iconFor(p).cls">
          <span aria-hidden="true">{{ iconFor(p).glyph }}</span>
        </div>
        <div class="acc-info">
          <div class="acc-title">
            {{ p.label }}
            <span v-if="p.authType === 'managed'" class="provider-pill">managed</span>
          </div>
          <div class="acc-sub">
            <span class="dot" :class="statusFor(p).dot" /> {{ statusFor(p).text }}
          </div>
          <div v-if="p.help || p.helpUrl" class="acc-help">
            {{ p.help }}
            <a
              v-if="p.helpUrl"
              class="acc-help-link"
              :href="p.helpUrl"
              target="_blank"
              rel="noopener noreferrer"
            >How to get a key</a>
          </div>
        </div>
        <div class="acc-action">
          <!-- Built-ins keep their own sign-in flows and their own routes. -->
          <template v-if="hasLoginFlow(p)">
            <button v-if="p.authenticated" class="btn-secondary" @click="openLogin(p)">
              Re-authenticate
            </button>
            <button v-else class="btn-connect" @click="openLogin(p)">
              Connect
            </button>
          </template>
          <!-- A config-declared provider the user supplies a key for. -->
          <template v-else-if="p.authType === 'user-key' && keyEditing !== p.id">
            <button class="btn-secondary" @click="startKeyEdit(p)">
              {{ p.authenticated ? 'Replace key' : 'Add key' }}
            </button>
            <button
              v-if="p.authenticated"
              class="btn-link"
              :disabled="keyBusy"
              @click="removeKey(p)"
            >
              Remove
            </button>
          </template>
          <!-- managed: deliberately no action. The user cannot fix this. -->

          <button
            v-if="p.authenticated"
            class="btn-link"
            :disabled="!!probing"
            @click="retest(p)"
          >
            {{ probing === p.id ? 'Testing…' : 'Test' }}
          </button>
          <!-- Signing out is what makes switching providers possible: while an
               unusable provider stays signed in it keeps being offered. -->
          <template v-if="p.canSignOut">
            <button
              v-if="confirmingSignOut !== p.id"
              class="btn-link"
              @click="confirmingSignOut = p.id"
            >
              Sign out
            </button>
            <template v-else>
              <button class="btn-danger" :disabled="!!signingOut" @click="doSignOut(p)">
                {{ signingOut === p.id ? 'Signing out…' : 'Confirm' }}
              </button>
              <button class="btn-link" :disabled="!!signingOut" @click="confirmingSignOut = ''">
                Cancel
              </button>
            </template>
          </template>
        </div>
      </div>

      <div v-if="p.reachability === 'unreachable' && p.reachabilityNote" class="acc-unreachable">
        {{ p.reachabilityNote }}
      </div>

      <div v-if="actionError" class="acc-error">{{ actionError }}</div>

      <div v-if="keyEditing === p.id" class="gh-form">
        <label class="gh-label" :for="`key-${p.id}`">API key for {{ p.label }}</label>
        <p class="gh-help">
          Your key is stored on this workspace only, and is used solely to reach {{ p.label }}.
        </p>
        <div class="gh-input-row">
          <input
            :id="`key-${p.id}`"
            v-model="keyValue"
            class="gh-input"
            type="password"
            autocomplete="off"
            spellcheck="false"
            placeholder="Paste your API key"
            :disabled="keyBusy"
            @keydown.enter.prevent="saveKey(p)"
          />
          <button class="btn-primary" :disabled="keyBusy || !keyValue.trim()" @click="saveKey(p)">
            {{ keyBusy ? 'Saving…' : 'Save' }}
          </button>
          <button class="btn-link" :disabled="keyBusy" @click="cancelKeyEdit">Cancel</button>
        </div>
        <div v-if="keyError" class="gh-error">{{ keyError }}</div>
      </div>
    </template>

    <!-- GitHub (MCP token for PRs / issues / repos) -->
    <div class="account-row gh-row">
      <div class="acc-icon github">
        <span aria-hidden="true">&#9670;</span>
      </div>
      <div class="acc-info">
        <div class="acc-title">GitHub (PRs &amp; repos)</div>
        <div class="acc-sub" v-if="ghLoading && !gh">
          <span class="dot pending" /> Checking…
        </div>
        <div class="acc-sub" v-else-if="ghConnectedForPRs">
          <span class="dot ok" /> Ready
          <span v-if="gh?.login" class="user">— <strong>{{ gh?.login }}</strong></span>
          <span v-if="gh?.source === 'env'" class="provider-pill">env</span>
          <span v-else-if="gh?.source === 'stored'" class="provider-pill">token</span>
        </div>
        <div class="acc-sub" v-else-if="ghLimited">
          <span class="dot off" /> Limited — login token can't read PRs
        </div>
        <div class="acc-sub" v-else>
          <span class="dot off" /> Not connected
        </div>
      </div>
      <div class="acc-action" v-if="!ghEditing">
        <button class="btn-secondary" @click="startGitHubEdit">
          {{ gh?.has_stored ? 'Update token' : 'Add token' }}
        </button>
        <button v-if="gh?.has_stored" class="btn-link" :disabled="ghBusy" @click="removeGitHubPat">
          Remove
        </button>
      </div>
    </div>

    <!-- PAT entry form -->
    <div v-if="ghEditing" class="gh-form">
      <label class="gh-label" for="gh-pat">GitHub Personal Access Token</label>
      <p class="gh-help">
        Classic token with the <code>repo</code> scope, or a fine-grained token with
        <em>Pull requests</em> and <em>Contents: read</em>. Stored locally (0600) and used only
        to authenticate the GitHub MCP server.
        <a href="https://github.com/settings/tokens" target="_blank" rel="noopener">Create one ↗</a>
      </p>
      <div class="gh-input-row">
        <input
          id="gh-pat"
          v-model="ghToken"
          class="gh-input"
          type="password"
          autocomplete="off"
          spellcheck="false"
          placeholder="ghp_… or github_pat_…"
          :disabled="ghBusy"
          @keydown.enter.prevent="saveGitHubPat"
        />
        <button class="btn-primary" :disabled="ghBusy || !ghToken.trim()" @click="saveGitHubPat">
          {{ ghBusy ? 'Validating…' : 'Save' }}
        </button>
        <button class="btn-link" :disabled="ghBusy" @click="ghEditing = false">Cancel</button>
      </div>
      <div v-if="ghError" class="gh-error">{{ ghError }}</div>
    </div>

    <p class="footnote">
      Signing in connects the assistant so it can load and use its models.
    </p>

    <div class="accounts-footer">
      <button class="accounts-close-btn" @click="ui.closeModal()">Close</button>
    </div>
  </div>
</template>

<style scoped>
/* Wider than the 560px default: a row carries an icon, a title, a status line
   and up to three controls, and at the default width the controls squeezed the
   status into wrapping mid-word. */
.accounts-modal {
  width: min(760px, 95vw);
}

.accounts-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}

.accounts-header h2 {
  margin: 0 0 6px;
}

.accounts-icon-close {
  width: 36px;
  height: 36px;
  min-width: 36px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--surface2);
  color: var(--text2);
  cursor: pointer;
  font-size: 16px;
  line-height: 1;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  transition: background .15s, border-color .15s, color .15s;
}

.accounts-icon-close:hover {
  background: var(--surface3);
  border-color: var(--text3);
  color: var(--text);
}

.account-row {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 14px 16px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--surface2);
  margin-bottom: 10px;
}
/* A row followed by a reason keeps its top corners only, so the two read as
   one card. */
.account-row:has(+ .acc-unreachable) {
  border-radius: 10px 10px 0 0;
  border-bottom-color: transparent;
}
.acc-icon {
  width: 36px;
  height: 36px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 18px;
  color: #fff;
  flex-shrink: 0;
}
.acc-icon.opencode { background: #f97316; }
.acc-icon.vertex { background: #4285f4; }
.acc-icon.github { background: #24292f; }
/* Config-declared providers share one neutral icon: requiring a per-vendor
   glyph would mean a source change per provider, which is the thing
   configurable providers exist to remove. */
.acc-icon.generic { background: var(--text3); }

.acc-help {
  margin-top: 4px;
  font-size: 12px;
  color: var(--text2);
  line-height: 1.5;
}
.acc-unreachable {
  /* Pulled up under its own row: in the gap between two cards the reason reads
     as though it belongs to the provider below it. */
  margin: -14px 0 10px;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-top: none;
  border-left: 2px solid #f59e0b;
  border-radius: 0 0 10px 10px;
  background: var(--surface2);
  font-size: 12px;
  color: var(--text2);
  line-height: 1.5;
}
.acc-error {
  margin: 6px 0 0;
  font-size: 12px;
  color: #ef4444;
}
.btn-danger {
  background: #ef4444;
  color: #fff;
  border: 1px solid #ef4444;
  border-radius: 6px;
  padding: 5px 10px;
  font-size: 12px;
  cursor: pointer;
}
.btn-danger:disabled { opacity: .6; cursor: default; }
.acc-help-link {
  color: var(--accent);
  text-decoration: none;
  white-space: nowrap;
}
.acc-help-link:hover { text-decoration: underline; }

.acc-action {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
}
/* Matches the neutral outlined shape of .btn-secondary (Re-authenticate / Add token)
   but keeps the accent color as the border + text so Connect still reads as the
   primary action. Scoped to this modal only. */
.btn-connect {
  background: var(--surface2);
  border: 1px solid var(--accent);
  color: var(--accent);
  padding: 8px 18px;
  border-radius: 7px;
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
}
.btn-connect:hover { background: var(--surface3); }
.btn-connect:disabled { opacity: .6; cursor: default; }
.btn-link {
  background: none; border: none; padding: 4px 6px;
  color: var(--text2); cursor: pointer; font-size: 12px; font-weight: 600;
}
.btn-link:hover:not(:disabled) { color: #cf222e; text-decoration: underline; }
.btn-link:disabled { opacity: .5; cursor: not-allowed; }

.gh-form {
  margin: -2px 0 10px;
  padding: 12px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--surface2);
}
.gh-label { display: block; font-size: 12px; font-weight: 600; margin-bottom: 4px; }
.gh-help { margin: 0 0 8px; font-size: 12px; color: var(--text2); line-height: 1.5; }
.gh-help code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  background: var(--surface3); padding: 1px 5px; border-radius: 4px;
}
.gh-help a { color: var(--accent); text-decoration: none; }
.gh-help a:hover { text-decoration: underline; }
.gh-input-row { display: flex; gap: 8px; align-items: center; }
.gh-input {
  flex: 1; min-width: 0;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 13px; padding: 8px 12px;
  background: var(--surface); color: var(--text);
  border: 1px solid var(--border); border-radius: 8px;
}
.gh-input:focus { outline: 2px solid var(--accent); outline-offset: 0; }
.gh-error {
  margin-top: 8px; padding: 8px 10px;
  background: rgba(207,34,46,.10); border: 1px solid rgba(207,34,46,.35);
  color: #cf222e; border-radius: 6px; font-size: 12px;
}

.acc-info { flex: 1; min-width: 0; }
.acc-title {
  font-weight: 600;
  font-size: 14px;
  color: var(--text);
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.provider-pill {
  font-weight: 400;
  font-size: 11px;
  padding: 2px 7px;
  border-radius: 999px;
  background: var(--surface3);
  color: var(--text2);
  border: 1px solid var(--border);
}
.acc-sub {  margin-top: 4px;
  font-size: 12px;
  color: var(--text2);
  display: flex;
  align-items: center;
  gap: 6px;
}
.acc-sub .user { color: var(--text2); }
.acc-sub strong { color: var(--text); font-weight: 600; }

.dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  display: inline-block;
}
.dot.ok      { background: #22c55e; box-shadow: 0 0 0 2px rgba(34,197,94,.18); }
.dot.off     { background: #ef4444; box-shadow: 0 0 0 2px rgba(239,68,68,.18); }
.dot.err     { background: #f59e0b; box-shadow: 0 0 0 2px rgba(245,158,11,.18); }
.dot.pending { background: #9ca3af; box-shadow: 0 0 0 2px rgba(156,163,175,.18); }

.footnote {
  margin: 14px 0 0;
  font-size: 12px;
  color: var(--text2);
  line-height: 1.5;
}
.footnote a { color: var(--accent); text-decoration: none; }
.footnote a:hover { text-decoration: underline; }

.accounts-footer {
  display: flex;
  justify-content: flex-end;
  margin-top: 22px;
}

.accounts-close-btn {
  min-width: 104px;
  min-height: 40px;
  padding: 10px 18px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--surface2);
  color: var(--text);
  cursor: pointer;
  font-size: 14px;
  font-weight: 600;
  transition: background .15s, border-color .15s, color .15s;
}

.accounts-close-btn:hover {
  background: var(--surface3);
  border-color: var(--text3);
}

/* Below this width a row cannot hold an icon, a title and three controls side
   by side — the title was being squeezed to nothing. Let the controls take
   their own line instead. */
@media (max-width: 560px) {
  .account-row {
    flex-wrap: wrap;
    row-gap: 10px;
  }
  .acc-info {
    /* Icon + gap, so the info still sits beside the icon on the first line. */
    flex-basis: calc(100% - 50px);
  }
  .acc-action {
    width: 100%;
    justify-content: flex-end;
    flex-wrap: wrap;
  }
}
</style>
