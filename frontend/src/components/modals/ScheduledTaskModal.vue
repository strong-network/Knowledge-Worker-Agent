<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useScheduledTasksStore } from '../../stores/scheduledTasks'
import { useSessionsStore } from '../../stores/sessions'
import { useProjectsStore } from '../../stores/projects'
import { useProvidersStore } from '../../stores/providers'
import { useUiStore } from '../../stores/ui'
import type { TaskRepeat } from '../../api'
import { resolvePreset } from '../../utils/models'

// Create or edit a scheduled task. Day/time is entered in the
// workspace's local time; we convert to/from RFC3339 for the backend.
const store = useScheduledTasksStore()
const sessions = useSessionsStore()
const projectsStore = useProjectsStore()
const providers = useProvidersStore()
const ui = useUiStore()

const editingId = computed(() => ui.editingScheduledTaskId)
const isEdit = computed(() => !!editingId.value)

const name = ref('')
const prompt = ref('')
const repeat = ref<TaskRepeat>('none')
// The task's model is chosen via the same two friendly presets as the chat
// composer — Default and Thinking — rather than a raw model list, so a task can
// never pin a model the backend doesn't serve. The presets resolve to concrete
// model ids at save time (Default -> latest Sonnet, Thinking -> latest Opus),
// mirroring the composer's agent selector.
type ModelPreset = 'default' | 'thinking'
const modelPreset = ref<ModelPreset>('default')
const when = ref('') // datetime-local value: YYYY-MM-DDTHH:mm
const busy = ref(false)
const error = ref('')

const MODEL_PRESETS: { value: ModelPreset; label: string }[] = [
  { value: 'default', label: 'Default' },
  { value: 'thinking', label: 'Thinking' },
]

// Resolve a preset to a concrete model id from the available list. Returns ''
// when no matching model is available yet (e.g. before sign-in), which the
// backend treats as the workspace default. What each preset means comes from
// the provider registry when the workspace's providers nominate models, and
// from the Claude heuristic otherwise.
function resolvePresetModel(preset: ModelPreset): string {
  return resolvePreset(preset, sessions.models, providers.presets)
}

// Map a stored model id back to a preset for the dropdown. Only the resolved
// Thinking model counts as Thinking; everything else — empty (workspace
// default), the Default model, or any legacy model a task may still carry —
// shows as Default, matching the composer's own preset detection.
function presetFromModel(m: string): ModelPreset {
  const thinking = resolvePresetModel('thinking')
  return m && thinking && m === thinking ? 'thinking' : 'default'
}
// Scheduled tasks: where the task's runs execute. 'own' auto-creates a dedicated folder
// (the default, blank workdir); 'project' binds every run to a project's
// shared workspace directory so results accumulate alongside the project.
const workspaceMode = ref<'own' | 'project'>('own')
const projectId = ref('')

// Models offered in the picker (empty until the user is signed in).
const models = computed(() => sessions.models)

// The user's projects, offered as run targets. Sorted by recency.
const projects = computed(() => projectsStore.sortedProjects)
const hasProjects = computed(() => projects.value.length > 0)

const REPEATS: { value: TaskRepeat; label: string }[] = [
  { value: 'none', label: 'Once' },
  { value: 'daily', label: 'Every day' },
  { value: 'weekdays', label: 'Weekdays (Mon–Fri)' },
  { value: 'weekly', label: 'Every week' },
]

// datetime-local <-> RFC3339 (local time) helpers.
function toLocalInput(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}
function rfc3339FromLocalInput(v: string): string {
  // Interpret the datetime-local value as local time, emit an absolute instant.
  const d = new Date(v)
  return d.toISOString()
}

// The workspace binding is fixed at creation: the whole control
// is read-only when editing an existing task.
const workspaceLocked = computed(() => isEdit.value)

