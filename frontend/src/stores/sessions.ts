// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import * as api from '../api'
import type { Session, Message, SessionConfig, AgentInfo } from '../api'
import { useChatStore } from './chat'
import { useUiStore } from './ui'
import { countWord } from '../utils/temporary'

function ensureArray<T>(val: T[] | null | undefined): T[] {
  return Array.isArray(val) ? val : []
}

export const useSessionsStore = defineStore('sessions', () => {
  const sessions = ref<Session[]>([])
  const currentSessionId = ref<string | null>(null)
  const messages = ref<Map<string, Message[]>>(new Map())
  const drafts = ref<Map<string, string>>(new Map())
  // Models are provider/model identifiers (e.g.
  // "github-copilot/claude-sonnet-4.6"). The list is empty until the user has
  // signed in and the server has refreshed its cache.
  const models = ref<string[]>([])
  const agents = ref<AgentInfo[]>([])
  const loading = ref(false)

  // Client-side filter for the sidebar chat list. Filters by chat
  // name/title only; empty string shows everything.
  const searchQuery = ref('')

  const currentSession = computed(() =>
    sessions.value.find(s => s.id === currentSessionId.value) || null
  )

  // True when the active session is a deprecated legacy GitHub Copilot
  // session: the UI shows it read-only and blocks sending.
  const currentSessionDeprecated = computed(() =>
    currentSession.value?.deprecated === true
  )

  // The project the active session belongs to ('' for loose chats), used to
  // show the project-context banner and scope the "+" menu.
  const currentProjectId = computed(() => currentSession.value?.project_id || '')

  const currentMessages = computed(() =>
    messages.value.get(currentSessionId.value || '') || []
  )

  const currentDraft = computed(() =>
    drafts.value.get(currentSessionId.value || '') || ''
  )

  const sortedSessions = computed(() => {
    const q = searchQuery.value.trim().toLowerCase()
    const match = (s: Session) =>
      !q || (s.name || '').toLowerCase().includes(q)
    // Temporary chats aren't in the history: they have a group of their own.
    const temporary = sessions.value.filter(s => s.temporary && !s.task_id && match(s))
    const favorites = sessions.value.filter(s => !s.temporary && s.favorite && match(s))
    const pinned = sessions.value.filter(s => !s.temporary && !s.favorite && s.pinned && match(s))
    // Scheduled tasks: chats created by a scheduled task are surfaced under the sidebar's
    // "Scheduled tasks" section, so keep them out of the Recent list here.
    // Chat sharing: shared chats get their own section above Recent. A starred or
    // pinned one stays where it is and carries the badge instead.
    const shared = sessions.value.filter(
      s => s.shared && !s.temporary && !s.favorite && !s.pinned && !s.task_id && match(s),
    )
    const unpinned = sessions.value.filter(
      s => !s.temporary && !s.favorite && !s.pinned && !s.shared && !s.task_id && match(s),
    )
    const byDate = (a: Session, b: Session) =>
      new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime()
    favorites.sort(byDate)
    pinned.sort(byDate)
    shared.sort(byDate)
    temporary.sort(byDate)
    unpinned.sort(byDate)
    return { favorites, pinned, shared, temporary, unpinned }
  })

  // Whether the user has any starred chats — drives the adaptive default of
  // which history section is expanded.
  const hasFavorites = computed(() => sessions.value.some(s => s.favorite))

  // True when a search is active but nothing matches — the list shows a
  // subtle empty state in that case.
  const searchHasNoMatches = computed(() => {
    if (!searchQuery.value.trim()) return false
    const g = sortedSessions.value
    return g.favorites.length === 0 && g.pinned.length === 0 && g.shared.length === 0 &&
      g.temporary.length === 0 && g.unpinned.length === 0
  })

  function setSearchQuery(q: string) {
    searchQuery.value = q
  }

  async function loadSessions() {
    try {
      const before = sessions.value
      sessions.value = await api.fetchSessions()
      // Seed per-session drafts from the server response so the textarea
      // shows what the user had typed last time without an extra round-trip.
      for (const s of sessions.value) {
        if (typeof s.draft === 'string' && s.draft.length > 0) {
          drafts.value.set(s.id, s.draft)
        }
      }
      await noticeExpired(before)
    } catch (e) {
      console.error('Failed to load sessions:', e)
    }
  }

  function toast(msg: string, ms = 6000) {
    ;(window as any).showToast?.(msg, ms)
  }

  function forget(id: string) {
    cancelPendingDraft(id)
    messages.value.delete(id)
    drafts.value.delete(id)
    try { useChatStore().dropSession(id) } catch {}
  }

  async function openNewChat() {
    useUiStore().newChatTemporary = false
    await createSession({ name: 'New chat' })
  }

  // The server deletes a temporary chat when its time runs out. Chats that were
  // due and are no longer listed went that way.
  async function noticeExpired(before: Session[]) {
    const here = new Set(sessions.value.map(s => s.id))
    const soon = Date.now() + 60_000
    const gone = before.filter(s => s.temporary && !here.has(s.id) && (s.expires_at ?? 0) <= soon)
    if (!gone.length) return
    gone.forEach(s => forget(s.id))
    toast(gone.length === 1
      ? `Deleted the temporary chat "${gone[0].name || 'Untitled'}" after 24 hours of inactivity`
      : `Deleted ${countWord(gone.length)} temporary chats after 24 hours of inactivity`)
    if (gone.some(s => s.id === currentSessionId.value)) await openNewChat()
  }

  // Look again once a temporary chat is due, so its deletion shows here too.
  let lastDueCheck = 0
  function checkDue() {
    const now = Date.now()
    if (now - lastDueCheck < 30_000) return
    if (!sessions.value.some(s => s.temporary && (s.expires_at ?? Infinity) <= now)) return
    lastDueCheck = now
    void loadSessions()
  }
  if (typeof window !== 'undefined') {
    window.setInterval(checkDue, 15_000)
    document.addEventListener('visibilitychange', () => { if (!document.hidden) checkDue() })
  }

  // Make temporary (on) or Keep chat (off), and say what happened. quiet skips
  // the confirmation, for a chat started temporary from New chat.
  async function setTemporary(id: string, on: boolean, quiet = false): Promise<boolean> {
    const s = sessions.value.find(x => x.id === id)
    const title = `"${s?.name || 'Untitled'}"`
    try {
      const st = await api.setSessionTemporary(id, on)
      if (s) {
        s.temporary = st.temporary
        s.expires_at = st.temporary ? Date.now() + st.expires_in * 1000 : undefined
        s.favorite = st.favorite
        s.pinned = st.pinned
      }
      if (!quiet) {
        toast(on
          ? `${title} is now temporary. It's deleted after 24 hours of inactivity.`
          : `Kept ${title}. It's saved to your chat history.`)
      }
      return true
    } catch (e) {
      if (e instanceof api.SessionError && e.status === 404) {
        await loadSessions()
        if (!s?.temporary) toast(e.message)
      } else if (e instanceof api.SessionError && e.status === 409) {
        toast(e.message)
      } else {
        toast(on ? `Couldn't make ${title} temporary. Try again.` : `Couldn't keep ${title}. Try again.`)
      }
      return false
    }
  }

  // A message went to a temporary chat deleted meanwhile: it moves to a new
  // temporary chat, ready to send.
  async function moveToNewChat(id: string, prompt: string, reason: string) {
    const wasOpen = currentSessionId.value === id
    forget(id)
    sessions.value = sessions.value.filter(s => s.id !== id)
    if (wasOpen) {
      await createSession({ name: 'New chat' })
      useUiStore().newChatTemporary = true
      if (currentSessionId.value) setDraft(currentSessionId.value, prompt)
    }
    toast(`${reason} Your message is in a new chat, ready to send.`, 8000)
  }

  async function loadModels() {
    try {
      models.value = ensureArray(await api.fetchModels())
    } catch (e) {
      console.error('Failed to load models:', e)
    }
  }

  async function loadAgents() {
    try {
      agents.value = ensureArray(await api.fetchAgents())
    } catch (e) {
      console.error('Failed to load agents:', e)
      agents.value = []
    }
  }

  // agentName maps an opencode-resolvable agent id (the value stored in
  // session.agent) to its friendly display name, falling back to the id itself
  // (e.g. before the agent list has loaded, or for an unknown agent).
  function agentName(id: string | undefined | null): string {
    if (!id) return ''
    return agents.value.find((a) => a.id === id)?.name || id
  }

  // Agents split by where they came from, so pickers can show provenance
  // instead of one undifferentiated list. The list mixes two tiers with very
  // different ownership: 'assigned' is the set the central config repo gives
  // this workspace and rewrites on every boot, while 'personal' is the user's
  // own OpenCode config, which nothing reconciles. Shown flat, an agent the
  // user built themselves is indistinguishable from one IT provides — which
  // is what made a short assignment look like a long list.
  //
  // Anything without a source counts as personal: an older server omits the
  // field, and claiming IT provided an agent we cannot actually attribute is
  // the one error worth ruling out.
  const assignedAgents = computed(() => agents.value.filter((a) => a.source === 'assigned'))
  const personalAgents = computed(() => agents.value.filter((a) => a.source !== 'assigned'))

  async function switchSession(id: string) {
    // Make sure any in-flight, debounced draft for the outgoing session is
    // persisted before we swap, so a fast Sidebar click doesn't drop the
    // last keystrokes.
    const prev = currentSessionId.value
    if (prev && prev !== id) flushDraft(prev)

    currentSessionId.value = id
    if (!messages.value.has(id)) {
      await loadHistory(id)
    }
    // Lazily fetch the draft from the server only if we haven't already
    // seeded one via loadSessions (or via createSession).
    if (!drafts.value.has(id)) {
      try {
        const text = await api.getSessionDraft(id)
        // Don't clobber any text the user has typed in the (now-active)
        // session between the click and this response landing.
        if (!drafts.value.get(id)) {
          drafts.value.set(id, text)
        }
      } catch (e) {
        console.error('Failed to load draft:', e)
        drafts.value.set(id, '')
      }
    }
  }

  // Merged into what is on screen, one reload at a time, by the chat store:
  // a plain replace would wipe tool calls and a reply still streaming in.
  async function loadHistory(id: string) {
    await useChatStore().reloadHistory(id)
  }

  async function createSession(config: SessionConfig & { name?: string }) {
    const session = await api.createSession(config)
    sessions.value.unshift(session)
    // New session always starts with an empty draft.
    drafts.value.set(session.id, '')
    await switchSession(session.id)
    return session
  }

  async function deleteSession(id: string, deleteNotes = false) {
    cancelPendingDraft(id)
    await api.deleteSession(id, deleteNotes)
    sessions.value = sessions.value.filter(s => s.id !== id)
    messages.value.delete(id)
    drafts.value.delete(id)
    try { useChatStore().dropSession(id) } catch {}
    if (currentSessionId.value === id) {
      currentSessionId.value = sessions.value[0]?.id || null
      if (currentSessionId.value) {
        await loadHistory(currentSessionId.value)
      }
    }
  }

  async function renameSession(id: string, name: string, auto = false) {
    await api.renameSession(id, name, auto)
    const s = sessions.value.find(s => s.id === id)
    if (s) s.name = name
  }

  async function pinSession(id: string, pinned: boolean) {
    await api.pinSession(id, pinned)
    const s = sessions.value.find(s => s.id === id)
    if (s) s.pinned = pinned
  }

  async function favoriteSession(id: string, favorite: boolean) {
    await api.favoriteSession(id, favorite)
    const s = sessions.value.find(s => s.id === id)
    if (s) s.favorite = favorite
  }

  function addMessage(sessionId: string, msg: Message) {
    const msgs = messages.value.get(sessionId) || []
    msgs.push(msg)
    messages.value.set(sessionId, msgs)
  }

  function updateLastAssistantMessage(sessionId: string, content: string) {
    const msgs = messages.value.get(sessionId) || []
    for (let i = msgs.length - 1; i >= 0; i--) {
      if (msgs[i].role === 'assistant') {
        msgs[i].content = content
        break
      }
    }
  }

  function setLastAssistantToolCalls(sessionId: string, calls: Message['toolCalls']) {
    const msgs = messages.value.get(sessionId) || []
    for (let i = msgs.length - 1; i >= 0; i--) {
      if (msgs[i].role === 'assistant') {
        msgs[i].toolCalls = calls
        break
      }
    }
  }

  function setLastAssistantUsage(sessionId: string, usage: Message['usage']) {
    const msgs = messages.value.get(sessionId) || []
    for (let i = msgs.length - 1; i >= 0; i--) {
      if (msgs[i].role === 'assistant') {
        msgs[i].usage = usage
        break
      }
    }
  }

  async function undoLast(sessionId: string) {
    await api.undoLastTurn(sessionId)
    await loadHistory(sessionId)
  }

  // ── Drafts ──
  //
  // The textarea content is owned by the store so that switching sessions
  // swaps the draft in/out and so that a refresh / reopen restores it.
  // Writes are debounced per-session to avoid hammering the DB on every
  // keystroke, but pending writes are flushed on session switch / page
  // unload so we never lose the last few characters.
  const DRAFT_DEBOUNCE_MS = 400
  const draftTimers = new Map<string, ReturnType<typeof setTimeout>>()
  const draftPending = new Map<string, string>()

  function cancelPendingDraft(sessionId: string) {
    const t = draftTimers.get(sessionId)
    if (t) {
      clearTimeout(t)
      draftTimers.delete(sessionId)
    }
    draftPending.delete(sessionId)
  }

  async function flushDraft(sessionId: string) {
    const t = draftTimers.get(sessionId)
    if (t) clearTimeout(t)
    draftTimers.delete(sessionId)
    if (!draftPending.has(sessionId)) return
    const text = draftPending.get(sessionId) ?? ''
    draftPending.delete(sessionId)
    try {
      await api.saveSessionDraft(sessionId, text)
    } catch (e) {
      console.error('Failed to flush draft:', e)
    }
  }

  function setDraft(sessionId: string, text: string) {
    drafts.value.set(sessionId, text)
    draftPending.set(sessionId, text)
    const existing = draftTimers.get(sessionId)
    if (existing) clearTimeout(existing)
    draftTimers.set(
      sessionId,
      setTimeout(() => {
        draftTimers.delete(sessionId)
        const pending = draftPending.get(sessionId)
        if (pending === undefined) return
        draftPending.delete(sessionId)
        api.saveSessionDraft(sessionId, pending).catch((e) => {
          console.error('Failed to save draft:', e)
        })
      }, DRAFT_DEBOUNCE_MS),
    )
  }

  function clearDraft(sessionId: string) {
    cancelPendingDraft(sessionId)
    drafts.value.set(sessionId, '')
    // Fire-and-forget — the user just sent the message, no need to await.
    api.saveSessionDraft(sessionId, '').catch((e) => {
      console.error('Failed to clear draft:', e)
    })
  }

  // Best-effort flush of all pending drafts when the tab is being closed.
  // sendBeacon would be more reliable than fetch on unload, but our drafts
  // endpoint expects PUT + JSON and beacons only support POST, so we fall
  // back to a synchronous-ish fetch with keepalive.
  if (typeof window !== 'undefined') {
    window.addEventListener('pagehide', () => {
      for (const [sid] of draftPending) {
        const text = draftPending.get(sid) ?? ''
        try {
          fetch(`/api/sessions/${sid}/draft`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ draft: text }),
            keepalive: true,
          })
        } catch {}
      }
    })
  }

  return {
    sessions, currentSessionId, messages, drafts, models, agents, loading,
    assignedAgents, personalAgents,
    searchQuery, hasFavorites, searchHasNoMatches, setSearchQuery,
    currentSession, currentMessages, currentDraft, sortedSessions,
    currentSessionDeprecated,
    currentProjectId,
    loadSessions, loadModels, loadAgents, agentName, switchSession, loadHistory,
    createSession, deleteSession, renameSession, pinSession, favoriteSession,
    setTemporary, moveToNewChat,
    addMessage, updateLastAssistantMessage, setLastAssistantToolCalls,
    setLastAssistantUsage, undoLast,
    setDraft, clearDraft, flushDraft,
  }
})
