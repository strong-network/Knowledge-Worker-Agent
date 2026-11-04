<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// One list of connectors or model providers in the setup guide and what's new:
// a letter, the name and what it gives the user, and a check mark once set up.
defineProps<{
  label: string
  items: { key: string; name: string; description?: string; done: boolean }[]
}>()
</script>

<template>
  <ul class="ob-items" :aria-label="label">
    <li v-for="item in items" :key="item.key" class="ob-item" :class="{ done: item.done }">
      <span class="ob-mark" aria-hidden="true">{{ item.done ? '✓' : (item.name.trim()[0] || '?').toUpperCase() }}</span>
      <span class="ob-text">
        <span class="ob-name">
          {{ item.name }}
          <span v-if="item.done" class="ob-done">Set up</span>
        </span>
        <span v-if="item.description" class="ob-desc">{{ item.description }}</span>
      </span>
    </li>
  </ul>
</template>

<style scoped>
.ob-items {
  list-style: none; margin: 0; padding: 0;
  display: flex; flex-direction: column; gap: 2px;
  max-height: 232px; overflow-y: auto;
}
.ob-item { display: flex; align-items: flex-start; gap: 10px; padding: 6px 4px; }
.ob-mark {
  flex: none; width: 26px; height: 26px; border-radius: 7px;
  display: inline-flex; align-items: center; justify-content: center;
  background: var(--surface2); border: 1px solid var(--border);
  font-size: 12px; font-weight: 650; color: var(--text2);
}
.ob-item.done .ob-mark {
  color: var(--text); border-color: var(--green);
  background: color-mix(in srgb, var(--green) 22%, var(--surface));
}
.ob-text { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.ob-name { font-size: 13px; font-weight: 600; color: var(--text); }
.ob-done { margin-left: 6px; font-size: 12px; font-weight: 500; color: var(--text2); }
.ob-desc { font-size: 12px; line-height: 1.45; color: var(--text2); }
</style>
