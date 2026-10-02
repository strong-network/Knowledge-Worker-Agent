<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, nextTick, watch, onBeforeUnmount, onMounted } from 'vue'
import { useChatStore } from '../../stores/chat'
import { useSessionsStore } from '../../stores/sessions'
import { useUploadsStore } from '../../stores/uploads'
import { useUiStore } from '../../stores/ui'
import { useProjectsStore } from '../../stores/projects'
import { useProvidersStore } from '../../stores/providers'
import { useToolboxStore } from '../../stores/toolbox'
import { exportSession as apiExportSession, updateSessionConfig, optimizeChatPrompt } from '../../api'
import { useMe } from '../../composables/useMe'
import { useDictation, dictationSupported, dictationMessages } from '../../composables/useDictation'
import { SLASH_COMMANDS } from '../../utils/constants'
import { resolvePreset, shortModelName } from '../../utils/models'
import SlashCommands from './SlashCommands.vue'

const chatStore = useChatStore()
const sessionsStore = useSessionsStore()
const uploads = useUploadsStore()
const ui = useUiStore()
const projectsStore = useProjectsStore()
const providers = useProvidersStore()
const toolbox = useToolboxStore()

// The textarea is bound to the active session's draft so switching sessions
// in the sidebar swaps the in-progress text in/out. Persistence is handled
// in the sessions store (debounced PUT to /api/sessions/{id}/draft).
const prompt = computed<string>({
  get: () => sessionsStore.currentDraft,
  set: (val: string) => {
    const sid = sessionsStore.currentSessionId
    if (!sid) return
    sessionsStore.setDraft(sid, val)
  },
})

const textareaEl = ref<HTMLTextAreaElement | null>(null)
const showSlash = ref(false)
const slashFilter = ref('')
const slashActiveIndex = ref(0)

const fileInputEl = ref<HTMLInputElement | null>(null)
const inputAreaEl = ref<HTMLElement | null>(null)

// Composer popovers ("+" menu and agent selector). Only one open at a time.
const showPlusMenu = ref(false)
const showAgentMenu = ref(false)

// ── Toolbox pill (replaces the connectors popover and the skills picker) ──
//
// Skills and connectors used to be two composer popovers plus a chip, each
// owning its own copy of the same lists. They are now one modal, so this
// component keeps no list state at all — it reads the summary it renders from
// the toolbox store and opens the modal.
//
// The pill has to be right at rest, not only after it has been opened once.
// The old plug button's count came from a list fetched when the popover opened,
// so it read 0 until you clicked it — tolerable for a badge on a button whose
// job was to open that list, and misleading on a pill whose job is to *be* the
// summary. So the selection is loaded on mount; the store reloads it on every
// session switch.
onMounted(() => { void toolbox.loadConnectors() })

const connectorsOn = computed(() => toolbox.connectorsOn)
// Falls back to the id when the skills list has not loaded, so `/skill <name>`
// followed immediately by a look at the composer still shows something. The
// fallback lives in the store; see `selectedSkill` there.
const armedSkill = computed(() => toolbox.selectedSkill)

const armedSkillTitle = computed(() =>
  armedSkill.value
    ? `The ${armedSkill.value.name} skill will be applied to your next message, then cleared.`
    : '')

// One entry point for every way the Toolbox opens: the pill, the armed-skill
// name, `/skill`, and Cmd/Ctrl+K. `prepareForOpen` clears any stale search;
// passing a type scopes the modal to the half the caller means.
function openToolbox(opts?: { query?: string; type?: 'skill' | 'connector' }) {
  closeMenus()
  toolbox.prepareForOpen(opts)
  ui.openModal('toolbox')
}

function clearArmedSkill() {
  toolbox.clearSkill()
}

// Reading and clearing in one step keeps the per-turn reset honest: every exit
// from the composer takes the skill with it, so it cannot reach a later turn.
function takeArmedSkill(): string | undefined {
  const id = toolbox.selectedSkillId || undefined
  toolbox.clearSkill()
  return id
}

const canSend = computed(() => prompt.value.trim() || uploads.attachedFiles.length > 0)
const isStreaming = computed(() => chatStore.streaming)
// Legacy GitHub Copilot sessions are read-only: block composing/sending.
const isDeprecated = computed(() => sessionsStore.currentSessionDeprecated)

// ── Optimize my prompt ─────────────────────────────────────────────
// A secondary composer action that rewrites the current draft to be clearer and
// more specific via a one-shot assist, replacing the draft in place. It never
// sends. The rewrite is reversible: `preOptimizeDraft` holds the exact text from
// before the last optimize so Undo can restore it, until the user sends or
// optimizes again. On failure the draft is left untouched and a short,
// non-blocking message is shown.
const optimizing = ref(false)
const preOptimizeDraft = ref<string | null>(null)
const optimizeError = ref('')

// Enabled only when there is a non-empty draft, not while streaming/optimizing,
// and not in a read-only session.
const canOptimize = computed(
  () => !!prompt.value.trim() && !optimizing.value && !isStreaming.value && !isDeprecated.value && !dictActive.value,
)
const canUndoOptimize = computed(() => preOptimizeDraft.value !== null && !optimizing.value)

async function optimizePrompt() {
  const draft = prompt.value.trim()
  if (!draft || optimizing.value) return
  optimizeError.value = ''
  optimizing.value = true
  try {
    const improved = await optimizeChatPrompt(draft)
    // Remember the pre-optimize draft for a single-level Undo, then replace.
    preOptimizeDraft.value = prompt.value
    prompt.value = improved
    nextTick(autoResize)
  } catch (e) {
    // Leave the draft exactly as typed; surface a short, non-blocking message.
    optimizeError.value = (e as Error)?.message || 'Couldn\u2019t optimize just now — try again'
  } finally {
    optimizing.value = false
  }
}

function undoOptimize() {
  if (preOptimizeDraft.value === null) return
  prompt.value = preOptimizeDraft.value
  preOptimizeDraft.value = null
  optimizeError.value = ''
  nextTick(autoResize)
}

// Clear the optimize/undo state once the draft leaves the composer (send/queue)
// or the user switches sessions, so Undo never restores into the wrong context.
function clearOptimizeState() {
  preOptimizeDraft.value = null
  optimizeError.value = ''
}

// ── Dictation ────────────────────────────────────────────────────
// Speech goes into the draft where the cursor was and updates in place while
// the recording runs; the composer is read-only until it ends, and nothing is
// ever sent. A dictation belongs to the chat it started in: switching chats
// discards it and puts that chat's draft back as it was.
const { me } = useMe()
const showDictate = computed(() => me.value.voice_available === true && dictationSupported())

// Where the words go: the draft split at the cursor when recording started.
let dictTarget: { sid: string; before: string; after: string } | null = null

function dictatedDraft(t: { before: string; after: string }, text: string): { value: string; caret: number } {
  if (!text) return { value: t.before + t.after, caret: t.before.length }
  const lead = t.before && !/\s$/.test(t.before) ? ' ' : ''
  const trail = /^[\p{L}\p{N}]/u.test(t.after) ? ' ' : ''
  const caret = t.before.length + lead.length + text.length
  return { value: t.before + lead + text + trail + t.after, caret }
}

const dict = useDictation({
  onText(text) {
    if (!dictTarget) return
    sessionsStore.setDraft(dictTarget.sid, dictatedDraft(dictTarget, text).value)
    nextTick(autoResize)
  },
  onEnd(text) {
    const t = dictTarget
    dictTarget = null
    if (!t) return
    const { value, caret } = dictatedDraft(t, text)
    sessionsStore.setDraft(t.sid, value)
    if (!text || t.sid !== sessionsStore.currentSessionId) return
    // Editable again, with the cursor after the dictated words.
    nextTick(() => {
      const el = textareaEl.value
      if (!el) return
      el.focus()
      el.setSelectionRange(caret, caret)
      autoResize()
    })
  },
})
const {
  state: dictState,
  active: dictActive,
  elapsed: dictElapsed,
  problem: dictProblem,
  announcement: dictAnnouncement,
} = dict

const dictLabel = computed(() =>
  dictState.value === 'finishing' ? 'Finishing' : dictActive.value ? 'Stop dictation' : 'Dictate',
)
const dictClock = computed(() => {
  const s = dictElapsed.value
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
})

function toggleDictation() {
  if (dictState.value === 'finishing') return
  if (dictState.value === 'idle') {
    const sid = sessionsStore.currentSessionId
    if (!sid || isDeprecated.value || optimizing.value) return
    const value = prompt.value
    const el = textareaEl.value
    const at = el ? Math.min(el.selectionEnd, value.length) : value.length
    dictTarget = { sid, before: value.slice(0, at), after: value.slice(at) }
    showSlash.value = false
    closeMenus()
  }
  dict.toggle()
}

