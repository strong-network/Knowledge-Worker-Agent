<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// One connector row. A vertical container: the grid row, then at
// most one contextual bar beneath it. The bar is the point of the redesign —
// turning on a connector that still needs credentials says so in place, rather
// than leaving it silently half-connected.
import { computed, ref } from 'vue'
import { useUiStore } from '../../stores/ui'
import { useConnectorsStore, type DecoratedConnector } from '../../stores/connectors'
import type { McpServer } from '../../api'
import {
  describeServer, displayName, helpText, isGithub, isOwned, isProvisioned,
  monogram, needsKey, oauthCapable, statusLabel,
} from '../../utils/connectorStatus'

const props = defineProps<{ entry: DecoratedConnector }>()
const emit = defineEmits<{
  (e: 'setup', server: McpServer): void
  (e: 'edit', server: McpServer): void
  (e: 'signin', server: McpServer): void
  (e: 'key', server: McpServer): void
}>()

const ui = useUiStore()
const store = useConnectorsStore()

const s = computed(() => props.entry.server)
const status = computed(() => props.entry.status)
const name = computed(() => s.value.name)
const title = computed(() => displayName(s.value))

// A blocked connector explains itself when you try to turn it on, not before.
// It sits in the OFF group with everything else the user hasn't asked for, and
// an amber bar on every visit would be noise.
const revealed = ref(false)

const bar = computed<'error' | 'signin' | 'key' | 'setup' | null>(() => {
  if (store.rowError[name.value]) return 'error'
  if (status.value === 'needs-signin' && !store.dismissed[name.value]) return 'signin'
  if (status.value === 'needs-key') return 'key'
  if (status.value === 'needs-setup') return 'setup'
  if (status.value === 'blocked' && revealed.value) return 'setup'
  return null
})

const setupCopy = computed(() => {
  if (status.value === 'blocked') {
    return 'GitHub needs a personal access token before it can connect. Add one in Accounts, then switch it on.'
  }
  return helpText(s.value) || `${title.value} needs some setup before it can connect.`
})

const canSignOut = computed(() =>
  oauthCapable(s.value) && store.authed[name.value.trim().toLowerCase()] === true)
const hasKey = computed(() => needsKey(s.value) && s.value.key_saved === true)
const canRemove = computed(() => !isProvisioned(s.value) && !isOwned(s.value))

// GitHub's credential lives in Accounts, so that row points there instead.
// A provisioned connector gets no button at all: the backend refuses to
// rewrite it (409), and the platform config dir is recreated every boot, so
// an edit would silently undo itself. Its help text is the whole answer.
const canEdit = computed(() => !isGithub(s.value) && !isProvisioned(s.value) && !isOwned(s.value))

const busy = computed(() => store.busyEnable === name.value)

function onToggleClick(e: MouseEvent) {
  // aria-disabled rather than disabled: the control stays focusable and
  // clickable so the answer ("add a token") is reachable, instead of being a
  // dead switch with a tooltip.
  if (status.value === 'blocked') {
    e.preventDefault()
    revealed.value = true
    return
  }
  if (busy.value) e.preventDefault()
}

function onToggleChange() {
  if (status.value === 'blocked' || busy.value) return
  store.toggle(s.value)
}

function signIn() {
  // The sheet renders inside the connectors modal. It used to
  // be its own top-level modal, which unmounted this list for the duration of
  // the sign-in — and the sign-in is exactly the moment the user most needs to
  // see the row they are fixing.
  emit('signin', s.value)
}

function openAccounts() {
  // Accounts is where the GitHub personal access token is entered — the only
  // credential that makes that server usable.
  ui.openModal('accounts')
}
</script>