const canSave = computed(
  () =>
    prompt.value.trim().length > 0 &&
    when.value.trim().length > 0 &&
    !busy.value &&
    // A project run must have a project chosen (unless editing, where it's fixed).
    (workspaceLocked.value || workspaceMode.value === 'own' || projectId.value !== ''),
)

onMounted(() => {
  // Ensure projects are available for the Workspace selector.
  if (!projectsStore.projects.length) void projectsStore.loadProjects()
  if (isEdit.value) {
    const t = store.getTask(editingId.value as string)
    if (t) {
      name.value = t.name
      prompt.value = t.prompt
      repeat.value = t.repeat
      modelPreset.value = presetFromModel(t.model || '')
      const base = t.next_run_at || t.first_run_at
      when.value = base ? toLocalInput(new Date(base)) : defaultWhen()
      // Reflect the existing binding: match the task's workdir to a project's
      // shared workspace; otherwise it's an own auto-created folder.
      const bound = t.workdir
        ? projectsStore.projects.find(p => p.workspace_path === t.workdir)
        : undefined
      if (bound) {
        workspaceMode.value = 'project'
        projectId.value = bound.id
      }
      return
    }
  }
  when.value = defaultWhen()
})

// Default to the next round hour, at least a few minutes out.
function defaultWhen(): string {
  const d = new Date(Date.now() + 60 * 60 * 1000)
  d.setMinutes(0, 0, 0)
  return toLocalInput(d)
}

