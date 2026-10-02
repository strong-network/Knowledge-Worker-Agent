<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, reactive, computed, onMounted } from 'vue'
import { useUiStore } from '../../stores/ui'
import { useSessionsStore } from '../../stores/sessions'
import {
  listBuilderAgents, getBuilderAgent, createBuilderAgent,
  updateBuilderAgent, deleteBuilderAgent, fetchModels,
  exportBuilderAgent, previewImportAgent, commitImportAgent,
  polishAgentPrompt, improveAgentDescription,
  AgentApiError, AGENT_CAPABILITIES,
  type AgentListItem, type AgentDraft, type AgentCapability,
  type PermissionAction, type AgentFieldError, type ImportPreview,
} from '../../api'

// Agent Builder. A list of the user's own agents, a three-tab editor
// (Simple / Advanced / Permissions), and an import preview. The backend owns
// file location and format; this form just produces the typed AgentDraft.

const ui = useUiStore()
const sessionsStore = useSessionsStore()

type View = 'list' | 'edit' | 'import'
type Tab = 'simple' | 'advanced' | 'permissions'

const view = ref<View>('list')
const tab = ref<Tab>('simple')

const agents = ref<AgentListItem[]>([])
const models = ref<string[]>([])
const loading = ref(false)
const listError = ref('')

// Editor state. editingId is '' for a new agent, otherwise the slug being edited.
const editingId = ref('')
const saving = ref(false)
const saveError = ref('')
const fieldErrors = reactive<Record<string, string>>({})

// A friendly, human-readable label for each capability.
const CAP_LABELS: Record<AgentCapability, string> = {
  read: 'Read files',
  edit: 'Edit files',
  glob: 'Find files by name',
  grep: 'Search file contents',
  list: 'List directories',
  bash: 'Run commands',
  task: 'Call other agents',
  external_directory: 'Access files outside the project',
  todowrite: 'Manage its to-do list',
  webfetch: 'Fetch from the web',
  websearch: 'Search the web',
  lsp: 'Use language tools',
  skill: 'Run skills',
  question: 'Ask you questions',
  doom_loop: 'Recover when stuck',
}

// Plain-language tooltip for each capability's "?" affordance.
const CAP_HELP: Record<AgentCapability, string> = {
  read: 'Open and read files in your workspace.',
  edit: 'Create or change files.',
  glob: 'Find files by name or pattern.',
  grep: 'Search inside files for text.',
  list: 'List folders and their contents.',
  bash: 'Run terminal commands.',
  task: 'Hand work off to other agents.',
  external_directory: 'Reach files outside the current project folder.',
  todowrite: 'Keep its own task checklist.',
  webfetch: 'Open a specific web page or URL.',
  websearch: 'Search the web for information.',
  lsp: 'Use code intelligence like definitions and errors.',
  skill: 'Load specialized skill instructions.',
  question: 'Pause to ask you a question.',
  doom_loop: 'Keep trying to get itself unstuck.',
}

// Display order for the permissions list (matches the friendly grouping).
const CAP_ORDER: AgentCapability[] = [
  'read', 'edit', 'glob', 'grep', 'list', 'bash', 'task',
  'external_directory', 'todowrite', 'webfetch', 'websearch',
  'lsp', 'skill', 'question', 'doom_loop',
]

// Short help text for editor fields.
const FIELD_HELP = {
  name: 'Shown in the agent picker and used for the file name.',
  description: "Tells the assistant when to reach for this agent.",
  instructions: 'The system prompt: how the agent should behave.',
  model: 'Which model this agent runs on.',
  mode: 'Whether it can be picked directly, only delegated to, or both.',
  temperature: 'Higher is more varied wording; lower is more consistent.',
  top_p: 'Narrows word choice to the most likely options.',
  steps: 'Caps how many actions it takes in a single turn.',
  color: 'A label color for this agent in the picker.',
}

// Blank editable draft. permission.rules is a plain object we mutate in place.
// New agents default to "ask first" everywhere — the safe, explicit baseline.
function blankDraft(): AgentDraft {
  return {
    name: '', description: '', prompt: '',
    model: '', mode: '', temperature: null, top_p: null, steps: null,
    color: '', hidden: false, disable: false,
    permission: { global: 'ask', rules: {} },
  }
}

const draft = reactive<AgentDraft>(blankDraft())

// Per-capability override map (''=inherit the default).
const rules = reactive<Record<string, PermissionAction | ''>>({})

