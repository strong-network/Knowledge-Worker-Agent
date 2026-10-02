<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useScheduledTasksStore } from '../../stores/scheduledTasks'
import { useSessionsStore } from '../../stores/sessions'
import { useProjectsStore } from '../../stores/projects'
import { useUiStore } from '../../stores/ui'
import type { ScheduledTask, TaskRun } from '../../api'

// The Scheduled tasks surface. Lists the user's unattended prompt
// schedules, surfaces missed runs awaiting a decision, and lets each run's
// produced chat be opened for review.
const store = useScheduledTasksStore()
const sessionsStore = useSessionsStore()
const projectsStore = useProjectsStore()
const ui = useUiStore()

// Which task's run history is expanded.
const expanded = ref<Record<string, boolean>>({})
const busyId = ref<string>('')

onMounted(() => {
  void store.loadTasks()
  // Projects let us show which project a task runs in.
  if (!projectsStore.projects.length) void projectsStore.loadProjects()
})

function newTask() {
  ui.openScheduledTaskEditor(null)
}

function editTask(t: ScheduledTask) {
  ui.openScheduledTaskEditor(t.id)
}

async function toggleEnabled(t: ScheduledTask) {
  busyId.value = t.id
  try {
    await store.setEnabled(t.id, !t.enabled)
  } catch (e) {
    ;(window as any).showToast?.(e instanceof Error ? e.message : 'Could not update the task')
  } finally {
    busyId.value = ''
  }
}

async function runNow(t: ScheduledTask) {
  busyId.value = t.id
  try {
    await store.runNow(t.id)
    expanded.value[t.id] = true
    ;(window as any).showToast?.('Run started — it will appear in history shortly')
  } catch (e) {
    ;(window as any).showToast?.(e instanceof Error ? e.message : 'Could not start the run')
  } finally {
    busyId.value = ''
  }
}

async function removeTask(t: ScheduledTask) {
  if (!confirm(`Delete scheduled task "${t.name || 'Untitled task'}"? Its run history is removed. The shared working folder is left on disk.`)) return
  busyId.value = t.id
  try {
    await store.deleteTask(t.id)
    delete expanded.value[t.id]
  } catch (e) {
    ;(window as any).showToast?.(e instanceof Error ? e.message : 'Could not delete the task')
  } finally {
    busyId.value = ''
  }
}

async function resolvePending(t: ScheduledTask, action: 'run' | 'skip') {
  busyId.value = t.id
  try {
    await store.resolvePending(t.id, action)
    if (action === 'run') expanded.value[t.id] = true
  } catch (e) {
    ;(window as any).showToast?.(e instanceof Error ? e.message : 'Could not resolve the pending run')
  } finally {
    busyId.value = ''
  }
}

async function toggleHistory(t: ScheduledTask) {
  const open = !expanded.value[t.id]
  expanded.value[t.id] = open
  if (open) await store.loadRuns(t.id)
}

