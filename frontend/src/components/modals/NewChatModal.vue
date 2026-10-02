<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useSessionsStore } from '../../stores/sessions'
import { useUiStore } from '../../stores/ui'
import { browseFiles, createDirectory } from '../../api'
import type { SessionConfig } from '../../api'
import { useMe, loadBaseDir } from '../../composables/useMe'

const sessionsStore = useSessionsStore()
const ui = useUiStore()

const activeTab = ref(0)
const name = ref('')
const model = ref('')
const mode = ref('autopilot')
const agent = ref('')
const { baseDir } = useMe()
const workspace = ref('')
const systemPrompt = ref('')
const contextFiles = ref<string[]>([])
const contextFileInput = ref('')
const permissions = ref<Record<string, boolean>>({
  read: true, edit: true, create: true, bash: true, search: true, web: false
})
const yolo = ref(false)

// Create-folder mode: when enabled, the chat name and folder name are bound,
// and the new folder is created (under parentDir) on submit.
const createFolder = ref(true)
const parentDir = ref('')
const folderName = ref('')
const creating = ref(false)
const createError = ref('')

// Replace characters that aren't safe in a folder name with '-'. Allows
// letters, digits, dot, dash, underscore, and space.
function sanitizeFolderName(s: string): string {
  return s.replace(/[^A-Za-z0-9._\- ]+/g, '-').replace(/^[.\-\s]+/, '').trim()
}

let syncingNameToFolder = false
let syncingFolderToName = false

watch(name, (v) => {
  if (!createFolder.value || syncingFolderToName) return
  syncingNameToFolder = true
  folderName.value = sanitizeFolderName(v)
  syncingNameToFolder = false
})

watch(folderName, (v) => {
  if (!createFolder.value || syncingNameToFolder) return
  syncingFolderToName = true
  name.value = v
  syncingFolderToName = false
})

watch(createFolder, (enabled) => {
  if (enabled && !folderName.value && name.value) {
    folderName.value = sanitizeFolderName(name.value)
  } else if (enabled && !name.value && folderName.value) {
    name.value = folderName.value
  }
})

const dirEntries = ref<{ name: string; path: string; is_dir: boolean }[]>([])
const currentBrowsePath = ref('')
const parentDirEntries = ref<{ name: string; path: string; is_dir: boolean }[]>([])
const parentBrowsePath = ref('')

onMounted(async () => {
  sessionsStore.loadAgents()
  sessionsStore.loadModels()
  const base = await loadBaseDir()
  if (!workspace.value) workspace.value = base
  if (!parentDir.value) parentDir.value = base
})

// Models offered in the Model dropdown (provider/model identifiers).
// Include any pre-set model that isn't in the list so the selection isn't lost.
const modelOptions = computed(() => {
  const base = sessionsStore.models
  if (model.value && !base.includes(model.value)) {
    return [model.value, ...base]
  }
  return base
})

function switchTab(n: number) {
  activeTab.value = n
}

function togglePerm(key: string) {
  permissions.value[key] = !permissions.value[key]
}

function toggleYolo() {
  yolo.value = !yolo.value
  if (yolo.value) {
    Object.keys(permissions.value).forEach(k => permissions.value[k] = true)
  }
}

function addContextFile() {
  const v = contextFileInput.value.trim()
  if (v && !contextFiles.value.includes(v)) {
    contextFiles.value.push(v)
  }
  contextFileInput.value = ''
}

function removeContextFile(i: number) {
  contextFiles.value.splice(i, 1)
}

async function browseTo(path: string) {
  try {
    const entries = await browseFiles(path)
    dirEntries.value = entries.filter(e => e.is_dir)
    currentBrowsePath.value = path
  } catch (e) {
    console.error(e)
  }
}

function selectDir(path: string) {
  workspace.value = path
}

async function browseParentTo(path: string) {
  try {
    const entries = await browseFiles(path)
    parentDirEntries.value = entries.filter(e => e.is_dir)
    parentBrowsePath.value = path
  } catch (e) {
    console.error(e)
  }
}

function selectParentDir(path: string) {
  parentDir.value = path
}

function joinPath(parent: string, child: string): string {
  const p = parent.replace(/\/+$/, '')
  const c = child.replace(/^\/+/, '')
  return `${p}/${c}`
}

function basename(p: string): string {
  return p.replace(/\/+$/, '').split('/').pop() || p
}

