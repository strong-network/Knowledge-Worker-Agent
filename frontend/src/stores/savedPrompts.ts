// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import * as api from '../api'
import type { SavedPrompt, SavedPromptInput } from '../api'

// Saved prompts store: the user-owned starter chips shown on the New
// Chat surface. CRUD + drag-to-reorder, persisted per user via the API. The
// maximum is enforced server-side; MAX mirrors it for disabling the add control.
export const MAX_SAVED_PROMPTS = 12

export const useSavedPromptsStore = defineStore('savedPrompts', () => {
  const prompts = ref<SavedPrompt[]>([])
  const loaded = ref(false)
  const loading = ref(false)

  // Prompts are stored in display order by the API; keep that order as-is.
  const ordered = computed(() => prompts.value)
  const atCapacity = computed(() => prompts.value.length >= MAX_SAVED_PROMPTS)

  async function load() {
    if (loading.value) return
    loading.value = true
    try {
      prompts.value = await api.fetchSavedPrompts()
      loaded.value = true
    } catch (e) {
      console.error('[savedPrompts] load failed', e)
    } finally {
      loading.value = false
    }
  }

  // Ensure the list is loaded once (e.g. when the New Chat surface mounts).
  async function ensureLoaded() {
    if (!loaded.value && !loading.value) await load()
  }

  async function add(input: SavedPromptInput): Promise<SavedPrompt> {
    const created = await api.createSavedPrompt(input)
    // The API prepends new prompts; reflect that locally without a refetch.
    prompts.value = [created, ...prompts.value]
    return created
  }

  async function update(id: string, input: SavedPromptInput): Promise<SavedPrompt> {
    const updated = await api.updateSavedPrompt(id, input)
    const i = prompts.value.findIndex(p => p.id === id)
    if (i >= 0) prompts.value[i] = updated
    return updated
  }

  async function remove(id: string): Promise<void> {
    await api.deleteSavedPrompt(id)
    prompts.value = prompts.value.filter(p => p.id !== id)
  }

  // Persist a new order given the full list of ids (drag-to-reorder). Applies
  // optimistically, then reconciles with the server's canonical order.
  async function reorder(ids: string[]): Promise<void> {
    const byId = new Map(prompts.value.map(p => [p.id, p]))
    prompts.value = ids.map(id => byId.get(id)).filter((p): p is SavedPrompt => !!p)
    try {
      prompts.value = await api.reorderSavedPrompts(ids)
    } catch (e) {
      console.error('[savedPrompts] reorder failed', e)
      await load()
    }
  }

  return {
    prompts, ordered, loaded, loading, atCapacity,
    load, ensureLoaded, add, update, remove, reorder,
  }
})