// Open the chat a run produced, marking the run as reviewed.
async function openRun(t: ScheduledTask, run: TaskRun) {
  if (!run.session_id) return
  try {
    await sessionsStore.switchSession(run.session_id)
    ui.showChat()
    if (!run.opened) void store.markRunOpened(run.id, t.id)
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

function triggerLabel(run: TaskRun): string {
  switch (run.trigger) {
    case 'scheduled': return 'Scheduled'
    case 'catch-up': return 'Catch-up'
    case 'manual': return 'Manual'
    default: return run.trigger || ''
  }
}

// Scheduled tasks: where a task's runs execute — the bound project's name, or "Own
// folder" when the task uses its own auto-created directory.
function targetLabel(t: ScheduledTask): string {
  if (!t.workdir) return 'Own folder'
  const p = projectsStore.projects.find(pr => pr.workspace_path === t.workdir)
  return p ? p.name : 'Own folder'
}
</script>

<template>
  <div id="scheduled-view">
    <div class="sv-header">
      <div class="sv-heading">
        <h1>Scheduled tasks</h1>
        <p class="sv-sub">Run a prompt unattended on a schedule. Each run opens as its own chat you can review and continue.</p>
      </div>
      <button class="btn btn-primary" @click="newTask">
        <svg viewBox="0 0 16 16" fill="currentColor" width="13" height="13">
          <path d="M8 1.5a.5.5 0 0 1 .5.5v5.5H14a.5.5 0 0 1 0 1H8.5V14a.5.5 0 0 1-1 0V8.5H2a.5.5 0 0 1 0-1h5.5V2a.5.5 0 0 1 .5-.5z"/>
        </svg>
        New task
      </button>
    </div>

    <div class="sv-body">
      <div v-if="store.loading && !store.tasks.length" class="sv-empty">Loading…</div>

      <div v-else-if="!store.tasks.length" class="sv-empty">
        <div class="sv-empty-icon" aria-hidden="true">🗓️</div>
        <div class="sv-empty-title">No scheduled tasks yet</div>
        <div class="sv-empty-text">Create a task to run a prompt automatically at a time you choose.</div>
        <button class="btn btn-primary" @click="newTask">New task</button>
      </div>

      <template v-else>
        <div v-for="t in store.sortedTasks" :key="t.id" class="task-card" :class="{ disabled: !t.enabled }">
          <!-- Pending missed-run banner -->
          <div v-if="t.pending_catchup_at" class="task-pending">
            <span class="tp-text">
              A scheduled run was missed while the workspace was off ({{ fmt(t.pending_catchup_at) }}).
            </span>
            <span class="tp-actions">
              <button class="btn btn-primary btn-sm" :disabled="busyId === t.id" @click="resolvePending(t, 'run')">Run now</button>
              <button class="btn btn-ghost btn-sm" :disabled="busyId === t.id" @click="resolvePending(t, 'skip')">Skip</button>
            </span>
          </div>

          <div class="task-main">
            <div class="task-info">
              <div class="task-title-row">
                <span class="task-name">{{ t.name || 'Untitled task' }}</span>
                <span v-if="!t.enabled" class="task-badge">Paused</span>
              </div>
              <div class="task-summary">{{ t.summary }}</div>
              <div class="task-meta">
                <span v-if="t.enabled">Next run {{ fmt(t.next_run_at) }}</span>
                <span v-else>Paused</span>
                <span class="task-dot">·</span>
                <span>{{ t.run_count }} run{{ t.run_count === 1 ? '' : 's' }}</span>
                <span class="task-dot">·</span>
                <span class="task-target">{{ targetLabel(t) }}</span>
              </div>
              <div class="task-prompt">{{ t.prompt }}</div>
            </div>

            <div class="task-actions">
              <label class="task-toggle" :title="t.enabled ? 'Pause' : 'Resume'">
                <input
                  type="checkbox"
                  :checked="t.enabled"
                  :disabled="busyId === t.id"
                  @change="toggleEnabled(t)"
                />
                <span class="tt-track"><span class="tt-thumb" /></span>
              </label>
              <button class="task-btn" :disabled="busyId === t.id" @click="runNow(t)" title="Run now">Run now</button>
              <button class="task-btn" @click="editTask(t)" title="Edit task">Edit</button>
              <button class="task-btn danger" :disabled="busyId === t.id" @click="removeTask(t)" title="Delete task">Delete</button>
            </div>
          </div>

          <button class="task-history-toggle" @click="toggleHistory(t)">
            <span class="tht-caret" :class="{ open: expanded[t.id] }" aria-hidden="true">›</span>
            Run history
          </button>

          <div v-if="expanded[t.id]" class="task-history">
            <div v-if="!store.runsFor(t.id).length" class="th-empty">No runs yet.</div>
            <button
              v-for="run in store.runsFor(t.id)"
              :key="run.id"
              class="run-row"
              :class="{ unopened: !run.opened && run.status !== 'running' }"
              :disabled="!run.session_id"
              @click="openRun(t, run)"
            >
              <span class="run-status" :class="run.status">{{ statusLabel(run) }}</span>
              <span class="run-when">{{ fmt(run.scheduled_for || run.created_at) }}</span>
              <span class="run-trigger">{{ triggerLabel(run) }}</span>
              <span v-if="run.error" class="run-error" :title="run.error">{{ run.error }}</span>
              <span v-if="!run.opened && run.status !== 'running'" class="run-new">New</span>
              <span class="run-open" aria-hidden="true">›</span>
            </button>
          </div>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
#scheduled-view {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  min-width: 0;
}
.sv-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  padding: 20px 24px 16px;
  border-bottom: 1px solid var(--border);
}
.sv-heading h1 { font-size: 20px; font-weight: 700; color: var(--text); margin: 0; }
.sv-sub { font-size: 13px; color: var(--text2); margin: 4px 0 0; max-width: 560px; line-height: 1.4; }

.sv-body { flex: 1; overflow-y: auto; padding: 18px 24px 32px; }

.sv-empty {
  display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: 8px; text-align: center; color: var(--text2); padding: 60px 20px;
}
.sv-empty-icon { font-size: 40px; }
.sv-empty-title { font-size: 16px; font-weight: 600; color: var(--text); }
.sv-empty-text { font-size: 13px; color: var(--text3); margin-bottom: 8px; }

.task-card {
  border: 1px solid var(--border);
  border-radius: 12px;
  background: var(--surface);
  margin-bottom: 12px;
  overflow: hidden;
  max-width: 860px;
}
.task-card.disabled { opacity: .72; }

.task-pending {
  display: flex; align-items: center; justify-content: space-between; gap: 12px;
  padding: 10px 14px;
  background: rgba(250, 204, 21, .12);
  border-bottom: 1px solid var(--border);
}
.tp-text { font-size: 12.5px; color: var(--text); }
.tp-actions { display: flex; gap: 6px; flex-shrink: 0; }

