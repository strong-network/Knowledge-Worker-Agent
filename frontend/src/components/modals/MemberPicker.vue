<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, watch, nextTick, onBeforeUnmount } from 'vue'
import type { ShareMember } from '../../api'

// A multi-select of project members with search. The platform gives
// us emails only, so the avatar is the email's initial. Emits the whole new
// selection on every change.
const props = defineProps<{ members: ShareMember[]; selected: number[]; label: string }>()
const emit = defineEmits<{ change: [ids: number[]] }>()

const open = ref(false)
const query = ref('')
const active = ref(0)
const root = ref<HTMLElement | null>(null)
const search = ref<HTMLInputElement | null>(null)
const caret = ref<HTMLButtonElement | null>(null)
const uid = `mp-${Math.random().toString(36).slice(2, 8)}`

const chosen = computed(() => new Set(props.selected))
const chips = computed(() => props.members.filter(m => chosen.value.has(m.id)))
const shown = computed(() => {
  const q = query.value.trim().toLowerCase()
  return q ? props.members.filter(m => m.email.toLowerCase().includes(q)) : props.members
})
watch(shown, () => { active.value = 0 })

const initial = (email: string) => (email.trim()[0] || '?').toUpperCase()
const optId = (i: number) => `${uid}-opt-${i}`

function toggle(id: number) {
  const next = props.selected.filter(x => x !== id)
  if (!chosen.value.has(id)) next.push(id)
  emit('change', next)
}

function remove(id: number) {
  emit('change', props.selected.filter(x => x !== id))
}

function openList() {
  if (open.value) return
  open.value = true
  query.value = ''
  active.value = 0
  nextTick(() => search.value?.focus())
}

function closeList(returnFocus: boolean) {
  if (!open.value) return
  open.value = false
  if (returnFocus) caret.value?.focus()
}

function scrollActive() {
  nextTick(() => document.getElementById(optId(active.value))?.scrollIntoView({ block: 'nearest' }))
}

function onSearchKey(e: KeyboardEvent) {
  const n = shown.value.length
  if (e.key === 'ArrowDown' && n) {
    e.preventDefault()
    active.value = (active.value + 1) % n
    scrollActive()
  } else if (e.key === 'ArrowUp' && n) {
    e.preventDefault()
    active.value = (active.value - 1 + n) % n
    scrollActive()
  } else if (e.key === 'Enter') {
    e.preventDefault()
    const m = shown.value[active.value]
    if (m) toggle(m.id)
  } else if (e.key === 'Tab') {
    closeList(false)
  }
}

// On window, so it runs before the modal's own capture-phase Escape handler on
// document: Escape closes the list and leaves the modal open.
function onWindowKey(e: KeyboardEvent) {
  if (e.key !== 'Escape' || !open.value) return
  e.stopPropagation()
  e.preventDefault()
  closeList(true)
}

// On click, not pointerdown: closing shrinks the modal, and mid-press that
// moves what was pressed, so the release lands elsewhere and the click is lost.
function onOutsideClick(e: MouseEvent) {
  if (root.value && !root.value.contains(e.target as Node)) closeList(false)
}

function listen(on: boolean) {
  if (on) {
    window.addEventListener('keydown', onWindowKey, true)
    document.addEventListener('click', onOutsideClick, true)
  } else {
    window.removeEventListener('keydown', onWindowKey, true)
    document.removeEventListener('click', onOutsideClick, true)
  }
}
watch(open, listen)
onBeforeUnmount(() => listen(false))
</script>

