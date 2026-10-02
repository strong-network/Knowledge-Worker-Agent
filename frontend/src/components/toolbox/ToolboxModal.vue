<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// The Toolbox — one surface for everything the agent can use in this
// chat. Replaces the skills popover and the connectors popover in the composer.
//
// Hosts ConnectorSignInSheet in a scrim, reused unchanged from the connectors modal. It renders
// inside this modal rather than as its own ui.activeModal value, because
// navigating away unmounts the list, and the sign-in is exactly the
// moment the user most needs to see the card they are fixing.
import { computed, onMounted, ref } from 'vue'
import { useUiStore } from '../../stores/ui'
import { useConnectorsStore } from '../../stores/connectors'
import { useSessionsStore } from '../../stores/sessions'
import { useToolboxStore, type ToolboxItem } from '../../stores/toolbox'
import type { McpServer } from '../../api'
import ConnectorSignInSheet from '../connectors/ConnectorSignInSheet.vue'
import ToolboxRail from './ToolboxRail.vue'
import ToolboxExplainer from './ToolboxExplainer.vue'
import ToolboxSkillsNotice from './ToolboxSkillsNotice.vue'
import ToolboxCard from './ToolboxCard.vue'
import ToolboxEmpty from './ToolboxEmpty.vue'

const ui = useUiStore()
const store = useToolboxStore()
const connectors = useConnectorsStore()
const sessionsStore = useSessionsStore()

const searchEl = ref<HTMLInputElement | null>(null)

onMounted(() => {
  // Not awaited together, and that is the point: the connector half is a local
  // config read, the skills half can block on an `opencode serve` start. One
  // combined promise would hold the connector cards hostage to that worst case.
  void store.loadSkills()
  void store.loadConnectors()
  searchEl.value?.focus()
})

// The store outlives this component (ModalHost mounts with v-if), so a search
// typed last time would still be applied. `type` and `activeOnly` deliberately
// survive — they are a browsing stance, visible in the rail and one click to
// clear — but a stale query can hide the whole library behind "Nothing matches".
//
// Consumes whatever the opener asked for rather than blanking the field: an
// opener that wants a query (the `/skill <query>` command) sets it through
// `prepareForOpen`, and one that does not gets the empty default — so the reset
// does not depend on every caller remembering it.
store.applyPendingOpen()

const explainerType = computed<'skill' | 'connector' | null>(() =>
  store.type === 'skill' ? 'skill' : store.type === 'connector' ? 'connector' : null)

// Two clauses, each dropping out when empty. "{n} active for this chat" is
// the spec's line and it is wrong for the skill half: a connector stays on, a
// skill applies once. Saying so here is one of the three places the mixed
// lifetime is carried.
const summary = computed(() => {
  const on = store.connectorsOn
  const skill = store.selectedSkill
  const parts: string[] = []
  if (on) parts.push(`${on} connector${on === 1 ? '' : 's'} on for this chat`)
  if (skill) parts.push(`${skill.name} armed for your next message`)
  if (!parts.length) return 'Nothing selected for this chat'
  return parts.join(' · ')
})

function openManage() {
  // The full catalogue: add, remove, globally enable, sign out. The Toolbox
  // picks tools for a chat; that modal manages them.
  ui.openModal('mcp-servers')
}

// In a project chat the connector selection is project-level and shared by
// every chat in it, so the cards' switches are inert. The card
// shows that by disabling them, which says *that* they cannot be changed but
// not why or where — so this carries the reason and the route, as the composer
// popover this modal replaces used to.
function openProjectSettings() {
  const pid = sessionsStore.currentProjectId
  if (!pid) return
  ui.activeProjectId = pid
  ui.openModal('project-settings')
}

function onToggle(item: ToolboxItem) {
  if (item.kind === 'skill') {
    // Exclusive and self-clearing: picking another swaps, picking the armed one
    // clears. Selecting does not close the modal — the user may still want to
    // flip a connector; Done closes.
    store.selectSkill(item.selected ? null : item.id)
    return
  }
  void store.toggleConnector(item)
}

const signingIn = ref<McpServer | null>(null)

function serverFor(item: ToolboxItem): McpServer | undefined {
  return connectors.servers.find(s => s.name === item.id)
}

function openSignIn(item: ToolboxItem) {
  const s = serverFor(item)
  if (s) signingIn.value = s
}

function closeSignIn(signedIn: boolean) {
  signingIn.value = null
  // Without this the card keeps its amber band after a successful sign-in,
  // because the cached status map predates the token — and the flow reads as
  // broken when it actually worked.
  if (signedIn) store.refreshConnectorStatuses()
}
</script>