.task-main { display: flex; align-items: flex-start; gap: 16px; padding: 14px 16px; }
.task-info { flex: 1; min-width: 0; }
.task-title-row { display: flex; align-items: center; gap: 8px; }
.task-name { font-size: 15px; font-weight: 600; color: var(--text); }
.task-badge {
  font-size: 10px; font-weight: 600; text-transform: uppercase; letter-spacing: .04em;
  color: var(--text3); border: 1px solid var(--border); border-radius: 5px; padding: 1px 5px;
}
.task-summary { font-size: 13px; color: var(--text); margin-top: 3px; }
.task-meta { font-size: 12px; color: var(--text3); margin-top: 3px; display: flex; gap: 6px; align-items: center; }
.task-dot { color: var(--text3); }
.task-target { color: var(--text2); }
.task-prompt {
  font-size: 12.5px; color: var(--text2); margin-top: 8px; line-height: 1.4;
  display: -webkit-box; -webkit-line-clamp: 2; line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden;
}

.task-actions { display: flex; align-items: center; gap: 6px; flex-shrink: 0; }
.task-toggle { position: relative; display: inline-flex; cursor: pointer; margin-right: 2px; }
.task-toggle input { position: absolute; opacity: 0; width: 0; height: 0; }
.tt-track {
  width: 34px; height: 20px; border-radius: 999px; background: var(--surface3);
  border: 1px solid var(--border); transition: background .15s; display: inline-flex; align-items: center; padding: 0 2px;
}
.tt-thumb {
  width: 14px; height: 14px; border-radius: 50%; background: var(--text2);
  transition: transform .15s, background .15s;
}
.task-toggle input:checked + .tt-track { background: var(--accent); border-color: transparent; }
.task-toggle input:checked + .tt-track .tt-thumb { transform: translateX(14px); background: #fff; }

.task-btn {
  padding: 5px 10px; font-size: 12px; font-weight: 500; font-family: inherit;
  border: 1px solid var(--border); border-radius: 7px; background: none; color: var(--text2);
  cursor: pointer; transition: all .12s;
}
.task-btn:hover:not(:disabled) { color: var(--text); border-color: var(--text3); background: var(--surface2); }
.task-btn:disabled { opacity: .5; cursor: default; }
.task-btn.danger:hover:not(:disabled) { color: var(--red); border-color: var(--red); background: rgba(218,63,63,.1); }

.task-history-toggle {
  display: flex; align-items: center; gap: 6px; width: 100%;
  padding: 8px 16px; background: none; border: none; border-top: 1px solid var(--border);
  color: var(--text2); font-size: 12px; font-weight: 500; font-family: inherit; cursor: pointer; text-align: left;
}
.task-history-toggle:hover { color: var(--text); background: var(--surface2); }
.tht-caret { font-size: 14px; transition: transform .12s; }
.tht-caret.open { transform: rotate(90deg); }

.task-history { padding: 4px 16px 12px; border-top: 1px solid var(--border); }
.th-empty { font-size: 12px; color: var(--text3); padding: 8px 2px; }

.run-row {
  display: flex; align-items: center; gap: 10px; width: 100%;
  padding: 8px 10px; margin-top: 4px;
  background: none; border: 1px solid var(--border); border-radius: 8px;
  color: var(--text2); font-size: 12px; font-family: inherit; cursor: pointer; text-align: left;
  transition: background .12s, border-color .12s;
}
.run-row:hover:not(:disabled) { background: var(--surface2); border-color: var(--text3); color: var(--text); }
.run-row:disabled { cursor: default; opacity: .7; }
.run-row.unopened { border-color: var(--accent); }
.run-status { font-weight: 600; flex-shrink: 0; min-width: 74px; }
.run-status.succeeded { color: #16a34a; }
.run-status.failed { color: var(--red); }
.run-status.running { color: var(--accent); }
.run-when { flex-shrink: 0; color: var(--text2); }
.run-trigger { flex-shrink: 0; color: var(--text3); }
.run-error { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--red); }
.run-new {
  margin-left: auto; flex-shrink: 0; font-size: 10px; font-weight: 700; text-transform: uppercase;
  letter-spacing: .04em; color: #fff; background: var(--accent); border-radius: 5px; padding: 1px 6px;
}
.run-open { flex-shrink: 0; color: var(--text3); font-size: 15px; }
.run-row.unopened .run-open { margin-left: 0; }

.btn {
  padding: 8px 14px; font-size: 13px; font-weight: 600; font-family: inherit;
  border-radius: 8px; cursor: pointer; border: 1px solid transparent;
  display: inline-flex; align-items: center; gap: 6px;
}
.btn-sm { padding: 5px 10px; font-size: 12px; }
.btn-primary { background: var(--accent); color: #fff; }
.btn-primary:hover:not(:disabled) { background: var(--accent-h); }
.btn-primary:disabled { opacity: .6; cursor: default; }
.btn-ghost { background: none; border-color: var(--border); color: var(--text2); }
.btn-ghost:hover:not(:disabled) { color: var(--text); border-color: var(--text3); background: var(--surface2); }
</style>
