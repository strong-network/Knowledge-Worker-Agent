<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// The list pane: search box, status group headings, rows.
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useConnectorsStore } from '../../stores/connectors'
import { GROUP_LABELS } from '../../utils/connectorStatus'
import ConnectorRow from './ConnectorRow.vue'
import type { McpServer } from '../../api'

const store = useConnectorsStore()
const emit = defineEmits<{
  (e: 'setup', server: McpServer): void
  (e: 'edit', server: McpServer): void
  (e: 'signin', server: McpServer): void
  (e: 'key', server: McpServer): void
}>()

const emptyReason = computed(() => {
  if (store.servers.length === 0) return 'No connectors are configured yet.'
  if (store.query.trim()) return `Nothing matches “${store.query.trim()}”.`
  return 'Nothing in this group.'
})

// Filtering and searching rewrite the list silently — the only feedback is
// visual. Debounced because this is driven by typing, and a screen reader
// reading a new count on every keystroke is worse than no count at all.
const liveCount = ref('')
let countTimer: number | undefined

watch(
  () => [store.visible.length, store.filter, store.query] as const,
  ([n]) => {
    window.clearTimeout(countTimer)
    countTimer = window.setTimeout(() => {
      liveCount.value = n === 0
        ? emptyReason.value
        : `${n} connector${n === 1 ? '' : 's'} shown.`
    }, 400)
  },
)

onBeforeUnmount(() => window.clearTimeout(countTimer))
</script>

<template>
  <div class="conn-list">
    <div class="conn-search">
      <span class="conn-search-icon" aria-hidden="true">⌕</span>
      <input
        v-model="store.query"
        type="search"
        placeholder="Search connectors"
        aria-label="Search connectors"
      />
    </div>

    <div class="conn-scroll">
      <div v-if="store.error" class="conn-error">{{ store.error }}</div>

      <p v-if="store.loading && !store.servers.length" class="conn-empty">Loading connectors…</p>

      <p v-else-if="!store.groups.length" class="conn-empty">{{ emptyReason }}</p>

      <section v-for="g in store.groups" :key="g.key" class="conn-group">
        <h3 class="conn-group-head">{{ GROUP_LABELS[g.key] }}</h3>
        <ConnectorRow
          v-for="d in g.items"
          :key="d.server.name"
          :entry="d"
          @setup="emit('setup', $event)"
          @edit="emit('edit', $event)"
          @signin="emit('signin', $event)"
          @key="emit('key', $event)"
        />
      </section>
    </div>

    <p class="sr-only" role="status" aria-live="polite">{{ liveCount }}</p>
  </div>
</template>

<style scoped>
.conn-list { display: flex; flex-direction: column; min-width: 0; min-height: 0; flex: 1; }

.conn-search {
  position: relative; margin: 14px 16px 10px;
}
.conn-search-icon {
  position: absolute; left: 11px; top: 50%; transform: translateY(-50%);
  color: var(--text3); font-size: 14px; pointer-events: none;
}
.conn-search input {
  width: 100%; box-sizing: border-box;
  padding: 8px 11px 8px 30px;
  background: var(--surface2); border: 1px solid var(--border); border-radius: 8px;
  color: var(--text); font-size: 13px; font-family: inherit; outline: none;
}
.conn-search input:focus { border-color: var(--accent); }

.conn-scroll { flex: 1; min-height: 0; overflow-y: auto; padding: 0 16px 16px; }

.conn-error {
  background: color-mix(in srgb, var(--red) 12%, transparent);
  border: 1px solid color-mix(in srgb, var(--red) 40%, transparent);
  color: var(--text); padding: 8px 10px; border-radius: 6px; font-size: 12px;
  margin-bottom: 10px; word-break: break-word;
}

.conn-empty { color: var(--text3); font-size: 13px; text-align: center; padding: 28px 0; }

.conn-group { margin-bottom: 18px; }
.conn-group > :deep(.conn-row) { margin-bottom: 8px; }
.conn-group-head {
  font-size: 11px; font-weight: 700; letter-spacing: .06em; text-transform: uppercase;
  color: var(--text3); margin: 6px 0 8px;
}
</style>