// Escape discards the dictation, unless something nearer the user wants it
// first: an open composer menu or slash list closes instead, and a dialog or
// another panel with focus keeps its own Escape. Capture phase, so the menus
// are still open when this looks.
function onDictationKeydown(e: KeyboardEvent) {
  if (e.key !== 'Escape' || e.defaultPrevented) return
  if (showSlash.value || showPlusMenu.value || showAgentMenu.value || ui.activeModal) return
  const focused = document.activeElement
  if (focused && focused !== document.body && !inputAreaEl.value?.contains(focused)) return
  e.preventDefault()
  dict.discard()
}
watch(dictActive, (on) => {
  if (on) document.addEventListener('keydown', onDictationKeydown, true)
  else document.removeEventListener('keydown', onDictationKeydown, true)
})
onBeforeUnmount(() => document.removeEventListener('keydown', onDictationKeydown, true))

// Projects: in a project chat the working directory is fixed to the shared
// project workspace, so the repo/folder "+" items (which change the workdir)
// are hidden, and a project-context banner is shown above the composer.
const inProject = computed(() => !!sessionsStore.currentProjectId)
const currentProject = computed(() =>
  inProject.value ? projectsStore.getProject(sessionsStore.currentProjectId) : undefined,
)

const filteredSlashCommands = computed(() => {
  const f = slashFilter.value.toLowerCase()
  return SLASH_COMMANDS.filter(c => c.cmd.includes(f) || c.desc.toLowerCase().includes(f))
})

// ── Agent selector ───────────────────────────────────────────────────────
//
// The friendly, non-technical selector abstracts raw model/mode choices into
// three categories:
//   • Default  — everyday, cost-efficient model (no agent).
//   • Thinking — higher-capability model for complex reasoning.
//   • Custom agents — grouped by where they came from: the set the central
//     config repo assigns to this workspace ("Provided by IT") and the user's
//     own agents ("My agents"). They used to share one list, which made an
//     agent nobody assigned indistinguishable from one IT provides.
//
// Default/Thinking are represented here as a session flag we persist to the
// session config; custom agents set config.agent. Selecting any of these
// updates the current session via the existing config API, so it also works
// mid-conversation.
const currentAgent = computed(() => sessionsStore.currentSession?.agent || '')

// The stored session agent is an opencode-resolvable id (file stem); map it to
// its friendly display name for labels/tooltips, falling back to the id.
const currentAgentName = computed(() => {
  const id = currentAgent.value
  if (!id) return ''
  const match = sessionsStore.agents.find((a) => a.id === id)
  return match?.name || id
})

// Which built-in preset (if any) the current session's model corresponds to.
// Default maps to the latest Sonnet, Thinking to the latest Opus. Any other
// explicitly-picked model is neither preset and is shown by name.
//
// A session persists whichever model was pinned when it was last configured,
// and that pin can name a model the user can no longer run (e.g. a Vertex model
// pinned before signing in to GitHub Copilot). The backend already falls back to
// the default in that case, so an unavailable pin is treated here as "no model"
// — the composer shows Default rather than advertising a model that won't run.
const rawModel = computed(() => sessionsStore.currentSession?.model || '')
const currentModel = computed(() => {
  const m = rawModel.value
  if (!m) return ''
  const list = sessionsStore.models
  // Until the model list loads, trust the stored pin.
  if (!list.length) return m
  return list.includes(m) ? m : ''
})

// The label shown on the composer's model/agent button.
//   • A custom agent → the agent name.
//   • No model / the resolved Default preset → "Default".
//   • The resolved Thinking preset → "Thinking".
//   • Any other explicitly-picked model → that model's short name, so a custom
//     model chosen via "Select Model" is shown instead of a built-in preset.
//
// What each preset resolves to comes from the provider registry when the
// workspace's providers nominate models, and from the Claude heuristic
// otherwise — so on a non-Claude provider the chips still name a model
// that exists instead of both collapsing to nothing.
const defaultModel = computed(() =>
  resolvePreset('default', sessionsStore.models, providers.presets),
)
const thinkingModel = computed(() =>
  resolvePreset('thinking', sessionsStore.models, providers.presets),
)
const isDefaultPreset = computed(
  () => !currentModel.value || currentModel.value === defaultModel.value,
)
const isThinkingPreset = computed(
  () => !!currentModel.value && currentModel.value === thinkingModel.value,
)
const selectedLabel = computed(() => {
  if (currentAgent.value) return currentAgentName.value
  if (isThinkingPreset.value) return 'Thinking'
  if (isDefaultPreset.value) return 'Default'
  return shortModelName(currentModel.value)
})

// Tooltip for the composer button. Names the kind (agent / preset / model) and
// shows the full model id for a custom model so the exact selection is clear.
const selectedTitle = computed(() => {
  if (currentAgent.value) return 'Agent: ' + currentAgentName.value
  if (isThinkingPreset.value || isDefaultPreset.value) return 'Model: ' + selectedLabel.value
  return 'Model: ' + currentModel.value
})

async function pickBuiltin(kind: 'default' | 'thinking') {
  showAgentMenu.value = false
  const sid = sessionsStore.currentSessionId
  if (!sid) return
  // Resolve the friendly preset to a concrete model from the available list.
  const model = kind === 'thinking' ? thinkingModel.value : defaultModel.value
  // Clear any custom agent; Default/Thinking are agent-less presets.
  await applyConfig(sid, { agent: '', model: model || undefined })
  const s = sessionsStore.currentSession
  if (s) {
    s.agent = ''
    if (model) s.model = model
  }
  if (!model) {
    // No matching model available yet (e.g. not signed in) — still switch the
    // preset label, but note the model couldn't be pinned.
    ;(window as any).showToast?.(
      kind === 'thinking'
        ? 'Thinking selected — Opus model not available yet'
        : 'Default selected — Sonnet model not available yet',
    )
    return
  }
  ;(window as any).showToast?.(
    kind === 'thinking' ? `Thinking · ${model}` : `Default · ${model}`,
  )
}

async function pickAgent(id: string) {
  showAgentMenu.value = false
  const sid = sessionsStore.currentSessionId
  if (!sid) return
  await applyConfig(sid, { agent: id })
  const s = sessionsStore.currentSession
  if (s) s.agent = id
}

function buildNewAgent() {
  showAgentMenu.value = false
  ui.openModal('agent-builder')
}

async function applyConfig(sid: string, cfg: { agent?: string; model?: string }) {
  try {
    await updateSessionConfig(sid, cfg)
  } catch (e) {
    console.error('Failed to update session config:', e)
    ;(window as any).showToast?.('Could not change agent')
  }
}

// ── "+" menu actions ─────────────────────────────────────────────────────
function plusAddFiles() {
  showPlusMenu.value = false
  triggerFileInput()
}
function plusMcp() {
  showPlusMenu.value = false
  ui.openModal('mcp-servers')
}
function plusAdvanced() {
  showPlusMenu.value = false
  // Advanced options opens the full (legacy) New Chat experience unchanged.
  ui.openModal('new-chat')
}
// Connect a GitHub repository / Use an existing folder open focused pickers
// (not the full Advanced config view).
function plusGithub() {
  showPlusMenu.value = false
  ui.openModal('github-repo')
}
function plusExistingFolder() {
  showPlusMenu.value = false
  ui.openModal('folder-picker')
}

function closeMenus() {
  showPlusMenu.value = false
  showAgentMenu.value = false
}

// ── Popover placement ────────────────────────────────────────────────────
//
// The composer popovers are anchored to buttons that sit in very different
// places depending on the chat state: on the new-chat screen the composer is
// centred on the mid-line, while in an active chat it is pinned to the bottom
// of the window. A fixed direction therefore cannot work for both.
//
// These menus used to be pinned above the button unconditionally and capped
// with a viewport-relative `max-height`. On the new-chat screen that opened
// the agent list upwards off the top of the window, and because the cap was
// measured against the viewport rather than against the space actually above
// the button, it never engaged — so a long list was hard-clipped by
// `.chat-main { overflow: hidden }` with no scrollbar to reach the items.
//
// Placement is measured instead. We prefer to drop downwards, and only flip
// up when there isn't enough room below. Either way the menu is capped to the
// space that actually exists, so a long agent list scrolls rather than being
// cut off.
const POP_GAP = 8      // matches the offset baked into the popover position
const POP_EDGE = 12    // breathing room against the clipping edge
const POP_MIN = 180    // a shorter drop-down than this isn't usable — flip up

