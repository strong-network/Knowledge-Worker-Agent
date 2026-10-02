<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount, nextTick, watch } from 'vue'
import { useSessionsStore } from '../../stores/sessions'
import { useSavedPromptsStore, MAX_SAVED_PROMPTS } from '../../stores/savedPrompts'
import { useProvidersStore } from '../../stores/providers'
import { updateSessionConfig, type SavedPrompt, type Session } from '../../api'
import { resolvePreset } from '../../utils/models'

const sessionsStore = useSessionsStore()
const savedPrompts = useSavedPromptsStore()
const providers = useProvidersStore()

// What Default/Thinking resolve to for this workspace: the providers'
// nominations when there are any, the Claude heuristic otherwise.
const defaultModel = computed(() =>
  resolvePreset('default', sessionsStore.models, providers.presets),
)
const thinkingModel = computed(() =>
  resolvePreset('thinking', sessionsStore.models, providers.presets),
)

// Saved prompts: the starter-chip row is a user-owned, persisted set.
// A first-time user sees the five built-in starter prompts (seeded server-side); once
// they add their own, those appear first. Clicking a chip pre-fills the
// composer (does not auto-send) and arms the chip's agent for that turn.
const chips = computed(() => savedPrompts.ordered)

// ── Click a chip: pre-fill + arm agent (fail soft on a missing agent) ───────
const clickError = ref('')

async function applyPreset(sid: string, s: Session | null, model: string | undefined) {
  try {
    await updateSessionConfig(sid, { agent: '', model: model || undefined })
    if (s) { s.agent = ''; if (model) s.model = model }
  } catch { /* non-fatal: the prompt text still fills the composer */ }
}

async function armAgent(p: SavedPrompt): Promise<void> {
  const sid = sessionsStore.currentSessionId
  if (!sid) return
  const s = sessionsStore.currentSession

  // A custom agent may have been deleted since the prompt was saved. Default
  // and Thinking are always valid. When a custom agent is unavailable, fail
  // soft: fall back to Default and surface a clear, non-blocking message.
  if (p.agentKind === 'custom') {
    const available = sessionsStore.agents.some(a => a.id === p.agentId)
    if (!available) {
      const missing = sessionsStore.agentName(p.agentId) || p.agentId
      clickError.value = `This saved prompt uses the agent "${missing}", which isn't available. Running with Default instead.`
      await applyPreset(sid, s, defaultModel.value)
      return
    }
    try {
      await updateSessionConfig(sid, { agent: p.agentId })
      if (s) s.agent = p.agentId
    } catch { /* leave the current agent; text still fills below */ }
    return
  }

  if (p.agentKind === 'thinking') {
    await applyPreset(sid, s, thinkingModel.value)
    return
  }
  await applyPreset(sid, s, defaultModel.value)
}

async function clickChip(p: SavedPrompt): Promise<void> {
  if (dragging.value) return // a drag just ended; don't treat as a click
  clickError.value = ''
  await armAgent(p)
  const sid = sessionsStore.currentSessionId
  if (sid) sessionsStore.setDraft(sid, p.prompt)
  const input = document.querySelector('#prompt-input') as HTMLTextAreaElement | null
  input?.focus()
}

// ── Add / edit form ─────────────────────────────────────────────────────────
type AgentKind = 'default' | 'thinking' | 'custom'
const showForm = ref(false)
const editingId = ref<string | null>(null)
const formName = ref('')
const formPrompt = ref('')
const formAgentKind = ref<AgentKind>('default')
const formAgentId = ref('')
const formError = ref('')
const saving = ref(false)
const showAgentMenu = ref(false)

const isEditing = computed(() => editingId.value !== null)

// This block sits below the composer, near the bottom of the window, so an
// opening form can extend past the fold — including its Save button. Bring the
// whole form into view, not just the focused field: focus() alone scrolls the
// input into view and leaves the actions hidden underneath.
function revealForm() {
  const form = document.querySelector('.sp-form') as HTMLElement | null
  form?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
  ;(document.querySelector('#sp-name-input') as HTMLInputElement | null)?.focus({ preventScroll: true })
}

