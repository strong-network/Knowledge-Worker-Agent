<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { isTodoTool, parseTodos, todoIcon, todoCounts, type TodoItem } from '../../utils/todos'

// "Plan & progress": the agent's current todo checklist as a persistent
// tracker, so it doesn't scroll away inside the conversation. Renders from
// props: the owner's app feeds it from the stores (GoalsPanel),
// the guest page from its own transcript.
interface Call { name: string; args?: string }

const props = defineProps<{
  // The running turn's tool calls, newest last.
  liveCalls: Call[]
  // The transcript, oldest first; finished turns keep their calls here.
  messages: Array<{ toolCalls?: Call[] }>
}>()

const collapsed = ref(false)
// Dismissed for the current todo revision — reopens when the list changes.
const dismissed = ref(false)

// The most recent todo list: prefer the live tool calls while a turn streams,
// otherwise scan the transcript for the last todowrite call.
const todos = computed<TodoItem[] | null>(() => {
  const live = [...props.liveCalls].reverse().find(t => isTodoTool(t.name) && parseTodos(t.args))
  if (live) return parseTodos(live.args)

  const msgs = props.messages
  for (let i = msgs.length - 1; i >= 0; i--) {
    const calls = msgs[i].toolCalls
    if (!calls) continue
    for (let j = calls.length - 1; j >= 0; j--) {
      if (isTodoTool(calls[j].name)) {
        const parsed = parseTodos(calls[j].args)
        if (parsed) return parsed
      }
    }
  }
  return null
})

const counts = computed(() => (todos.value ? todoCounts(todos.value) : null))
const progressPct = computed(() => {
  const c = counts.value
  if (!c || c.total === 0) return 0
  return Math.round((c.done / c.total) * 100)
})

const visible = computed(() => !!todos.value && !dismissed.value)

// Reopen the panel whenever the todo list changes (new revision) so progress
// updates resurface even if the user dismissed a prior list.
watch(
  () => (todos.value ? todos.value.map(t => t.content + ':' + t.status).join('|') : ''),
  () => { dismissed.value = false },
)
</script>

<template>
  <div v-if="visible" class="goals-panel" :class="{ collapsed }">
    <div class="gp-header" @click="collapsed = !collapsed">
      <span class="gp-title-ico" aria-hidden="true">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="15" height="15">
          <path d="M9 11l3 3L22 4" stroke-linecap="round" stroke-linejoin="round"/>
          <path d="M21 12v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
      </span>
      <span class="gp-title">Plan &amp; progress</span>
      <span v-if="counts" class="gp-count">{{ counts.done }}/{{ counts.total }}</span>
      <button
        class="gp-icon-btn"
        @click.stop="collapsed = !collapsed"
        :title="collapsed ? 'Expand' : 'Collapse'"
        :aria-label="collapsed ? 'Expand' : 'Collapse'"
      >
        <svg viewBox="0 0 16 16" fill="currentColor" width="12" height="12" :style="{ transform: collapsed ? 'rotate(180deg)' : 'none' }">
          <path d="M4.22 9.78a.75.75 0 0 0 1.06 0L8 7.06l2.72 2.72a.75.75 0 1 0 1.06-1.06L8.53 5.47a.75.75 0 0 0-1.06 0L4.22 8.72a.75.75 0 0 0 0 1.06z"/>
        </svg>
      </button>
      <button class="gp-icon-btn" @click.stop="dismissed = true" title="Hide" aria-label="Hide">
        <svg viewBox="0 0 16 16" fill="currentColor" width="12" height="12"><path d="M3.72 3.72a.75.75 0 0 1 1.06 0L8 6.94l3.22-3.22a.75.75 0 1 1 1.06 1.06L9.06 8l3.22 3.22a.75.75 0 1 1-1.06 1.06L8 9.06l-3.22 3.22a.75.75 0 0 1-1.06-1.06L6.94 8 3.72 4.78a.75.75 0 0 1 0-1.06z"/></svg>
      </button>
    </div>

    <div class="gp-progress" v-if="!collapsed && counts">
      <div class="gp-progress-bar"><div class="gp-progress-fill" :style="{ width: progressPct + '%' }"></div></div>
    </div>

    <div v-if="!collapsed" class="gp-list">
      <div
        v-for="(t, ti) in todos!"
        :key="ti"
        class="gp-item"
        :class="['status-' + t.status]"
      >
        <span class="gp-check">{{ todoIcon(t.status) }}</span>
        <span class="gp-text">{{ t.content }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.goals-panel {
  width: 100%;
  max-height: 50vh;
  flex-shrink: 0;
  display: flex; flex-direction: column;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 12px;
  box-shadow: 0 4px 16px rgba(0,0,0,.08);
  overflow: hidden;
  animation: gpIn .18s ease;
}
@keyframes gpIn { from { opacity: 0; transform: translateY(-6px); } to { opacity: 1; transform: translateY(0); } }

.gp-header {
  display: flex; align-items: center; gap: 8px;
  padding: 10px 12px; cursor: pointer; user-select: none;
  border-bottom: 1px solid var(--border);
  background: var(--surface2);
}
.goals-panel.collapsed .gp-header { border-bottom: none; }
.gp-title-ico { color: var(--accent); display: inline-flex; flex-shrink: 0; }
.gp-title { font-size: 13px; font-weight: 700; color: var(--text); flex: 1; }
.gp-count {
  font-size: 11px; font-weight: 600; color: var(--text2);
  background: var(--surface); border: 1px solid var(--border);
  border-radius: 999px; padding: 1px 8px;
}
.gp-icon-btn {
  width: 22px; height: 22px; display: flex; align-items: center; justify-content: center;
  background: none; border: none; color: var(--text2); border-radius: 5px; cursor: pointer;
  transition: background .12s, color .12s;
}
.gp-icon-btn:hover { background: var(--surface3); color: var(--text); }

.gp-progress { padding: 8px 12px 4px; }
.gp-progress-bar { height: 5px; background: var(--surface3); border-radius: 999px; overflow: hidden; }
.gp-progress-fill { height: 100%; background: var(--accent); border-radius: 999px; transition: width .3s ease; }

.gp-list { overflow-y: auto; padding: 6px 8px 10px; }
.gp-item {
  display: flex; align-items: flex-start; gap: 8px;
  padding: 6px 6px; border-radius: 7px; font-size: 13px; line-height: 1.4;
}
.gp-item + .gp-item { margin-top: 1px; }
.gp-check {
  flex-shrink: 0; width: 16px; text-align: center; font-size: 12px; line-height: 1.5;
  color: var(--text3);
}
.gp-text { color: var(--text2); }
.gp-item.status-in_progress { background: rgba(3,169,241,.08); }
.gp-item.status-in_progress .gp-check { color: var(--accent); }
.gp-item.status-in_progress .gp-text { color: var(--text); font-weight: 600; }
.gp-item.status-completed .gp-check,
.gp-item.status-done .gp-check { color: var(--green); }
.gp-item.status-completed .gp-text,
.gp-item.status-done .gp-text { text-decoration: line-through; color: var(--text3); }
.gp-item.status-cancelled .gp-text,
.gp-item.status-canceled .gp-text { text-decoration: line-through; color: var(--text3); opacity: .7; }
</style>
