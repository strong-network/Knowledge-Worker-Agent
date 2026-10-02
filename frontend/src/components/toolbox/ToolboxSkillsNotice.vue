<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// The skills half reporting on itself, above the grid.
//
// This is a separate component from ToolboxEmpty because the two answer
// different questions at the same time. The state it exists for is "connectors
// have landed, skills have not" — and in that state the grid is *not* empty, so
// an empty-state component never mounts. Putting these two states there would
// make them unreachable in exactly the case the independent loading exists to
// produce, and the screen would be indistinguishable from a finished load that
// found no skills.
//
// The genuinely-empty case is deliberately absent here: when there are no
// skills and nothing else to show, that is the grid's own empty state.
// `skillsNotice` returns null for it.
import { useToolboxStore } from '../../stores/toolbox'

const store = useToolboxStore()
</script>

<template>
  <div v-if="store.skillsNotice" class="tb-notice" :class="store.skillsNotice" role="status">
    <template v-if="store.skillsNotice === 'loading'">
      <span class="tb-spinner" aria-hidden="true"></span>
      <span class="tb-notice-text">Loading skills…</span>
    </template>

    <!-- Never "no skills": `ListSkills` returns an empty list *and*
         available:false on any failure, so telling someone with a dozen skills
         that they have none is the mistake this copy exists to prevent. -->
    <template v-else>
      <span class="tb-notice-text">The workspace agent is still starting up.</span>
      <!-- Reaches opencode rather than re-reading a cached failure: the backend
           never stores a failed enumeration. -->
      <button type="button" class="tb-notice-btn" @click="store.loadSkills()">Try again</button>
    </template>
  </div>
</template>

<style scoped>
.tb-notice {
  display: flex; align-items: center; gap: 9px;
  margin: 16px 16px 0;
  padding: 10px 14px;
  border-radius: 12px;
  background: var(--surface2);
  border: 1px solid var(--border);
  font-size: 12.5px; color: var(--text2);
}
.tb-notice.unavailable { background: color-mix(in srgb, var(--orange) 10%, transparent); }
.tb-notice-text { flex: 1; min-width: 0; }

.tb-notice-btn {
  flex-shrink: 0;
  background: var(--surface); border: 1px solid var(--border); color: var(--text2);
  padding: 5px 12px; border-radius: 7px; cursor: pointer;
  font-family: inherit; font-size: 12px;
}
.tb-notice-btn:hover { color: var(--text); background: var(--surface3); }

.tb-spinner {
  width: 13px; height: 13px; flex-shrink: 0;
  border: 2px solid var(--border); border-top-color: var(--accent);
  border-radius: 50%;
  animation: tb-spin .7s linear infinite;
}
@keyframes tb-spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) { .tb-spinner { animation-duration: 2.4s; } }
</style>
