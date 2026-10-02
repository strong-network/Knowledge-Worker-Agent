<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// The filter rail. Two groups today — THIS CHAT and TYPE — and a
// CATEGORY group that appears by itself the moment any item carries a category
// (seam 2). The groups are built as data rather than markup precisely so that
// adding the third one later is a store change, not a component change.
//
// Counts come from `countFor()`, which releases the row's own dimension. A row
// whose count was "the set the grid currently renders" would read 0 for
// Connectors as soon as you picked Skills — which is the number you can no
// longer see, not the number that is there.
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useToolboxStore, type ToolboxType } from '../../stores/toolbox'

const store = useToolboxStore()

interface Row {
  id: string
  label: string
  count: number
  active: boolean
  icon: 'spark' | 'wrench' | 'plug' | 'list' | 'tag'
  accent?: 'skill' | 'connector'
  select: () => void
}

const typeRows = computed<Row[]>(() => [
  {
    id: 'type-all', label: 'Everything', icon: 'spark',
    count: store.countFor({ type: 'all' }),
    active: store.type === 'all' && !store.category,
    // Everything resets category too — it is the "show me the lot"
    // row, and leaving a category applied would make it lie.
    select: () => { store.type = 'all'; store.category = '' },
  },
  {
    id: 'type-skill', label: 'Skills', icon: 'wrench', accent: 'skill',
    count: store.countFor({ type: 'skill' }),
    active: store.type === 'skill',
    select: () => { store.type = 'skill' },
  },
  {
    id: 'type-connector', label: 'Connectors', icon: 'plug', accent: 'connector',
    count: store.countFor({ type: 'connector' }),
    active: store.type === 'connector',
    select: () => { store.type = 'connector' },
  },
])

// Deliberately not a Row. Every row in this rail picks one value out of a set —
// pressing Skills replaces Everything — and THIS CHAT does not work that way: it
// is an independent boolean that narrows whatever type is already selected. Given
// the same shape, the same press-to-activate styling and a count in the same
// slot, it read as a fourth alternative sitting above the other three, and
// "Active 0" looked like an empty category rather than a filter that is off.
// A switch says which of the two states is current without being pressed first.
//
// It also carries no count, which the row did. A number in the rail's count
// column is what made the old control read as a category in the first place, and
// the answer it gave — whether turning this on would show anything — is better
// given by the empty state, which appears exactly when it matters and says what
// to do about it. The compact chip keeps its count: there is no count column
// there to misread it against, and the select beside it is numbered too.
const activeCount = computed(() => store.countFor({ activeOnly: true }))

const categoryRows = computed<Row[]>(() => {
  if (!store.hasCategories) return []
  return [
    {
      id: 'cat-all', label: 'All', icon: 'list',
      count: store.countFor({ category: '' }),
      active: !store.category,
      select: () => { store.category = '' },
    },
    ...store.categories.map((c): Row => ({
      // Category rows must not reuse the wrench or plug glyph — those two marks
      // are reserved for type identity.
      id: `cat-${c}`, label: c, icon: 'tag',
      count: store.categoryCount(c),
      active: store.category === c,
      select: () => { store.category = c },
    })),
  ]
})

const groups = computed(() => [
  { key: 'type', label: 'Type', rows: typeRows.value },
  { key: 'category', label: 'Category', rows: categoryRows.value },
].filter(g => g.rows.length > 0))

// Below this the grid has less room than a card needs and the rail is still
// taking a fixed slice of it. Same reasoning and same breakpoint as
// ConnectorRail: this is a capped modal, so a wider query would hide the
// filters at the width where there is most room for them.
const COMPACT = '(max-width: 600px)'

// Swapped in JS rather than shown/hidden in CSS: rendering both would put two
// sets of filter controls in the accessibility tree when only one is real.
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

// The compact control collapses to type only: Active stays reachable as its own
// chip beside it, and categories do not exist yet.
const compactType = computed({
  get: () => store.type,
  set: (v: ToolboxType) => { store.type = v; if (v === 'all') store.category = '' },
})
</script>

<template>
  <div v-if="compact" class="tb-rail compact">
    <label class="tb-rail-select">
      <span class="sr-only">Filter the toolbox</span>
      <select v-model="compactType">
        <option value="all">Everything ({{ store.countFor({ type: 'all' }) }})</option>
        <option value="skill">Skills ({{ store.countFor({ type: 'skill' }) }})</option>
        <option value="connector">Connectors ({{ store.countFor({ type: 'connector' }) }})</option>
      </select>
    </label>
    <button
      type="button" class="tb-chip" :class="{ on: store.activeOnly }"
      :aria-pressed="store.activeOnly"
      @click="store.activeOnly = !store.activeOnly"
    >Active only ({{ activeCount }})</button>
  </div>

  <nav v-else class="tb-rail" aria-label="Filter the toolbox">
    <h3 class="tb-rail-head">This chat</h3>
    <label class="tb-only">
      <span class="tb-only-label">Show active only</span>
      <input
        type="checkbox"
        role="switch"
        :checked="store.activeOnly"
        :aria-checked="store.activeOnly"
        @change="store.activeOnly = !store.activeOnly"
      />
      <span class="tb-only-track" aria-hidden="true"></span>
    </label>

    <template v-for="g in groups" :key="g.key">
      <h3 class="tb-rail-head">{{ g.label }}</h3>
      <button
        v-for="row in g.rows"
        :key="row.id"
        type="button"
        class="tb-row"
        :class="[{ active: row.active }, row.accent ? `a-${row.accent}` : '']"
        :aria-pressed="row.active"
        @click="row.select()"
      >
        <span class="tb-row-icon" aria-hidden="true">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor"
               stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
            <template v-if="row.icon === 'spark'">
              <path d="M12 3v4M12 17v4M3 12h4M17 12h4" />
              <path d="M12 8.5 13.2 11 16 12l-2.8 1-1.2 2.5L10.8 13 8 12l2.8-1Z" />
            </template>
            <template v-else-if="row.icon === 'wrench'">
              <path d="M14.7 6.3a4 4 0 0 0 5 5L15 16l-4-4 3.7-5.7Z" />
              <path d="m11 12-6.3 6.3a1.8 1.8 0 0 0 2.5 2.5L13.5 15" />
            </template>
            <template v-else-if="row.icon === 'plug'">
              <path d="M9 2v6M15 2v6" />
              <path d="M6 8h12v3a6 6 0 0 1-12 0V8Z" />
              <path d="M12 17v5" />
            </template>
            <template v-else-if="row.icon === 'list'">
              <path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01" />
            </template>
            <template v-else>
              <path d="M3 11V5a2 2 0 0 1 2-2h6l9 9-8 8-9-9Z" />
              <circle cx="7.5" cy="7.5" r="1.2" />
            </template>
          </svg>
        </span>
        <span class="tb-row-label">{{ row.label }}</span>
        <span class="tb-row-count">{{ row.count }}</span>
      </button>
    </template>
  </nav>