const popoverDrop = ref<'up' | 'down'>('down')
const popoverMaxHeight = ref(0)
const popoverClass = computed(() => `drop-${popoverDrop.value}`)
const popoverStyle = computed(() =>
  popoverMaxHeight.value ? { maxHeight: `${popoverMaxHeight.value}px` } : {},
)

function placePopover() {
  const pop = inputAreaEl.value?.querySelector('.composer-popover') as HTMLElement | null
  if (!pop) return
  const anchor = pop.parentElement as HTMLElement | null
  if (!anchor) return
  const a = anchor.getBoundingClientRect()

  // An absolutely positioned menu is still cut off by an ancestor's
  // `overflow: hidden`, so the usable band is the viewport intersected with
  // every clipping ancestor — not the viewport alone.
  let top = 0
  let bottom = window.innerHeight
  for (let n = anchor.parentElement; n && n !== document.documentElement; n = n.parentElement) {
    const s = getComputedStyle(n)
    if (s.overflow === 'visible' && s.overflowX === 'visible' && s.overflowY === 'visible') continue
    const r = n.getBoundingClientRect()
    top = Math.max(top, r.top)
    bottom = Math.min(bottom, r.bottom)
  }

  const below = bottom - a.bottom - POP_GAP - POP_EDGE
  const above = a.top - top - POP_GAP - POP_EDGE
  // Prefer downwards; fall back to whichever side has more room.
  const drop = below >= POP_MIN || below >= above ? 'down' : 'up'
  popoverDrop.value = drop
  popoverMaxHeight.value = Math.max(120, Math.floor(drop === 'down' ? below : above))
}

function togglePlusMenu() {
  showAgentMenu.value = false
  showPlusMenu.value = !showPlusMenu.value
}
function toggleAgentMenu() {
  showPlusMenu.value = false
  showAgentMenu.value = !showAgentMenu.value
}

// Close popovers on outside click / Escape.
function onGlobalPointerDown(e: MouseEvent) {
  const t = e.target as HTMLElement
  if (!t.closest('.composer-menu-wrap')) closeMenus()
}
function onGlobalKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') closeMenus()
}
watch(() => showPlusMenu.value || showAgentMenu.value, async (open) => {
  if (open) {
    document.addEventListener('pointerdown', onGlobalPointerDown)
    document.addEventListener('keydown', onGlobalKeydown)
    window.addEventListener('resize', placePopover)
    // The menu has to exist before it can be measured.
    await nextTick()
    placePopover()
  } else {
    document.removeEventListener('pointerdown', onGlobalPointerDown)
    document.removeEventListener('keydown', onGlobalKeydown)
    window.removeEventListener('resize', placePopover)
  }
})
onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onGlobalPointerDown)
  document.removeEventListener('keydown', onGlobalKeydown)
  window.removeEventListener('resize', placePopover)
  window.removeEventListener('resize', autoResize)
})

// Resize the textarea whenever the bound draft changes — both because the
// user is typing and because they just switched to a session that has a
// multi-line draft saved.
watch(() => sessionsStore.currentSessionId, () => {
  // A dictation belongs to the chat it started in.
  dict.discard()
  dict.clearProblem()
  // A switched-to session has its own draft; drop any pending Undo state.
  clearOptimizeState()
  nextTick(() => {
    autoResize()
    // Close the slash popover / menus when changing sessions.
    showSlash.value = false
    closeMenus()
  })
})
watch(prompt, () => {
  nextTick(autoResize)
})

// Size the textarea to any restored draft on mount (e.g. after a reload) so a
// long saved message shows at full height without needing a keystroke.
onMounted(() => {
  nextTick(autoResize)
  // The room the composer has is measured, so it has to be re-measured when
  // the window changes size — otherwise a cap worked out for a tall window
  // survives into a short one and the composer hangs off the bottom again.
  window.addEventListener('resize', autoResize)
})

// Breathing room kept under the composer so it never sits flush against the
// bottom edge of the chat area.
const COMPOSER_BOTTOM_GAP = 12
// Matches the `min-height` on #prompt-input: one line of text.
const MIN_TEXTAREA_HEIGHT = 26

// The tallest the textarea may grow to *right now*.
//
// This is measured rather than a fixed fraction of the viewport. A blind
// `innerHeight / 2` ignores where the composer actually starts: on the
// new-chat screen its top edge is pinned just above the middle of the chat
// area (ChatView gives the greeting a fixed share), so half the *window*
// is more than the half-a-chat-area that is actually left below that edge —
// and the difference, plus the composer's own controls and hint rows, hung
// off the bottom of the window, taking the Send button with it.
function maxTextareaHeight(el: HTMLTextAreaElement): number {
  const area = inputAreaEl.value
  const chat = area?.closest('.chat-main') as HTMLElement | null
  // Never take more than half the space: there should always be conversation
  // (or the greeting) left to see above the composer.
  const half = Math.round((chat?.clientHeight || window.innerHeight) * 0.5)
  if (!area || !chat) return half

  // Everything in the composer that isn't the textarea — attachments, the
  // controls row, the hint line, padding. Measured, because it varies with
  // what's attached or queued.
  const chrome = Math.max(0, area.offsetHeight - el.offsetHeight)

  let room: number
  if (getComputedStyle(area).position === 'absolute') {
    // Floating over a conversation: the composer is anchored to the bottom
    // and its top edge rises as it grows, so the whole chat area is the room.
    room = chat.clientHeight - chrome
  } else {
    // New-chat screen: the top edge is pinned and does not move, so the room
    // is only what lies below it. The starter prompts underneath give way
    // (they scroll) rather than push the composer off the line it sits on.
    const gap = chat.getBoundingClientRect().bottom - area.getBoundingClientRect().top
    room = gap - chrome
  }

  // A floor keeps a usable box on very short windows; the textarea's own
  // `overflow-y: auto` takes over from there.
  return Math.max(MIN_TEXTAREA_HEIGHT, Math.min(half, room - COMPOSER_BOTTOM_GAP))
}

function autoResize() {
  const el = textareaEl.value
  if (!el) return
  el.style.height = 'auto'
  // Grow with the content, but never past the room the composer actually has;
  // beyond that the textarea scrolls internally.
  el.style.height = Math.min(el.scrollHeight, maxTextareaHeight(el)) + 'px'
}

function onInput() {
  autoResize()
  if (prompt.value.startsWith('/')) {
    slashFilter.value = prompt.value
    showSlash.value = true
    slashActiveIndex.value = 0
  } else {
    showSlash.value = false
  }
}

function onKeydown(e: KeyboardEvent) {
  // Cmd/Ctrl+K opens the Toolbox. Handled before the slash popover so it works
  // mid-command too. The modal focuses its own search field on mount, so there
  // is nothing to do here beyond opening it.
  if ((e.metaKey || e.ctrlKey) && !e.altKey && (e.key === 'k' || e.key === 'K')) {
    e.preventDefault()
    if (!isDeprecated.value) openToolbox()
    return
  }

  // Read-only while dictating: Enter must not send the words half-written.
  if (dictActive.value) {
    if (e.key === 'Enter') e.preventDefault()
    return
  }

  if (showSlash.value) {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      const len = filteredSlashCommands.value.length
      if (len > 0) {
        slashActiveIndex.value = (slashActiveIndex.value + 1) % len
      }
      return
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      const len = filteredSlashCommands.value.length
      if (len > 0) {
        slashActiveIndex.value = (slashActiveIndex.value - 1 + len) % len
      }
      return
    }
    if (e.key === 'Enter' && !e.shiftKey) {
      const cmds = filteredSlashCommands.value
      if (cmds.length > 0) {
        e.preventDefault()
        selectSlashCommand(cmds[slashActiveIndex.value].cmd)
        return
      }
      // Nothing left to pick — typically because an argument has been typed
      // after the command, as in `/skill road`. The popover must get out of
      // the way rather than swallow the send.
      showSlash.value = false
      // Deliberately no `return`: fall through to the normal Enter handling.
    } else if (e.key === 'Escape') {
      showSlash.value = false
      return
    } else {
      return
    }
  }

  // Ctrl+Enter or Cmd+Enter: queue message while streaming. So does a plain
  // Enter while the turn running is someone else's: typing a reply
  // must not stop a guest's turn; the Stop button still does.
  if (e.key === 'Enter' && isStreaming.value && (e.ctrlKey || e.metaKey || (!e.shiftKey && chatStore.streamingFollowed && prompt.value.trim()))) {
    e.preventDefault()
    queueCurrentMessage()
    return
  }
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    send()
  }
}

