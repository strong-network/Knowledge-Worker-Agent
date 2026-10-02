<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// The empty state. Never leave the grid blank — the copy depends on
// why it is empty, because "your search matched nothing" and "you have nothing"
// need different answers.
//
// Note what is *not* here: the skills loading and still-starting-up states.
// They used to be specified for this component, but it only mounts when the
// grid is empty, and the case they exist for — connectors landed, skills still
// in flight — is a grid with cards in it. They live in ToolboxSkillsNotice
// instead.
import { computed } from 'vue'
import { useToolboxStore } from '../../stores/toolbox'

const store = useToolboxStore()
const emit = defineEmits<{ (e: 'manage'): void }>()

const copy = computed(() => {
  if (store.emptyReason === 'query') {
    return {
      title: `Nothing matches “${store.query.trim()}”`,
      body: 'Try a shorter term, or search by what you want to do rather than the exact name.',
    }
  }
  if (store.emptyReason === 'active') {
    const what = store.type === 'skill' ? 'No skills are active in this chat'
      : store.type === 'connector' ? 'No connectors are active in this chat'
      : 'Nothing is active in this chat'
    return {
      title: what,
      body: 'Switch off "Show active only" to browse the full library and turn something on.',
    }
  }
  if (store.emptyReason === 'filter') {
    return {
      title: 'Nothing here yet',
      body: 'Nothing in the library matches this filter combination.',
    }
  }
  // Nothing is filtered — the library itself is empty.
  return {
    title: 'Nothing here yet',
    body: 'Connectors you turn on and skills in this workspace will show up here.',
  }
})

// Only worth offering when a filter is actually set. With an empty library it
// would be a button that visibly does nothing.
const showClear = computed(() => store.emptyReason !== 'none')

// The route to the catalogue matters most when the reason there is nothing to
// show is that no connectors exist yet — a filter reset would not help there.
const showManage = computed(() =>
  (store.emptyReason === 'filter' || store.emptyReason === 'none')
  && !store.anyConnectors && store.type !== 'skill')
</script>

<template>
  <div class="tb-empty">
    <span class="tb-empty-icon" aria-hidden="true">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"
           stroke-linecap="round" stroke-linejoin="round">
        <circle cx="11" cy="11" r="7" /><path d="m20 20-3.5-3.5" />
      </svg>
    </span>
    <h3>{{ copy.title }}</h3>
    <p>{{ copy.body }}</p>
    <div class="tb-empty-actions">
      <button v-if="showClear" type="button" class="tb-btn-ghost" @click="store.clearFilters()">Clear filters</button>
      <button v-if="showManage" type="button" class="tb-btn-ghost" @click="emit('manage')">
        Manage connectors
      </button>
    </div>
  </div>
</template>

<style scoped>
/* Spans the whole grid rather than sitting in the first cell. */
.tb-empty {
  grid-column: 1 / -1;
  display: flex; flex-direction: column; align-items: center; text-align: center;
  padding: 56px 24px;
}
.tb-empty-icon {
  width: 44px; height: 44px; border-radius: 14px;
  display: flex; align-items: center; justify-content: center;
  background: var(--surface3); color: var(--text3);
  margin-bottom: 14px;
}
.tb-empty-icon svg { width: 21px; height: 21px; }
.tb-empty h3 { margin: 0 0 6px; font-size: 15.5px; font-weight: 700; color: var(--text); }
.tb-empty p { margin: 0; font-size: 13px; line-height: 1.55; color: var(--text3); max-width: 320px; }
.tb-empty-actions { display: flex; gap: 8px; margin-top: 16px; flex-wrap: wrap; justify-content: center; }

.tb-btn-ghost {
  background: var(--surface); border: 1px solid var(--border); color: var(--text2);
  padding: 7px 14px; border-radius: 8px; cursor: pointer;
  font-family: inherit; font-size: 12.5px;
}
.tb-btn-ghost:hover { color: var(--text); background: var(--surface3); }
</style>