function resetDraft(src?: AgentDraft) {
  const d = src ? { ...blankDraft(), ...src } : blankDraft()
  Object.assign(draft, d)
  draft.permission = draft.permission || { global: '', rules: {} }
  for (const cap of AGENT_CAPABILITIES) rules[cap] = ''
  const r = draft.permission.rules || {}
  for (const cap of AGENT_CAPABILITIES) {
    if (r[cap]) rules[cap] = r[cap] as PermissionAction
  }
  for (const k of Object.keys(fieldErrors)) delete fieldErrors[k]
  saveError.value = ''
}

// Re-reads this modal's list, and with it the shared agent list every other
// surface renders from.
//
// The two are fed by different endpoints — this management list by
// /api/agent-builder/agents, the composer's picker by the sessions store's
// /api/agents — so refreshing only this one left an agent the user had just
// built missing from the picker until something else happened to re-fetch
// (opening New Chat or Session Settings, or reloading the app). Refreshing
// both here, rather than at each call site, means a future mutation cannot
// reintroduce that gap by forgetting one of them.
//
// The shared reload is fired first and deliberately not awaited: it must not
// be gated on this modal's own list request, which can fail on its own and
// would otherwise leave the picker stale after a save that did succeed.
async function refresh() {
  loading.value = true
  listError.value = ''
  sessionsStore.loadAgents()
  try {
    agents.value = await listBuilderAgents()
  } catch (e: any) {
    listError.value = e?.message || String(e)
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  refresh()
  try { models.value = await fetchModels() } catch { /* non-fatal */ }
})

function newAgent() {
  editingId.value = ''
  resetDraft()
  tab.value = 'simple'
  view.value = 'edit'
}

async function editAgent(item: AgentListItem) {
  editingId.value = item.id
  saveError.value = ''
  try {
    const full = await getBuilderAgent(item.id)
    resetDraft(full)
  } catch (e: any) {
    resetDraft()
    saveError.value = e?.message || String(e)
  }
  tab.value = 'simple'
  view.value = 'edit'
}

function backToList() {
  view.value = 'list'
  refresh()
}

// Preview the file name the current draft name will produce.
const savedFilename = computed(() => {
  const slug = draft.name.trim().toLowerCase()
    .replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '')
  return slug ? `${slug}.md` : ''
})

// Assemble the wire payload from the reactive form.
function buildPayload(): AgentDraft {
  const outRules: Partial<Record<AgentCapability, PermissionAction>> = {}
  for (const cap of AGENT_CAPABILITIES) {
    const v = rules[cap]
    if (v) outRules[cap] = v
  }
  const global = draft.permission?.global || ''
  const permission =
    global || Object.keys(outRules).length
      ? { global: global || undefined, rules: outRules }
      : null
  return {
    name: draft.name.trim(),
    description: draft.description,
    prompt: draft.prompt,
    model: draft.model || undefined,
    mode: draft.mode || undefined,
    temperature: draft.temperature ?? null,
    top_p: draft.top_p ?? null,
    steps: draft.steps ?? null,
    color: draft.color || undefined,
    hidden: !!draft.hidden,
    disable: !!draft.disable,
    permission,
  }
}

function applyFieldErrors(errs: AgentFieldError[] | undefined) {
  for (const k of Object.keys(fieldErrors)) delete fieldErrors[k]
  if (!errs) return
  for (const e of errs) fieldErrors[e.field] = e.message
  const first = errs[0]?.field || ''
  if (first === 'name') tab.value = 'simple'
  else if (first.startsWith('permission')) tab.value = 'permissions'
  else tab.value = 'advanced'
}

async function save() {
  saving.value = true
  saveError.value = ''
  for (const k of Object.keys(fieldErrors)) delete fieldErrors[k]
  const payload = buildPayload()
  try {
    if (editingId.value) await updateBuilderAgent(editingId.value, payload)
    else await createBuilderAgent(payload)
    ;(window as any).showToast?.(editingId.value ? 'Agent updated' : 'Agent created')
    backToList()
  } catch (e: any) {
    if (e instanceof AgentApiError) {
      saveError.value = e.message
      if (e.status === 422) applyFieldErrors(e.fields)
    } else {
      saveError.value = e?.message || String(e)
    }
  } finally {
    saving.value = false
  }
}

// ── Permissions helpers ─────────────────────────────────────────────────────
// The effective action shown for a capability: its explicit rule, else the
// global default. Clicking a value equal to the default clears the rule so the
// saved payload stays minimal while the row still reflects the right state.
const globalAction = computed<PermissionAction>(
  () => (draft.permission?.global as PermissionAction) || 'ask',
)
function effAction(cap: AgentCapability): PermissionAction {
  return (rules[cap] as PermissionAction) || globalAction.value
}
function setRule(cap: AgentCapability, val: PermissionAction) {
  rules[cap] = val === (draft.permission?.global || '') ? '' : val
}
function setGlobal(val: PermissionAction) {
  if (draft.permission) draft.permission.global = val
}

