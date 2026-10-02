<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref } from 'vue'
import { useScheduledTasksStore } from '../../stores/scheduledTasks'
import { useSessionsStore } from '../../stores/sessions'
import { useUiStore } from '../../stores/ui'
import { loadFlag, saveFlag } from '../../utils/sectionState'
import type { ScheduledTask, TaskRun } from '../../api'

// The sidebar "Scheduled tasks" section. Mirrors ProjectsSection so a
// task reads like a project — a collapsible header, task rows with an expand
// caret, and (on expand) the task's most recent runs, each opening the chat it
// produced. Item 2/3 reuse the run history + "new" indicator from the full
// Scheduled tasks view.
const store = useScheduledTasksStore()
const sessionsStore = useSessionsStore()
const ui = useUiStore()

// Section collapse is remembered across reloads (default expanded).
const COLLAPSED_KEY = 'scheduledTasksCollapsed'
const collapsed = ref(loadFlag(COLLAPSED_KEY, false))

function toggleCollapsed() {
  collapsed.value = !collapsed.value
  saveFlag(COLLAPSED_KEY, collapsed.value)
}

function openCreate() {
  ui.openScheduledTaskEditor(null)
}

// Clicking a task row opens its editor — the task's "detail", analogous to
// opening a project.
function openTask(id: string) {
  ui.openScheduledTaskEditor(id)
  closeSidebarOnMobile()
}

// The caret expands a task to reveal its recent runs; runs load lazily on first
// expand, just like a project's chats. The store owns which tasks are expanded
// so the choice survives a reload.
async function toggleTask(id: string, e: Event) {
  e.stopPropagation()
  await store.toggleExpanded(id)
}

function openViewAll() {
  ui.openScheduled()
  closeSidebarOnMobile()
}

// The five most recent runs (store returns newest first).
function recentRuns(id: string): TaskRun[] {
  return store.runsFor(id).slice(0, 5)
}

// Open the chat a run produced, marking the run as reviewed (clears "New").
async function openRun(t: ScheduledTask, run: TaskRun) {
  if (!run.session_id) return
  try {
    await sessionsStore.switchSession(run.session_id)
    ui.showChat()
    if (!run.opened) void store.markRunOpened(run.id, t.id)
    closeSidebarOnMobile()
  } catch (e) {
    ;(window as any).showToast?.('Could not open that run — its chat may have been deleted')
  }
}

function fmt(iso: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (isNaN(d.getTime())) return '—'
  return d.toLocaleString([], {
    month: 'short', day: 'numeric',
    hour: 'numeric', minute: '2-digit',
  })
}

function statusLabel(run: TaskRun): string {
  switch (run.status) {
    case 'succeeded': return 'Succeeded'
    case 'failed': return 'Failed'
    case 'running': return 'Running'
    default: return run.status || 'Unknown'
  }
}

function closeSidebarOnMobile() {
  if (window.matchMedia('(max-width: 768px)').matches) ui.closeSidebar()
}
</script>

<template>
  <div class="sched-section">
    <!-- Header mirrors the Projects header: collapse toggle + "New task". -->
    <div class="ph-header">
      <button class="ph-toggle" :aria-expanded="!collapsed" @click="toggleCollapsed">
        <span class="ph-caret" :class="{ open: !collapsed }" aria-hidden="true">›</span>
        <span class="ph-label">Scheduled tasks</span>
        <span v-if="store.pendingTasks.length" class="ph-badge">{{ store.pendingTasks.length }}</span>
      </button>
      <button class="ph-new" @click="openCreate" title="New scheduled task" aria-label="New scheduled task">
        <svg viewBox="0 0 16 16" fill="currentColor" width="13" height="13">
          <path d="M8 1.5a.5.5 0 0 1 .5.5v5.5H14a.5.5 0 0 1 0 1H8.5V14a.5.5 0 0 1-1 0V8.5H2a.5.5 0 0 1 0-1h5.5V2a.5.5 0 0 1 .5-.5z"/>
        </svg>
      </button>
    </div>

    <template v-if="!collapsed">
      <div v-if="!store.sortedTasks.length" class="ph-empty">No scheduled tasks yet</div>

      <template v-for="t in store.sortedTasks" :key="t.id">
        <!-- Task row: same look & feel as a project row. -->
        <div class="task-row" :class="{ disabled: !t.enabled }" @click="openTask(t.id)" :title="t.summary">
          <button class="pr-caret" :class="{ open: store.expanded[t.id] }"
                  @click="toggleTask(t.id, $event)" :aria-label="store.expanded[t.id] ? 'Collapse' : 'Expand'">›</button>
          <span class="pr-icon" aria-hidden="true">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" width="15" height="15">
              <rect x="3" y="4.5" width="18" height="16" rx="2"/>
              <path d="M3 9h18M8 2.5v4M16 2.5v4" stroke-linecap="round"/>
            </svg>
          </span>
          <span class="pr-name">{{ t.name || t.prompt }}</span>
          <span v-if="t.pending_catchup_at" class="pr-dot" title="Missed run needs a decision" aria-label="Needs attention"></span>
        </div>

        <!-- Expanded: the task's five most recent runs. -->
        <template v-if="store.expanded[t.id]">
          <button
            v-for="run in recentRuns(t.id)"
            :key="run.id"
            class="run-row"
            :class="{ unopened: !run.opened && run.status !== 'running' }"
            :disabled="!run.session_id"
            @click="openRun(t, run)"
            :title="run.error || statusLabel(run)"
          >
            <span class="run-status" :class="run.status">{{ statusLabel(run) }}</span>
            <span class="run-when">{{ fmt(run.scheduled_for || run.created_at) }}</span>
            <span v-if="!run.opened && run.status !== 'running'" class="run-new">New</span>
            <span class="run-open" aria-hidden="true">›</span>
          </button>
          <div v-if="!store.runsFor(t.id).length" class="run-empty">No runs yet</div>
        </template>
      </template>

      <button class="sched-viewall" @click="openViewAll">View all</button>
    </template>
  </div>
