<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { todoIcon, todoCounts, type TodoItem } from '../../utils/todos'

// The agent's plan, at the top of its reply: open while the turn runs, then folded to one line.
const props = defineProps<{
  items: TodoItem[]
  running?: boolean
}>()

const open = ref(!!props.running)
watch(() => props.running, (running) => { open.value = !!running })

const counts = computed(() => todoCounts(props.items))
const summary = computed(() => `${counts.value.done} of ${counts.value.total} done`)
const pct = computed(() => (counts.value.total ? Math.round((counts.value.done / counts.value.total) * 100) : 0))
</script>

<template>
  <section class="turn-plan" :class="{ open }">
    <button type="button" class="tp-head" :aria-expanded="open" @click="open = !open">
      <span class="tp-label">Plan:</span>
      <span class="tp-value">{{ summary }}</span>
      <span v-if="!open && counts.active" class="tp-detail">{{ counts.active.content }}</span>
      <span class="tp-chevron" aria-hidden="true">{{ open ? '▾' : '▸' }}</span>
    </button>
    <div v-if="open" class="tp-body">
      <div
        class="tp-bar"
        role="progressbar"
        aria-label="Plan progress"
        aria-valuemin="0"
        :aria-valuemax="counts.total"
        :aria-valuenow="counts.done"
        :aria-valuetext="summary"
      >
        <div class="tp-fill" :style="{ width: pct + '%' }"></div>
      </div>
      <ol class="tp-list">
        <li v-for="(t, i) in items" :key="i" class="tp-item" :class="'status-' + t.status">
          <span class="tp-check">{{ todoIcon(t.status) }}</span>
          <span class="tp-text">{{ t.content }}</span>
        </li>
      </ol>
    </div>
  </section>
</template>

<style scoped>
.turn-plan { margin-bottom: 6px; }

/* Folded, it reads like the Skills and Tools lines under it. */
.tp-head {
  display: flex; align-items: baseline; gap: 6px; width: 100%;
  padding: 2px; margin: 0;
  font: inherit; font-size: 12px; line-height: 1.5;
  color: var(--text3); text-align: left;
  background: none; border: none; border-radius: 5px; cursor: pointer;
  transition: color .12s, background .12s;
}
.tp-head:hover { color: var(--text2); background: var(--surface2); }
.tp-label { font-weight: 600; white-space: nowrap; }
.tp-value { color: var(--text2); white-space: nowrap; }
.tp-detail { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tp-chevron { margin-left: auto; font-size: 10px; }

.tp-body {
  margin: 4px 0 6px;
  padding: 10px 10px 6px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 10px;
}
.tp-bar { height: 5px; background: var(--surface3); border-radius: 999px; overflow: hidden; margin: 0 2px 6px; }
.tp-fill { height: 100%; background: var(--accent); border-radius: 999px; transition: width .3s ease; }

.tp-list { list-style: none; margin: 0; padding: 0; }
.tp-item {
  display: flex; align-items: flex-start; gap: 8px;
  padding: 5px 6px; border-radius: 7px; font-size: 13px; line-height: 1.4;
}
.tp-check { flex-shrink: 0; width: 16px; text-align: center; font-size: 12px; line-height: 1.5; color: var(--text3); }
.tp-text { color: var(--text2); }
.tp-item.status-in_progress { background: rgba(3, 169, 241, .08); }
.tp-item.status-in_progress .tp-check { color: var(--accent); }
.tp-item.status-in_progress .tp-text { color: var(--text); font-weight: 600; }
.tp-item.status-completed .tp-check,
.tp-item.status-done .tp-check { color: var(--green); }
.tp-item.status-completed .tp-text,
.tp-item.status-done .tp-text { text-decoration: line-through; color: var(--text3); }
.tp-item.status-cancelled .tp-text,
.tp-item.status-canceled .tp-text { text-decoration: line-through; color: var(--text3); opacity: .7; }

@media (prefers-reduced-motion: reduce) {
  .tp-fill { transition: none; }
}
</style>