function getPromptText(): string {
  let finalPrompt = prompt.value.trim()
  const uploadedFiles = uploads.attachedFiles.filter(f => f.path)
  if (uploadedFiles.length > 0) {
    const refs = uploadedFiles.map(f => `[Attached: ${f.path}]`).join('\n')
    finalPrompt = finalPrompt ? `${finalPrompt}\n\n${refs}` : refs
  }
  return finalPrompt
}

async function send() {
  const text = prompt.value.trim()

  if (isStreaming.value) {
    chatStore.stopStreaming()
    return
  }

  // Read-only deprecated (legacy Copilot) session: never send.
  if (isDeprecated.value) return

  // Nothing is sent while dictating.
  if (dictActive.value) return

  if (!text && uploads.attachedFiles.length === 0) return

  // Handle slash commands
  if (text.startsWith('/') && uploads.attachedFiles.length === 0) {
    const handled = await handleSlash(text)
    if (handled) return
    // If not fully handled, the text may have been updated
    const updatedText = prompt.value.trim()
    if (!updatedText || updatedText.startsWith('/')) return
  }

  // Build final prompt with file references
  const finalPrompt = getPromptText()
  const skill = takeArmedSkill()
  const sid = sessionsStore.currentSessionId
  if (sid) sessionsStore.clearDraft(sid)
  clearOptimizeState()
  dict.clearProblem()
  uploads.clear()
  autoResize()
  await chatStore.sendMessage(finalPrompt, undefined, skill)
}

function queueCurrentMessage() {
  if (dictActive.value) return
  const finalPrompt = getPromptText()
  if (!finalPrompt) return
  // The armed skill belongs to the message it was armed for, so it travels
  // with the queued item rather than staying behind for whatever is typed next.
  chatStore.queueMessage(finalPrompt, undefined, takeArmedSkill())
  const sid = sessionsStore.currentSessionId
  if (sid) sessionsStore.clearDraft(sid)
  clearOptimizeState()
  dict.clearProblem()
  uploads.clear()
  autoResize()
}

async function handleSlash(text: string): Promise<boolean> {
  const parts = text.split(' ')
  const cmd = parts[0].toLowerCase()
  const arg = parts.slice(1).join(' ').trim()

  // Slash commands the web UI handles locally:
  switch (cmd) {
    case '/new':
    case '/clear':
      prompt.value = ''
      sessionsStore.createSession({ name: 'New Chat' })
      return true
    case '/mcp':
      prompt.value = ''
      ui.openModal('mcp-servers')
      return true
    case '/model':
      prompt.value = ''
      ui.openModal('model-picker')
      return true
    case '/agent':
      prompt.value = ''
      ui.openModal('settings')
      return true
    // Opens the Toolbox scoped to skills, with anything typed after the command
    // as the search. The trigger text is removed either way: the armed skill is
    // shown by the composer pill, so it must not also survive as prose in the
    // prompt.
    case '/skill':
      prompt.value = ''
      openToolbox({ query: arg, type: 'skill' })
      return true
    case '/undo':
    case '/rewind':
      prompt.value = ''
      if (sessionsStore.currentSessionId) {
        sessionsStore.undoLast(sessionsStore.currentSessionId)
      }
      return true
    case '/export':
      prompt.value = ''
      exportSession()
      return true
    case '/copy':
      prompt.value = ''
      copyLastResponse()
      return true
    case '/help':
      prompt.value = ''
      ;(window as any).showToast?.(
        'Type / to see commands. CLI-shell commands like /compact, /usage, /login are not available in the web UI.',
      )
      return true

    // Prompt templates — leave them in the input box for the user to send.
    case '/diff':
      prompt.value = 'Show me a summary of the changes in the current working directory using git diff and git status'
      return false
    case '/review':
      prompt.value = 'Run a thorough code review of the current changes in the working directory. Check for bugs, security issues, style problems, and improvements.'
      return false
    case '/plan':
      prompt.value = 'Create a detailed implementation plan for ' + arg
      return false
    case '/research':
      prompt.value = 'Research deeply: ' + arg
      return false
    case '/pr':
      prompt.value = 'Review the pull requests for the current branch. Show open PRs, their status, CI check results, and any review comments.'
      return false
    case '/init':
      prompt.value = 'Initialize agent instructions for this repository. Create or update AGENTS.md with appropriate context about this project\'s structure, coding standards, and key patterns.'
      return false
    case '/ask':
      if (arg) {
        prompt.value = `Answer this as a quick side question — do not create or modify any files, just respond concisely: ${arg}`
        return false
      }
      return false

    default:
      // Anything else starting with `/` is a CLI-shell-only command (e.g.
      // /compact, /usage, /login). Sending it to the agent would just be
      // chatted about — warn the user instead.
      prompt.value = ''
      ;(window as any).showToast?.(
        `${cmd} is only available in the CLI shell — not in this web UI.`,
      )
      return true
  }
}

async function exportSession() {
  if (!sessionsStore.currentSessionId) return
  const data = await apiExportSession(sessionsStore.currentSessionId!)
  const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `session-${sessionsStore.currentSessionId}.json`
  a.click()
  URL.revokeObjectURL(url)
}

function copyLastResponse() {
  const msgs = sessionsStore.currentMessages
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (msgs[i].role === 'assistant') {
      navigator.clipboard.writeText(msgs[i].content)
      ;(window as any).showToast?.('Copied to clipboard')
      break
    }
  }
}

function selectSlashCommand(cmd: string) {
  prompt.value = cmd + ' '
  showSlash.value = false
  textareaEl.value?.focus()
}

// ── File Upload ──

function triggerFileInput() {
  fileInputEl.value?.click()
}

function onFileInputChange(e: Event) {
  const input = e.target as HTMLInputElement
  if (input.files && input.files.length > 0) {
    uploads.handleFiles(Array.from(input.files))
    input.value = ''
  }
}

function onPaste(e: ClipboardEvent) {
  const items = e.clipboardData?.items
  if (!items) return
  const files: File[] = []
  for (let i = 0; i < items.length; i++) {
    if (items[i].kind === 'file') {
      const f = items[i].getAsFile()
      if (f) files.push(f)
    }
  }
  if (files.length > 0) {
    e.preventDefault()
    uploads.handleFiles(files)
  }
}
</script>