<template>
  <div class="modal tb-modal" @click.stop>
    <header class="tb-head">
      <div class="tb-head-text">
        <h2>Toolbox</h2>
        <p>Turn on what this chat should be able to use. Skills last one message; connectors stay on.</p>
      </div>
      <button type="button" class="tb-close" aria-label="Close" @click="ui.closeModal()">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9"
             stroke-linecap="round" aria-hidden="true">
          <path d="M6 6l12 12M18 6L6 18" />
        </svg>
      </button>
    </header>

    <div class="tb-body">
      <ToolboxRail />

      <div class="tb-pane">
        <label class="tb-search">
          <span class="sr-only">Search the toolbox</span>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9"
               stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <circle cx="11" cy="11" r="7" /><path d="m20 20-3.5-3.5" />
          </svg>
          <input
            ref="searchEl"
            v-model="store.query"
            type="search"
            placeholder="Search by name or what it does…"
          />
        </label>
        <ToolboxExplainer v-if="explainerType" :type="explainerType" @manage="openManage" />
        <ToolboxSkillsNotice />

        <p v-if="store.readOnlyConnectors && store.type !== 'skill'" class="tb-ro-note">
          Connectors are shared by every chat in this project.
          <button type="button" class="tb-ro-link" @click="openProjectSettings">Manage in project settings</button>
        </p>

        <div class="tb-grid">
          <ToolboxCard
            v-for="item in store.visible"
            :key="`${item.kind}:${item.id}`"
            :item="item"
            :busy="store.busy[item.id]"
            @toggle="onToggle"
            @signin="openSignIn"
            @setup="openManage"
            @manage="openManage"
          />
          <ToolboxEmpty v-if="store.emptyReason" @manage="openManage" />
        </div>
      </div>
    </div>

    <footer class="tb-foot">
      <span class="tb-summary">{{ summary }}</span>
      <button type="button" class="tb-done" @click="ui.closeModal()">Done</button>
    </footer>

    <!-- No click-outside-to-close: an OAuth flow is running behind this sheet,
         and a stray click should not abandon it halfway. -->
    <div v-if="signingIn" class="tb-sheet-scrim">
      <ConnectorSignInSheet :server="signingIn" @close="closeSignIn" />
    </div>
  </div>
</template>

<style scoped>
/* The shared .modal rule pads and scrolls its whole body; this one scrolls its
   two panes independently so the header, rail and footer stay put. */
.tb-modal {
  width: min(1120px, calc(100vw - 48px));
  max-width: min(1120px, calc(100vw - 48px));
  height: min(760px, 90vh);
  max-height: 90vh;
  padding: 0;
  overflow: hidden;
  display: flex; flex-direction: column;
  position: relative;
}

.tb-head {
  display: flex; align-items: flex-start; gap: 16px;
  padding: 20px 20px 16px;
  border-bottom: 1px solid var(--border);
}
.tb-head-text { flex: 1; min-width: 0; }
.tb-head h2 { margin: 0 0 4px; font-size: 19px; font-weight: 700; color: var(--text); }
.tb-head p { margin: 0; font-size: 12px; color: var(--text2); }

.tb-search { position: relative; margin: 14px 16px 10px; display: flex; align-items: center; }
.tb-search svg {
  position: absolute; left: 11px; width: 15px; height: 15px;
  color: var(--text3); pointer-events: none;
}
.tb-search input {
  width: 100%; box-sizing: border-box;
  padding: 8px 11px 8px 32px;
  background: var(--surface2); border: 1px solid var(--border); border-radius: 8px;
  color: var(--text); font-family: inherit; font-size: 13px; outline: none;
}
.tb-search input::placeholder { color: var(--text3); }
.tb-search input:focus { border-color: var(--accent); outline: none; }/* The UA's own clear affordance sits where our layout does not expect it. */
.tb-search input::-webkit-search-cancel-button { -webkit-appearance: none; }

.tb-close {
  width: 34px; height: 34px; flex-shrink: 0;
  display: flex; align-items: center; justify-content: center;
  background: none; border: 1px solid transparent; border-radius: 9px;
  color: var(--text2); cursor: pointer;
}
.tb-close:hover { background: var(--hover); color: var(--text); }
.tb-close svg { width: 17px; height: 17px; }

.tb-body { flex: 1; min-height: 0; display: flex; }
.tb-pane { flex: 1; min-width: 0; overflow-y: auto; }

.tb-ro-note {
  margin: 14px 16px 0; padding: 9px 12px; border-radius: 11px;
  background: var(--surface2); border: 1px solid var(--border);
  font-size: 12px; line-height: 1.45; color: var(--text2);
}
.tb-ro-link {
  background: none; border: none; padding: 0; margin-left: 4px;
  font: inherit; color: var(--accent); cursor: pointer; text-decoration: underline;
}
.tb-ro-link:hover { color: var(--accent-h); }

/* auto-fill rather than auto-fit: this is one column at every width the modal
   reaches, and auto-fit would stretch a lone card across the full width.
   The min track is wider than the widest pane (1120 - rail - padding) on purpose,
   so widening the modal to match the Connectors shell did not quietly buy a
   second column — D7 wants that only on genuinely wide viewports, and a second
   column of variable-height cards reads as ragged next to the Connectors list. */
.tb-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(520px, 1fr));
  gap: 8px;
  align-items: start;
  /* Matching the row gap at the top too: the explainer and the notices above the
     grid carry no bottom margin, so with no padding here they touch the first
     card. */
  padding: 8px 16px 16px;
}

.tb-foot {
  display: flex; align-items: center; justify-content: space-between; gap: 16px;
  padding: 12px 20px;
  border-top: 1px solid var(--border);
  background: var(--surface2);
}
.tb-summary { font-size: 12px; color: var(--text2); min-width: 0; }
.tb-done {
  flex-shrink: 0;
  background: var(--surface2); border: 1px solid var(--border); color: var(--text2);
  padding: 7px 16px; border-radius: 7px; cursor: pointer;
  font-family: inherit; font-size: 13px;
}
.tb-done:hover { color: var(--text); background: var(--surface3); }

.tb-sheet-scrim {
  position: absolute; inset: 0; z-index: 2;
  background: rgba(0, 0, 0, .45);
  display: flex; align-items: center; justify-content: center;
  padding: 24px;
}

@media (max-width: 600px) {
  .tb-modal { width: calc(100vw - 24px); max-width: calc(100vw - 24px); height: 92vh; }
  /* The rail has collapsed to a strip above the pane at this width. */
  .tb-body { flex-direction: column; }
  .tb-grid { grid-template-columns: 1fr; padding: 8px 12px 12px; }
  .tb-search { margin: 12px 12px 8px; }
}
</style>
