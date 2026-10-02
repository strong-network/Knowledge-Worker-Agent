<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed } from 'vue'
import { useProjectsStore } from '../../stores/projects'
import { useUiStore } from '../../stores/ui'

// Create a project. Repo-optional: the shared workspace is a fresh
// folder by default, or a cloned GitHub repository when a URL is supplied.
const projectsStore = useProjectsStore()
const ui = useUiStore()

const name = ref('')
const description = ref('')
const useRepo = ref(false)
const repoUrl = ref('')
const busy = ref(false)
const error = ref('')

const canCreate = computed(
  () => name.value.trim().length > 0 && (!useRepo.value || repoUrl.value.trim().length > 0),
)

async function create() {
  if (!canCreate.value || busy.value) return
  busy.value = true
  error.value = ''
  try {
    const p = await projectsStore.createProject({
      name: name.value.trim(),
      description: description.value.trim() || undefined,
      repo_url: useRepo.value ? repoUrl.value.trim() : undefined,
    })
    ui.closeModal()
    ui.openProject(p.id)
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not create project'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="modal" @click.stop style="width:460px">
    <h2>New project</h2>
    <p class="modal-subtitle">
      A project keeps related chats together with a shared workspace and context.
    </p>

    <label class="cp-label">Name</label>
    <input
      v-model="name"
      class="form-input"
      placeholder="e.g. Acme Deal"
      :disabled="busy"
      @keydown.enter="create"
      autofocus
    />

    <label class="cp-label">Description <span class="cp-opt">(optional)</span></label>
    <input
      v-model="description"
      class="form-input"
      placeholder="What is this project about?"
      :disabled="busy"
    />

    <label class="cp-check">
      <input type="checkbox" v-model="useRepo" :disabled="busy" />
      <span>Back this project with a GitHub repository</span>
    </label>
    <p class="cp-hint">
      Otherwise a fresh workspace folder is created under <code>~/Projects</code>.
    </p>

    <template v-if="useRepo">
      <label class="cp-label">Repository URL</label>
      <input
        v-model="repoUrl"
        class="form-input mono"
        placeholder="https://github.com/org/repo.git"
        :disabled="busy"
        @keydown.enter="create"
      />
      <p class="cp-hint">The repo is cloned and used as the project's shared workspace.</p>
    </template>

    <p v-if="error" class="cp-error">{{ error }}</p>

    <div class="modal-footer">
      <button class="btn btn-ghost" @click="ui.closeModal()" :disabled="busy">Cancel</button>
      <button class="btn btn-primary" @click="create" :disabled="!canCreate || busy">
        {{ busy ? (useRepo ? 'Cloning…' : 'Creating…') : 'Create project' }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.cp-label {
  display: block; font-size: 12px; font-weight: 600; color: var(--text2);
  margin: 12px 0 5px;
}
.cp-opt { font-weight: 400; color: var(--text3); }
.form-input {
  width: 100%; padding: 9px 11px; font-size: 14px;
  border: 1px solid var(--border); border-radius: 8px;
  background: var(--surface); color: var(--text); font-family: inherit;
}
.form-input.mono { font-family: var(--mono); font-size: 13px; }
.form-input:focus { outline: none; border-color: var(--accent); }
.cp-check {
  display: flex; align-items: center; gap: 8px; margin: 16px 0 0;
  font-size: 13px; color: var(--text); cursor: pointer;
}
.cp-hint { font-size: 12px; color: var(--text3); margin: 5px 0 0; }
.cp-hint code { font-family: var(--mono); font-size: 11px; }
.cp-error { color: var(--red); font-size: 12px; margin-top: 10px; }
</style>
