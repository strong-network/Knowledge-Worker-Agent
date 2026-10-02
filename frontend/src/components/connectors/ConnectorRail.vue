<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// The filter rail. Counts describe the whole set, not the current search, so
// they stay a reliable answer to "what have I got?" while the list is filtered.
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useConnectorsStore, type ConnectorFilter } from '../../stores/connectors'

const store = useConnectorsStore()
const emit = defineEmits<{ (e: 'add'): void }>()

const filters: { key: ConnectorFilter; label: string; dot: string }[] = [
  { key: 'all', label: 'All connectors', dot: '' },
  { key: 'on', label: 'On', dot: 'on' },
  { key: 'attention', label: 'Needs attention', dot: 'attention' },
  { key: 'off', label: 'Off', dot: 'off' },
]

// Below this the list has less room than the rows need, and the rail is still
// taking a fixed slice of it. The design put this collapse at 1180px, but that
// number was read off a full-width frame: this is a modal capped at 1120px, so
// it stops growing at a 1168px viewport and every wider window looks identical
// to it. A query up there would hide the filters at the exact moment there is
// most room for them.
const COMPACT = '(max-width: 600px)'

// Swapped in JS rather than shown and hidden in CSS: rendering both would put
// two sets of filter controls in the accessibility tree, and only one of them
// is real at any width.
const compact = ref(false)
let mq: MediaQueryList | undefined

function syncCompact(e: MediaQueryList | MediaQueryListEvent) {
  compact.value = e.matches
}

onMounted(() => {
  mq = window.matchMedia(COMPACT)
  syncCompact(mq)
  mq.addEventListener('change', syncCompact)
})

onBeforeUnmount(() => mq?.removeEventListener('change', syncCompact))
</script>

<template>
  <!-- Compact: one labelled select. The rail's other two items are not lost —
       "Custom server URL…" duplicates the header's Add connector button, and
       the provisioned note is already hidden at this width. -->
  <div v-if="compact" class="conn-rail compact">
    <label class="conn-rail-select">
      <span class="sr-only">Filter connectors</span>
      <select v-model="store.filter">
        <option v-for="f in filters" :key="f.key" :value="f.key">
          {{ f.label }} ({{ store.counts[f.key] }})
        </option>
      </select>
    </label>
  </div>

  <nav v-else class="conn-rail" aria-label="Filter connectors">
    <button
      v-for="f in filters"
      :key="f.key"
      class="conn-filter"
      :class="{ active: store.filter === f.key }"
      :aria-pressed="store.filter === f.key"
      @click="store.filter = f.key"
    >
      <i v-if="f.dot" class="dot" :class="f.dot" aria-hidden="true"></i>
      <span class="label">{{ f.label }}</span>
      <span class="count">{{ store.counts[f.key] }}</span>
    </button>

    <hr class="conn-rail-sep" />

    <h3 class="conn-rail-head">Add</h3>
    <button class="conn-rail-link" @click="emit('add')">Custom server URL…</button>

    <p v-if="store.hasProvisioned" class="conn-rail-note">
      Connectors marked <strong>Provided by IT</strong> are set up for you — you only choose
      whether they're on.
    </p>
  </nav>
</template>

<style scoped>
.conn-rail {
  width: 216px; flex-shrink: 0;
  border-right: 1px solid var(--border);
  background: var(--surface2);
  padding: 14px 12px;
  display: flex; flex-direction: column; gap: 2px;
  overflow-y: auto;
}

.conn-filter {
  display: flex; align-items: center; gap: 8px;
  width: 100%; text-align: left;
  background: none; border: none; border-radius: 7px;
  padding: 7px 10px; cursor: pointer;
  font-size: 13px; color: var(--text2); font-family: inherit;
}
.conn-filter:hover { background: var(--hover); color: var(--text); }
.conn-filter.active { background: var(--surface3); color: var(--text); font-weight: 600; }
.conn-filter .label { flex: 1; min-width: 0; }
.conn-filter .count { font-size: 12px; color: var(--text3); font-variant-numeric: tabular-nums; }
.conn-filter.active .count { color: var(--text2); }
.conn-filter .dot { width: 7px; height: 7px; border-radius: 50%; flex-shrink: 0; }
.dot.on { background: var(--green); }
.dot.attention { background: var(--orange); }
.dot.off { background: var(--text3); }

.conn-rail-sep { border: none; border-top: 1px solid var(--border); margin: 12px 4px; }

.conn-rail-head {
  font-size: 10px; font-weight: 700; letter-spacing: .08em; text-transform: uppercase;
  color: var(--text3); padding: 0 10px; margin-bottom: 4px;
}
.conn-rail-link {
  background: none; border: none; text-align: left; width: 100%;
  padding: 7px 10px; border-radius: 7px; cursor: pointer;
  font-size: 13px; color: var(--accent); font-family: inherit;
}
.conn-rail-link:hover { background: var(--hover); }

.conn-rail-note {
  margin-top: auto; padding: 14px 10px 4px;
  font-size: 11px; line-height: 1.5; color: var(--text3);
}
.conn-rail-note strong { color: var(--text2); font-weight: 600; }

@media (max-width: 1000px) {
  .conn-rail { width: 180px; }
}
@media (max-width: 760px) {
  .conn-rail { width: 148px; padding: 12px 8px; }
  .conn-rail-note { display: none; }
}

/* The collapse. Below 600px the rail becomes a full-width strip above the list
   holding a single select — see the COMPACT note in the script for why this is
   not the design's 1180px. */
.conn-rail.compact {
  width: auto;
  border-right: none;
  border-bottom: 1px solid var(--border);
  padding: 10px 16px;
}
.conn-rail-select { display: block; }
.conn-rail-select select {
  width: 100%; box-sizing: border-box;
  padding: 8px 10px;
  background: var(--surface); border: 1px solid var(--border); border-radius: 8px;
  color: var(--text); font-size: 13px; font-family: inherit;
}
.conn-rail-select select:focus { border-color: var(--accent); outline: none; }
</style>