// ── Writing assists ─────────────────────────────────────────────────────────
const polishing = ref(false)
const improving = ref(false)
const assistError = ref('')

async function polishPrompt() {
  if (!draft.prompt.trim() || polishing.value) return
  polishing.value = true
  assistError.value = ''
  try {
    draft.prompt = await polishAgentPrompt({
      name: draft.name, description: draft.description, prompt: draft.prompt,
    })
    ;(window as any).showToast?.('Instructions polished')
  } catch (e: any) {
    assistError.value = e?.message || String(e)
  } finally {
    polishing.value = false
  }
}

async function improveDescription() {
  if (!draft.description.trim() || improving.value) return
  improving.value = true
  assistError.value = ''
  try {
    draft.description = await improveAgentDescription({
      name: draft.name, description: draft.description, prompt: draft.prompt,
    })
    ;(window as any).showToast?.('Description improved')
  } catch (e: any) {
    assistError.value = e?.message || String(e)
  } finally {
    improving.value = false
  }
}

const confirmingDelete = ref('')
async function doDelete(item: AgentListItem) {
  try {
    await deleteBuilderAgent(item.id)
    ;(window as any).showToast?.('Agent deleted')
    confirmingDelete.value = ''
    refresh()
  } catch (e: any) {
    listError.value = e?.message || String(e)
    confirmingDelete.value = ''
  }
}

// ── Export ──────────────────────────────────────────────────────────────────
async function exportAgent(item: AgentListItem) {
  try {
    await exportBuilderAgent(item.id, item.filename)
  } catch (e: any) {
    listError.value = e?.message || String(e)
  }
}

// ── Import ──────────────────────────────────────────────────────────────────
const fileInput = ref<HTMLInputElement | null>(null)
const importPreview = ref<ImportPreview | null>(null)
const importContent = ref('')
const importFilename = ref('')
const importName = ref('')
const importError = ref('')
const importBusy = ref(false)

function pickFile() {
  importError.value = ''
  fileInput.value?.click()
}

async function onFileChosen(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  importError.value = ''
  importName.value = ''
  try {
    importContent.value = await file.text()
    importFilename.value = file.name
    await runPreview()
    view.value = 'import'
  } catch (e: any) {
    importError.value = e?.message || String(e)
  }
}

async function runPreview() {
  try {
    importPreview.value = await previewImportAgent(
      importFilename.value, importContent.value, importName.value || undefined,
    )
    importError.value = ''
  } catch (e: any) {
    importPreview.value = null
    importError.value = e?.message || String(e)
  }
}

async function onImportNameInput() {
  await runPreview()
}

const canAdopt = computed(() => {
  const p = importPreview.value
  return !!p && p.valid && !p.collision && !importBusy.value
})

async function adoptImport() {
  if (!canAdopt.value) return
  importBusy.value = true
  importError.value = ''
  try {
    await commitImportAgent(
      importFilename.value, importContent.value, importName.value || undefined,
    )
    ;(window as any).showToast?.('Agent imported')
    view.value = 'list'
    importPreview.value = null
    refresh()
  } catch (e: any) {
    importError.value = e?.message || String(e)
  } finally {
    importBusy.value = false
  }
}

function cancelImport() {
  view.value = 'list'
  importPreview.value = null
  importError.value = ''
}

function capLabel(cap: string): string {
  return (CAP_LABELS as Record<string, string>)[cap] || cap
}

const previewPermRows = computed(() => {
  const p = importPreview.value?.permission
  if (!p) return [] as { label: string; action: string }[]
  const rows: { label: string; action: string }[] = []
  if (p.global) rows.push({ label: 'Everything (default)', action: p.global })
  for (const cap of AGENT_CAPABILITIES) {
    const a = p.rules?.[cap]
    if (a) rows.push({ label: capLabel(cap), action: a })
  }
  return rows
})
</script>