async function save() {
  if (!canSave.value) return
  busy.value = true
  error.value = ''
  try {
    // Resolve the working directory from the Workspace choice. 'own' sends a
    // blank workdir so the backend auto-provisions a folder; 'project'
    // binds runs to the chosen project's shared workspace. On edit the
    // backend ignores workdir (immutable), so we can safely send the resolved
    // value.
    let workdir: string | undefined
    if (workspaceMode.value === 'project') {
      const p = projectsStore.getProject(projectId.value)
      workdir = p?.workspace_path || undefined
    }
    const input = {
      name: name.value.trim() || undefined,
      prompt: prompt.value.trim(),
      workdir,
      repeat: repeat.value,
      model: resolvePresetModel(modelPreset.value) || undefined,
      first_run_at: rfc3339FromLocalInput(when.value),
    }
    if (isEdit.value) await store.updateTask(editingId.value as string, input)
    else await store.createTask(input)
    ui.closeModal()
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not save the scheduled task'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="modal" @click.stop style="width:520px">
    <h2>{{ isEdit ? 'Edit scheduled task' : 'New scheduled task' }}</h2>
    <p class="modal-subtitle">
      Run a prompt unattended on a schedule. Each run opens as its own chat you
      can review and continue. All runs share one working folder.
    </p>

    <label class="st-label">Name <span class="st-opt">(optional)</span></label>
    <input
      v-model="name"
      class="form-input"
      placeholder="e.g. Nightly dependency check"
      :disabled="busy"
    />

    <label class="st-label">Prompt</label>
    <textarea
      v-model="prompt"
      class="form-input st-textarea"
      rows="4"
      placeholder="What should the agent do each time this runs?"
      :disabled="busy"
    />

    <div class="st-row">
      <div class="st-col">
        <label class="st-label">First run</label>
        <input v-model="when" type="datetime-local" class="form-input" :disabled="busy" />
      </div>
      <div class="st-col">
        <label class="st-label">Repeat</label>
        <select v-model="repeat" class="form-input" :disabled="busy">
          <option v-for="r in REPEATS" :key="r.value" :value="r.value">{{ r.label }}</option>
        </select>
      </div>
    </div>

    <label class="st-label">Model</label>
    <select v-model="modelPreset" class="form-input" :disabled="busy">
      <option v-for="p in MODEL_PRESETS" :key="p.value" :value="p.value">{{ p.label }}</option>
    </select>

    <label class="st-label">Workspace</label>
    <p class="st-desc">Where this task's runs read and write files. All runs share one location.</p>
    <fieldset class="st-workspace" :disabled="busy || workspaceLocked">
      <label class="st-radio">
        <input type="radio" value="own" v-model="workspaceMode" />
        <span class="st-radio-body">
          <span class="st-radio-title">Own folder (auto-created)</span>
          <span class="st-radio-sub">A dedicated folder just for this task.</span>
        </span>
      </label>
      <label class="st-radio" :class="{ 'st-radio-disabled': !hasProjects && !workspaceLocked }">
        <input type="radio" value="project" v-model="workspaceMode" :disabled="!hasProjects && !workspaceLocked" />
        <span class="st-radio-body">
          <span class="st-radio-title">Run this task as part of a project</span>
          <span class="st-radio-sub">
            <template v-if="!hasProjects && !workspaceLocked">You don't have any projects yet.</template>
            <template v-else>Runs share the project's workspace, building on earlier runs.</template>
          </span>
        </span>
      </label>
      <select
        v-model="projectId"
        class="form-input st-project-select"
        :disabled="workspaceMode !== 'project' || busy || workspaceLocked"
      >
        <option value="" disabled>Choose a project…</option>
        <option v-for="p in projects" :key="p.id" :value="p.id">{{ p.name }}</option>
      </select>
    </fieldset>
    <p v-if="workspaceLocked" class="st-hint st-hint-tight">
      A task's workspace is fixed when it's created and can't be changed here.
    </p>

    <p class="st-hint">
      Times use this workspace's local time. Runs only fire while the workspace
      is up; a run missed while it was off is caught up on next start (or, if too
      stale, offered as a pending decision).
    </p>

    <p v-if="error" class="st-error">{{ error }}</p>

    <div class="modal-footer">
      <button class="btn btn-ghost" @click="ui.closeModal()" :disabled="busy">Cancel</button>
      <button class="btn btn-primary" @click="save" :disabled="!canSave">
        {{ busy ? 'Saving…' : (isEdit ? 'Save changes' : 'Create task') }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.st-label {
  display: block; font-size: 12px; font-weight: 600; color: var(--text2);
  margin: 12px 0 5px;
}
.st-opt { font-weight: 400; color: var(--text3); }
.form-input {
  width: 100%; padding: 9px 11px; font-size: 14px;
  border: 1px solid var(--border); border-radius: 8px;
  background: var(--surface); color: var(--text); font-family: inherit;
}
.form-input:focus { outline: none; border-color: var(--accent); }
.st-textarea { resize: vertical; min-height: 72px; line-height: 1.4; }
.st-row { display: flex; gap: 12px; }
.st-col { flex: 1; min-width: 0; }
.st-hint { font-size: 12px; color: var(--text3); margin: 10px 0 0; line-height: 1.4; }
.st-hint-tight { margin-top: 6px; }
.st-desc { font-size: 12px; color: var(--text3); margin: 0 0 8px; line-height: 1.4; }
.st-workspace {
  border: none; padding: 0; margin: 0; min-width: 0;
  display: flex; flex-direction: column; gap: 8px;
}
.st-workspace:disabled { opacity: 0.65; }
.st-radio {
  display: flex; align-items: flex-start; gap: 9px; cursor: pointer;
  padding: 9px 11px; border: 1px solid var(--border); border-radius: 8px;
  background: var(--surface);
}
.st-radio input { margin-top: 2px; accent-color: var(--accent); }
.st-radio-body { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.st-radio-title { font-size: 13px; font-weight: 600; color: var(--text); }
.st-radio-sub { font-size: 12px; color: var(--text3); line-height: 1.35; }
.st-radio-disabled { opacity: 0.55; cursor: not-allowed; }
.st-project-select { margin-top: 2px; }
.st-project-select:disabled { opacity: 0.55; }
.st-error { color: var(--red); font-size: 12px; margin-top: 10px; }
</style>
