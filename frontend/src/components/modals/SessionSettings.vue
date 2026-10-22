<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useSessionsStore } from '../../stores/sessions'
import { useUiStore } from '../../stores/ui'
import { getSessionConfig, updateSessionConfig } from '../../api'
import type { SessionConfig } from '../../api'
import { TEMPORARY_SHORT } from '../../utils/temporary'

const sessionsStore = useSessionsStore()
const ui = useUiStore()

const model = ref('')
const mode = ref('')
const agent = ref('')
const workspace = ref('')
const systemPrompt = ref('')
const yolo = ref(false)
const permissions = ref<Record<string, boolean>>({})
const temporary = ref(!!sessionsStore.currentSession?.temporary)
// A chat other people use can't be made temporary; the server checks shared files.
const temporaryBlocked = computed(() =>
  sessionsStore.currentSession?.shared && !temporary.value ? 'Stop sharing first. Shared chats are always saved.' : '',
)

onMounted(async () => {
  sessionsStore.loadAgents()
  sessionsStore.loadModels()
  if (!sessionsStore.currentSessionId) return
  try {
    const cfg = await getSessionConfig(sessionsStore.currentSessionId)
    model.value = cfg.model || sessionsStore.currentSession?.model || ''
    mode.value = cfg.mode || sessionsStore.currentSession?.mode || 'autopilot'
    agent.value = (cfg as any).agent || ''
    workspace.value = cfg.workspace || sessionsStore.currentSession?.workspace || ''
    systemPrompt.value = cfg.system_prompt || ''
    yolo.value = cfg.yolo || false
    permissions.value = cfg.permissions || {}
  } catch (e) {
    console.error(e)
  }
})

// Models offered in the Model dropdown. If the session already has a model
// that isn't in the fetched list (e.g. list not loaded yet), include it so the
// current selection isn't lost.
const modelOptions = computed(() => {
  const base = sessionsStore.models
  if (model.value && !base.includes(model.value)) {
    return [model.value, ...base]
  }
  return base
})

async function save() {
  if (!sessionsStore.currentSessionId) return
  const config: SessionConfig = {
    model: model.value || undefined,
    mode: mode.value || undefined,
    workspace: workspace.value || undefined,
    system_prompt: systemPrompt.value || undefined,
    yolo: yolo.value || undefined,
    permissions: Object.keys(permissions.value).length ? permissions.value : undefined,
    agent: agent.value || undefined,
  }
  const id = sessionsStore.currentSessionId
  await updateSessionConfig(id, config)
  // Update local state
  const s = sessionsStore.currentSession
  if (s) {
    s.model = model.value
    if (config.mode) s.mode = config.mode
    if (config.workspace) s.workspace = config.workspace
  }
  ui.closeModal()
  // Making it temporary or keeping it says so itself.
  if (temporary.value !== !!s?.temporary) await sessionsStore.setTemporary(id, temporary.value)
  else (window as any).showToast?.('Settings saved')
}
</script>

<template>
  <div class="modal" @click.stop>
    <h2>⚙ Session Settings</h2>
    <p class="modal-subtitle">Edit settings for the current session</p>

    <div class="form-group">
      <label class="form-label">Model</label>
      <select v-model="model" class="form-select">
        <option value="">Default model</option>
        <option v-for="m in modelOptions" :key="m" :value="m">{{ m }}</option>
      </select>
      <p class="form-hint">Leave as “Default model” to use the default.</p>
    </div>
    <div class="form-group">
      <label class="form-label">Mode</label>
      <select v-model="mode" class="form-select">
        <option value="autopilot">Autopilot</option>
        <option value="plan">Plan</option>
        <option value="interactive">Interactive</option>
      </select>
    </div>
    <div class="form-group" v-if="sessionsStore.agents.length">
      <label class="form-label">Agent</label>
      <select v-model="agent" class="form-select">
        <option value="">None (default)</option>
        <optgroup v-if="sessionsStore.assignedAgents.length" label="Provided by IT">
          <option v-for="a in sessionsStore.assignedAgents" :key="a.name" :value="a.name">{{ a.name }}</option>
        </optgroup>
        <optgroup v-if="sessionsStore.personalAgents.length" label="My agents">
          <option v-for="a in sessionsStore.personalAgents" :key="a.name" :value="a.name">{{ a.name }}</option>
        </optgroup>
      </select>
    </div>
    <div class="form-group">
      <label class="form-label">Workspace</label>
      <input v-model="workspace" class="form-input" placeholder="/path/to/project" />
    </div>
    <div class="form-group">
      <label class="form-label">System prompt</label>
      <textarea v-model="systemPrompt" class="form-textarea" rows="3" placeholder="Optional..."></textarea>
    </div>
    <div v-if="!sessionsStore.currentSessionDeprecated" class="form-group">
      <label class="temp-row" :class="{ blocked: temporaryBlocked }">
        <span>
          <span class="temp-label">Temporary chat</span>
          <span class="temp-desc">{{ temporaryBlocked || TEMPORARY_SHORT }}</span>
        </span>
        <span class="toggle">
          <input type="checkbox" v-model="temporary" :disabled="!!temporaryBlocked" aria-label="Temporary chat">
          <span class="slider"></span>
        </span>
      </label>
    </div>

    <div class="modal-footer">
      <button class="btn btn-ghost" @click="ui.closeModal()">Cancel</button>
      <button class="btn btn-primary" @click="save">Save</button>
    </div>
  </div>
</template>

<style scoped>
.temp-row {
  display: flex; align-items: center; justify-content: space-between; gap: 12px;
  padding: 10px 12px; background: var(--surface2); border: 1px solid var(--border);
  border-radius: 8px; cursor: pointer;
}
.temp-row.blocked { cursor: default; }
.temp-label { display: block; font-size: 13px; font-weight: 500; }
.temp-desc { display: block; font-size: 11px; color: var(--text2); margin-top: 2px; }
</style>