// The friendly label for the currently-selected agent in the form.
const formAgentLabel = computed(() => {
  if (formAgentKind.value === 'thinking') return 'Thinking'
  if (formAgentKind.value === 'custom') {
    return sessionsStore.agentName(formAgentId.value) || 'Custom agent'
  }
  return 'Default'
})

function openAdd() {
  editingId.value = null
  formName.value = ''
  formPrompt.value = ''
  formAgentKind.value = 'default'
  formAgentId.value = ''
  formError.value = ''
  showForm.value = true
  nextTick(revealForm)
}

function openEdit(p: SavedPrompt) {
  openMenuFor.value = null
  editingId.value = p.id
  formName.value = p.name
  formPrompt.value = p.prompt
  formAgentKind.value = p.agentKind
  formAgentId.value = p.agentId
  formError.value = ''
  showForm.value = true
  nextTick(revealForm)
}

function cancelForm() {
  showForm.value = false
  showAgentMenu.value = false
}

function pickFormAgent(kind: AgentKind, id = '') {
  formAgentKind.value = kind
  formAgentId.value = kind === 'custom' ? id : ''
  showAgentMenu.value = false
}

async function saveForm() {
  const name = formName.value.trim()
  const prompt = formPrompt.value.trim()
  if (!name || !prompt) {
    formError.value = 'A name and a prompt are both required.'
    return
  }
  saving.value = true
  formError.value = ''
  try {
    const input = { name, prompt, agentKind: formAgentKind.value, agentId: formAgentId.value }
    if (editingId.value) {
      await savedPrompts.update(editingId.value, input)
    } else {
      await savedPrompts.add(input)
    }
    showForm.value = false
    nextTick(measureRows)
  } catch (e) {
    formError.value = (e as Error)?.message || 'Could not save the prompt.'
  } finally {
    saving.value = false
  }
}

// ── Per-chip overflow menu (edit / delete) ──────────────────────────────────
// The chip is a pill with `overflow: hidden` and it can sit inside the
// `overflow: hidden` collapsed row, so an in-flow dropdown gets clipped inside
// the chip. We teleport the menu to <body> and position it (fixed) at the
// kebab's on-screen rect so it floats free of both clipping ancestors.
const openMenuFor = ref<string | null>(null)
const menuAnchor = ref<DOMRect | null>(null)
const MENU_WIDTH = 140

// The chip whose menu is open (drives the single teleported menu).
const openMenuChip = computed(
  () => chips.value.find(c => c.id === openMenuFor.value) || null,
)

// Fixed-position style for the teleported menu, right-aligned to the kebab and
// clamped to stay on-screen.
const menuStyle = computed(() => {
  const r = menuAnchor.value
  if (!r) return {}
  const left = Math.min(Math.max(8, r.right - MENU_WIDTH), window.innerWidth - MENU_WIDTH - 8)
  return { top: `${r.bottom + 4}px`, left: `${left}px`, width: `${MENU_WIDTH}px` }
})

function toggleMenu(id: string, ev: MouseEvent) {
  if (openMenuFor.value === id) { openMenuFor.value = null; return }
  menuAnchor.value = (ev.currentTarget as HTMLElement).getBoundingClientRect()
  openMenuFor.value = id
}

function closeMenu() {
  openMenuFor.value = null
}

async function deleteChip(p: SavedPrompt) {
  openMenuFor.value = null
  try {
    await savedPrompts.remove(p.id)
    nextTick(measureRows)
  } catch {
    ;(window as any).showToast?.('Could not delete the prompt')
  }
}

// ── Drag-to-reorder (within the row) ────────────────────────────────────────
// A grip handle is the drag source so a plain click on the chip still fills the
// composer and never inserts by accident. Order is persisted per user.
const dragging = ref(false)
const dragFromId = ref<string | null>(null)
const dragOverId = ref<string | null>(null)