</template>

<style scoped>
.tb-rail {
  width: 250px; flex-shrink: 0;
  border-right: 1px solid var(--border);
  background: var(--surface2);
  padding: 14px 12px;
  display: flex; flex-direction: column; gap: 2px;
  overflow-y: auto;
}

/* The switch row. Same padding and type as .tb-row so the rail still scans as
   one column, but no hover-to-activate and no pressed state — the track is the
   state, and a row that highlights on hover would re-suggest "press to pick". */
.tb-only {
  display: flex; align-items: center; gap: 9px;
  padding: 7px 10px; border-radius: 9px; cursor: pointer;
  font-size: 13.5px; font-weight: 500; color: var(--text2);
}
.tb-only:hover { color: var(--text); }
.tb-only-label { flex: 1; min-width: 0; }
.tb-only input { position: absolute; opacity: 0; width: 0; height: 0; }
/* Matches ToolboxCard's connector switch: the same gesture should look the same
   wherever it appears, even though the two are in different scoped sheets. */
.tb-only-track {
  position: relative; display: block; flex-shrink: 0;
  width: 34px; height: 20px; border-radius: 999px;
  background: var(--surface3); border: 1px solid var(--border);
  transition: background .12s, border-color .12s;
}
.tb-only-track::after {
  content: ''; position: absolute; top: 2px; left: 2px; width: 14px; height: 14px;
  border-radius: 50%; background: var(--text3);
  transition: transform .12s, background .12s;
}
.tb-only input:checked ~ .tb-only-track { background: var(--accent); border-color: var(--accent); }
.tb-only input:checked ~ .tb-only-track::after { transform: translateX(14px); background: #fff; }
.tb-only input:focus-visible ~ .tb-only-track { outline: 2px solid var(--accent); outline-offset: 2px; }

.tb-rail-head {
  font-size: 10.5px; font-weight: 700; letter-spacing: .11em; text-transform: uppercase;
  color: var(--text3); padding: 0 10px; margin: 12px 0 5px;
}
.tb-rail-head:first-child { margin-top: 0; }

.tb-row {
  display: flex; align-items: center; gap: 9px;
  width: 100%; text-align: left;
  background: none; border: none; border-radius: 9px;
  padding: 7px 10px; cursor: pointer;
  font-family: inherit; font-size: 13.5px; font-weight: 500; color: var(--text2);
}
.tb-row:hover { background: var(--hover); color: var(--text); }
.tb-row.active { background: var(--surface3); color: var(--text); font-weight: 700; }
.tb-row:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }

.tb-row-icon { display: flex; flex-shrink: 0; color: var(--text3); }
.tb-row-icon svg { width: 16px; height: 16px; }
/* The accent only lands on the selected row, so the rail stays quiet at rest
   and the type marks still read as the card icons' siblings when chosen. */
.tb-row.active.a-skill .tb-row-icon { color: var(--accent); }
.tb-row.active.a-connector .tb-row-icon { color: var(--accent2); }

.tb-row-label { flex: 1; min-width: 0; }
.tb-row-count { font-size: 12px; font-weight: 600; color: var(--text3); font-variant-numeric: tabular-nums; }
.tb-row.active .tb-row-count { color: var(--text2); }

@media (max-width: 1000px) { .tb-rail { width: 214px; } }
@media (max-width: 760px) { .tb-rail { width: 186px; padding: 12px 8px; } }

.tb-rail.compact {
  width: auto;
  flex-direction: row; align-items: center; gap: 8px;
  border-right: none; border-bottom: 1px solid var(--border);
  padding: 10px 16px;
}
.tb-rail-select { display: block; flex: 1; min-width: 0; }
.tb-rail-select select {
  width: 100%; box-sizing: border-box;
  padding: 8px 10px;
  background: var(--surface); border: 1px solid var(--border); border-radius: 8px;
  color: var(--text); font-size: 13px; font-family: inherit;
}
.tb-rail-select select:focus { border-color: var(--accent); outline: none; }
.tb-chip {
  flex-shrink: 0;
  background: var(--surface); border: 1px solid var(--border); border-radius: 999px;
  padding: 7px 13px; cursor: pointer;
  font-family: inherit; font-size: 12.5px; color: var(--text2);
}
.tb-chip.on { border-color: var(--accent); color: var(--accent); }
</style>