<template>
  <div class="modal agent-modal" @click.stop>
    <!-- Header: shared across all views -->
    <div class="ab-head">
      <button v-if="view !== 'list'" class="ab-icon-btn ab-back" title="Back"
        @click="view === 'import' ? cancelImport() : backToList()">
        <svg viewBox="0 0 24 24" width="18" height="18"><path fill="none" stroke="currentColor" stroke-width="2"
          stroke-linecap="round" stroke-linejoin="round" d="M15 18l-6-6 6-6" /></svg>
      </button>
      <span class="ab-logo" aria-hidden="true">
        <svg viewBox="0 0 24 24" width="20" height="20"><path fill="currentColor"
          d="M9 3a2 2 0 0 0-2 2v1H4a2 2 0 0 0-2 2v3h20V8a2 2 0 0 0-2-2h-3V5a2 2 0 0 0-2-2H9zm0 2h6v1H9V5zM2 13v5a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-5h-8v1a2 2 0 0 1-4 0v-1H2z" /></svg>
      </span>
      <h2>Agent builder</h2>
      <button class="ab-icon-btn ab-close" title="Close" @click="ui.closeModal()">
        <svg viewBox="0 0 24 24" width="18" height="18"><path fill="none" stroke="currentColor" stroke-width="2"
          stroke-linecap="round" d="M6 6l12 12M18 6L6 18" /></svg>
      </button>
    </div>
    <p class="ab-subtitle">
      Build your own agents. Describe what an agent does and when to use it, and
      it's ready to run — no files to manage.
    </p>

    <!-- ── List view ────────────────────────────────────────────────── -->
    <template v-if="view === 'list'">
      <div v-if="listError" class="ab-error">{{ listError }}</div>

      <div class="ab-toolbar">
        <button class="btn btn-primary" @click="newAgent">
          <svg viewBox="0 0 24 24" width="15" height="15"><path fill="none" stroke="currentColor" stroke-width="2"
            stroke-linecap="round" d="M12 5v14M5 12h14" /></svg>
          Create agent
        </button>
        <button class="btn btn-ghost" @click="pickFile">
          <svg viewBox="0 0 24 24" width="15" height="15"><path fill="none" stroke="currentColor" stroke-width="2"
            stroke-linecap="round" stroke-linejoin="round" d="M12 15V3m0 0l-4 4m4-4l4 4M4 17v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2" /></svg>
          Import agent
        </button>
        <input ref="fileInput" type="file" accept=".md,text/markdown"
          class="ab-file-input" @change="onFileChosen" />
      </div>

      <div class="ab-list">
        <div v-if="!agents.length && !loading" class="ab-empty">
          You haven't created any agents yet. Select <strong>Create agent</strong>
          to build your first one.
        </div>
        <button v-for="a in agents" :key="a.id" class="ab-card" @click="editAgent(a)">
          <span class="ab-card-logo" aria-hidden="true">
            <svg viewBox="0 0 24 24" width="18" height="18"><path fill="currentColor"
              d="M9 3a2 2 0 0 0-2 2v1H4a2 2 0 0 0-2 2v3h20V8a2 2 0 0 0-2-2h-3V5a2 2 0 0 0-2-2H9zm0 2h6v1H9V5zM2 13v5a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-5h-8v1a2 2 0 0 1-4 0v-1H2z" /></svg>
          </span>
          <span class="ab-card-body">
            <span class="ab-card-name">
              {{ a.name }}
              <span v-if="a.hidden" class="ab-badge">Hidden</span>
              <span v-if="a.disable" class="ab-badge off">Off</span>
            </span>
            <span class="ab-card-desc">{{ a.description || 'No description' }}</span>
            <span class="ab-card-file">{{ a.filename }}</span>
          </span>
          <span class="ab-card-actions">
            <template v-if="confirmingDelete === a.id">
              <span class="ab-confirm" @click.stop>
                Delete?
                <button class="ab-link-danger" @click.stop="doDelete(a)">Yes</button>
                <button class="ab-link" @click.stop="confirmingDelete = ''">No</button>
              </span>
            </template>
            <template v-else>
              <span class="ab-icon-btn" title="Edit" @click.stop="editAgent(a)">
                <svg viewBox="0 0 24 24" width="15" height="15"><path fill="none" stroke="currentColor" stroke-width="2"
                  stroke-linecap="round" stroke-linejoin="round" d="M12 20h9M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4 12.5-12.5z" /></svg>
              </span>
              <span class="ab-icon-btn" title="Export" @click.stop="exportAgent(a)">
                <svg viewBox="0 0 24 24" width="15" height="15"><path fill="none" stroke="currentColor" stroke-width="2"
                  stroke-linecap="round" stroke-linejoin="round" d="M12 3v12m0 0l-4-4m4 4l4-4M4 17v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2" /></svg>
              </span>
              <span class="ab-icon-btn danger" title="Delete" @click.stop="confirmingDelete = a.id">
                <svg viewBox="0 0 24 24" width="15" height="15"><path fill="none" stroke="currentColor" stroke-width="2"
                  stroke-linecap="round" stroke-linejoin="round" d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2m2 0v14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2V6" /></svg>
              </span>
            </template>
          </span>
        </button>
      </div>
    </template>

    <!-- ── Import preview view ──────────────────────────────────────── -->
    <template v-else-if="view === 'import'">
      <div v-if="importError" class="ab-error">{{ importError }}</div>

      <div v-if="importPreview" class="ab-pane">
        <p class="ab-help">
          Review this agent before adding it to your workspace. It carries its own
          instructions and permissions.
        </p>

        <div v-if="!importPreview.valid" class="ab-error">
          This file can't be imported:
          <ul class="ab-err-list">
            <li v-for="(e, i) in importPreview.errors" :key="i">{{ e.message }}</li>
          </ul>
        </div>

        <div class="form-row">
          <label>Name</label>
          <input v-model="importName" @input="onImportNameInput"
            :placeholder="importPreview.name" />
          <small v-if="importPreview.collision" class="err">
            You already have an agent named “{{ importPreview.name }}”. Enter a
            different name to import this one.
          </small>
          <small v-else>Will be saved as <code>{{ importPreview.filename }}</code>.</small>
        </div>

        <div class="ab-preview-block">
          <div class="ab-preview-label">Description</div>
          <div class="ab-preview-text">{{ importPreview.description || 'None' }}</div>
        </div>

        <div v-if="importPreview.requires" class="ab-preview-block">
          <div class="ab-preview-label">Author-declared dependencies</div>
          <div class="ab-preview-text ab-requires">{{ importPreview.requires }}</div>
          <small>These aren't bundled with the file — make sure your workspace has them.</small>
        </div>

        <div v-if="importPreview.model" class="ab-preview-block">
          <div class="ab-preview-label">Model</div>
          <div class="ab-preview-text">
            <code>{{ importPreview.model }}</code>
            <span v-if="!importPreview.model_available" class="ab-warn">
              ⚠ Not available in your workspace — you can import it, then pick a model you can access.
            </span>
          </div>
        </div>

        <div class="ab-preview-block">
          <div class="ab-preview-label">Requested permissions</div>
          <div v-if="!previewPermRows.length" class="ab-preview-text">
            Uses your defaults (no special permissions requested).
          </div>
          <div v-else class="ab-perm-rows">
            <div v-for="(row, i) in previewPermRows" :key="i" class="ab-perm-row">
              <span>{{ row.label }}</span>
              <span class="ab-perm-action" :class="row.action">{{ row.action }}</span>
            </div>
          </div>
        </div>
      </div>

      <div class="modal-actions">
        <button class="btn btn-ghost" @click="cancelImport">Cancel</button>
        <button class="btn btn-primary" @click="adoptImport" :disabled="!canAdopt">
          {{ importBusy ? 'Importing…' : 'Import agent' }}
        </button>
      </div>
    </template>

    <!-- ── Editor view ──────────────────────────────────────────────── -->
    <template v-else>
      <div v-if="saveError" class="ab-error">{{ saveError }}</div>

      <div class="modal-tabs" role="tablist">
        <button class="modal-tab" :class="{ active: tab === 'simple' }" @click="tab = 'simple'">Simple</button>
        <button class="modal-tab" :class="{ active: tab === 'advanced' }" @click="tab = 'advanced'">Advanced</button>
        <button class="modal-tab" :class="{ active: tab === 'permissions' }" @click="tab = 'permissions'">Permissions</button>
      </div>

      <div class="ab-pane">
        <!-- Simple -->
        <div v-show="tab === 'simple'">
          <div class="form-row">
            <label>Name <span class="ab-q" :title="FIELD_HELP.name">?</span></label>
            <input v-model="draft.name" placeholder="e.g. Release notes writer" />
            <small v-if="fieldErrors.name" class="err">{{ fieldErrors.name }}</small>
            <small v-else-if="savedFilename">Saved as <code>{{ savedFilename }}</code></small>
            <small v-else>Becomes the agent's file name.</small>
          </div>
          <div class="form-row">
            <label class="ab-label-row">
              <span>Description <span class="ab-q" :title="FIELD_HELP.description">?</span></span>
              <button type="button" class="ab-assist" :disabled="improving || !draft.description.trim()"
                @click="improveDescription">
                <svg viewBox="0 0 24 24" width="13" height="13"><path fill="currentColor"
                  d="M11 3l1.9 4.6L17.5 9.5 12.9 11.4 11 16l-1.9-4.6L4.5 9.5 9.1 7.6 11 3zm7 9l.9 2.1L21 15l-2.1.9L18 18l-.9-2.1L15 15l2.1-.9L18 12z" /></svg>
                {{ improving ? 'Improving…' : 'Improve description' }}
              </button>
            </label>
            <textarea v-model="draft.description" rows="3"
              placeholder="When should the assistant use this agent? Start a line with 'Requires:' to note anything it depends on."></textarea>
          </div>
          <div class="form-row">
            <label class="ab-label-row">
              <span>Instructions <span class="ab-q" :title="FIELD_HELP.instructions">?</span></span>
              <button type="button" class="ab-assist" :disabled="polishing || !draft.prompt.trim()"
                @click="polishPrompt">
                <svg viewBox="0 0 24 24" width="13" height="13"><path fill="currentColor"
                  d="M11 3l1.9 4.6L17.5 9.5 12.9 11.4 11 16l-1.9-4.6L4.5 9.5 9.1 7.6 11 3zm7 9l.9 2.1L21 15l-2.1.9L18 18l-.9-2.1L15 15l2.1-.9L18 12z" /></svg>
                {{ polishing ? 'Polishing…' : 'Polish with AI' }}
              </button>
            </label>
            <textarea v-model="draft.prompt" rows="9"
              placeholder="Who this agent is, how it behaves, and what it should (and shouldn't) do."></textarea>
          </div>
          <small v-if="assistError" class="err">{{ assistError }}</small>
        </div>

        <!-- Advanced -->
        <div v-show="tab === 'advanced'">
          <div class="form-grid">
            <div class="form-row">
              <label>Model <span class="ab-q" :title="FIELD_HELP.model">?</span></label>
              <select v-model="draft.model">
                <option value="">Default (OpenCode chooses)</option>
                <option v-for="m in models" :key="m" :value="m">{{ m }}</option>
              </select>
            </div>
            <div class="form-row">
              <label>Agent type <span class="ab-q" :title="FIELD_HELP.mode">?</span></label>
              <select v-model="draft.mode">
                <option value="">Inherit default</option>
                <option value="subagent">Subagent</option>
                <option value="primary">Primary</option>
                <option value="all">All</option>
              </select>
              <small v-if="fieldErrors.mode" class="err">{{ fieldErrors.mode }}</small>
            </div>
            <div class="form-row">
              <label>Response variability <span class="ab-q" :title="FIELD_HELP.temperature">?</span></label>
              <input v-model.number="draft.temperature" type="number" step="0.1" min="0" max="2" placeholder="0.7" />
              <small v-if="fieldErrors.temperature" class="err">{{ fieldErrors.temperature }}</small>
            </div>
            <div class="form-row">
              <label>Response focus <span class="ab-q" :title="FIELD_HELP.top_p">?</span></label>
              <input v-model.number="draft.top_p" type="number" step="0.05" min="0" max="1" placeholder="1.0" />
              <small v-if="fieldErrors.top_p" class="err">{{ fieldErrors.top_p }}</small>
            </div>
            <div class="form-row">
              <label>Maximum steps <span class="ab-q" :title="FIELD_HELP.steps">?</span></label>
              <input v-model.number="draft.steps" type="number" step="1" min="0" placeholder="default" />
              <small v-if="fieldErrors.steps" class="err">{{ fieldErrors.steps }}</small>
            </div>
            <div class="form-row">
              <label>Color <span class="ab-q" :title="FIELD_HELP.color">?</span></label>
              <input v-model="draft.color" type="color" class="ab-color" />
            </div>
          </div>

          <div class="ab-toggle-row">
            <span>Hide from the agent list</span>
            <label class="toggle">
              <input type="checkbox" v-model="draft.hidden" />
              <span class="slider"></span>
            </label>
          </div>
          <div class="ab-toggle-row">
            <span>Turn off this agent</span>
            <label class="toggle">
              <input type="checkbox" v-model="draft.disable" />
              <span class="slider"></span>
            </label>
          </div>
        </div>

        <!-- Permissions -->
        <div v-show="tab === 'permissions'">
          <p class="ab-help">
            Decide what your agent can do on its own and when it should check with
            you first.
          </p>
          <small v-if="fieldErrors.permission" class="err block">{{ fieldErrors.permission }}</small>

          <div class="ab-perm-card ab-perm-default">
            <span class="ab-perm-name">Default for every capability</span>
            <div class="ab-seg">
              <button :class="{ active: globalAction === 'allow' }" @click="setGlobal('allow')">Allow</button>
              <button :class="{ active: globalAction === 'ask' }" @click="setGlobal('ask')">Ask first</button>
              <button :class="{ active: globalAction === 'deny' }" @click="setGlobal('deny')">Deny</button>
            </div>
          </div>

          <div v-for="cap in CAP_ORDER" :key="cap" class="ab-perm-card">
            <span class="ab-perm-name">
              {{ CAP_LABELS[cap] }} <span class="ab-q" :title="CAP_HELP[cap]">?</span>
            </span>
            <div class="ab-seg">
              <button :class="{ active: effAction(cap) === 'allow' }" @click="setRule(cap, 'allow')">Allow</button>
              <button :class="{ active: effAction(cap) === 'ask' }" @click="setRule(cap, 'ask')">Ask first</button>
              <button :class="{ active: effAction(cap) === 'deny' }" @click="setRule(cap, 'deny')">Deny</button>
            </div>
          </div>
        </div>
      </div>

      <div class="modal-actions">
        <button class="btn btn-ghost" @click="backToList">Cancel</button>
        <button class="btn btn-primary" @click="save" :disabled="saving">
          {{ saving ? 'Saving…' : 'Save agent' }}
        </button>
      </div>
    </template>
  </div>