function onDragStart(p: SavedPrompt, e: DragEvent) {
  dragging.value = true
  dragFromId.value = p.id
  e.dataTransfer?.setData('text/plain', p.id)
  if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move'
}

function onDragOver(p: SavedPrompt, e: DragEvent) {
  e.preventDefault()
  dragOverId.value = p.id
  if (e.dataTransfer) e.dataTransfer.dropEffect = 'move'
}

async function onDrop(target: SavedPrompt) {
  const fromId = dragFromId.value
  dragOverId.value = null
  if (!fromId || fromId === target.id) { endDrag(); return }
  const ids = chips.value.map(c => c.id)
  const from = ids.indexOf(fromId)
  const to = ids.indexOf(target.id)
  if (from < 0 || to < 0) { endDrag(); return }
  ids.splice(to, 0, ids.splice(from, 1)[0])
  await savedPrompts.reorder(ids)
  nextTick(measureRows)
  endDrag()
}

function endDrag() {
  dragFromId.value = null
  dragOverId.value = null
  // Defer clearing so the click that follows a drop is still suppressed.
  setTimeout(() => (dragging.value = false), 0)
}

// ── Two-row wrap + "More" overflow ──────────────────────────────────────────
// The row wraps to at most two rows; anything beyond collapses behind a "More"
// toggle rather than pushing the composer down. Rows are measured from the
// laid-out chip positions so it adapts to any window width.
const ROW_GAP = 8
const rowRef = ref<HTMLElement | null>(null)
const needsOverflow = ref(false)
const expanded = ref(false)
const collapsedMaxHeight = ref<number | undefined>(undefined)
const collapsed = computed(() => needsOverflow.value && !expanded.value)

function measureRows() {
  const el = rowRef.value
  if (!el) return
  const kids = Array.from(el.children) as HTMLElement[]
  if (kids.length === 0) { needsOverflow.value = false; return }
  const tops = Array.from(new Set(kids.map(k => k.offsetTop))).sort((a, b) => a - b)
  if (tops.length <= 2) {
    needsOverflow.value = false
    collapsedMaxHeight.value = undefined
    return
  }
  needsOverflow.value = true
  // Clip right at the end of the second row: two rows minus the gap above row 3.
  collapsedMaxHeight.value = tops[2] - tops[0] - ROW_GAP
}

let ro: ResizeObserver | null = null
onMounted(() => {
  savedPrompts.ensureLoaded().then(() => nextTick(measureRows))
  nextTick(measureRows)
  if (typeof ResizeObserver !== 'undefined' && rowRef.value) {
    ro = new ResizeObserver(() => measureRows())
    ro.observe(rowRef.value)
  }
  window.addEventListener('resize', measureRows)
  window.addEventListener('resize', closeMenu)
  // Any scroll (in any ancestor) invalidates the fixed menu anchor — close it.
  window.addEventListener('scroll', closeMenu, true)
  document.addEventListener('pointerdown', onGlobalPointerDown)
})
onBeforeUnmount(() => {
  ro?.disconnect()
  window.removeEventListener('resize', measureRows)
  window.removeEventListener('resize', closeMenu)
  window.removeEventListener('scroll', closeMenu, true)
  document.removeEventListener('pointerdown', onGlobalPointerDown)
})
watch(chips, () => nextTick(measureRows), { deep: true })

function onGlobalPointerDown(e: MouseEvent) {
  const t = e.target as HTMLElement
  // The menu is teleported to <body>, so exclude both the trigger wrap and the
  // menu itself before dismissing.
  if (!t.closest('.sp-chip-menu-wrap') && !t.closest('.sp-menu')) openMenuFor.value = null
  if (!t.closest('.sp-agent-wrap')) showAgentMenu.value = false
}
</script>