</template>

<style scoped>
/* Container + header mirror ProjectsSection for a consistent look & feel. */
.sched-section { border-bottom: 1px solid var(--border); padding-bottom: 6px; flex-shrink: 0; max-height: 45vh; overflow-y: auto; }
.ph-header { display: flex; align-items: center; padding: 10px 12px 4px; gap: 4px; }
.ph-toggle {
  display: flex; align-items: center; gap: 6px; flex: 1;
  font-size: 11px; font-weight: 600; color: var(--text3); text-transform: uppercase;
  letter-spacing: .06em; background: none; border: none; cursor: pointer;
  font-family: inherit; text-align: left; padding: 0;
}
.ph-toggle:hover { color: var(--text2); }
.ph-caret {
  display: inline-flex; align-items: center; justify-content: center;
  width: 12px; font-size: 14px; color: var(--text3); transition: transform .12s; text-transform: none;
}
.ph-caret.open { transform: rotate(90deg); }
.ph-label { flex: 1; }
.ph-badge {
  flex-shrink: 0; min-width: 16px; height: 16px; padding: 0 4px; text-transform: none;
  display: inline-flex; align-items: center; justify-content: center;
  font-size: 10px; font-weight: 700; color: #fff; background: var(--accent); border-radius: 999px;
}
.ph-new {
  width: 22px; height: 22px; display: flex; align-items: center; justify-content: center;
  background: none; border: none; color: var(--text2); border-radius: 5px; cursor: pointer;
  transition: background .12s, color .12s;
}
.ph-new:hover { background: var(--hover); color: var(--text); }
.ph-empty { padding: 4px 14px 6px 30px; font-size: 12px; color: var(--text3); }

/* Task row: same metrics as .project-row. */
.task-row {
  display: flex; align-items: center; gap: 5px; padding: 7px 10px 7px 8px;
  border-radius: 7px; cursor: pointer; font-size: 13px; color: var(--text2);
  margin: 0 6px 1px; position: relative; transition: background .1s, color .1s;
}
.task-row:hover { background: var(--hover); color: var(--text); }
.task-row.disabled .pr-name, .task-row.disabled .pr-icon { opacity: .55; }
.pr-caret {
  width: 14px; flex-shrink: 0; background: none; border: none; cursor: pointer;
  color: var(--text3); font-size: 14px; transition: transform .12s; padding: 0;
}
.pr-caret.open { transform: rotate(90deg); }
.pr-icon { color: var(--accent); flex-shrink: 0; display: inline-flex; }
.pr-name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
.pr-dot { flex-shrink: 0; width: 7px; height: 7px; border-radius: 999px; background: var(--accent); }

/* Run rows: a compact version of the View-all run history, nested like chats. */
.run-row {
  display: flex; align-items: center; gap: 8px; width: auto;
  padding: 5px 8px; margin: 0 6px 2px 30px;
  background: none; border: 1px solid var(--border); border-radius: 6px;
  color: var(--text2); font-size: 11.5px; font-family: inherit; cursor: pointer; text-align: left;
  transition: background .12s, border-color .12s;
}
.run-row:hover:not(:disabled) { background: var(--hover); border-color: var(--text3); color: var(--text); }
.run-row:disabled { cursor: default; opacity: .7; }
.run-row.unopened { border-color: var(--accent); }
.run-status { font-weight: 600; flex-shrink: 0; }
.run-status.succeeded { color: #16a34a; }
.run-status.failed { color: var(--red); }
.run-status.running { color: var(--accent); }
.run-when { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text3); }
.run-new {
  flex-shrink: 0; font-size: 9px; font-weight: 700; text-transform: uppercase; letter-spacing: .04em;
  color: #fff; background: var(--accent); border-radius: 4px; padding: 1px 5px;
}
.run-open { flex-shrink: 0; color: var(--text3); font-size: 14px; }
.run-empty { padding: 4px 14px 4px 34px; font-size: 12px; color: var(--text3); }

.sched-viewall {
  margin: 4px 6px 2px 30px; padding: 5px 8px; background: none; border: none; border-radius: 6px;
  color: var(--accent); font-size: 12px; font-weight: 600; font-family: inherit;
  cursor: pointer; text-align: left; transition: background .12s;
}
.sched-viewall:hover { background: var(--hover); }
</style>