async function createChat() {
  createError.value = ''
  let workspacePath = workspace.value || undefined

  if (createFolder.value) {
    const fname = sanitizeFolderName(folderName.value)
    if (!fname) {
      createError.value = 'Please enter a folder name.'
      return
    }
    const parent = parentDir.value || baseDir.value
    creating.value = true
    try {
      // Backend handles collision detection atomically and appends a suffix
      // (e.g. `my-chat-nova`) if the desired name is already taken.
      workspacePath = await createDirectory(joinPath(parent, fname), true)
      const actual = basename(workspacePath)
      if (actual !== fname) {
        // Reflect the de-duplicated name in both fields so the chat label
        // matches the folder that was actually created.
        syncingFolderToName = true
        folderName.value = actual
        name.value = actual
        syncingFolderToName = false
      }
    } catch (e: any) {
      createError.value = e?.message || 'Failed to create folder'
      creating.value = false
      return
    }
    creating.value = false
  }

  const config: SessionConfig & { name?: string } = {
    name: name.value || undefined,
    model: model.value || undefined,
    mode: mode.value || undefined,
    workspace: workspacePath,
    system_prompt: systemPrompt.value || undefined,
    context_files: contextFiles.value.length ? contextFiles.value : undefined,
    permissions: Object.keys(permissions.value).some(k => permissions.value[k]) ? permissions.value : undefined,
    yolo: yolo.value || undefined,
    agent: agent.value || undefined,
  }
  await sessionsStore.createSession(config)
  ui.closeModal()
}
</script>

<template>
  <div class="modal" @click.stop>
    <h2>🆕 New Chat</h2>
    <p class="modal-subtitle">Configure a new session</p>

    <div class="modal-tabs">
      <button class="modal-tab" :class="{ active: activeTab === 0 }" @click="switchTab(0)">Basic</button>
      <button class="modal-tab" :class="{ active: activeTab === 1 }" @click="switchTab(1)">Permissions</button>
      <button class="modal-tab" :class="{ active: activeTab === 2 }" @click="switchTab(2)">Advanced</button>
    </div>

    <!-- Basic tab -->
    <div class="modal-panel" :class="{ active: activeTab === 0 }">
      <div class="form-group">
        <label class="form-label">Session name</label>
        <input v-model="name" class="form-input" placeholder="My Chat" />
      </div>
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
      <div class="form-group">
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
  <p class="form-hint" v-if="agent && sessionsStore.agents.find(a => a.name === agent)?.description">
    {{ sessionsStore.agents.find(a => a.name === agent)?.description }}
  </p>