<template>
  <div class="conn-row" :class="[`st-${status}`, { 'has-bar': !!bar }]">
    <div class="conn-grid">
      <div class="conn-mono" aria-hidden="true">{{ monogram(s) }}</div>

      <div class="conn-text">
        <div class="conn-title">
          <span class="conn-name">{{ title }}</span>
          <span
            v-if="isProvisioned(s)"
            class="conn-tag"
            title="Provided by your organization. You choose whether it's on; its address and settings are managed centrally and can't be edited here."
          >Provided by IT</span>
        </div>
        <div class="conn-desc" :title="describeServer(s)">{{ describeServer(s) }}</div>
      </div>

      <div class="conn-actions">
        <button
          v-if="canSignOut"
          class="conn-link"
          :disabled="store.busyAuth === name"
          @click="store.signOut(name)"
        >{{ store.busyAuth === name ? '…' : 'Sign out' }}</button>
        <template v-if="hasKey">
          <button class="conn-link" @click="emit('key', s)">Replace key</button>
          <button
            class="conn-link danger"
            :disabled="store.busyAuth === name"
            @click="store.removeKey(s)"
          >{{ store.busyAuth === name ? '…' : 'Remove key' }}</button>
        </template>
        <button
          v-if="canEdit"
          class="conn-link"
          :aria-label="`Edit ${title}`"
          @click="emit('edit', s)"
        >Edit</button>
        <button v-if="canRemove" class="conn-link danger" @click="store.remove(name)">Remove</button>

        <span class="conn-pill" :class="`p-${status}`">
          <i class="dot" aria-hidden="true"></i>{{ statusLabel(status) }}
        </span>

        <label class="conn-switch">
          <input
            type="checkbox"
            role="switch"
            :checked="s.enabled === true"
            :aria-checked="s.enabled === true"
            :aria-disabled="status === 'blocked' || busy"
            :aria-label="s.enabled ? `Turn off ${title}` : `Turn on ${title}`"
            @click="onToggleClick"
            @change="onToggleChange"
          />
          <span class="conn-track" aria-hidden="true"></span>
        </label>
      </div>
    </div>

    <div v-if="bar === 'error'" class="conn-bar err">
      <span class="conn-bar-text">{{ store.rowError[name] }}</span>
      <button class="conn-btn-ghost" @click="store.setRowError(name, '')">Dismiss</button>
    </div>

    <div v-else-if="bar === 'signin'" class="conn-bar warn">
      <span class="conn-bar-text">
        One more step — sign in with your {{ title }} account so the assistant can use it.
      </span>
      <button class="conn-btn" @click="signIn">Sign in</button>
      <button class="conn-btn-ghost" @click="store.dismissed[name] = true">Later</button>
    </div>

    <div v-else-if="bar === 'key'" class="conn-bar warn">
      <span class="conn-bar-text">
        <template v-if="s.key_saved">
          {{ title }} didn't connect with the key you saved. Check that it's the whole key, or replace it.
        </template>
        <template v-else>{{ title }} needs your personal API key before it can connect.</template>
      </span>
      <button class="conn-btn" @click="emit('key', s)">{{ s.key_saved ? 'Replace key' : 'Add key' }}</button>
    </div>

    <div v-else-if="bar === 'setup'" class="conn-bar warn">
      <span class="conn-bar-text">
        {{ setupCopy }}
        <a
          v-if="s.helpUrl"
          :href="s.helpUrl"
          target="_blank"
          rel="noopener noreferrer"
        >How to set this up ↗</a>
      </span>
      <button v-if="isGithub(s)" class="conn-btn" @click="openAccounts">
        {{ store.githubReady ? 'Manage token' : 'Add GitHub token' }}
      </button>
      <button v-else-if="canEdit" class="conn-btn" @click="emit('setup', s)">Set up</button>
    </div>
  </div>
</template>

<style scoped>
.conn-row {
  border: 1px solid var(--border);
  border-left: 3px solid transparent;
  border-radius: 10px;
  background: var(--surface2);
  overflow: hidden;
}
.conn-row.st-on { border-left-color: var(--green); }
.conn-row.st-needs-signin,
.conn-row.st-needs-key,
.conn-row.st-needs-setup { border-left-color: var(--orange); }
.conn-row.st-needs-signin,
.conn-row.st-needs-key,
.conn-row.st-needs-setup { background: color-mix(in srgb, var(--orange) 7%, var(--surface2)); }

.conn-grid {
  display: grid;
  grid-template-columns: 34px minmax(0, 1fr) auto;
  align-items: center;
  gap: 12px;
  padding: 10px 12px;
}

.conn-mono {
  width: 34px; height: 34px; border-radius: 8px;
  display: flex; align-items: center; justify-content: center;
  font-weight: 700; font-size: 14px;
  color: var(--accent);
  background: color-mix(in srgb, var(--accent) 12%, transparent);
}
.conn-row.st-needs-signin .conn-mono,
.conn-row.st-needs-key .conn-mono,
.conn-row.st-needs-setup .conn-mono {
  color: var(--orange);
  background: color-mix(in srgb, var(--orange) 14%, transparent);
}

