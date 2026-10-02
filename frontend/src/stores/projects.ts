// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import * as api from '../api'
import type { Project, Session } from '../api'
import { useSessionsStore } from './sessions'
import { loadExpandedIds, saveExpandedIds } from '../utils/sectionState'

// Projects store: the user's projects, their chats, and CRUD.
export const useProjectsStore = defineStore('projects', () => {
  const projects = ref<Project[]>([])
  const loading = ref(false)
  // Which projects are expanded in the sidebar (id -> true), remembered across
  // reloads so the user's arrangement of the tree survives a visit.
  const EXPANDED_KEY = 'expandedProjects'
  const expanded = ref<Record<string, boolean>>(loadExpandedIds(EXPANDED_KEY))
  // Cached chats per project (id -> sessions), loaded lazily.
  const projectChats = ref<Record<string, Session[]>>({})

  const sortedProjects = computed(() =>
    [...projects.value].sort(
      (a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime(),
    ),
  )

  async function loadProjects() {
    loading.value = true
    try {
      projects.value = await api.fetchProjects()
      await hydrateExpanded()
    } catch (e) {
      console.error('[projects] load failed', e)
    } finally {
      loading.value = false
    }
  }

  // A project restored as expanded never went through toggleExpanded, so
  // nothing fetched its chats — it would render as an open, empty folder. Fill
  // them in once the project list arrives, and take the same opportunity to
  // drop ids for projects that no longer exist (deleted in another tab, say)
  // so the saved set can't grow without bound.
  //
  // Fetches chats directly rather than via loadProjectChats: that helper also
  // reloads the global session list, which would fire once per expanded
  // project for a list the app loads on its own anyway.
  async function hydrateExpanded() {
    const live = new Set(projects.value.map(p => p.id))
    const stale = Object.keys(expanded.value).filter(id => !live.has(id))
    for (const id of stale) delete expanded.value[id]
    if (stale.length) saveExpandedIds(EXPANDED_KEY, expanded.value)

    const pending = Object.keys(expanded.value).filter(
      id => expanded.value[id] && !projectChats.value[id],
    )
    await Promise.all(
      pending.map(async id => {
        try {
          projectChats.value[id] = await api.fetchProjectChats(id)
        } catch {
          /* chatsFor still derives this project's chats from global sessions */
        }
      }),
    )
  }

  function getProject(id: string): Project | undefined {
    return projects.value.find(p => p.id === id)
  }

  async function refreshProject(id: string) {
    try {
      const p = await api.fetchProject(id)
      const i = projects.value.findIndex(x => x.id === id)
      if (i >= 0) projects.value[i] = p
      else projects.value.push(p)
    } catch (e) {
      console.error('[projects] refresh failed', e)
    }
  }

  // Ensure the global session list (source of truth for chat titles/stars) is
  // fresh, then derive the project's chats reactively from it via chatsFor.
  async function loadProjectChats(id: string) {
    const sessionsStore = useSessionsStore()
    await sessionsStore.loadSessions()
    // Seed the star cache from the authoritative project endpoint (the global
    // list already carries project_starred, but this guards ordering parity).
    try {
      projectChats.value[id] = await api.fetchProjectChats(id)
    } catch {
      /* the reactive derivation below still works from global sessions */
    }
  }

  // chatsFor derives a project's chats reactively from the global session list
  // (so renames / first-message auto-titles reflect immediately in both the
  // sidebar and the project view), sorted project-starred first then by recency.
  function chatsFor(id: string): Session[] {
    const sessionsStore = useSessionsStore()
    const list = sessionsStore.sessions.filter(s => s.project_id === id)
    if (list.length === 0) {
      // Fall back to the last fetched snapshot until the global list loads.
      return projectChats.value[id] || []
    }
    return [...list].sort((a, b) => {
      const sa = a.project_starred ? 1 : 0
      const sb = b.project_starred ? 1 : 0
      if (sa !== sb) return sb - sa
      return new Date(b.updated_at || '').getTime() - new Date(a.updated_at || '').getTime()
    })
  }

  async function createProject(input: { name: string; description?: string; repo_url?: string }) {
    const p = await api.createProject(input)
    projects.value.unshift(p)
    return p
  }

  async function renameProject(id: string, name: string) {
    const p = await api.updateProject(id, { name })
    const i = projects.value.findIndex(x => x.id === id)
    if (i >= 0) projects.value[i] = p
    return p
  }

  async function updateProject(
    id: string,
    patch: Partial<Pick<Project, 'name' | 'description' | 'instructions' | 'data_sources'>>,
  ) {
    const p = await api.updateProject(id, patch)
    const i = projects.value.findIndex(x => x.id === id)
    if (i >= 0) projects.value[i] = p
    return p
  }

  async function deleteProject(id: string, deleteChats = false) {
    await api.deleteProject(id, deleteChats)
    projects.value = projects.value.filter(p => p.id !== id)
    delete projectChats.value[id]
    delete expanded.value[id]
    saveExpandedIds(EXPANDED_KEY, expanded.value)
  }

  async function toggleExpanded(id: string) {
    expanded.value[id] = !expanded.value[id]
    saveExpandedIds(EXPANDED_KEY, expanded.value)
    if (expanded.value[id] && !projectChats.value[id]) {
      await loadProjectChats(id)
    }
  }

  async function newChat(projectId: string): Promise<string> {
    const sid = await api.createProjectChat(projectId)
    // Refresh the project's chat list + count so the UI reflects the new chat.
    await Promise.all([loadProjectChats(projectId), refreshProject(projectId)])
    return sid
  }

  async function setStar(projectId: string, sessionId: string, starred: boolean) {
    await api.setProjectStar(sessionId, starred)
    // Update the global session (source of truth for chatsFor) so the star +
    // re-sort reflect immediately without a refetch.
    const sessionsStore = useSessionsStore()
    const s = sessionsStore.sessions.find(x => x.id === sessionId)
    if (s) s.project_starred = starred
    const cached = projectChats.value[projectId]?.find(c => c.id === sessionId)
    if (cached) cached.project_starred = starred
  }

  return {
    projects, sortedProjects, loading, expanded, projectChats,
    loadProjects, getProject, refreshProject, loadProjectChats, chatsFor,
    createProject, renameProject, updateProject, deleteProject,
    toggleExpanded, newChat, setStar,
  }
})