<template>
  <div ref="root" class="mp">
    <div class="mp-field" :class="{ open }" @click="open ? closeList(false) : openList()">
      <span v-for="m in chips" :key="m.id" class="mp-chip" @click.stop>
        <span class="mp-avatar" aria-hidden="true">{{ initial(m.email) }}</span>
        <span class="mp-chip-text">{{ m.email }}</span>
        <button type="button" class="mp-chip-x" :aria-label="`Remove ${m.email}`" @click.stop="remove(m.id)">×</button>
      </span>
      <span v-if="!chips.length" class="mp-placeholder">No one chosen</span>
      <button
        ref="caret"
        type="button"
        class="mp-caret"
        :aria-expanded="open"
        :aria-controls="`${uid}-list`"
        :aria-label="label"
        @click.stop="open ? closeList(false) : openList()"
      >
        <svg viewBox="0 0 16 16" width="14" height="14" fill="currentColor" aria-hidden="true">
          <path d="M4.22 9.78a.75.75 0 0 0 1.06 0L8 7.06l2.72 2.72a.75.75 0 1 0 1.06-1.06L8.53 5.47a.75.75 0 0 0-1.06 0L4.22 8.72a.75.75 0 0 0 0 1.06z"/>
        </svg>
      </button>
    </div>

    <div v-if="open" class="mp-panel">
      <div class="mp-search">
        <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
          <circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5" stroke-linecap="round"/>
        </svg>
        <input
          ref="search"
          v-model="query"
          type="text"
          role="combobox"
          aria-autocomplete="list"
          aria-expanded="true"
          :aria-controls="`${uid}-list`"
          :aria-activedescendant="shown.length ? optId(active) : undefined"
          aria-label="Search project members"
          placeholder="Search…"
          autocomplete="off"
          @keydown="onSearchKey"
        />
      </div>
      <ul :id="`${uid}-list`" class="mp-list" role="listbox" aria-multiselectable="true" :aria-label="label">
        <li
          v-for="(m, i) in shown"
          :id="optId(i)"
          :key="m.id"
          role="option"
          :aria-selected="chosen.has(m.id)"
          class="mp-opt"
          :class="{ selected: chosen.has(m.id), active: i === active }"
          @mousedown.prevent
          @mousemove="active = i"
          @click="toggle(m.id)"
        >
          <span class="mp-avatar" aria-hidden="true">{{ initial(m.email) }}</span>
          <span class="mp-opt-text">{{ m.email }}</span>
          <span v-if="chosen.has(m.id)" class="mp-opt-x" aria-hidden="true">×</span>
        </li>
        <li v-if="!shown.length" class="mp-empty" role="presentation">No one matches “{{ query.trim() }}”.</li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.mp { display: flex; flex-direction: column; gap: 8px; }

.mp-field {
  display: flex; flex-wrap: wrap; align-items: center; gap: 8px;
  min-height: 44px; padding: 6px 8px;
  border: 1px solid var(--border); border-radius: 8px;
  background: var(--surface2); cursor: pointer;
}
.mp-field.open, .mp-field:focus-within { border-color: var(--accent); }

.mp-chip {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 3px 4px 3px 4px; border-radius: 999px;
  background: var(--surface); border: 1px solid var(--border);
  font-size: 13px; color: var(--text); max-width: 100%; cursor: default;
}
.mp-chip-text { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.mp-chip-x, .mp-caret {
  display: inline-flex; align-items: center; justify-content: center;
  border: none; background: none; color: var(--text2); cursor: pointer;
  border-radius: 999px; font-size: 16px; line-height: 1;
}
.mp-chip-x { width: 22px; height: 22px; }
.mp-chip-x:hover, .mp-caret:hover { color: var(--text); background: var(--surface3); }
.mp-placeholder { flex: 1; font-size: 13px; color: var(--text2); padding-left: 4px; }
.mp-caret { margin-left: auto; width: 28px; height: 28px; transition: transform .12s; }
.mp-caret:focus-visible, .mp-chip-x:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
.mp-field:not(.open) .mp-caret { transform: rotate(180deg); }

.mp-avatar {
  display: inline-flex; align-items: center; justify-content: center; flex-shrink: 0;
  width: 24px; height: 24px; border-radius: 50%;
  font-size: 12px; font-weight: 600;
  background: var(--surface3); color: var(--text2);
}

.mp-panel {
  border: 1px solid var(--border); border-radius: 8px;
  background: var(--surface); overflow: hidden;
}
.mp-search {
  display: flex; align-items: center; gap: 8px;
  padding: 0 12px; border-bottom: 1px solid var(--border);
  background: var(--surface2); color: var(--text2);
}
.mp-search input {
  flex: 1; min-width: 0; height: 40px;
  border: none; outline: none; background: none;
  color: var(--text); font-size: 13px; font-family: inherit;
}

.mp-list { list-style: none; margin: 0; padding: 4px 0; max-height: 240px; overflow-y: auto; }
.mp-opt {
  display: flex; align-items: center; gap: 10px;
  padding: 8px 12px; font-size: 13px; color: var(--text); cursor: pointer;
}
.mp-opt.active { background: var(--hover); }
.mp-opt.selected { background: var(--accent); color: #fff; }
.mp-opt.selected .mp-avatar { background: rgba(255, 255, 255, .25); color: #fff; }
.mp-opt.selected.active { background: var(--accent-h); }
.mp-opt-text { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.mp-opt-x { font-size: 16px; line-height: 1; }
.mp-empty { padding: 8px 12px; font-size: 12px; color: var(--text2); }
</style>
