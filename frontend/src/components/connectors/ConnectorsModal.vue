<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// Connectors — replaces McpServersModal. Two-pane shell: a filter rail
// with live counts, and one list grouped by status. There is no catalog pane:
// mcp.EnsureDefaults already registers every catalogue entry at startup
// (switched off), and in a centrally-managed workspace the set comes from the
// central configuration repository — so "the list" and "the catalog" are the same rows.
import { onMounted, computed, ref } from 'vue'
import { useUiStore } from '../../stores/ui'
import { useConnectorsStore } from '../../stores/connectors'
import type { McpServer } from '../../api'
import ConnectorRail from './ConnectorRail.vue'
import ConnectorList from './ConnectorList.vue'
import AddCustomServerSheet from './AddCustomServerSheet.vue'
import ConnectorSignInSheet from './ConnectorSignInSheet.vue'
import ConnectorKeySheet from './ConnectorKeySheet.vue'

const ui = useUiStore()
const store = useConnectorsStore()

onMounted(store.ensureLoaded)

const summary = computed(() => {
  const { all, on, attention } = store.counts
  const noun = all === 1 ? 'connector' : 'connectors'
  const head = `${on} of ${all} ${noun} on`
  if (!attention) return head
  return `${head} · ${attention} need${attention === 1 ? 's' : ''} attention`
})

// The sheet renders inside this modal rather than as its own ui.activeModal
// value: navigating away would unmount the list and lose filter and scroll.
// `editing` is what distinguishes adding from editing, and `repairing` tells
// the sheet the edit came from a `setup` connector's "Set up" button.
const sheetOpen = ref(false)
const editing = ref<McpServer | null>(null)
const repairing = ref(false)

function openAdd() {
  editing.value = null
  repairing.value = false
  sheetOpen.value = true
}

function openEdit(server: McpServer, repair = false) {
  editing.value = server
  repairing.value = repair
  sheetOpen.value = true
}

function closeSheet() {
  sheetOpen.value = false
  editing.value = null
}

// Sign-in is a second sheet over the same list. It used to be a
// separate top-level modal, so signing in unmounted the list and dropped the
// user back at square one afterwards.
const signingIn = ref<McpServer | null>(null)

function openSignIn(server: McpServer) {
  signingIn.value = server
}

async function closeSignIn(signedIn: boolean) {
  signingIn.value = null
  // The cached status map predates the token, so the row would keep claiming a
  // sign-in is needed for a connector that just finished signing in.
  if (signedIn) await store.loadStatuses({ fresh: true })
}

// Connector API keys: pasting a personal API key, a third sheet over the same list.
const keying = ref<McpServer | null>(null)
</script>

<template>
  <!-- role/aria-modal to match the two sheets inside it, which have carried
       them since phases 3 and 4. Focus trapping and Escape now come from
       ModalHost, which installs them once for every modal it hosts; the sheets
       take the keyboard back while they are open. -->
  <div
    class="modal conn-modal"
    role="dialog"
    aria-modal="true"
    aria-labelledby="conn-modal-title"
    @click.stop
  >
    <header class="conn-head">
      <div class="conn-head-text">
        <h2 id="conn-modal-title">Connectors</h2>
        <p>Turn on a connector to let the assistant use it. You can turn it off again any time.</p>
      </div>
      <div class="conn-head-actions">
        <button class="conn-btn-ghost" :disabled="store.loading" @click="store.load()">
          {{ store.loading ? 'Refreshing…' : 'Refresh' }}
        </button>
        <button class="conn-btn" @click="openAdd">Add connector</button>
      </div>
    </header>

    <div class="conn-body">
      <ConnectorRail @add="openAdd" />
      <ConnectorList
        @setup="openEdit($event, true)"
        @edit="openEdit($event)"
        @signin="openSignIn"
        @key="keying = $event"
      />
    </div>

    <!-- Toggling a connector settles its status a moment later, in a row the
         user may not be looking at and a screen reader is not told about. -->
    <p class="sr-only" role="status" aria-live="polite">{{ store.announcement }}</p>

    <footer class="conn-foot">
      <span class="conn-summary">{{ summary }}</span>
      <button class="conn-btn-ghost" @click="ui.closeModal()">Done</button>
    </footer>

    <div v-if="sheetOpen" class="conn-sheet-scrim" @click.self="closeSheet">
      <AddCustomServerSheet :editing="editing" :repair="repairing" @close="closeSheet" />
    </div>

    <!-- No click-outside-to-close: an OAuth flow is running behind this sheet,
         and a stray click should not abandon it halfway. -->
    <div v-if="signingIn" class="conn-sheet-scrim">
      <ConnectorSignInSheet :server="signingIn" @close="closeSignIn" />
    </div>

    <div v-if="keying" class="conn-sheet-scrim" @click.self="keying = null">
      <ConnectorKeySheet :server="keying" @close="keying = null" />
    </div>
  </div>
</template>

<style scoped>
/* The shared .modal rule pads and scrolls its whole body; this one scrolls its
   two panes independently instead, so the header, rail and footer stay put. */
.conn-modal {
  width: min(1120px, calc(100vw - 48px));
  max-width: min(1120px, calc(100vw - 48px));
  height: min(760px, 90vh);
  max-height: 90vh;
  padding: 0;
  overflow: hidden;
  display: flex;
  flex-direction: column;
  position: relative;
}

.conn-head {
  display: flex; align-items: flex-start; gap: 16px;
  padding: 20px 20px 16px;
  border-bottom: 1px solid var(--border);
}
.conn-head-text { flex: 1; min-width: 0; }
.conn-head h2 { font-size: 19px; font-weight: 700; margin: 0 0 4px; }
.conn-head p { font-size: 12px; color: var(--text2); margin: 0; }
.conn-head-actions { display: flex; gap: 8px; flex-shrink: 0; }

.conn-body { flex: 1; min-height: 0; display: flex; }

.conn-foot {
  display: flex; align-items: center; justify-content: space-between; gap: 16px;
  padding: 12px 20px;
  border-top: 1px solid var(--border);
  background: var(--surface2);
}
.conn-summary { font-size: 12px; color: var(--text2); }

.conn-btn {
  background: var(--accent); color: #fff; border: 1px solid transparent;
  padding: 7px 16px; border-radius: 7px; cursor: pointer; font-size: 13px; font-weight: 600;
}
.conn-btn:hover { background: var(--accent-h); }
.conn-btn:disabled { opacity: .6; cursor: default; }
.conn-btn-ghost {
  background: var(--surface2); border: 1px solid var(--border); color: var(--text2);
  padding: 7px 16px; border-radius: 7px; cursor: pointer; font-size: 13px;
}
.conn-btn-ghost:hover { color: var(--text); background: var(--surface3); }
.conn-btn-ghost:disabled { opacity: .6; cursor: default; }

/* Sheets render inside this modal rather than as separate ui.activeModal
   values: navigating away would unmount the list and lose filter and scroll. */
.conn-sheet-scrim {
  position: absolute; inset: 0; z-index: 2;
  background: rgba(0, 0, 0, .45);
  display: flex; align-items: center; justify-content: center;
  padding: 24px;
}

@media (max-width: 760px) {
  .conn-modal { width: calc(100vw - 24px); max-width: calc(100vw - 24px); height: 92vh; }
  .conn-head { flex-direction: column; }
  .conn-sheet-scrim { padding: 12px; }
}

/* The rail collapses to a strip above the list at this width (ConnectorRail
   swaps its markup at the same breakpoint), so the body stacks. */
@media (max-width: 600px) {
  .conn-body { flex-direction: column; }
}
</style>