<template>
  <div id="input-area" ref="inputAreaEl">
    <SlashCommands
      v-if="showSlash"
      :filter="slashFilter"
      :active-index="slashActiveIndex"
      @select="selectSlashCommand"
      @close="showSlash = false"
    />
    <!-- Projects: project-context banner — makes the inherited shared workspace
         explicit before the user sends. -->
    <div v-if="inProject && currentProject" class="project-banner" @click="ui.openProject(currentProject.id)">
      <span class="pb-ico" aria-hidden="true">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" width="14" height="14">
          <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
      </span>
      <span class="pb-text">
        In project <strong>{{ currentProject.name }}</strong> — using its shared workspace and context
      </span>
    </div>
    <!-- File previews -->
    <div v-if="uploads.attachedFiles.length > 0" class="file-previews">
      <div v-for="(f, idx) in uploads.attachedFiles" :key="idx" class="file-chip">
        <img v-if="f.previewUrl" :src="f.previewUrl" class="file-thumb" />
        <span v-else class="file-icon">📄</span>
        <span class="file-name">{{ f.name }}</span>
        <span v-if="f.uploading" class="file-uploading">⏳</span>
        <button class="file-remove" @click="uploads.removeFile(idx)" title="Remove">×</button>
      </div>
    </div>
    <div id="input-wrap" :class="{ 'is-streaming': isStreaming, 'is-disabled': isDeprecated }">
      <input
        ref="fileInputEl"
        type="file"
        multiple
        style="display:none"
        @change="onFileInputChange"
      />
      <textarea
        ref="textareaEl"
        id="prompt-input"
        v-model="prompt"
        @input="onInput"
        @keydown="onKeydown"
        @paste="onPaste"
        :placeholder="isDeprecated ? 'This chat is deprecated — create a new session to continue' : dictActive ? 'Listening…' : (isStreaming ? 'Type a follow-up…' : 'Ask anything…')"
        :disabled="isDeprecated"
        :readonly="dictActive"
        rows="1"
      ></textarea>

      <!-- Control row: + menu · agent selector … send -->
      <div class="composer-controls">
        <!-- "+" menu -->
        <div class="composer-menu-wrap">
          <button
            class="ctrl-btn plus-btn"
            :class="{ active: showPlusMenu }"
            @click="togglePlusMenu"
            title="Add context"
            aria-label="Add context"
            :disabled="isDeprecated"
          >
            <svg viewBox="0 0 16 16" fill="currentColor" width="15" height="15">
              <path d="M8 1.5a.5.5 0 0 1 .5.5v5.5H14a.5.5 0 0 1 0 1H8.5V14a.5.5 0 0 1-1 0V8.5H2a.5.5 0 0 1 0-1h5.5V2a.5.5 0 0 1 .5-.5z"/>
            </svg>
          </button>
          <div v-if="showPlusMenu" class="composer-popover plus-popover" :class="popoverClass" :style="popoverStyle" role="menu">
            <button class="cp-item" @click="plusAddFiles">
              <span class="cp-ico" aria-hidden="true">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="16" height="16"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" stroke-linecap="round" stroke-linejoin="round"/><path d="M14 2v6h6M12 11v6M9 14h6" stroke-linecap="round" stroke-linejoin="round"/></svg>
              </span>
              Add files
            </button>
            <button v-if="!inProject" class="cp-item" @click="plusGithub">
              <span class="cp-ico" aria-hidden="true">
                <svg viewBox="0 0 24 24" fill="currentColor" width="16" height="16"><path d="M12 2C6.48 2 2 6.58 2 12.25c0 4.53 2.87 8.37 6.84 9.73.5.1.68-.22.68-.49 0-.24-.01-.87-.01-1.71-2.78.62-3.37-1.37-3.37-1.37-.45-1.18-1.11-1.49-1.11-1.49-.91-.64.07-.63.07-.63 1 .07 1.53 1.06 1.53 1.06.89 1.56 2.34 1.11 2.91.85.09-.66.35-1.11.63-1.37-2.22-.26-4.55-1.14-4.55-5.07 0-1.12.39-2.03 1.03-2.75-.1-.26-.45-1.3.1-2.71 0 0 .84-.28 2.75 1.05a9.3 9.3 0 0 1 5 0c1.91-1.33 2.75-1.05 2.75-1.05.55 1.41.2 2.45.1 2.71.64.72 1.03 1.63 1.03 2.75 0 3.94-2.34 4.81-4.57 5.06.36.32.68.94.68 1.9 0 1.37-.01 2.48-.01 2.82 0 .27.18.6.69.49A10.02 10.02 0 0 0 22 12.25C22 6.58 17.52 2 12 2z"/></svg>
              </span>
              Connect GitHub repository
            </button>
            <button v-if="!inProject" class="cp-item" @click="plusExistingFolder">
              <span class="cp-ico" aria-hidden="true">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="16" height="16"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" stroke-linecap="round" stroke-linejoin="round"/><path d="M9 13h6M12 10v6" stroke-linecap="round"/></svg>
              </span>
              Use existing folder
            </button>
            <button class="cp-item" @click="plusMcp">
              <span class="cp-ico" aria-hidden="true">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="16" height="16"><path d="M9 2v6M15 2v6M7 8h10v3a5 5 0 0 1-10 0zM12 16v6" stroke-linecap="round" stroke-linejoin="round"/></svg>
              </span>
              Connectors / MCP Servers
            </button>
            <div class="cp-divider" role="separator"></div>
            <button class="cp-item" @click="plusAdvanced">
              <span class="cp-ico" aria-hidden="true">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="16" height="16"><path d="M4 6h10M4 12h7M4 18h13" stroke-linecap="round"/><circle cx="18" cy="6" r="2"/><circle cx="15" cy="12" r="2"/><circle cx="19" cy="18" r="2"/></svg>
              </span>
              Advanced options
            </button>
          </div>
        </div>

        <!-- Agent selector -->
        <div class="composer-menu-wrap agent-wrap">
          <button
            class="agent-select"
            :class="{ active: showAgentMenu }"
            @click="toggleAgentMenu"
            :title="selectedTitle"
            :disabled="isDeprecated"
          >
            <svg class="agent-spark" viewBox="0 0 24 24" fill="currentColor" width="15" height="15" aria-hidden="true"><path d="M12 2l1.6 4.7L18 8.3l-4.4 1.6L12 14.6l-1.6-4.7L6 8.3l4.4-1.6L12 2zM19 13l.9 2.6L22 16.5l-2.1.9L19 20l-.9-2.6L16 16.5l2.1-.9L19 13z"/></svg>
            <span class="agent-label">{{ selectedLabel }}</span>
            <svg class="agent-caret" viewBox="0 0 16 16" fill="currentColor" width="12" height="12" aria-hidden="true"><path d="M4.22 6.22a.75.75 0 0 1 1.06 0L8 8.94l2.72-2.72a.75.75 0 1 1 1.06 1.06l-3.25 3.25a.75.75 0 0 1-1.06 0L4.22 7.28a.75.75 0 0 1 0-1.06z"/></svg>
          </button>
          <div v-if="showAgentMenu" class="composer-popover agent-popover" :class="popoverClass" :style="popoverStyle" role="menu">
            <button class="ag-item" @click="pickBuiltin('default')">
              <span class="ag-ico spark" aria-hidden="true">
                <svg viewBox="0 0 24 24" fill="currentColor" width="17" height="17"><path d="M12 2l1.6 4.7L18 8.3l-4.4 1.6L12 14.6l-1.6-4.7L6 8.3l4.4-1.6L12 2zM19 13l.9 2.6L22 16.5l-2.1.9L19 20l-.9-2.6L16 16.5l2.1-.9L19 13z"/></svg>
              </span>
              <span class="ag-text">
                <span class="ag-name">Default</span>
                <span class="ag-desc">Fast, cost-efficient all-rounder. Recommended for everyday work.</span>
              </span>
              <span v-if="!currentAgent && isDefaultPreset && !isThinkingPreset" class="ag-check" aria-hidden="true">✓</span>
            </button>
            <button class="ag-item" @click="pickBuiltin('thinking')">
              <span class="ag-ico brain" aria-hidden="true">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" width="17" height="17"><path d="M9 4a2.5 2.5 0 0 0-2.5 2.5A2.5 2.5 0 0 0 5 11a2.5 2.5 0 0 0 1 4.5V18a2 2 0 0 0 2 2h1V4zM15 4a2.5 2.5 0 0 1 2.5 2.5A2.5 2.5 0 0 1 19 11a2.5 2.5 0 0 1-1 4.5V18a2 2 0 0 1-2 2h-1V4z" stroke-linecap="round" stroke-linejoin="round"/></svg>
              </span>
              <span class="ag-text">
                <span class="ag-name">Thinking</span>
                <span class="ag-desc">Higher-capability model for advanced, complex reasoning.</span>
              </span>
              <span v-if="!currentAgent && isThinkingPreset" class="ag-check" aria-hidden="true">✓</span>
            </button>

            <!-- Grouped by where the agent came from. A single "Custom agents"
                 list mixed the centrally assigned set with the user's own, so
                 a workspace assigned a handful of agents could show a long
                 list and no way to tell which was which. The headings only
                 appear for groups that have something in them, so an
                 unmanaged workspace still sees one plain list. -->
            <template v-if="sessionsStore.assignedAgents.length">
              <div class="ag-section">Provided by IT</div>
              <button
                v-for="a in sessionsStore.assignedAgents"
                :key="a.id"
                class="ag-item compact"
                @click="pickAgent(a.id)"
                :title="a.description || a.name"
              >
                <span class="ag-ico bot" aria-hidden="true">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" width="16" height="16"><rect x="4" y="7" width="16" height="12" rx="2"/><path d="M12 3v4M9 13h.01M15 13h.01" stroke-linecap="round"/></svg>
                </span>
                <span class="ag-name">{{ a.name }}</span>
                <span v-if="currentAgent === a.id" class="ag-check" aria-hidden="true">✓</span>
              </button>
            </template>

            <template v-if="sessionsStore.personalAgents.length">
              <div class="ag-section">My agents</div>
              <button
                v-for="a in sessionsStore.personalAgents"
                :key="a.id"
                class="ag-item compact"
                @click="pickAgent(a.id)"
                :title="a.description || a.name"
              >
                <span class="ag-ico bot" aria-hidden="true">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" width="16" height="16"><rect x="4" y="7" width="16" height="12" rx="2"/><path d="M12 3v4M9 13h.01M15 13h.01" stroke-linecap="round"/></svg>
                </span>
                <span class="ag-name">{{ a.name }}</span>
                <span v-if="currentAgent === a.id" class="ag-check" aria-hidden="true">✓</span>
              </button>
            </template>

            <div class="ag-divider" role="separator"></div>
            <button class="ag-item ag-build" @click="buildNewAgent">
              <span class="ag-ico plus" aria-hidden="true">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" width="16" height="16"><path d="M12 5v14M5 12h14" stroke-linecap="round"/></svg>
              </span>
              <span class="ag-name">Build a new agent</span>
            </button>
          </div>
        </div>

        <!-- Toolbox pill.
             -
             One control replacing the plug button, the skills picker and the
             armed-skill chip. The chip and the pill would otherwise sit inches
             apart showing the same state.

             Structure is dictated by the chip's three affordances having to
             survive without nesting a button inside a button: clear, re-open,
             and explain the lifetime. When nothing is armed this is a single
             button and a single tab stop. When a skill is armed the label
             splits off so the wrench group can sit between the label and the
             connector count, and its two controls are real siblings. -->
        <div class="toolbox-pill" :class="{ armed: !!armedSkill, off: isDeprecated }">
          <button
            v-if="armedSkill"
            type="button"
            class="tbp-seg tbp-label-btn"
            @click="openToolbox()"
            :disabled="isDeprecated"
          >Toolbox</button>

          <span v-if="armedSkill" class="tbp-seg tbp-skill" :title="armedSkillTitle">
            <!-- Clicking the name lands where it used to: the skills half. -->
            <button
              type="button"
              class="tbp-skill-name"
              @click="openToolbox({ type: 'skill' })"
              :disabled="isDeprecated"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="13" height="13" aria-hidden="true"><path d="M14.7 6.3a4 4 0 0 0-5.4 5.4l-6 6a1.5 1.5 0 0 0 2.1 2.1l6-6a4 4 0 0 0 5.4-5.4l-2.5 2.5-2.1-2.1z" stroke-linecap="round" stroke-linejoin="round"/></svg>
              <span class="tbp-name-text">{{ armedSkill.name }}</span>
            </button>
            <button
              type="button"
              class="tbp-clear"
              @click="clearArmedSkill"
              :aria-label="`Clear the ${armedSkill.name} skill`"
              :disabled="isDeprecated"
            >
              <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.9" width="11" height="11" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" stroke-linecap="round"/></svg>
            </button>
          </span>

          <button
            type="button"
            class="tbp-seg tbp-tail"
            @click="openToolbox()"
            :disabled="isDeprecated"
            :aria-label="armedSkill ? 'Open the Toolbox' : undefined"
          >
            <span v-if="!armedSkill" class="tbp-label">Toolbox</span>
            <!-- Hidden on zero rather than showing a 0. -->
            <span v-if="connectorsOn" class="tbp-conn" :title="`${connectorsOn} connector${connectorsOn === 1 ? '' : 's'} on for this chat`">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="13" height="13" aria-hidden="true"><path d="M9 2v6M15 2v6M7 8h10v3a5 5 0 0 1-10 0zM12 16v6" stroke-linecap="round" stroke-linejoin="round"/></svg>
              {{ connectorsOn }}
            </span>
            <svg class="tbp-caret" viewBox="0 0 16 16" fill="currentColor" width="12" height="12" aria-hidden="true"><path d="M4.22 6.22a.75.75 0 0 1 1.06 0L8 8.94l2.72-2.72a.75.75 0 1 1 1.06 1.06l-3.25 3.25a.75.75 0 0 1-1.06 0L4.22 7.28a.75.75 0 0 1 0-1.06z"/></svg>
          </button>
        </div>

        <div class="ctrl-spacer"></div>

        <!-- Dictation: speech into the draft, never sent. Absent where
             the workspace or the browser can't do it. -->
        <button
          v-if="showDictate"
          type="button"
          class="ctrl-btn dictate-btn"
          :class="{ recording: dictActive && dictState !== 'finishing', finishing: dictState === 'finishing' }"
          @click="toggleDictation"
          :disabled="isDeprecated || (!dictActive && optimizing)"
          :aria-disabled="dictState === 'finishing' ? 'true' : undefined"
          :title="dictLabel"
          :aria-label="dictLabel"
        >
          <svg v-if="dictState === 'finishing'" class="opt-spin" viewBox="0 0 16 16" width="15" height="15" aria-hidden="true"><circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-dasharray="28" stroke-dashoffset="10"/></svg>
          <template v-else-if="dictActive">
            <span class="dict-dot" aria-hidden="true"></span>
            <span class="dict-clock" aria-hidden="true">{{ dictClock }}</span>
            <svg viewBox="0 0 16 16" fill="currentColor" width="11" height="11" aria-hidden="true"><rect x="2" y="2" width="12" height="12" rx="2"/></svg>
          </template>
          <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="16" height="16" aria-hidden="true"><rect x="9" y="3" width="6" height="11" rx="3"/><path d="M5.5 11a6.5 6.5 0 0 0 13 0M12 17.5V21" stroke-linecap="round"/></svg>
        </button>

        <!-- Optimize my prompt: a secondary, low-emphasis control that
             rewrites the draft in place before sending. Never sends. -->
        <button
          v-if="!isStreaming && canUndoOptimize && !dictActive"
          type="button"
          class="optimize-undo"
          @click="undoOptimize"
          title="Restore what you typed"
          aria-label="Undo optimize — restore your original prompt"
        >
          <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" width="12" height="12" aria-hidden="true"><path d="M4 8H10.5a3 3 0 0 1 0 6H6M4 8l2.5-2.5M4 8l2.5 2.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
          <span>Undo</span>
        </button>
        <button
          v-if="!isStreaming && !dictActive"
          type="button"
          class="ctrl-btn optimize-btn"
          :class="{ busy: optimizing }"
          @click="optimizePrompt"
          :disabled="!canOptimize"
          title="Rewrite your draft to be clearer and more specific before you send it"
          aria-label="Optimize my prompt"
        >
          <!-- Spinner while the rewrite runs -->
          <svg v-if="optimizing" class="opt-spin" viewBox="0 0 16 16" width="15" height="15" aria-hidden="true"><circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-dasharray="28" stroke-dashoffset="10"/></svg>
          <!-- Cobalt light-bulb icon (the control's single accent) -->
          <svg v-else class="optimize-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" width="16" height="16" aria-hidden="true"><path d="M9 18h6M10 21h4M12 3a6 6 0 0 0-4 10.5c.6.6 1 1.4 1 2.5h6c0-1.1.4-1.9 1-2.5A6 6 0 0 0 12 3z" stroke-linecap="round" stroke-linejoin="round"/></svg>
        </button>

        <!-- Queue button (Ctrl+Enter) when streaming and has text -->
        <button
          v-if="isStreaming && canSend && !dictActive"
          class="queue-btn"
          @click="queueCurrentMessage"
          title="Queue message (Ctrl+Enter) — sends after current finishes"
        >
          <svg viewBox="0 0 16 16" fill="currentColor" width="14" height="14">
            <path d="M2 3.5A1.5 1.5 0 013.5 2h9A1.5 1.5 0 0114 3.5v1A1.5 1.5 0 0112.5 6h-9A1.5 1.5 0 012 4.5v-1zm0 5A1.5 1.5 0 013.5 7h9A1.5 1.5 0 0114 8.5v1a1.5 1.5 0 01-1.5 1.5h-9A1.5 1.5 0 012 9.5v-1zm1.5 3.5A1.5 1.5 0 002 13.5v1A1.5 1.5 0 003.5 16h9a1.5 1.5 0 001.5-1.5v-1a1.5 1.5 0 00-1.5-1.5h-9z"/>
          </svg>
        </button>
        <!-- Send/Stop button -->
        <button
          id="send-btn"
          @click="send"
          :disabled="isDeprecated || (!isStreaming && (!canSend || dictActive))"
          :title="isStreaming ? 'Stop response' : 'Send (Enter)'"
          :class="{ 'abort-mode': isStreaming }"
        >
          <!-- X/stop icon while a response is running -->
          <svg v-if="isStreaming" viewBox="0 0 16 16" fill="currentColor" width="16" height="16">
            <path d="M3.72 3.72a.75.75 0 0 1 1.06 0L8 6.94l3.22-3.22a.75.75 0 1 1 1.06 1.06L9.06 8l3.22 3.22a.75.75 0 1 1-1.06 1.06L8 9.06l-3.22 3.22a.75.75 0 0 1-1.06-1.06L6.94 8 3.72 4.78a.75.75 0 0 1 0-1.06z"/>
          </svg>
          <!-- Normal send icon -->
          <svg v-else viewBox="0 0 16 16" fill="currentColor" width="16" height="16">
            <path d="M8.53 1.22a.75.75 0 0 0-1.06 0l-5 5a.75.75 0 1 0 1.06 1.06L7.25 3.56V14a.75.75 0 0 0 1.5 0V3.56l3.72 3.72a.75.75 0 1 0 1.06-1.06l-5-5z"/>
          </svg>
        </button>
      </div>
    </div>
    <!-- Queued messages -->
    <div v-if="chatStore.queuedMessages.length" class="queued-list">
      <div
        v-for="(qm, i) in chatStore.queuedMessages"
        :key="i"
        class="queued-indicator"
      >
        <span class="queued-icon">⏳</span>
        <span class="queued-pos">{{ i + 1 }}.</span>
        <span v-if="qm.author_name" class="queued-author">{{ qm.author_name }}:</span>
        <span class="queued-text">{{ qm.prompt.slice(0, 80) }}{{ qm.prompt.length > 80 ? '…' : '' }}</span>
        <button class="queued-cancel" @click="chatStore.removeQueuedMessage(qm.id)" title="Remove from queue">×</button>
      </div>
    </div>
    <p v-if="showDictate" class="sr-only" role="status" aria-live="polite">{{ dictAnnouncement }}</p>
    <div id="input-hint">
      <template v-if="dictProblem"><span class="dictate-error"><strong>{{ dictationMessages[dictProblem].title }}</strong> {{ dictationMessages[dictProblem].body }}</span></template>
      <template v-else-if="optimizeError"><span class="optimize-error">{{ optimizeError }}</span></template>
      <template v-else-if="dictState === 'finishing'">Finishing • Esc to discard</template>
      <template v-else-if="dictActive">Recording • Stop when you’re done • Esc to discard</template>
      <template v-else-if="isDeprecated">This session is read-only. Start a new chat to continue.</template>
      <template v-else-if="isStreaming && chatStore.streamingFollowed">Enter to queue • Shift+Enter for new line</template>
      <template v-else-if="isStreaming">Enter to stop • Ctrl+Enter to queue • Shift+Enter for new line</template>
      <template v-else>Type / for commands • Shift+Enter for new line • ↑ or drop files to attach</template>
    </div>
  </div>
</template>

<style scoped>
/* The composer draws no tray of its own: no rule above it, no band of colour.
   ChatView floats it over the conversation and supplies the fade that the
   messages pass under. The old border ran the width of the chat column only,
   so with the Files panel open it stopped halfway across the window. */
#input-area { padding: 12px 20px 18px; background: transparent; position: relative; }
.project-banner {
  max-width: 860px; margin: 0 auto 8px; display: flex; align-items: center; gap: 8px;
  padding: 7px 12px; border-radius: 10px; cursor: pointer;
  background: rgba(3,169,241,.08); border: 1px solid rgba(3,169,241,.25);
  color: var(--text2); font-size: 12.5px; transition: background .12s;
}
.project-banner:hover { background: rgba(3,169,241,.14); }
.project-banner .pb-ico { color: var(--accent); flex-shrink: 0; display: inline-flex; }
.project-banner .pb-text { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.project-banner strong { color: var(--text); font-weight: 600; }
#input-wrap {
  max-width: 860px; margin: 0 auto; background: var(--surface);
  border: 1px solid var(--border); border-radius: 16px;
  display: flex; flex-direction: column; gap: 6px; padding: 10px 12px 10px;
  transition: border-color .15s, box-shadow .15s, background .15s;
}
#input-wrap:focus-within { border-color: var(--accent); box-shadow: 0 0 0 3px rgba(3,169,241,.15); }
#input-wrap.is-disabled { opacity: .6; }
#input-wrap.is-disabled:focus-within { border-color: var(--border); box-shadow: none; }
#input-wrap.is-disabled #prompt-input { cursor: not-allowed; }
#prompt-input {
  background: none; border: none; outline: none; color: var(--text);
  font-size: 15px; line-height: 1.5; resize: none;
  min-height: 26px; padding: 4px 4px 0; overflow-y: auto; font-family: inherit;
  width: 100%; display: block;
  /* Height is driven by the JS auto-resize (grows with content up to ~50vh).
     Do NOT use flex:1 here — in the column-flex wrapper it would override the
     inline height and clip earlier lines instead of growing. */
  flex: 0 0 auto;
}
#prompt-input::placeholder { color: var(--text2); }