</template>

<style scoped>
.agent-modal {
  max-width: 620px; width: 92vw; max-height: 88vh;
  display: flex; flex-direction: column; padding: 22px 24px;
}

/* Header */
.ab-head { display: flex; align-items: center; gap: 10px; }
.ab-head h2 { margin: 0; font-size: 18px; font-weight: 700; flex: 1; }
.ab-logo { color: var(--accent); display: inline-flex; }
.ab-icon-btn {
  display: inline-flex; align-items: center; justify-content: center;
  width: 30px; height: 30px; border-radius: 8px; border: none; background: none;
  color: var(--text2); cursor: pointer; transition: all .12s; flex-shrink: 0;
}
.ab-icon-btn:hover { background: var(--surface2); color: var(--text); }
.ab-icon-btn.danger:hover { color: var(--red, #f85149); background: rgba(248,81,73,.12); }
.ab-close { margin-left: auto; }
.ab-subtitle { color: var(--text2); font-size: 13px; line-height: 1.5; margin: 6px 0 4px; }

.ab-error {
  background: rgba(248,81,73,.10); border: 1px solid rgba(248,81,73,.4);
  color: var(--red, #f85149); padding: 8px 10px; border-radius: 8px; font-size: 12px;
  margin: 10px 0 0; word-break: break-word;
}

/* Help "?" affordance */
.ab-q {
  display: inline-flex; align-items: center; justify-content: center;
  width: 14px; height: 14px; border-radius: 50%; font-size: 10px; font-weight: 700;
  background: var(--surface3); color: var(--text2); cursor: help; vertical-align: middle;
  margin-left: 2px;
}

/* Toolbar */
.ab-toolbar { display: flex; gap: 10px; margin: 16px 0 4px; }
.ab-toolbar .btn { display: inline-flex; align-items: center; gap: 7px; }
.ab-file-input { display: none; }

/* List */
.ab-list { overflow-y: auto; flex: 1; min-height: 100px; margin-top: 12px; display: flex; flex-direction: column; gap: 10px; }
.ab-empty { color: var(--text2); font-size: 13px; padding: 32px 8px; text-align: center; }
.ab-card {
  display: flex; align-items: flex-start; gap: 12px; padding: 13px 14px; width: 100%;
  border: 1px solid var(--border); border-radius: 12px; background: var(--surface);
  text-align: left; cursor: pointer; transition: border-color .12s, background .12s; font-family: inherit;
}
.ab-card:hover { border-color: var(--accent); background: var(--surface2); }
.ab-card-logo { color: var(--accent); display: inline-flex; margin-top: 1px; flex-shrink: 0; }
.ab-card-body { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.ab-card-name { font-weight: 600; font-size: 14px; color: var(--text); display: flex; align-items: center; gap: 6px; }
.ab-card-desc { color: var(--text2); font-size: 12px; line-height: 1.45; }
.ab-card-file { color: var(--text2); opacity: .8; font-size: 11px; font-family: var(--mono); margin-top: 2px; }
.ab-badge {
  font-size: 10px; font-weight: 600; padding: 1px 6px; border-radius: 10px;
  background: var(--surface3); color: var(--text2); border: 1px solid var(--border);
}
.ab-badge.off { color: var(--red, #f85149); border-color: rgba(248,81,73,.4); }
.ab-card-actions { display: flex; align-items: center; gap: 2px; flex-shrink: 0; opacity: 0; transition: opacity .12s; }
.ab-card:hover .ab-card-actions { opacity: 1; }
.ab-confirm { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--text2); }
.ab-link, .ab-link-danger { background: none; border: none; cursor: pointer; font-size: 12px; font-weight: 600; font-family: inherit; padding: 2px 4px; }
.ab-link { color: var(--text2); }
.ab-link-danger { color: var(--red, #f85149); }

/* Tabs — reuse shared .modal-tabs; just spacing */
.modal-tabs { margin: 16px 0 16px; }

/* Panes & forms */
.ab-pane { overflow-y: auto; flex: 1; padding-right: 2px; }
.form-row { margin-bottom: 14px; display: flex; flex-direction: column; gap: 5px; }
.form-row label { font-size: 12px; font-weight: 600; color: var(--text); }
.form-row input, .form-row select, .form-row textarea {
  background: var(--surface2); border: 1px solid var(--border); border-radius: 8px;
  padding: 8px 10px; font-size: 13px; color: var(--text); font-family: inherit; width: 100%;
  outline: none; transition: border-color .12s;
}
.form-row input:focus, .form-row select:focus, .form-row textarea:focus { border-color: var(--accent); }
.form-row textarea { resize: vertical; line-height: 1.5; }
.form-row small { font-size: 11px; color: var(--text2); }
.form-row small code, .ab-preview-text code { font-family: var(--mono); background: var(--surface2); padding: 1px 6px; border-radius: 4px; font-size: 11px; }
.form-row small.err, small.err { color: var(--red, #f85149); }
small.err.block { display: block; margin-bottom: 10px; }
.form-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px 16px; }
.ab-color { padding: 3px !important; height: 38px; cursor: pointer; }

.ab-label-row { display: flex; align-items: center; justify-content: space-between; }
.ab-assist {
  display: inline-flex; align-items: center; gap: 5px;
  background: none; border: none; padding: 2px 4px; font-size: 12px; font-weight: 600;
  cursor: pointer; color: var(--accent); font-family: inherit;
}
.ab-assist:hover:not(:disabled) { text-decoration: underline; }
.ab-assist:disabled { opacity: .45; cursor: default; }

/* Toggle rows */
.ab-toggle-row {
  display: flex; align-items: center; justify-content: space-between;
  padding: 12px 0; border-top: 1px solid var(--border); font-size: 13px; color: var(--text);
}

/* Permissions */
.ab-help { font-size: 12px; color: var(--text2); line-height: 1.5; margin: 0 0 14px; }
.ab-perm-card {
  display: flex; align-items: center; justify-content: space-between; gap: 12px;
  padding: 11px 13px; border: 1px solid var(--border); border-radius: 10px; margin-bottom: 8px;
}
.ab-perm-default { background: var(--surface2); }
.ab-perm-name { font-size: 13px; font-weight: 600; color: var(--text); }
.ab-perm-default .ab-perm-name { font-weight: 700; }

.ab-seg { display: inline-flex; background: var(--surface2); border: 1px solid var(--border); border-radius: 8px; padding: 2px; flex-shrink: 0; }
.ab-seg button {
  border: none; background: none; padding: 5px 11px; font-size: 12px; font-weight: 500;
  color: var(--text2); cursor: pointer; border-radius: 6px; font-family: inherit; transition: all .12s;
}
.ab-seg button:hover { color: var(--text); }
.ab-seg button.active { background: var(--accent); color: #fff; font-weight: 600; }

/* Footer actions */
.modal-actions { display: flex; justify-content: flex-end; gap: 10px; margin-top: 16px; padding-top: 14px; border-top: 1px solid var(--border); }

/* Import preview */
.ab-err-list { margin: 6px 0 0; padding-left: 18px; }
.ab-preview-block { margin-bottom: 14px; }
.ab-preview-label { font-size: 11px; font-weight: 700; text-transform: uppercase; letter-spacing: .04em; color: var(--text2); margin-bottom: 4px; }
.ab-preview-text { font-size: 13px; color: var(--text); white-space: pre-wrap; }
.ab-requires { font-family: var(--mono); font-size: 12px; }
.ab-warn { display: block; margin-top: 4px; color: #d97706; font-size: 12px; }
.ab-perm-rows { display: flex; flex-direction: column; gap: 4px; }
.ab-perm-row { display: flex; align-items: center; justify-content: space-between; font-size: 13px; padding: 5px 0; border-bottom: 1px solid var(--border); }
.ab-perm-action { font-size: 11px; font-weight: 600; padding: 1px 8px; border-radius: 10px; text-transform: capitalize; }
.ab-perm-action.allow { background: rgba(34,197,94,.15); color: #16a34a; }
.ab-perm-action.ask { background: rgba(234,179,8,.15); color: #ca8a04; }
.ab-perm-action.deny { background: rgba(248,81,73,.15); color: var(--red, #f85149); }

@media (max-width: 560px) { .form-grid { grid-template-columns: 1fr; } }
</style>