</div>
      <div class="form-group">
        <label class="form-label">Workspace</label>
        <div class="toggle-card" :class="{ enabled: createFolder }" @click="createFolder = !createFolder">
          <div>
            <div class="tc-label">📁 Create a new folder for this chat</div>
            <div class="tc-desc">Folder name and chat name stay in sync</div>
          </div>
          <label class="toggle" @click.stop>
            <input type="checkbox" v-model="createFolder">
            <span class="slider"></span>
          </label>
        </div>
      </div>

      <div v-if="createFolder" class="form-group">
        <label class="form-label">Folder name</label>
        <input v-model="folderName" class="form-input" placeholder="my-new-chat" />
        <p class="form-hint">Created at <code>{{ joinPath(parentDir || baseDir, sanitizeFolderName(folderName) || '…') }}</code></p>
      </div>

      <div v-if="createFolder" class="form-group">
        <label class="form-label">Parent directory</label>
        <input v-model="parentDir" class="form-input" :placeholder="baseDir" @focus="browseParentTo(parentDir || baseDir)" />
        <div class="dir-browser" v-if="parentDirEntries.length">
          <div class="dir-item nav-up" @click="browseParentTo(parentBrowsePath.split('/').slice(0,-1).join('/') || '/')">⬆ ..</div>
          <div class="dir-item" v-for="d in parentDirEntries" :key="d.path" @click="selectParentDir(d.path); browseParentTo(d.path)">
            📁 {{ d.name }}
          </div>
        </div>
        <p v-if="createError" class="form-hint" style="color: var(--red);">{{ createError }}</p>
      </div>

      <div v-else class="form-group">
        <label class="form-label">Workspace directory</label>
        <input v-model="workspace" class="form-input" :placeholder="baseDir" @focus="browseTo(workspace || baseDir)" />
        <div class="dir-browser" v-if="dirEntries.length">
          <div class="dir-item nav-up" @click="browseTo(currentBrowsePath.split('/').slice(0,-1).join('/') || '/')">⬆ ..</div>
          <div class="dir-item" v-for="d in dirEntries" :key="d.path" @click="selectDir(d.path); browseTo(d.path)">
            📁 {{ d.name }}
          </div>
        </div>
      </div>
    </div>

    <!-- Permissions tab -->
    <div class="modal-panel" :class="{ active: activeTab === 1 }">
      <div class="form-group">
        <div class="toggle-card" :class="{ enabled: yolo }" @click="toggleYolo">
          <div><div class="tc-label">⚡ YOLO mode</div><div class="tc-desc">Enable all permissions</div></div>
          <label class="toggle" @click.stop><input type="checkbox" :checked="yolo" @change="toggleYolo"><span class="slider"></span></label>
        </div>
      </div>
      <div class="perm-grid">
        <div
          v-for="(val, key) in permissions"
          :key="key"
          class="toggle-card"
          :class="{ enabled: val }"
          @click="togglePerm(key as string)"
        >
          <div class="tc-label">{{ key }}</div>
          <label class="toggle" @click.stop><input type="checkbox" :checked="val" @change="togglePerm(key as string)"><span class="slider"></span></label>
        </div>
      </div>
    </div>

    <!-- Advanced tab -->
    <div class="modal-panel" :class="{ active: activeTab === 2 }">
      <div class="form-group">
        <label class="form-label">System prompt</label>
        <textarea v-model="systemPrompt" class="form-textarea" rows="4" placeholder="Optional system prompt..."></textarea>
      </div>
      <div class="form-group">
        <label class="form-label">Context files</label>
        <div class="tag-input-wrap">
          <span v-for="(f, i) in contextFiles" :key="i" class="tag">
            {{ f.split('/').pop() }}
            <button class="tag-del" @click="removeContextFile(i)">×</button>
          </span>
          <input
            v-model="contextFileInput"
            class="tag-field"
            placeholder="Add file path..."
            @keydown.enter.prevent="addContextFile"
          />
        </div>
      </div>
    </div>

    <div class="modal-footer">
      <button class="btn btn-ghost" @click="ui.closeModal()" :disabled="creating">Cancel</button>
      <button class="btn btn-primary" @click="createChat" :disabled="creating">
        {{ creating ? 'Creating folder…' : 'Create Chat' }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.form-hint { font-size: 11px; color: var(--text2); margin-top: 4px; line-height: 1.4; }
.perm-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
.toggle-card {
  display: flex; align-items: center; justify-content: space-between;
  padding: 10px 12px; background: var(--surface2); border: 1px solid var(--border);
  border-radius: 8px; cursor: pointer; transition: border-color .15s;
}
.toggle-card:hover { border-color: var(--accent2); }
.toggle-card.enabled { border-color: rgba(3,169,241,.5); background: rgba(3,169,241,.08); }
.tc-label { font-size: 13px; font-weight: 500; }
.tc-desc { font-size: 11px; color: var(--text2); margin-top: 2px; }
.dir-browser { background: var(--bg); border: 1px solid var(--border); border-radius: 7px; max-height: 160px; overflow-y: auto; margin-top: 6px; }
.dir-item { padding: 7px 12px; cursor: pointer; font-size: 12px; font-family: var(--mono); color: var(--text2); display: flex; align-items: center; gap: 7px; transition: background .1s; }
.dir-item:hover { background: var(--surface2); color: var(--text); }
.dir-item.nav-up { color: var(--blue); font-style: italic; }
.tag-input-wrap { display: flex; flex-wrap: wrap; gap: 5px; padding: 6px; background: var(--surface2); border: 1px solid var(--border); border-radius: 7px; min-height: 38px; align-items: flex-start; }
.tag-input-wrap:focus-within { border-color: var(--accent); }
.tag { display: inline-flex; align-items: center; gap: 4px; background: var(--accent); color: #fff; border-radius: 4px; padding: 2px 8px; font-size: 12px; }
.tag-del { background: none; border: none; color: rgba(255,255,255,.7); cursor: pointer; font-size: 12px; padding: 0; line-height: 1; }
.tag-field { border: none; background: none; outline: none; color: var(--text); font-size: 13px; min-width: 80px; padding: 2px 4px; }

@media (max-width: 768px) {
  .perm-grid { grid-template-columns: 1fr; }
}
</style>