/* Control row under the textarea */
.composer-controls { display: flex; align-items: center; gap: 8px; }
.ctrl-spacer { flex: 1; }
.composer-menu-wrap { position: relative; display: inline-flex; }

.ctrl-btn {
  background: none; border: 1px solid var(--border); color: var(--text2);
  width: 34px; height: 34px; border-radius: 999px; cursor: pointer;
  display: flex; align-items: center; justify-content: center;
  transition: all .15s; flex-shrink: 0;
}
.ctrl-btn:hover, .ctrl-btn.active { color: var(--accent); border-color: var(--accent); background: var(--surface2); }

/* Optimize my prompt: a secondary control beside send. Neutral surface
   so it never competes with the primary send button; its one accent is the
   Cobalt light-bulb icon. */
.optimize-btn .optimize-ico { color: var(--accent); }
.optimize-btn:hover:not(:disabled) { color: var(--accent); border-color: var(--accent); background: var(--surface2); }
.optimize-btn.busy { color: var(--accent); border-color: var(--accent); cursor: default; }
.opt-spin { animation: opt-spin .8s linear infinite; }
@keyframes opt-spin { to { transform: rotate(360deg); } }

/* Undo affordance shown after a rewrite. A small, low-emphasis inline chip. */
.optimize-undo {
  display: inline-flex; align-items: center; gap: 4px; flex-shrink: 0;
  height: 34px; padding: 0 10px; border-radius: 999px; cursor: pointer;
  background: none; border: 1px solid var(--border); color: var(--text2);
  font-size: 12px; transition: all .15s;
}
.optimize-undo:hover { color: var(--accent); border-color: var(--accent); background: var(--surface2); }
.optimize-error { color: var(--red); }

