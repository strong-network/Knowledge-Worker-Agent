<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useProjectsStore } from '../../stores/projects'
import { useUiStore } from '../../stores/ui'
import { getProjectMcp, setProjectMcp, type SessionConnector } from '../../api'

// Project settings: name/description and standing instructions. Also delete.
const projectsStore = useProjectsStore()
const ui = useUiStore()

const project = computed(() => (ui.activeProjectId ? projectsStore.getProject(ui.activeProjectId) : undefined))

const name = ref('')
const description = ref('')
const instructions = ref('')
const busy = ref(false)
const error = ref('')

// Connectors: project settings is the sole edit surface for the
// project-level MCP selection, shared by every chat in the project's workspace.
const connectors = ref<SessionConnector[]>([])
const connectorsLoading = ref(false)
const connectorsBusy = ref<Record<string, boolean>>({})
const chatCount = computed(() => project.value?.chat_count ?? 0)

async function loadConnectors(pid: string) {
  connectorsLoading.value = true
  try {
    connectors.value = await getProjectMcp(pid)
  } catch {
    connectors.value = []
  } finally {
    connectorsLoading.value = false
  }
}

async function toggleConnector(c: SessionConnector) {
  const p = project.value
  if (!p || connectorsBusy.value[c.name]) return
  const next = !c.selected
  connectorsBusy.value = { ...connectorsBusy.value, [c.name]: true }
  try {
    connectors.value = await setProjectMcp(p.id, { [c.name]: next })
  } catch {
    error.value = 'Could not update connector'
  } finally {
    const b = { ...connectorsBusy.value }
    delete b[c.name]
    connectorsBusy.value = b
  }
}

function connectorDotClass(status: string): string {
  switch (status) {
    case 'connected': return 'ok'
    case 'connecting':
    case 'pending': return 'pending'
    case 'error':
    case 'failed': return 'err'
    default: return 'off'
  }
}

watch(
  project,
  (p) => {
    if (!p) return
    name.value = p.name
    description.value = p.description
    instructions.value = p.instructions
    loadConnectors(p.id)
  },
  { immediate: true },
)

async function save() {
  const p = project.value
  if (!p || busy.value) return
  const nm = name.value.trim()
  if (!nm) { error.value = 'Name is required'; return }
  busy.value = true
  error.value = ''
  try {
    await projectsStore.updateProject(p.id, {
      name: nm,
      description: description.value.trim(),
      instructions: instructions.value,
    })
    ui.closeModal()
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not save settings'
  } finally {
    busy.value = false
  }
}

async function remove() {
  const p = project.value
  if (!p) return
  if (!confirm(`Delete project "${p.name}"? Its chats become loose chats and the project's files are left on disk.`)) return
  busy.value = true
  try {
    await projectsStore.deleteProject(p.id, false)
    ui.closeModal()
    ui.showChat()
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not delete project'
    busy.value = false
  }
}
</script>

<template>
  <div class="modal" @click.stop style="width:480px" v-if="project">
    <h2>Project settings</h2>
    <p class="modal-subtitle">Name and standing instructions for this project.</p>

    <label class="ps-label">Name</label>
    <input v-model="name" class="form-input" :disabled="busy" />

    <label class="ps-label">Description</label>
    <input v-model="description" class="form-input" :disabled="busy" placeholder="Optional" />

    <label class="ps-label">Project instructions</label>
    <textarea
      v-model="instructions"
      class="form-input ps-textarea"
      :disabled="busy"
      rows="4"
      placeholder="Standing guidance applied to every chat in this project (e.g. 'Always use Acme's terminology'). Supplements global instructions."
    ></textarea>

    <label class="ps-label">Connectors</label>
    <p class="ps-hint">
      MCP servers available to this project. Changes apply to all
      {{ chatCount === 1 ? '1 chat' : `${chatCount} chats` }} in this project.
    </p>
    <div class="ps-connectors">
      <div v-if="connectorsLoading" class="ps-cn-empty">Loading…</div>
      <div v-else-if="!connectors.length" class="ps-cn-empty">
        No MCP servers enabled. Turn one on from Settings → Connectors / MCP Servers.
      </div>
      <button
        v-for="c in connectors"
        :key="c.name"
        type="button"
        class="ps-cn-item"
        :disabled="busy || !!connectorsBusy[c.name]"
        @click="toggleConnector(c)"
      >
        <span class="ps-cn-dot" :class="connectorDotClass(c.status)" aria-hidden="true"></span>
        <span class="ps-cn-name">{{ c.name }}</span>
        <span class="ps-cn-toggle" :class="{ on: c.selected }" aria-hidden="true"></span>
      </button>
    </div>

    <p v-if="error" class="ps-error">{{ error }}</p>

    <div class="modal-footer ps-footer">
      <button class="btn btn-ghost ps-delete" @click="remove" :disabled="busy" title="Delete project">Delete project</button>
      <span class="ps-spacer" />
      <button class="btn btn-ghost" @click="ui.closeModal()" :disabled="busy">Cancel</button>
      <button class="btn btn-primary" @click="save" :disabled="busy">{{ busy ? 'Saving…' : 'Save' }}</button>
    </div>
  </div>
</template>

<style scoped>
.ps-label { display: block; font-size: 12px; font-weight: 600; color: var(--text2); margin: 14px 0 5px; }
.form-input {
  width: 100%; padding: 9px 11px; font-size: 14px;
  border: 1px solid var(--border); border-radius: 8px;
  background: var(--surface); color: var(--text); font-family: inherit;
}
.form-input:focus { outline: none; border-color: var(--accent); }
.ps-textarea { resize: vertical; font-size: 13px; line-height: 1.5; }
.ps-error { color: var(--red); font-size: 12px; margin-top: 10px; }
.ps-hint { font-size: 12px; color: var(--text2); margin: 4px 0 8px; line-height: 1.4; }
.ps-connectors { display: flex; flex-direction: column; gap: 2px; }
.ps-cn-empty { font-size: 13px; color: var(--text2); padding: 4px 2px; }
.ps-cn-item {
  display: flex; align-items: center; gap: 10px; width: 100%;
  padding: 8px 8px; border: none; background: none; cursor: pointer;
  border-radius: 8px; font-size: 14px; color: var(--text); text-align: left;
  font-family: inherit; transition: background .1s;
}
.ps-cn-item:hover { background: var(--surface2); }
.ps-cn-item:disabled { opacity: .6; cursor: default; }
.ps-cn-dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; background: var(--text2); }
.ps-cn-dot.ok { background: #2ecc71; }
.ps-cn-dot.pending { background: #f1c40f; }
.ps-cn-dot.err { background: #e74c3c; }
.ps-cn-dot.off { background: var(--border); }
.ps-cn-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ps-cn-toggle {
  width: 30px; height: 17px; border-radius: 9px; background: var(--border);
  position: relative; flex-shrink: 0; transition: background .12s;
}
.ps-cn-toggle::after {
  content: ''; position: absolute; top: 2px; left: 2px; width: 13px; height: 13px;
  border-radius: 50%; background: #fff; transition: transform .12s;
}
.ps-cn-toggle.on { background: var(--accent); }
.ps-cn-toggle.on::after { transform: translateX(13px); }
.ps-footer { display: flex; align-items: center; }
.ps-spacer { flex: 1; }
.ps-delete { color: var(--red); border-color: rgba(218,63,63,.3); }
.ps-delete:hover { background: rgba(218,63,63,.1); }
</style>
