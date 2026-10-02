<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { computed, ref, onMounted } from 'vue'
import { useSessionsStore } from '../../stores/sessions'
import { useUiStore } from '../../stores/ui'
import { useProvidersStore } from '../../stores/providers'
import { providerOf, shortModelName } from '../../utils/models'
import { updateSessionConfig } from '../../api'

const sessionsStore = useSessionsStore()
const ui = useUiStore()
const providers = useProvidersStore()
const search = ref('')
const loading = ref(false)

// The list shown by the picker: the assistant's available models. A searchable
// dropdown — no manual model entry.
const listModels = computed(() => sessionsStore.models)
const filteredModels = computed(() =>
  listModels.value.filter(m => m.toLowerCase().includes(search.value.toLowerCase())),
)
const hasModels = computed(() => listModels.value.length > 0)

// Grouped by provider, labelled with the provider's display name.
//
// A flat list of raw ids was readable while there were two providers and every
// id started with a familiar prefix. Once a workspace can be assigned several
// config-declared providers, the prefix is the only thing telling two similarly
// named models apart, and it is the least legible part of the row — so promote
// it to a heading and show the model name on its own.
//
// Group order follows the model list, which the server sorts, so it is stable
// between openings.
const groupedModels = computed(() => {
  const groups: { id: string; label: string; models: string[] }[] = []
  const byId = new Map<string, { id: string; label: string; models: string[] }>()
  for (const m of filteredModels.value) {
    const pid = providerOf(m)
    let g = byId.get(pid)
    if (!g) {
      // An id with no provider prefix should still be selectable rather than
      // silently dropped, so it gets its own untitled group.
      g = { id: pid, label: pid ? providers.labelFor(pid) : 'Other', models: [] }
      byId.set(pid, g)
      groups.push(g)
    }
    g.models.push(m)
  }
  return groups
})

async function reloadModels() {
  loading.value = true
  try {
    await sessionsStore.loadModels()
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  // Refresh the list when the picker opens so a fresh login is reflected
  // without a page reload. Show a spinner only if we have nothing cached yet
  // (otherwise refresh silently in the background).
  if (sessionsStore.models.length === 0) {
    reloadModels()
  } else {
    sessionsStore.loadModels()
  }
})

async function selectModel(model: string) {
  if (!sessionsStore.currentSessionId) return
  await updateSessionConfig(sessionsStore.currentSessionId, { model })
  const s = sessionsStore.currentSession
  if (s) s.model = model
  ui.closeModal()
}

function openOpencodeLogin() {
  ui.openModal('accounts')
}
</script>

<template>
  <div class="modal" @click.stop style="width:420px">
    <h2>🤖 Select Model</h2>
    <p class="modal-subtitle">Choose a model for this session</p>

    <!-- Loading the list for the first time -->
    <div v-if="loading && !hasModels" class="mp-state">
      <span class="mp-spinner" aria-hidden="true" />
      Loading models…
    </div>

    <!-- No models available (not signed in / cache empty) -->
    <div v-else-if="!hasModels" class="mp-state">
      <p class="mp-empty-title">No models available</p>
      <p class="form-hint">
        Sign in to load the model list, then reload.
      </p>
      <div class="mp-empty-actions">
        <button class="btn btn-primary" @click="openOpencodeLogin">Connect</button>
        <button class="btn btn-ghost" @click="reloadModels">Reload models</button>
      </div>
    </div>

    <!-- Searchable model list -->
    <template v-else>
      <input v-model="search" class="form-input" placeholder="Search models..." style="margin-bottom:12px" />
      <div class="model-list">
        <template v-for="g in groupedModels" :key="g.id">
          <div class="model-group">{{ g.label }}</div>
          <div
            v-for="m in g.models"
            :key="m"
            class="model-item"
            :class="{ active: m === sessionsStore.currentSession?.model }"
            :title="m"
            @click="selectModel(m)"
          >
            {{ shortModelName(m) }}
            <span v-if="m === sessionsStore.currentSession?.model" class="check">✓</span>
          </div>
        </template>
        <div v-if="!filteredModels.length" class="model-empty">No models match “{{ search }}”.</div>
      </div>
    </template>

    <div class="modal-footer">
      <button
        v-if="hasModels"
        class="btn btn-ghost"
        :disabled="loading"
        @click="reloadModels"
        title="Re-fetch the model list"
      >
        {{ loading ? 'Reloading…' : 'Reload' }}
      </button>
      <button class="btn btn-ghost" @click="ui.closeModal()">Close</button>
    </div>
  </div>
</template>

<style scoped>
.model-list { max-height: 300px; overflow-y: auto; border: 1px solid var(--border); border-radius: 8px; }
.model-group {
  position: sticky; top: 0; z-index: 1;
  padding: 7px 14px;
  background: var(--surface2);
  border-bottom: 1px solid var(--border);
  font-size: 11px; font-weight: 600; letter-spacing: .03em;
  text-transform: uppercase; color: var(--text2);
}
.model-item {
  padding: 10px 14px; cursor: pointer; font-size: 13px; font-family: var(--mono);
  border-bottom: 1px solid var(--border); display: flex; align-items: center; justify-content: space-between;
  transition: background .1s;
}
.model-item:last-child { border-bottom: none; }
.model-item:hover { background: var(--surface2); }
.model-item.active { background: rgba(3,169,241,.1); color: var(--accent); }
.check { color: var(--accent); font-weight: 700; }
.model-empty { padding: 12px 14px; color: var(--text2); font-size: 12px; }

.mp-state {
  display: flex; flex-direction: column; gap: 10px;
  align-items: center; justify-content: center; text-align: center;
  padding: 24px 16px; color: var(--text2); font-size: 13px;
  border: 1px solid var(--border); border-radius: 8px; margin-bottom: 4px;
}
.mp-empty-title { margin: 0; font-weight: 600; color: var(--text); }
.mp-empty-actions { display: flex; gap: 8px; flex-wrap: wrap; justify-content: center; }
.mp-spinner {
  display: inline-block; width: 16px; height: 16px;
  border: 2px solid var(--border); border-top-color: var(--accent, #1f6feb);
  border-radius: 50%; animation: mp-spin .8s linear infinite;
}
@keyframes mp-spin { to { transform: rotate(360deg); } }
</style>