.conn-text { min-width: 0; }
.conn-title { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.conn-name { font-size: 13px; font-weight: 600; color: var(--text); }
.conn-desc {
  font-size: 11px; color: var(--text2); font-family: var(--mono);
  margin-top: 2px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.conn-row.st-off .conn-name,
.conn-row.st-off .conn-desc,
.conn-row.st-blocked .conn-name,
.conn-row.st-blocked .conn-desc { opacity: .65; }

/* Provenance, not a warning — there is nothing wrong with the row. */
.conn-tag {
  font-size: 10px; font-weight: 600; letter-spacing: .02em; white-space: nowrap;
  padding: 2px 7px; border-radius: 999px; cursor: help;
  color: var(--text2); background: var(--surface3);
  border: 1px solid var(--border);
}

.conn-actions { display: flex; align-items: center; gap: 8px; }

.conn-link {
  background: none; border: none; padding: 2px 4px; cursor: pointer;
  font-size: 11px; color: var(--text3); text-decoration: underline;
  text-underline-offset: 2px;
}
.conn-link:hover { color: var(--text); }
.conn-link.danger:hover { color: var(--red); }
.conn-link:disabled { opacity: .5; cursor: default; }

.conn-pill {
  display: inline-flex; align-items: center; gap: 6px;
  font-size: 11px; font-weight: 600; white-space: nowrap;
  padding: 3px 10px; border-radius: 999px;
  border: 1px solid transparent;
}
.conn-pill .dot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
.p-on {
  color: var(--green);
  background: color-mix(in srgb, var(--green) 14%, transparent);
  border-color: color-mix(in srgb, var(--green) 35%, transparent);
}
.p-needs-signin, .p-needs-key, .p-needs-setup {
  color: var(--orange);
  background: color-mix(in srgb, var(--orange) 14%, transparent);
  border-color: color-mix(in srgb, var(--orange) 35%, transparent);
}
.p-off, .p-blocked {
  color: var(--text2);
  background: var(--surface3);
  border-color: var(--border);
}

.conn-switch { position: relative; display: inline-flex; align-items: center; cursor: pointer; flex-shrink: 0; }
.conn-switch input { position: absolute; opacity: 0; width: 0; height: 0; }
.conn-track {
  position: relative; display: block; width: 34px; height: 20px; border-radius: 999px;
  background: var(--surface3); border: 1px solid var(--border);
  transition: background .12s, border-color .12s;
}
.conn-track::after {
  content: ''; position: absolute; top: 2px; left: 2px; width: 14px; height: 14px;
  border-radius: 50%; background: var(--text3); transition: transform .12s, background .12s;
}
.conn-switch input:checked + .conn-track { background: var(--green); border-color: var(--green); }
.conn-switch input:checked + .conn-track::after { transform: translateX(14px); background: #fff; }
.conn-switch input[aria-disabled="true"] + .conn-track { opacity: .55; }
.conn-switch input:focus-visible + .conn-track { outline: 2px solid var(--accent); outline-offset: 2px; }

.conn-bar {
  display: flex; align-items: center; gap: 10px; flex-wrap: wrap;
  padding: 9px 12px 10px 58px;
  border-top: 1px solid var(--border);
  font-size: 12px; line-height: 1.45;
}
.conn-bar.warn { background: color-mix(in srgb, var(--orange) 10%, transparent); color: var(--text); }
.conn-bar.err { background: color-mix(in srgb, var(--red) 10%, transparent); color: var(--text); }
.conn-bar-text { flex: 1; min-width: 200px; word-break: break-word; }
.conn-bar a { color: var(--accent); text-decoration: none; white-space: nowrap; margin-left: 6px; }
.conn-bar a:hover { text-decoration: underline; }

.conn-btn {
  background: var(--accent); color: #fff; border: 1px solid transparent;
  padding: 5px 14px; border-radius: 6px; cursor: pointer; font-size: 12px; font-weight: 600;
}
.conn-btn:hover { background: var(--accent-h); }
.conn-btn-ghost {
  background: none; border: 1px solid var(--border); color: var(--text2);
  padding: 5px 12px; border-radius: 6px; cursor: pointer; font-size: 12px;
}
.conn-btn-ghost:hover { color: var(--text); background: var(--surface3); }

@media (max-width: 760px) {
  .conn-grid { grid-template-columns: 34px minmax(0, 1fr); }
  .conn-actions { grid-column: 1 / -1; justify-content: flex-end; }
  .conn-bar { padding-left: 12px; }
}
</style>
