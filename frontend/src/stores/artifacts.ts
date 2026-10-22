// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { artifactFix, listArtifacts, type Artifact, type ArtifactFix } from '../api'
import { useUiStore } from './ui'

const FIX_POLL_MS = 3000

// Shared files ("artifacts" in the UI), for the Files widget's markers and the
// Share dialog. Loaded when something may have changed them, never polled: each
// load asks the platform. Running fixes are polled, a local read, until they end.
export const useArtifactsStore = defineStore('artifacts', () => {
  const list = ref<Artifact[]>([])
  const enabled = ref(false)
  // Why the list may be incomplete or sharing is off, in words.
  const problem = ref('')
  const loaded = ref(false)
  // The file the Share dialog is for.
  const target = ref<{ sessionId: string; path: string; name: string } | null>(null)
  // Fixes started or seen since the page loaded, as last heard.
  const fixes = ref<ArtifactFix[]>([])
  let loading: Promise<void> | null = null

  const byPath = computed(() => new Map(list.value.map(a => [a.path, a])))
  const fixingAt = computed(() => new Map(fixes.value.filter(f => f.state === 'running').map(f => [f.path, f])))

  function load(): Promise<void> {
    if (loading) return loading
    loading = (async () => {
      try {
        const r = await listArtifacts()
        list.value = r.artifacts || []
        enabled.value = r.enabled
        problem.value = r.problem || ''
        loaded.value = true
        for (const f of r.fixes || []) putFix(f)
      } catch {
        // Keep what's shown; the next load tries again.
      } finally {
        loading = null
      }
    })()
    return loading
  }

  function put(a: Artifact) {
    const i = list.value.findIndex(x => x.id === a.id)
    if (i < 0) list.value = [...list.value, a]
    else list.value = list.value.map(x => (x.id === a.id ? a : x))
  }

  function drop(id: string) {
    list.value = list.value.filter(x => x.id !== id)
  }

  function putFix(f: ArtifactFix) {
    const i = fixes.value.findIndex(x => x.id === f.id)
    fixes.value = i < 0 ? [...fixes.value, f] : fixes.value.map(x => (x.id === f.id ? f : x))
    if (f.state === 'running') void follow()
  }

  function lastFixAt(path: string): ArtifactFix | undefined {
    return [...fixes.value].reverse().find(f => f.path === path)
  }

  // A fix that ends changes its file, and a shared file's status, so the list
  // is read again.
  let following = false
  async function follow() {
    if (following) return
    following = true
    try {
      while (fixes.value.some(f => f.state === 'running')) {
        await new Promise(r => setTimeout(r, FIX_POLL_MS))
        let ended = false
        for (const f of fixes.value.filter(x => x.state === 'running')) {
          try {
            const now = await artifactFix(f.id)
            putFix(now)
            if (now.state !== 'running') {
              ended = true
              tell(now)
            }
          } catch {
            // Asked again next round.
          }
        }
        if (ended) void load()
      }
    } finally {
      following = false
    }
  }

  // The Share dialog shows how its file's fix ended; elsewhere, a toast does.
  function tell(f: ArtifactFix) {
    if (useUiStore().activeModal === 'artifact-share' && target.value?.path === f.path) return
    const text = f.state === 'done' ? `${f.name} is self-contained now` : `Couldn't make ${f.name} self-contained`
    ;(window as any).showToast?.(text)
  }

  function openShare(sessionId: string, path: string) {
    target.value = { sessionId, path, name: path.slice(path.lastIndexOf('/') + 1) }
    useUiStore().openModal('artifact-share')
  }

  return { list, enabled, problem, loaded, target, fixes, byPath, fixingAt, load, put, drop, putFix, lastFixAt, openShare }
})
