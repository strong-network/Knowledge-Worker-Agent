// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import { defineStore } from 'pinia'
import { ref, computed, reactive, nextTick, toRaw } from 'vue'
import {
  streamChat, attachChat, respondPermission,
  fetchSessionQueue, addToSessionQueue, removeFromSessionQueue, clearSessionQueue,
  isBackendUnavailableError, stopChat, fetchHistory, followEvents,
  type ServerQueuedPrompt, type PermissionResponse, type UsageInfo, type Message,
} from '../api'
import { useSessionsStore } from './sessions'
import { useNotifications } from '../composables/useNotifications'
import { TurnFollower, type TurnStart } from '../utils/turnFollower'
import { useShareStore } from './share'

// How close to the end still counts as "following along". Loose enough to
// survive a stray trackpad nudge or the sub-pixel rounding you get on zoomed
// displays, tight enough that scrolling up to re-read registers as leaving.
// Shared with ChatView so the transcript and the jump-to-bottom button can
// never disagree about whether the reader is at the end.
export const FOLLOW_THRESHOLD_PX = 120

export function isFollowingBottom(el: HTMLElement): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight <= FOLLOW_THRESHOLD_PX
}

export interface ToolCall {
  name: string
  callId: string
  args?: string
  status: 'running' | 'done' | 'error' | 'blocked'
  output?: string
  // The MCP server providing this tool, when it came from one. Resolved
  // server-side (the tool name alone cannot be split reliably) and empty for
  // ordinary built-in tools like read or bash.
  mcpServer?: string
  // Subagent (`task`) live progress: the child's current activity label (its
  // tool name only) and how many progress heartbeats have arrived. Shown on a
  // running `task` row so a delegation doesn't look frozen.
  progress?: string
  progressSteps?: number
}

export interface QuestionEvent {
  question: string
  choices?: string[]
  allow_freeform?: boolean
}

// PermissionRequest is an interactive tool-permission prompt raised by the
// backend. The user allows once, allows always, or rejects.
export interface PermissionRequest {
  permissionId: string
  permission: string
  patterns?: string[]
  callId?: string
}

// UsageInfo is defined alongside the API types (it is a server payload) and
// re-exported here for the components that already import it from the store.
export type { UsageInfo }

export interface SessionChatState {
  streaming: boolean
  streamingContent: string
  toolCalls: ToolCall[]
  currentQuestion: QuestionEvent | null
  currentPermission: PermissionRequest | null
  abortController: AbortController | null
  errorMessage: string | null
  queuedMessages: ServerQueuedPrompt[]
  notifiedQuestionKey: string | null
  stepCount: number
  // The turn this tab is showing live, from its stream's first frame.
  // Null while a stream has not said yet; kept once it ends, so the same turn
  // arriving on /events is recognised.
  turnId: string | null
  // The live turn came from /events: someone else started it.
  followed: boolean
}

function newState(): SessionChatState {
  return {
    streaming: false,
    streamingContent: '',
    toolCalls: [],
    currentQuestion: null,
    currentPermission: null,
    abortController: null,
    errorMessage: null,
    queuedMessages: [],
    notifiedQuestionKey: null,
    stepCount: 0,
    turnId: null,
    followed: false,
  }
}