/* Dictation. Recording widens the button into a pill carrying a red
   dot and the elapsed time, so it is obvious the microphone is live. */
.dictate-btn.recording {
  width: auto; padding: 0 11px; gap: 6px;
  color: var(--text); border-color: var(--red);
  font-size: 12px; font-variant-numeric: tabular-nums;
}
.dictate-btn.recording:hover { color: var(--red); border-color: var(--red); }
.dictate-btn.finishing { color: var(--accent); border-color: var(--accent); cursor: default; }
.dictate-btn:disabled { opacity: .45; cursor: not-allowed; }
.dict-dot {
  width: 8px; height: 8px; border-radius: 50%; background: var(--red);
  animation: dict-pulse 1.4s ease-in-out infinite;
}
@keyframes dict-pulse { 50% { opacity: .35; } }
@media (prefers-reduced-motion: reduce) { .dict-dot { animation: none; } }
.dictate-error { color: var(--red); }
.dictate-error strong { font-weight: 600; }

/* Agent selector pill */
.agent-select {
  display: inline-flex; align-items: center; gap: 6px;
  height: 34px; padding: 0 10px; border-radius: 999px;
  background: none; border: 1px solid var(--border); color: var(--text);
  font-size: 13px; font-weight: 600; cursor: pointer; font-family: inherit;
  transition: all .15s; max-width: 220px;
}
.agent-select:hover, .agent-select.active { border-color: var(--accent); background: var(--surface2); }
.agent-spark { color: var(--accent); flex-shrink: 0; }
.agent-label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.agent-caret { color: var(--text2); flex-shrink: 0; }

/* Shared popover surface. The direction is a modifier because placement is
   measured at open time (see placePopover): these menus are anchored to a
   composer that sits mid-screen on the new-chat view and bottom-pinned in an
   active chat, so neither direction is right on its own. `max-height` is set
   inline from the space actually available, which is what makes a long list
   scroll instead of being clipped by an ancestor's `overflow: hidden`. */
.composer-popover {
  position: absolute; left: 0; z-index: 60;
  background: var(--surface); border: 1px solid var(--border);
  border-radius: 12px; box-shadow: 0 12px 40px rgba(0,0,0,.28);
  padding: 6px; min-width: 240px; animation: popIn .12s ease;
  overflow-y: auto;
  /* Reaching the end of the menu shouldn't start scrolling the chat behind it. */
  overscroll-behavior: contain;
}
.composer-popover.drop-up { bottom: calc(100% + 8px); }
.composer-popover.drop-down { top: calc(100% + 8px); }
@keyframes popIn { from { opacity: 0; transform: translateY(4px); } to { opacity: 1; transform: translateY(0); } }

/* "+" menu items */
.cp-item {
  display: flex; align-items: center; gap: 10px; width: 100%;
  padding: 9px 10px; border: none; background: none; cursor: pointer;
  border-radius: 8px; font-size: 14px; color: var(--text); text-align: left;
  font-family: inherit; transition: background .1s;
}
.cp-item:hover { background: var(--surface2); }
.cp-ico { display: inline-flex; color: var(--text2); flex-shrink: 0; }
.cp-item:hover .cp-ico { color: var(--text); }
.cp-divider { height: 1px; background: var(--border); margin: 6px 4px; }