<template>
  <div class="empty-state">
    <!-- Saved prompts (chips). User prompts first; wraps to two rows then More. -->
    <div class="sp-wrap">
      <div
        class="chips"
        ref="rowRef"
        :class="{ collapsed }"
        :style="collapsed && collapsedMaxHeight ? { maxHeight: collapsedMaxHeight + 'px' } : {}"
      >
        <div
          v-for="p in chips"
          :key="p.id"
          class="sp-chip"
          :class="{ 'drag-over': dragOverId === p.id, 'dragging': dragFromId === p.id }"
          @dragover="onDragOver(p, $event)"
          @drop="onDrop(p)"
        >
          <span
            class="sp-grip"
            title="Drag to reorder"
            aria-label="Drag to reorder"
            draggable="true"
            @dragstart="onDragStart(p, $event)"
            @dragend="endDrag"
          >
            <svg viewBox="0 0 16 16" width="10" height="10" fill="currentColor" aria-hidden="true"><circle cx="5" cy="3" r="1.3"/><circle cx="11" cy="3" r="1.3"/><circle cx="5" cy="8" r="1.3"/><circle cx="11" cy="8" r="1.3"/><circle cx="5" cy="13" r="1.3"/><circle cx="11" cy="13" r="1.3"/></svg>
          </span>
          <button class="sp-label" type="button" :title="p.prompt" @click="clickChip(p)">
            {{ p.name }}
          </button>
          <div class="sp-chip-menu-wrap">
            <button
              class="sp-kebab"
              type="button"
              title="Edit or delete"
              aria-label="Edit or delete this saved prompt"
              @click.stop="toggleMenu(p.id, $event)"
            >
              <svg viewBox="0 0 16 16" width="14" height="14" fill="currentColor" aria-hidden="true"><circle cx="8" cy="3" r="1.4"/><circle cx="8" cy="8" r="1.4"/><circle cx="8" cy="13" r="1.4"/></svg>
            </button>
          </div>
        </div>

        <!-- Add prompt (hidden once the 12-chip cap is reached). -->
        <button
          v-if="!savedPrompts.atCapacity"
          class="sp-add"
          type="button"
          title="Save a reusable prompt with its own agent"
          aria-label="Add a saved prompt"
          @click="openAdd"
        >
          <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><path d="M8 3v10M3 8h10" stroke-linecap="round"/></svg>
          <span>Add prompt</span>
        </button>
      </div>

      <div class="sp-rowtools">
        <button v-if="needsOverflow" class="sp-more" type="button" @click="expanded = !expanded">
          {{ expanded ? 'Less' : 'More' }}
        </button>
        <span v-if="savedPrompts.atCapacity" class="sp-cap-note">
          Maximum of {{ MAX_SAVED_PROMPTS }} saved prompts reached — delete one to add another.
        </span>
      </div>

      <p v-if="clickError" class="sp-click-error">{{ clickError }}</p>
    </div>

    <!-- Per-chip overflow menu, teleported to <body> so it escapes the chip's
         (and the collapsed row's) overflow:hidden clipping. -->
    <Teleport to="body">
      <div v-if="openMenuChip" class="sp-menu" role="menu" :style="menuStyle">
        <button type="button" role="menuitem" @click="openEdit(openMenuChip)">Edit</button>
        <button type="button" role="menuitem" class="danger" @click="deleteChip(openMenuChip)">Delete</button>
      </div>
    </Teleport>

    <!-- Add / edit form (inline, no modal). -->
    <div v-if="showForm" class="sp-form" role="dialog" aria-label="Saved prompt">
      <div class="sp-form-title">{{ isEditing ? 'Edit saved prompt' : 'New saved prompt' }}</div>
      <label class="sp-field">
        <span>Name</span>
        <input id="sp-name-input" v-model="formName" type="text" maxlength="60" placeholder="e.g. Account briefing" @keydown.enter.prevent="saveForm" />
      </label>
      <label class="sp-field">
        <span>Prompt</span>
        <textarea v-model="formPrompt" rows="3" placeholder="The text dropped into the composer when you click this chip"></textarea>
      </label>
      <div class="sp-field">
        <span>Agent</span>
        <div class="sp-agent-wrap">
          <button type="button" class="sp-agent-btn" @click="showAgentMenu = !showAgentMenu">
            {{ formAgentLabel }}
            <svg viewBox="0 0 16 16" width="12" height="12" fill="currentColor" aria-hidden="true"><path d="M4 6l4 4 4-4z"/></svg>
          </button>
          <div v-if="showAgentMenu" class="sp-agent-menu" role="menu">
            <button type="button" role="menuitem" @click="pickFormAgent('default')">Default</button>
            <button type="button" role="menuitem" @click="pickFormAgent('thinking')">Thinking</button>
            <template v-if="sessionsStore.assignedAgents.length">
              <div class="sp-agent-sec">Provided by IT</div>
              <button
                v-for="a in sessionsStore.assignedAgents"
                :key="a.id"
                type="button"
                role="menuitem"
                @click="pickFormAgent('custom', a.id)"
              >
                {{ a.name }}
              </button>
            </template>
            <template v-if="sessionsStore.personalAgents.length">
              <div class="sp-agent-sec">My agents</div>
              <button
                v-for="a in sessionsStore.personalAgents"
                :key="a.id"
                type="button"
                role="menuitem"
                @click="pickFormAgent('custom', a.id)"
              >
                {{ a.name }}
              </button>
            </template>
          </div>
        </div>
      </div>
      <p v-if="formError" class="sp-form-error">{{ formError }}</p>
      <div class="sp-form-actions">
        <button type="button" class="sp-btn-ghost" @click="cancelForm">Cancel</button>
        <button type="button" class="sp-btn-primary" :disabled="saving" @click="saveForm">
          {{ saving ? 'Saving…' : (isEditing ? 'Save' : 'Add prompt') }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* The starter-prompt row on a new chat. The greeting above the composer is
   NewChatGreeting; this block sits below it, so it owns no vertical
   centring of its own — ChatView places it. */
.empty-state {
  display: flex; flex-direction: column; align-items: center;
  gap: 14px; color: var(--text2); padding: 4px 40px 0;
}

.sp-wrap { width: 100%; max-width: 640px; display: flex; flex-direction: column; align-items: center; gap: 6px; }
.chips { display: flex; flex-wrap: wrap; gap: 8px; justify-content: center; width: 100%; }
.chips.collapsed { overflow: hidden; }

.sp-chip {
  display: inline-flex; align-items: center;
  background: var(--surface); border: 1px solid var(--border); border-radius: 20px;
  color: var(--text2); transition: background .15s, color .15s, border-color .15s; overflow: hidden;
}
.sp-chip:hover { background: var(--surface2); color: var(--text); border-color: var(--accent2); }
.sp-chip.drag-over { border-color: var(--accent); box-shadow: 0 0 0 2px rgba(3,169,241,.2); }
.sp-chip.dragging { opacity: .5; }

.sp-grip {
  display: none; align-items: center; padding: 0 2px 0 8px; cursor: grab; color: var(--text3);
}
.sp-grip:active { cursor: grabbing; }
.sp-chip:hover .sp-grip { display: inline-flex; }

.sp-label {
  background: none; border: none; color: inherit; cursor: pointer; font-family: inherit;
  font-size: 13px; padding: 8px 6px 8px 12px; max-width: 260px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.sp-chip:hover .sp-label { padding-left: 4px; }

.sp-chip-menu-wrap { position: relative; display: inline-flex; }
.sp-kebab {
  display: none; align-items: center; justify-content: center;
  background: none; border: none; color: var(--text3); cursor: pointer;
  padding: 0 8px 0 2px; height: 100%;
}
.sp-chip:hover .sp-kebab { display: inline-flex; }
.sp-kebab:hover { color: var(--accent); }

.sp-menu {
  position: fixed; z-index: 200;
  background: var(--surface); border: 1px solid var(--border); border-radius: 8px;
  box-shadow: 0 6px 20px rgba(0,0,0,.18); min-width: 120px; padding: 4px; display: flex; flex-direction: column;
}
.sp-menu button {
  background: none; border: none; text-align: left; padding: 7px 10px; border-radius: 6px;
  font-size: 13px; color: var(--text); cursor: pointer; font-family: inherit;
}
.sp-menu button:hover { background: var(--surface2); }
.sp-menu button.danger { color: var(--red); }
.sp-menu button.danger:hover { background: rgba(218,63,63,.1); }

.sp-add {
  display: inline-flex; align-items: center; gap: 6px;
  background: none; border: 1px dashed var(--border); border-radius: 20px;
  padding: 8px 14px; font-size: 13px; color: var(--text2); cursor: pointer;
  transition: all .15s; font-family: inherit;
}
.sp-add:hover { color: var(--accent); border-color: var(--accent); background: var(--surface2); }

.sp-rowtools { display: flex; align-items: center; gap: 10px; min-height: 4px; }
.sp-more {
  background: none; border: none; color: var(--accent); cursor: pointer;
  font-size: 12.5px; font-family: inherit; padding: 2px 6px; border-radius: 6px;
}
.sp-more:hover { background: var(--surface2); }
.sp-cap-note { font-size: 11.5px; color: var(--text3); }
.sp-click-error {
  font-size: 12.5px; color: var(--red); text-align: center; max-width: 480px; line-height: 1.5; margin: 2px 0 0;
}

/* Inline add/edit form */
.sp-form {
  width: 100%; max-width: 480px; margin-top: 6px;
  background: var(--surface); border: 1px solid var(--border); border-radius: 12px;
  padding: 16px; display: flex; flex-direction: column; gap: 10px; text-align: left;
}
.sp-form-title { font-size: 14px; font-weight: 600; color: var(--text); }
.sp-field { display: flex; flex-direction: column; gap: 4px; }
.sp-field > span { font-size: 12px; color: var(--text2); font-weight: 600; }
.sp-field input, .sp-field textarea {
  background: var(--bg); border: 1px solid var(--border); border-radius: 8px;
  padding: 8px 10px; font-size: 13px; color: var(--text); font-family: inherit; resize: vertical;
}
.sp-field input:focus, .sp-field textarea:focus { outline: none; border-color: var(--accent); }

.sp-agent-wrap { position: relative; }
.sp-agent-btn {
  display: inline-flex; align-items: center; gap: 6px;
  background: var(--bg); border: 1px solid var(--border); border-radius: 8px;
  padding: 8px 10px; font-size: 13px; color: var(--text); cursor: pointer; font-family: inherit;
}
.sp-agent-btn:hover { border-color: var(--accent); }
.sp-agent-menu {
  position: absolute; top: calc(100% + 4px); left: 0; z-index: 20; min-width: 200px;
  background: var(--surface); border: 1px solid var(--border); border-radius: 8px;
  box-shadow: 0 6px 20px rgba(0,0,0,.18); padding: 4px; display: flex; flex-direction: column;
  max-height: 240px; overflow-y: auto;
}
.sp-agent-menu button {
  background: none; border: none; text-align: left; padding: 7px 10px; border-radius: 6px;
  font-size: 13px; color: var(--text); cursor: pointer; font-family: inherit;
}
.sp-agent-menu button:hover { background: var(--surface2); }
.sp-agent-sec {
  padding: 8px 10px 4px; font-size: 11px; font-weight: 600; color: var(--text2);
  border-top: 1px solid var(--border); margin-top: 4px;
}

.sp-form-error { font-size: 12.5px; color: var(--red); margin: 0; }
.sp-form-actions { display: flex; justify-content: flex-end; gap: 8px; }
.sp-btn-ghost {
  background: none; border: 1px solid var(--border); border-radius: 8px;
  padding: 7px 14px; font-size: 13px; color: var(--text2); cursor: pointer; font-family: inherit;
}
.sp-btn-ghost:hover { background: var(--surface2); color: var(--text); }
.sp-btn-primary {
  background: var(--accent); border: none; border-radius: 8px; color: #fff;
  padding: 7px 14px; font-size: 13px; cursor: pointer; font-family: inherit;
}
.sp-btn-primary:hover { background: var(--accent-h); }
.sp-btn-primary:disabled { opacity: .5; cursor: not-allowed; }
</style>