export const useChatStore = defineStore('chat', () => {
  // Per-session reactive chat state. Stored in a ref<Map> so that
  // adding/removing a session entry is reactive; the inner objects are
  // reactive() so prop mutations propagate to consumers.
  const states = ref<Map<string, SessionChatState>>(new Map())

  function getState(sessionId: string): SessionChatState {
    let s = states.value.get(sessionId)
    if (!s) {
      s = reactive(newState()) as SessionChatState
      states.value.set(sessionId, s)
    }
    return s
  }

  function currentId(): string | null {
    return useSessionsStore().currentSessionId
  }

  // ── Reactive proxies for the *currently-displayed* session ──
  const streaming = computed(() => {
    const id = currentId()
    return id ? !!states.value.get(id)?.streaming : false
  })
  const streamingContent = computed(() => {
    const id = currentId()
    return id ? states.value.get(id)?.streamingContent || '' : ''
  })
  const toolCalls = computed<ToolCall[]>(() => {
    const id = currentId()
    return id ? states.value.get(id)?.toolCalls || [] : []
  })
  const currentQuestion = computed<QuestionEvent | null>(() => {
    const id = currentId()
    return id ? states.value.get(id)?.currentQuestion || null : null
  })
  const currentPermission = computed<PermissionRequest | null>(() => {
    const id = currentId()
    return id ? states.value.get(id)?.currentPermission || null : null
  })
  const errorMessage = computed(() => {
    const id = currentId()
    return id ? states.value.get(id)?.errorMessage || null : null
  })
  const queuedMessages = computed<ServerQueuedPrompt[]>(() => {
    const id = currentId()
    return id ? states.value.get(id)?.queuedMessages || [] : []
  })
  const queuedMessage = computed<string | null>(() => {
    const arr = queuedMessages.value
    return arr.length ? arr[0].prompt : null
  })
  const stepCount = computed<number>(() => {
    const id = currentId()
    return id ? states.value.get(id)?.stepCount || 0 : 0
  })
  // The open chat's live turn is someone else's.
  const streamingFollowed = computed(() => {
    const id = currentId()
    const s = id ? states.value.get(id) : undefined
    return !!s?.streaming && s.followed
  })

  /** Returns true if any session is currently streaming. */
  const anyStreaming = computed(() => {
    for (const s of states.value.values()) {
      if (s.streaming) return true
    }
    return false
  })

  function isStreamingSession(sessionId: string): boolean {
    return !!states.value.get(sessionId)?.streaming
  }

  function resetState(s: SessionChatState) {
    s.streaming = false
    s.streamingContent = ''
    s.toolCalls = []
    s.currentQuestion = null
    s.currentPermission = null
    s.errorMessage = null
    s.notifiedQuestionKey = null
    s.stepCount = 0
    s.turnId = null
    s.followed = false
  }

  function sessionName(sessionId: string): string {
    return useSessionsStore().sessions.find(s => s.id === sessionId)?.name || 'Untitled chat'
  }

  function shouldNotifyForSession(sessionId: string): boolean {
    return currentId() !== sessionId || document.hidden || !document.hasFocus()
  }

  function notifyInactiveSession(sessionId: string, title: string, body?: string) {
    if (!shouldNotifyForSession(sessionId)) return
    const { notify } = useNotifications()
    notify(title, body, { toast: document.hasFocus(), requireBlur: true })
  }

  function removeEmptyAssistantPlaceholder(sessionId: string) {
    const sessionsStore = useSessionsStore()
    const msgs = sessionsStore.messages.get(sessionId) || []
    const last = msgs[msgs.length - 1]
    if (last?.role === 'assistant' && !last.content && !last.toolCalls?.length) {
      msgs.pop()
      sessionsStore.messages.set(sessionId, msgs)
    }
  }

  function notifyQuestion(sessionId: string, question: QuestionEvent) {
    const text = question.question.trim()
    if (!text) return

    const s = getState(sessionId)
    const key = `${text}\n${(question.choices || []).join('\n')}`
    if (s.notifiedQuestionKey === key) return
    s.notifiedQuestionKey = key

    notifyInactiveSession(
      sessionId,
      `${sessionName(sessionId)} needs your input`,
      text.length > 120 ? `${text.slice(0, 117)}...` : text,
    )
  }

  function scrollToBottomIfActive(sessionId: string) {
    if (currentId() !== sessionId) return
    const msgs = document.getElementById('messages')
    if (!msgs) return
    // Tool calls, questions, permissions and step updates all grow the
    // transcript mid-turn, and each used to force the view to the end. Follow
    // only a reader who is already there: someone who scrolled up to re-read
    // should stay put, with the jump button to bring them back.
    //
    // Measured before the tick on purpose. Vue applies DOM changes in the
    // tick, so this reads the geometry the reader is actually looking at
    // rather than one that already includes the content being announced.
    if (!isFollowingBottom(msgs)) return
    nextTick(() => {
      const el = document.getElementById('messages')
      if (el) el.scrollTop = el.scrollHeight
    })
  }

  // `skill` is armed in the composer for this one message; it is
  // passed per send rather than held in the store so nothing can carry it
  // into a later turn.
  async function sendMessage(prompt: string, targetSessionId?: string, skill?: string) {
    const sessionsStore = useSessionsStore()
    let sessionId = targetSessionId || sessionsStore.currentSessionId

    // Auto-create session if none exists
    if (!sessionId) {
      const session = await sessionsStore.createSession({ name: 'New chat' })
      sessionId = session.id
    }

    if (!sessionId) return
    const s = getState(sessionId)
    if (s.streaming) return

    resetState(s)
    s.streaming = true

    // Name the session from the first prompt so the sidebar entry is not left
    // as "New chat". This is a placeholder: the server generates a title from
    // the prompt's intent and replaces it (see the 'title' event). Flagged as
    // automatic so it doesn't count as the user naming the chat.
    const session = sessionsStore.sessions.find(x => x.id === sessionId)
    if (session && (session.name === 'New chat' || session.name === 'New Chat' || !session.name)) {
      const label = prompt.slice(0, 45) + (prompt.length > 45 ? '…' : '')
      session.name = label
      sessionsStore.renameSession(sessionId, label, true).catch(() => {})
    }

    // Stamp both messages now so they show a relative time immediately. The
    // server records the assistant message when the turn finishes, so its
    // stored time can be later than this by the duration of the turn; the
    // server value replaces this one when the history is next loaded.
    const sentAt = new Date().toISOString()
    const asked: Message = { role: 'user', content: prompt, createdAt: sentAt }
    const reply: Message = { role: 'assistant', content: '', createdAt: sentAt }
    sessionsStore.addMessage(sessionId, asked)
    sessionsStore.addMessage(sessionId, reply)
    scrollToBottomIfActive(sessionId)

    s.abortController = streamChat(
      sessionId,
      prompt,
      (event, data) => {
        if (event === 'session_id' && data?.turn_id) asked.turnId = data.turn_id
        if (event === 'queued') {
          // Someone else's turn is running, so the server queued this prompt.
          // It is not in the transcript yet: show it in the queue until its
          // turn arrives, rather than above a reply that is not its own.
          dropMessages(sessionId!, [asked, reply])
          refreshQueue(sessionId!)
        }
        handleEvent(sessionId!, event, data)
      },
      () => finalize(sessionId!),
      (err) => {
        console.error(`[session ${sessionId}] Stream error:`, err)
        s.streaming = false
        pumpFollower(sessionId!)
        if (isBackendUnavailableError(err)) {
          s.errorMessage = err.message
          removeEmptyAssistantPlaceholder(sessionId!)
          scrollToBottomIfActive(sessionId!)
          return
        }
        sessionsStore.updateLastAssistantMessage(sessionId!, `Error: ${err.message}`)
        scrollToBottomIfActive(sessionId!)
        notifyInactiveSession(sessionId!, `${sessionName(sessionId!)} stopped`, err.message)
        processQueue(sessionId!)
      },
      skill,
    )
  }

  /** Abort current stream of the given (or current) session and send a new message. */
  async function abortAndSend(prompt: string, targetSessionId?: string) {
    const sessionId = targetSessionId || currentId()
    if (!sessionId) return
    const s = getState(sessionId)
    s.queuedMessages = []
    clearSessionQueue(sessionId).catch(() => {})
    stopStreaming(sessionId)
    await nextTick()
    await sendMessage(prompt, sessionId)
  }

  /** Refresh the queue snapshot for a session from the server. */
  async function refreshQueue(targetSessionId?: string) {
    const sessionId = targetSessionId || currentId()
    if (!sessionId) return
    const state = await fetchSessionQueue(sessionId)
    if (!state) return
    const s = getState(sessionId)
    s.queuedMessages = state.queue || []
  }

  /**
   * Queue a prompt on the server. If no stream is active for the session, the
   * server starts one immediately (and the caller should still rely on its
   * normal sendMessage path / attach to see live output). When a stream IS
   * active the prompt is appended to the server-side FIFO and will fire after
   * the current (and any earlier-queued) turn completes.
   */
  async function queueMessage(prompt: string, targetSessionId?: string, skill?: string) {
    const sessionId = targetSessionId || currentId()
    if (!sessionId) return
    const res = await addToSessionQueue(sessionId, prompt, skill)
    if (!res) return
    if (res.queued) {
      // Optimistically add a placeholder so the UI updates instantly; will be
      // overwritten by refreshQueue's authoritative snapshot.
      const s = getState(sessionId)
      s.queuedMessages.push({
        id: res.id || `tmp-${Date.now()}`,
        prompt,
        enqueued_at: new Date().toISOString(),
      })
    }
    refreshQueue(sessionId)
  }

  async function clearQueue(targetSessionId?: string) {
    const sessionId = targetSessionId || currentId()
    if (!sessionId) return
    const s = states.value.get(sessionId)
    if (s) s.queuedMessages = []
    await clearSessionQueue(sessionId)
  }

  async function removeQueuedMessage(idOrIndex: string | number, targetSessionId?: string) {
    const sessionId = targetSessionId || currentId()
    if (!sessionId) return
    const s = states.value.get(sessionId)
    if (!s) return
    let id: string | undefined
    if (typeof idOrIndex === 'number') {
      const item = s.queuedMessages[idOrIndex]
      id = item?.id
    } else {
      id = idOrIndex
    }
    if (!id) return
    s.queuedMessages = s.queuedMessages.filter(q => q.id !== id)
    await removeFromSessionQueue(sessionId, id)
  }

  function processQueue(sessionId: string) {
    // Queue draining is handled server-side: when the dispatcher finishes a
    // turn it auto-starts the next queued prompt (persisting that prompt's
    // user message to history). We must (a) reload history so the just-dequeued
    // user message appears in the thread, and (b) re-attach so the browser sees
    // the new stream's events.
    //
    // Timing note: by the time we poll, the server may have already popped the
    // next prompt off the queue and started streaming it. We poll briefly for a
    // new stream to appear, then sync history + attach.
    const s = states.value.get(sessionId)
    if (!s) return
    // A live /events connection brings the next turn, whoever started it.
    if (follow?.sid === sessionId && follow.follower.live) return
    waitForNextTurn(sessionId, 0)
  }

  // Polls the server's queue/streaming state until the next queued turn starts
  // (server reports streaming) or it's clear nothing will drain. Bounded to a
  // handful of attempts so a stuck queue can't spin forever.
  async function waitForNextTurn(sessionId: string, attempt: number) {
    if (getState(sessionId).streaming) return
    const state = await fetchSessionQueue(sessionId)
    if (!state) return
    const s = getState(sessionId)
    s.queuedMessages = state.queue || []

    if (state.streaming) {
      // A new turn is running (the dequeued user message is now persisted).
      await syncHistoryThenAttach(sessionId)
      return
    }
    const hasPending = !!state.queue && state.queue.length > 0
    if (hasPending && attempt < 10) {
      // Server hasn't started the next turn yet — check again shortly.
      setTimeout(() => { waitForNextTurn(sessionId, attempt + 1) }, 250)
    }
  }

  // Reloads the session's history from the server (picking up a user message
  // the server just persisted for a dequeued queued prompt) and then attaches
  // to the live stream. Ordering matters: attachStream ensures a trailing
  // assistant bubble, so we load history first.
  async function syncHistoryThenAttach(sessionId: string) {
    await reloadHistory(sessionId)
    if (!getState(sessionId).streaming) {
      attachStream(sessionId)
    }
  }

  // ── History reloads ──
  //
  // One at a time per session, and merged rather than replacing: the server
  // does not store tool calls (only the tab that watched a turn has them), and
  // the turn this tab is showing live has not stored its reply yet, so a plain
  // replace would wipe both. Two reloads overlapping could also land out of
  // order.
  const reloads = new Map<string, Promise<boolean>>()

  // Resolves false when the merge had to be skipped (see mergeHistory).
  function reloadHistory(sessionId: string): Promise<boolean> {
    const run = (reloads.get(sessionId) || Promise.resolve(true))
      .then(() => mergeHistory(sessionId))
      .catch((e) => {
        console.error('Failed to load history:', e)
        const sessionsStore = useSessionsStore()
        if (!sessionsStore.messages.has(sessionId)) sessionsStore.messages.set(sessionId, [])
        return true
      })
    reloads.set(sessionId, run)
    return run
  }

  async function mergeHistory(sessionId: string): Promise<boolean> {
    const sessionsStore = useSessionsStore()
    const fresh = (await fetchHistory(sessionId)).messages || []
    const s = getState(sessionId)
    // A stream that has not named its turn cannot be placed in the transcript,
    // so what is on screen stays. The follower retries once it has a name.
    if (s.streaming && !s.turnId && sessionsStore.messages.has(sessionId)) return false
    const local = sessionsStore.messages.get(sessionId) || []
    const calls = new Map<string, Message['toolCalls']>()
    for (const m of local) {
      if (m.role === 'assistant' && m.turnId && m.toolCalls?.length) calls.set(m.turnId, m.toolCalls)
    }
    for (const m of fresh) {
      const c = m.role === 'assistant' && m.turnId ? calls.get(m.turnId) : undefined
      if (c) m.toolCalls = c
    }
    const id = s.turnId
    if (s.streaming && id && !fresh.some(m => m.role === 'assistant' && m.turnId === id)) {
      // The turn on screen is still writing into its bubble: keep it, last.
      if (!fresh.some(m => m.role === 'user' && m.turnId === id)) {
        const asked = local.find(m => m.role === 'user' && m.turnId === id)
        if (asked) fresh.push(asked)
      }
      const live = local.find(m => m.role === 'assistant' && m.turnId === id)
        || [...local].reverse().find(m => m.role === 'assistant')
      if (live) fresh.push(live)
    }
    sessionsStore.messages.set(sessionId, fresh)
    return true
  }

  function dropMessages(sessionId: string, drop: Message[]) {
    const sessionsStore = useSessionsStore()
    const msgs = sessionsStore.messages.get(sessionId)
    if (!msgs) return
    sessionsStore.messages.set(sessionId, msgs.filter(m => !drop.includes(toRaw(m))))
  }

  function tagLastAssistant(sessionId: string, turnId: string) {
    const msgs = useSessionsStore().messages.get(sessionId) || []
    for (let i = msgs.length - 1; i >= 0; i--) {
      if (msgs[i].role === 'assistant') {
        if (!msgs[i].turnId) msgs[i].turnId = turnId
        return
      }
    }
  }

  // ── Following the open chat ──
  //
  // The tab shows the turns it starts on their own stream, and every other
  // turn — a guest's, one started in another tab, a queued prompt coming up —
  // from /events, through the same handleEvent pipeline, so tool calls,
  // questions, the plan panel and Stop behave as for its own.
  let follow: {
    sid: string
    follower: TurnFollower
    controller: AbortController | null
    timer?: ReturnType<typeof setTimeout>
    attempts: number
    ended: boolean
  } | null = null

  function pumpFollower(sessionId: string) {
    if (follow?.sid === sessionId) follow.follower.pump()
  }

  function hasReply(sessionId: string, turnId: string): boolean {
    const msgs = useSessionsStore().messages.get(sessionId) || []
    return msgs.some(m => m.role === 'assistant' && m.turnId === turnId)
  }

  function beginFollowedTurn(sessionId: string, turn: TurnStart) {
    const sessionsStore = useSessionsStore()
    const s = getState(sessionId)
    resetState(s)
    s.streaming = true
    s.followed = true
    s.turnId = turn.turn_id || null
    const now = new Date().toISOString()
    const msgs = sessionsStore.messages.get(sessionId) || []
    const reply: Message = { role: 'assistant', content: '', createdAt: now, turnId: turn.turn_id }
    // The prompt is already in history when the turn was running at reload.
    const at = turn.turn_id ? msgs.findIndex(m => m.role === 'user' && m.turnId === turn.turn_id) : -1
    if (at >= 0) {
      msgs.splice(at + 1, 0, reply)
    } else {
      if (turn.prompt) {
        msgs.push({
          role: 'user', content: turn.prompt, createdAt: now, turnId: turn.turn_id,
          authorId: turn.author_id || undefined, authorName: turn.author_name || undefined,
        })
      }
      msgs.push(reply)
    }
    sessionsStore.messages.set(sessionId, msgs)
    // The server has taken this prompt off its queue.
    refreshQueue(sessionId)
    scrollToBottomIfActive(sessionId)
  }

  function newFollower(sessionId: string): TurnFollower {
    // The turn the follower is showing. Once the tab has moved on — the owner
    // stopped it and sent something new — its late frames must not land in
    // the next reply.
    let shown: string | null = null
    const mine = () => shown !== null && getState(sessionId).turnId === shown
    return new TurnFollower({
      sync: () => reloadHistory(sessionId),
      canSync: () => { const s = getState(sessionId); return !(s.streaming && !s.turnId) },
      decide: (turn) => {
        const s = getState(sessionId)
        if (s.streaming && !s.turnId) return 'wait'
        if (turn.turn_id && turn.turn_id === s.turnId) return 'skip'
        if (turn.turn_id && hasReply(sessionId, turn.turn_id)) return 'skip'
        return s.streaming ? 'wait' : 'render'
      },
      begin: (turn) => {
        beginFollowedTurn(sessionId, turn)
        shown = getState(sessionId).turnId
      },
      frame: (event, data) => { if (mine()) handleEvent(sessionId, event, data) },
      end: (completed) => {
        const was = mine()
        shown = null
        if (!was) return
        if (completed) finalize(sessionId)
        else settleCutOff(sessionId)
      },
      closed: (reason) => {
        if (follow?.sid === sessionId) follow.ended = true
        if (reason === 'session_deleted') void forgetDeletedSession(sessionId)
      },
    })
  }

  // A followed turn left half-shown — the owner opened another chat, or the
  // connection dropped. Keep what arrived; the next reload replaces it with
  // what the server stored. Nothing finished, so nothing is announced, and the
  // turn is released so the next connection shows it again if it is running.
  function settleCutOff(sessionId: string) {
    const sessionsStore = useSessionsStore()
    const s = getState(sessionId)
    s.streaming = false
    s.turnId = null
    s.currentQuestion = null
    s.currentPermission = null
    if (s.toolCalls.length > 0) sessionsStore.setLastAssistantToolCalls(sessionId, [...s.toolCalls])
  }

  /** Follow the chat on screen; null stops following. */
  function followSession(sessionId: string | null) {
    if (follow?.sid === sessionId) return
    unfollow()
    if (!sessionId) return
    follow = { sid: sessionId, follower: newFollower(sessionId), controller: null, attempts: 0, ended: false }
    connectFollow()
  }

  function unfollow() {
    if (!follow) return
    const f = follow
    follow = null
    useShareStore().clearLive(f.sid)
    clearTimeout(f.timer)
    f.controller?.abort()
    f.follower.reset()
  }

  function connectFollow() {
    const f = follow
    if (!f || f.ended) return
    f.follower.reset()
    f.controller = followEvents(
      f.sid,
      (event, data) => {
        if (event === 'ready') f.attempts = 0
        // Who is here is about the chat, not a turn: it never waits in line.
        if (event === 'presence') {
          useShareStore().setLive(f.sid, Array.isArray(data?.guests) ? data.guests : [])
          return
        }
        // Includes what guests queued, which this tab has no other way to learn.
        if (event === 'queue') {
          getState(f.sid).queuedMessages = Array.isArray(data?.queue) ? data.queue : []
          return
        }
        f.follower.push(event, data)
      },
      () => retryFollow(f),
      () => retryFollow(f),
    )
  }

  function retryFollow(f: NonNullable<typeof follow>) {
    if (follow !== f || f.ended) return
    useShareStore().clearLive(f.sid)
    f.controller = null
    f.follower.reset()
    clearTimeout(f.timer)
    const delay = Math.min(30000, 1000 * 2 ** Math.min(f.attempts, 5))
    f.attempts++
    f.timer = setTimeout(() => { if (follow === f) connectFollow() }, delay)
  }

  // Another tab deleted the chat this one has open.
  async function forgetDeletedSession(sessionId: string) {
    const sessionsStore = useSessionsStore()
    await sessionsStore.loadSessions()
    if (sessionsStore.sessions.some(x => x.id === sessionId)) return
    dropSession(sessionId)
    sessionsStore.messages.delete(sessionId)
    if (sessionsStore.currentSessionId !== sessionId) return
    const next = sessionsStore.sessions[0]
    if (next) await sessionsStore.switchSession(next.id)
    else await sessionsStore.createSession({ name: 'New chat' })
  }

  function handleEvent(sessionId: string, event: string, data: any) {
    const sessionsStore = useSessionsStore()
    const s = getState(sessionId)

    switch (event) {
      case 'session_id':
        // The first frame of a turn names it: from here the follower
        // can tell this tab's turn from anyone else's.
        if (data?.turn_id) {
          s.turnId = data.turn_id
          tagLastAssistant(sessionId, data.turn_id)
          pumpFollower(sessionId)
        }
        break

      case 'chunk':
        s.streamingContent += (data.content || data.text || '')
        sessionsStore.updateLastAssistantMessage(sessionId, s.streamingContent)
        scrollToBottomIfActive(sessionId)
        break

      case 'tool_call': {
        const callId = data.call_id || `${data.tool || data.name}_${s.toolCalls.length}`
        const toolName = data.tool || data.name || 'unknown'
        const argsStr = typeof data.args === 'object' ? JSON.stringify(data.args) : (data.args || data.input || '')
        // A repeated call_id means an update to an existing tool row — used
        // by the todo tool, which re-sends the whole checklist under a
        // stable id as items move pending → in_progress → completed. Update
        // the row in place so it renders as one live checklist instead of a
        // new card per update.
        const existing = s.toolCalls.find(t => t.callId === callId)
        if (existing) {
          existing.name = toolName
          existing.args = argsStr
          existing.status = 'running'
          existing.mcpServer = data.mcp_server || ''
        } else {
          s.toolCalls.push({
            name: toolName,
            callId,
            args: argsStr,
            status: 'running',
            mcpServer: data.mcp_server || '',
          })
        }

        if (toolName === 'ask_user' && data.args && typeof data.args === 'object') {
          const question = data.args.question || data.args.text || ''
          if (question) {
            s.currentQuestion = {
              question,
              choices: data.args.choices,
              allow_freeform: data.args.allow_freeform !== false,
            }
            notifyQuestion(sessionId, s.currentQuestion)
          }
        }

        scrollToBottomIfActive(sessionId)
        break
      }

      case 'tool_done': {
        const callId = data.call_id
        const toolName = data.tool || data.name || ''
        let target = s.toolCalls.find(t => t.callId === callId)
        if (!target) {
          target = [...s.toolCalls].reverse().find(
            t => t.name === toolName && t.status === 'running'
          )
        }
        if (!target && s.toolCalls.length > 0) {
          target = [...s.toolCalls].reverse().find(t => t.status === 'running')
        }
        if (target) {
          target.status = data.success === false ? 'error' : 'done'
          target.output = data.output || data.result || ''
          // A tool_done matched by name or by "any running" may be the first
          // frame that carries the attribution, so fill it in if it is still
          // missing rather than assuming tool_call already set it.
          if (!target.mcpServer && data.mcp_server) target.mcpServer = data.mcp_server
        }

        if (toolName === 'ask_user' && s.currentQuestion) {
          s.currentQuestion = null
        }
        break
      }

      case 'tool_blocked': {
        // The backend blocked a tool call via its permission system (an "ask"
        // rule auto-rejected in headless run, or an explicit "deny"). Flip
        // the matching tool row into a distinct "blocked" state so the UI
        // can show a clear "needs approval / denied" card. 'reason' carries
        // the explanation.
        const callId = data.call_id
        const toolName = data.tool || data.name || ''
        let target = s.toolCalls.find(t => t.callId === callId)
        if (!target) {
          target = [...s.toolCalls].reverse().find(
            t => t.name === toolName && t.status === 'running'
          )
        }
        if (!target && s.toolCalls.length > 0) {
          target = [...s.toolCalls].reverse().find(t => t.status === 'running')
        }
        if (target) {
          target.status = 'blocked'
          target.output = data.reason || data.output || 'Permission denied'
          if (!target.mcpServer && data.mcp_server) target.mcpServer = data.mcp_server
        } else {
          // No matching row (e.g. the tool_call frame was missed) — append
          // one so the block is still visible.
          s.toolCalls.push({
            name: toolName || 'tool',
            callId: callId || `blocked_${s.toolCalls.length}`,
            args: typeof data.args === 'object' ? JSON.stringify(data.args) : (data.args || ''),
            status: 'blocked',
            output: data.reason || data.output || 'Permission denied',
            mcpServer: data.mcp_server || '',
          })
        }
        scrollToBottomIfActive(sessionId)
        break
      }

      case 'question':
        s.currentQuestion = {
          question: data.question || data.text || '',
          choices: data.choices,
          allow_freeform: data.allow_freeform !== false,
        }
        notifyQuestion(sessionId, s.currentQuestion)
        scrollToBottomIfActive(sessionId)
        break

      case 'permission':
        // Interactive tool-permission request from the backend. Surface an
        // allow/deny card; the user's decision is sent via respondPermission
        // (POST /api/sessions/{id}/permission).
        s.currentPermission = {
          permissionId: data.permission_id || '',
          permission: data.permission || '',
          patterns: Array.isArray(data.patterns) ? data.patterns : undefined,
          callId: data.call_id || undefined,
        }
        notifyQuestion(sessionId, {
          question: `Permission requested: ${s.currentPermission.permission || 'tool'}`,
        })
        scrollToBottomIfActive(sessionId)
        break

      case 'permission_answered':
        // Someone answered it, a guest of a shared chat perhaps:
        // the choice is made, so stop offering it.
        if (s.currentPermission?.permissionId === data.permission_id) {
          s.currentPermission = null
          s.notifiedQuestionKey = null
        }
        break

      case 'usage':
        // Attached to the message this turn produced rather than held per
        // session, so each response keeps its own figures instead of the
        // whole chat sharing the most recent ones.
        sessionsStore.setLastAssistantUsage(sessionId, {
          tokens_in: data.tokens_in,
          tokens_in_reported: data.tokens_in_reported,
          tokens_out: data.tokens_out,
          cost: data.cost,
          premium_reqs: data.premium_reqs,
          tool_calls: data.tool_calls,
          files_modified: data.files_modified,
          lines_added: data.lines_added,
          lines_removed: data.lines_removed,
        })
        break

      case 'error': {
        const msg = data.message || 'Unknown error'
        s.errorMessage = msg
        // Part of the streamed text, so finalize() and a replay keep it after the
        // partial reply; the server saves the same lines (chat/stream.go).
        const text = s.streamingContent
        s.streamingContent = `${text}${text && !text.endsWith('\n\n') ? '\n\n' : ''}⚠ ${msg}`
        sessionsStore.updateLastAssistantMessage(sessionId, s.streamingContent)
        scrollToBottomIfActive(sessionId)
        notifyInactiveSession(sessionId, `${sessionName(sessionId)} hit an error`, s.errorMessage || undefined)
        break
      }

      case 'done':
        break

      case 'title':
        // The server derived a title from the first prompt and has already
        // stored it; reflect it in the sidebar without waiting for a reload.
        if (data.title) {
          const s2 = sessionsStore.sessions.find(x => x.id === sessionId)
          if (s2) s2.name = data.title
        }
        break

      case 'step':
        // Lightweight progress heartbeat from backends that don't emit
        // per-token text deltas. We bump a counter so the typing indicator
        // can become more assertive ("Working…") as the agent makes progress
        // through steps.
        s.stepCount += 1
        // A demoted subagent heartbeat carries the child's current tool as a
        // label (data.tool, possibly empty). Attach it to any running `task`
        // row so the delegation shows live progress instead of a frozen pill.
        if (data.tool !== undefined) {
          for (const t of s.toolCalls) {
            if (t.name === 'task' && t.status === 'running') {
              t.progressSteps = (t.progressSteps || 0) + 1
              if (data.tool) t.progress = data.tool
            }
          }
        }
        scrollToBottomIfActive(sessionId)
        break

      case 'message':
        if (typeof data === 'string') {
          s.streamingContent += data
          sessionsStore.updateLastAssistantMessage(sessionId, s.streamingContent)
        } else if (data.content) {
          s.streamingContent += data.content
          sessionsStore.updateLastAssistantMessage(sessionId, s.streamingContent)
        }
        scrollToBottomIfActive(sessionId)
        break
    }
  }

  function finalize(sessionId: string) {
    const sessionsStore = useSessionsStore()
    const s = getState(sessionId)
    s.streaming = false
    if (s.streamingContent) {
      sessionsStore.updateLastAssistantMessage(sessionId, s.streamingContent)
    }
    if (s.toolCalls.length > 0) {
      sessionsStore.setLastAssistantToolCalls(sessionId, [...s.toolCalls])
    }
    notifyInactiveSession(sessionId, `${sessionName(sessionId)} finished`, 'The response is ready.')
    sessionsStore.loadSessions().catch(() => {})
    processQueue(sessionId)
    pumpFollower(sessionId)
  }

  function stopStreaming(targetSessionId?: string) {
    const sessionId = targetSessionId || currentId()
    if (!sessionId) return
    const s = states.value.get(sessionId)
    if (!s) return
    const wasStreaming = s.streaming
    if (s.abortController) {
      s.abortController.abort()
      s.abortController = null
    }
    s.streaming = false
    // Aborting the browser's SSE connection only disconnects this client; the
    // server keeps the backend process running (so work survives a reload).
    // Tell the server to actually stop the turn. Best-effort/fire-and-forget.
    if (wasStreaming) {
      void stopChat(sessionId)
    }
    pumpFollower(sessionId)
  }

  // opencode takes no answer mid-turn, so the answer goes out as the next message.
  async function answerQuestion(answer: string, targetSessionId?: string) {
    const sessionsStore = useSessionsStore()
    const sessionId = targetSessionId || sessionsStore.currentSessionId
    if (!sessionId) return
    const s = getState(sessionId)
    s.currentQuestion = null
    s.notifiedQuestionKey = null
    await sendMessage(answer, sessionId)
  }

  // respondToPermission sends the user's decision (once/always/reject) on an
  // interactive tool-permission request to the backend.
  async function respondToPermission(response: PermissionResponse, targetSessionId?: string) {
    const sessionsStore = useSessionsStore()
    const sessionId = targetSessionId || sessionsStore.currentSessionId
    if (!sessionId) return
    const s = getState(sessionId)
    const perm = s.currentPermission
    if (!perm) return
    // Optimistically clear the card so the UI doesn't double-submit.
    s.currentPermission = null
    s.notifiedQuestionKey = null
    try {
      await respondPermission(sessionId, perm.permissionId, response)
    } catch (e) {
      // Restore the card so the user can retry on failure.
      s.currentPermission = perm
      throw e
    }
  }

  /** Drop per-session state when a session is deleted. */
  function dropSession(sessionId: string) {
    if (follow?.sid === sessionId) unfollow()
    stopStreaming(sessionId)
    states.value.delete(sessionId)
  }

  /**
   * Re-attach to an in-flight (server-side) chat for the given session.
   * Used after a page reload so visible work resumes streaming. Re-uses the
   * normal handleEvent/finalize pipeline so tool calls, chunks, questions
   * and the final usage block all show up exactly as on the first connection.
   */
  function attachStream(sessionId: string) {
    if (!sessionId) return
    const s = getState(sessionId)
    if (s.streaming) return
    const sessionsStore = useSessionsStore()

    resetState(s)
    s.streaming = true

    // Make sure there's an assistant bubble to write into. If the last
    // message is already an assistant placeholder we re-use it; otherwise we
    // append a fresh empty assistant message.
    const msgs = sessionsStore.messages.get(sessionId) || []
    const last = msgs[msgs.length - 1]
    if (!last || last.role !== 'assistant') {
      sessionsStore.addMessage(sessionId, { role: 'assistant', content: '' })
    } else if (last.content) {
      // Server replays from the start of the run — start from a clean slate.
      sessionsStore.updateLastAssistantMessage(sessionId, '')
    }

    s.abortController = attachChat(
      sessionId,
      (event, data) => handleEvent(sessionId, event, data),
      () => finalize(sessionId),
      (err) => {
        console.error(`[session ${sessionId}] Attach error:`, err)
        s.streaming = false
        pumpFollower(sessionId)
        if (isBackendUnavailableError(err)) {
          s.errorMessage = err.message
          removeEmptyAssistantPlaceholder(sessionId)
        }
      },
    )
  }

  return {
    // reactive proxies for the active session
    streaming, streamingContent, toolCalls, currentQuestion, currentPermission, errorMessage, queuedMessage, queuedMessages,
    stepCount, streamingFollowed,
    // global helpers
    anyStreaming, isStreamingSession, getState,
    // actions
    sendMessage, abortAndSend, queueMessage, clearQueue, removeQueuedMessage, refreshQueue, stopStreaming, answerQuestion,
    respondToPermission,
    handleEvent, finalize, dropSession, attachStream,
    followSession, reloadHistory,
  }
})