/* Agent menu items */
.agent-popover { min-width: 300px; }
.ag-item {
  display: flex; align-items: flex-start; gap: 10px; width: 100%;
  padding: 9px 10px; border: none; background: none; cursor: pointer;
  border-radius: 8px; text-align: left; font-family: inherit; transition: background .1s;
}
.ag-item.compact { align-items: center; }
.ag-item:hover { background: var(--surface2); }
.ag-ico { display: inline-flex; flex-shrink: 0; margin-top: 1px; }
.ag-ico.spark { color: var(--accent); }
.ag-ico.brain { color: var(--accent); }
.ag-ico.bot { color: var(--text2); }
.ag-ico.plus { color: var(--accent); }
.ag-divider { height: 1px; background: var(--border); margin: 4px 0; }
.ag-item.ag-build { align-items: center; }
.ag-item.ag-build .ag-name { color: var(--accent); }
.ag-item.ag-build:hover { background: var(--surface2); }

/* Toolbox pill. Carries the composer's accent when it has
   something to report — an armed skill or connectors on — and sits quiet
   otherwise, so a glance at the control row answers "what is loaded?".

   Segments are separate elements because the clear button cannot nest inside
   the open button, so the pill's border lives on the container and the
   segments are transparent. Hover and focus are drawn per segment, which is
   also what makes the three targets legible as three targets. */
.toolbox-pill {
  display: inline-flex; align-items: center; flex-shrink: 1; min-width: 0;
  height: 34px; border-radius: 17px;
  border: 1px solid var(--border); background: var(--surface);
  color: var(--text2);
  font-size: 12px; font-weight: 600;
  max-width: 320px;
}
.toolbox-pill:hover { border-color: var(--accent); }
.toolbox-pill.armed { border-color: var(--accent); color: var(--accent); }
.toolbox-pill.off { opacity: .55; }

.tbp-seg {
  display: inline-flex; align-items: center; gap: 5px;
  height: 100%; min-width: 0;
  background: none; border: none; padding: 0 10px;
  font: inherit; color: inherit; cursor: pointer;
}
.tbp-seg:disabled { cursor: default; }
button.tbp-seg:hover:not(:disabled) { color: var(--accent); }
.tbp-seg:first-child { padding-left: 12px; border-radius: 17px 0 0 17px; }
.tbp-seg:last-child { padding-right: 10px; border-radius: 0 17px 17px 0; }
.toolbox-pill > .tbp-seg:only-child { border-radius: 17px; }
.tbp-seg:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }

/* The wrench group: the armed skill. Divided from the label and the tail so it
   reads as a distinct thing the pill is carrying rather than more of its own
   label, which is what the chip used to do with a border. */
.tbp-skill {
  gap: 3px; padding: 0 2px 0 8px;
  border-left: 1px solid var(--border); border-right: 1px solid var(--border);
  color: var(--accent);
}
.tbp-skill-name,
.tbp-clear {
  display: inline-flex; align-items: center; gap: 5px; min-width: 0;
  background: none; border: none; padding: 0 4px; height: 24px;
  border-radius: 12px;
  font: inherit; color: inherit; cursor: pointer;
}
.tbp-skill-name:hover:not(:disabled),
.tbp-clear:hover:not(:disabled) { background: var(--surface2); }
.tbp-skill-name:focus-visible,
.tbp-clear:focus-visible { outline: 2px solid var(--accent); outline-offset: -1px; }
.tbp-skill-name:disabled, .tbp-clear:disabled { cursor: default; }
/* A long skill name would otherwise push the send button around, because the
   pill also carries a label, a count and a chevron. */
.tbp-name-text {
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 130px;
}
.tbp-clear { color: var(--text2); padding: 0 5px; }
.tbp-clear:hover:not(:disabled) { color: var(--text); }

.tbp-label { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
/* On a phone the control row can't hold every control at full width, and Send
   must stay on screen: the agent and Toolbox pills give way first, truncating
   their labels. At desktop widths nothing shrinks. */
.agent-wrap, .agent-select { min-width: 0; }
.tbp-tail { overflow: hidden; }
.tbp-conn { display: inline-flex; align-items: center; gap: 4px; }
.toolbox-pill:not(.armed) .tbp-conn { color: var(--accent); }
.tbp-caret { flex-shrink: 0; opacity: .7; }
.ag-text { display: flex; flex-direction: column; gap: 2px; min-width: 0; flex: 1; }
.ag-name { font-size: 14px; font-weight: 600; color: var(--text); }
.ag-desc { font-size: 12px; color: var(--text2); line-height: 1.4; }
.ag-check { color: var(--accent); font-weight: 700; margin-left: auto; align-self: center; }
.ag-section {
  padding: 10px 10px 4px; font-size: 11px; font-weight: 600; color: var(--text2);
  text-transform: none; letter-spacing: .02em;
  border-top: 1px solid var(--border); margin-top: 4px;
}

#send-btn {
  background: var(--accent); border: none; color: #fff; border-radius: 999px;
  width: 36px; height: 36px; cursor: pointer; display: flex; align-items: center;
  justify-content: center; transition: all .15s; flex-shrink: 0;
}
#send-btn:hover { background: var(--accent-h); }
#send-btn:disabled { opacity: .35; cursor: not-allowed; }
#send-btn.abort-mode { background: var(--red); }
#send-btn.abort-mode:hover { background: #c92f2f; }
#input-hint {
  max-width: 860px; margin: 9px auto 0; font-size: 11px; line-height: 1.35;
  color: var(--text2); text-align: center;
}

/* Streaming state */
#input-wrap.is-streaming { border-color: var(--accent); box-shadow: 0 0 0 2px rgba(3,169,241,.14); }

/* Queue button */
.queue-btn {
  background: none; border: 1px solid var(--border); color: var(--text2); border-radius: 999px;
  width: 36px; height: 36px; cursor: pointer; display: flex; align-items: center;
  justify-content: center; transition: all .15s; flex-shrink: 0;
}
.queue-btn:hover { color: var(--accent); border-color: var(--accent); background: var(--surface2); }

/* Queued message indicator */
.queued-list { max-width: 860px; margin: 6px auto 0; display: flex; flex-direction: column; gap: 4px; }
.queued-indicator {
  display: flex; align-items: center; gap: 8px;
  padding: 6px 10px; background: var(--surface2); border: 1px solid var(--border);
  border-radius: 8px; font-size: 12px; color: var(--text2); animation: fadeIn .15s ease;
}
.queued-pos { font-weight: 600; color: var(--text2); flex-shrink: 0; }
.queued-icon { font-size: 13px; }
.queued-text { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.queued-cancel {
  background: none; border: none; color: var(--text3); cursor: pointer;
  font-size: 16px; line-height: 1; padding: 2px 4px; border-radius: 4px;
}
.queued-cancel:hover { color: var(--red); background: rgba(218,63,63,.1); }

@keyframes fadeIn { from { opacity: 0; } to { opacity: 1; } }

/* File previews */
.file-previews {
  max-width: 860px; margin: 0 auto 8px; display: flex; flex-wrap: wrap; gap: 8px;
}
.file-chip {
  display: flex; align-items: center; gap: 6px; padding: 4px 8px;
  background: var(--surface); border: 1px solid var(--border); border-radius: 8px;
  font-size: 12px; color: var(--text); max-width: 200px;
}
.file-thumb {
  width: 32px; height: 32px; object-fit: cover; border-radius: 4px;
}
.file-icon { font-size: 16px; }
.file-name {
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1;
}
.file-uploading { font-size: 12px; }
.file-remove {
  background: none; border: none; color: var(--text2); cursor: pointer;
  font-size: 16px; line-height: 1; padding: 0 2px; border-radius: 3px;
}
.file-remove:hover { color: var(--danger, #e53e3e); background: var(--surface2); }

@media (max-width: 768px) {
  /* Leave padding-top alone — it carries the fade height set by ChatView. */
  #input-area {
    padding-left: 8px; padding-right: 8px;
    padding-bottom: calc(8px + env(safe-area-inset-bottom));
  }
  #input-wrap { padding: 8px 10px; gap: 4px; }
  #prompt-input { font-size: 16px; } /* iOS no-zoom */
  #send-btn, .queue-btn, .ctrl-btn { width: 34px; height: 34px; }
  #input-hint { display: none; }
  .file-previews { padding: 0 8px; }
  .file-chip { max-width: 160px; font-size: 11px; }
  .agent-select { max-width: 150px; }
  .composer-popover { min-width: 220px; }
  .agent-popover { min-width: 260px; }
}
.queued-author { font-weight: 600; color: var(--text2); flex-shrink: 0; }
</style>
