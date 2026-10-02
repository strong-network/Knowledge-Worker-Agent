// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import * as api from '../api'
import type { ScheduledTask, TaskRun, ScheduledTaskInput } from '../api'
import { loadExpandedIds, saveExpandedIds } from '../utils/sectionState'

// Scheduled tasks store: the user's unattended prompt schedules, their
// run history, and CRUD. Mirrors the projects store shape.
export const useScheduledTasksStore = defineStore('scheduledTasks', () => {
  const tasks = ref<ScheduledTask[]>([])
  const loading = ref(false)
  // Cached runs per task (id -> runs), loaded lazily when a task is expanded.
  const runsByTask = ref<Record<string, TaskRun[]>>({})
  // Which tasks have their run history expanded in the sidebar, remembered
  // across reloads (as with projects).
  const EXPANDED_KEY = 'expandedScheduledTasks'
  const expanded = ref<Record<string, boolean>>(loadExpandedIds(EXPANDED_KEY))

  const sortedTasks = computed(() =>
    [...tasks.value].sort(
      (a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime(),
    ),
  )

  // Tasks with a missed run awaiting a user Run-now / Skip decision.
  const pendingTasks = computed(() => tasks.value.filter(t => !!t.pending_catchup_at))

  async function loadTasks() {
    loading.value = true
    try {
      tasks.value = await api.fetchScheduledTasks()
      await hydrateExpanded()
    } catch (e) {
      console.error('[scheduled-tasks] load failed', e)
    } finally {
      loading.value = false
    }
  }

  // Restored expansions never ran toggleExpanded, so their runs were never
  // fetched — they'd render as open rows with no history under them. Load those
  // now, and drop ids for tasks that no longer exist.
  async function hydrateExpanded() {
    const live = new Set(tasks.value.map(t => t.id))
    const stale = Object.keys(expanded.value).filter(id => !live.has(id))
    for (const id of stale) delete expanded.value[id]
    if (stale.length) saveExpandedIds(EXPANDED_KEY, expanded.value)

    const pending = Object.keys(expanded.value).filter(
      id => expanded.value[id] && !runsByTask.value[id],
    )
    await Promise.all(pending.map(id => loadRuns(id)))
  }

  async function toggleExpanded(id: string): Promise<void> {
    const open = !expanded.value[id]
    expanded.value[id] = open
    saveExpandedIds(EXPANDED_KEY, expanded.value)
    if (open) await loadRuns(id)
  }

  function getTask(id: string): ScheduledTask | undefined {
    return tasks.value.find(t => t.id === id)
  }

  function upsert(task: ScheduledTask) {
    const i = tasks.value.findIndex(t => t.id === task.id)
    if (i >= 0) tasks.value[i] = task
    else tasks.value.push(task)
  }

  async function createTask(input: ScheduledTaskInput): Promise<ScheduledTask> {
    const task = await api.createScheduledTask(input)
    upsert(task)
    return task
  }

  async function updateTask(id: string, input: ScheduledTaskInput): Promise<ScheduledTask> {
    const task = await api.updateScheduledTask(id, input)
    upsert(task)
    return task
  }

  async function deleteTask(id: string): Promise<void> {
    await api.deleteScheduledTask(id)
    tasks.value = tasks.value.filter(t => t.id !== id)
    delete runsByTask.value[id]
    delete expanded.value[id]
    saveExpandedIds(EXPANDED_KEY, expanded.value)
  }

  async function setEnabled(id: string, enabled: boolean): Promise<void> {
    const task = await api.setScheduledTaskEnabled(id, enabled)
    upsert(task)
  }

  async function runNow(id: string): Promise<void> {
    await api.runScheduledTaskNow(id)
    // A run row appears asynchronously; refresh shortly after so history updates.
    setTimeout(() => { void loadRuns(id) }, 1200)
  }

  async function resolvePending(id: string, action: 'run' | 'skip'): Promise<void> {
    const task = await api.resolveScheduledTaskPending(id, action)
    upsert(task)
    if (action === 'run') setTimeout(() => { void loadRuns(id) }, 1200)
  }

  async function loadRuns(id: string): Promise<void> {
    try {
      runsByTask.value[id] = await api.fetchScheduledTaskRuns(id)
    } catch (e) {
      console.error('[scheduled-tasks] load runs failed', e)
    }
  }

  function runsFor(id: string): TaskRun[] {
    return runsByTask.value[id] || []
  }

  async function markRunOpened(runId: string, taskId: string): Promise<void> {
    try {
      await api.markTaskRunOpened(runId)
      const runs = runsByTask.value[taskId]
      if (runs) {
        const r = runs.find(x => x.id === runId)
        if (r) r.opened = true
      }
    } catch (e) {
      console.error('[scheduled-tasks] mark opened failed', e)
    }
  }

  return {
    tasks, loading, runsByTask, expanded,
    sortedTasks, pendingTasks,
    loadTasks, getTask, createTask, updateTask, deleteTask,
    setEnabled, runNow, resolvePending, loadRuns, runsFor, markRunOpened,
    toggleExpanded,
  }
})
