<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script lang="ts">
import { ref } from 'vue'

export interface MenuItem {
  label: string
  // Runs once the menu has closed and focus is back on its button, so an
  // action that moves focus on purpose (a dialog, a rename field) has the
  // last word.
  action: () => void
  danger?: boolean
  // A line under the label saying what the item does.
  description?: string
  // Why the item can't be used now. The item stays in the menu, and stays
  // focusable, so the reason is heard as well as seen. Choosing it does nothing.
  unavailable?: string
  // Set on each of a set of choices: whether this one is chosen.
  checked?: boolean
}
export type MenuEntry = MenuItem | 'divider'

// One menu is open at a time: opening one closes the others.
const openMenu = ref(0)
let lastId = 0
</script>

<script setup lang="ts">
// A ⋯ button and the menu it opens: one control in place of a row of icon
// buttons. The menu is a menu to assistive technology and works from the
// keyboard: arrow keys, Home and End move through it, a letter jumps to the
// next item starting with it, Escape closes it and Tab moves on past its
// button. It's drawn at the top of the page, so a scrolling list or a clipped
// panel doesn't cut it off.
import { nextTick, onBeforeUnmount, watch } from 'vue'
import { vTip } from '../directives/tip'

const props = withDefaults(defineProps<{
  items: MenuEntry[]
  // The button's name.
  label: string
  // The menu's name.
  menuLabel: string
  // A bordered button for a toolbar, or a small plain one for a list row.
  // chip shows triggerText rather than ⋯, for a menu of choices.
  variant?: 'toolbar' | 'row' | 'chip'
  triggerText?: string
  tip?: string
}>(), { variant: 'row', tip: undefined })

const id = ++lastId
const menuId = `action-menu-${id}`
const open = ref(false)
const trigger = ref<HTMLButtonElement | null>(null)
const menu = ref<HTMLElement | null>(null)
// Hidden until it's measured, so it doesn't flash at the corner of the page.
const place = ref<Record<string, string>>({})

const note = (item: MenuItem) => item.unavailable || item.description || ''

function itemEls(): HTMLElement[] {
  return Array.from(menu.value?.querySelectorAll<HTMLElement>('[role^="menuitem"]') ?? [])
}
function focusAt(i: number) {
  itemEls()[i]?.focus()
}

// Below the button, right edges lined up; above it when there's no room below.
function position() {
  const t = trigger.value?.getBoundingClientRect()
  const m = menu.value
  if (!t || !m) return
  const edge = 8, gap = 4
  const w = m.offsetWidth, h = m.offsetHeight
  const vw = window.innerWidth, vh = window.innerHeight
  let left = t.right - w
  if (left < edge) left = Math.min(t.left, vw - w - edge)
  left = Math.max(edge, left)
  let top = t.bottom + gap
  if (top + h > vh - edge) top = t.top - gap - h >= edge ? t.top - gap - h : Math.max(edge, vh - edge - h)
  place.value = { left: `${left}px`, top: `${top}px` }
}

async function show(at: 'first' | 'last') {
  openMenu.value = id
  place.value = { left: '0px', top: '0px', visibility: 'hidden' }
  open.value = true
  listen(true)
  await nextTick()
  position()
  // Shown before it's focused: a hidden element can't take focus.
  await nextTick()
  const els = itemEls()
  if (at === 'last') { focusAt(els.length - 1); return }
  // The first item that can be used, though the others can still be reached.
  const first = els.findIndex(el => el.getAttribute('aria-disabled') !== 'true')
  focusAt(Math.max(0, first))
}

function hide(returnFocus: boolean) {
  if (!open.value) return
  open.value = false
  listen(false)
  if (openMenu.value === id) openMenu.value = 0
  if (returnFocus) trigger.value?.focus()
}

function toggle() {
  if (open.value) hide(true)
  else void show('first')
}

function choose(item: MenuItem) {
  if (item.unavailable) return
  hide(true)
  item.action()
}

function onTriggerKey(e: KeyboardEvent) {
  if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
  e.preventDefault()
  void show(e.key === 'ArrowDown' ? 'first' : 'last')
}

function onMenuKey(e: KeyboardEvent) {
  const els = itemEls()
  const n = els.length
  const i = els.indexOf(document.activeElement as HTMLElement)
  switch (e.key) {
    case 'ArrowDown': e.preventDefault(); focusAt((i + 1) % n); return
    case 'ArrowUp': e.preventDefault(); focusAt(i <= 0 ? n - 1 : i - 1); return
    case 'Home': e.preventDefault(); focusAt(0); return
    case 'End': e.preventDefault(); focusAt(n - 1); return
    case 'Escape':
      // Only the menu closes, not the panel or dialog it's in.
      e.preventDefault(); e.stopPropagation(); hide(true); return
    case 'Tab':
      // Back to the button, and the browser's Tab goes on from there: to what
      // follows the button, or with Shift what's before it.
      hide(true); return
  }
  if (e.key.length !== 1 || e.ctrlKey || e.metaKey || e.altKey || !e.key.trim()) return
  const key = e.key.toLowerCase()
  for (let k = 1; k <= n; k++) {
    const j = (i + k) % n
    if (els[j].textContent?.trim().toLowerCase().startsWith(key)) { focusAt(j); return }
  }
}

// A press anywhere else closes it, and so does scrolling or resizing the
// page, which would leave it pointing at nothing. Focus stays where it went.
function onPointerDown(e: PointerEvent) {
  const t = e.target as Node
  if (menu.value?.contains(t) || trigger.value?.contains(t)) return
  hide(false)
}
function onScroll(e: Event) {
  if (menu.value?.contains(e.target as Node)) return
  hide(false)
}
function onResize() { hide(false) }
function listen(on: boolean) {
  if (on) {
    document.addEventListener('pointerdown', onPointerDown, true)
    window.addEventListener('scroll', onScroll, true)
    window.addEventListener('resize', onResize)
  } else {
    document.removeEventListener('pointerdown', onPointerDown, true)
    window.removeEventListener('scroll', onScroll, true)
    window.removeEventListener('resize', onResize)
  }
}

watch(openMenu, (v) => { if (v !== id) hide(false) })
onBeforeUnmount(() => hide(false))

defineExpose({ focus: () => trigger.value?.focus() })
</script>

<template>
  <span class="am" :class="{ open }" @click.stop>
    <button
      ref="trigger"
      type="button"
      class="am-trigger"
      :class="variant"
      :aria-label="label"
      aria-haspopup="menu"
      :aria-expanded="open"
      :aria-controls="open ? menuId : undefined"
      v-tip="tip"
      @click="toggle"
      @keydown="onTriggerKey"
    >
      <template v-if="triggerText">
        <span>{{ triggerText }}</span>
        <svg viewBox="0 0 16 16" width="10" height="10" fill="currentColor" aria-hidden="true"><path d="M4.22 6.22a.75.75 0 0 1 1.06 0L8 8.94l2.72-2.72a.75.75 0 1 1 1.06 1.06l-3.25 3.25a.75.75 0 0 1-1.06 0L4.22 7.28a.75.75 0 0 1 0-1.06z"/></svg>
      </template>
      <svg v-else viewBox="0 0 24 24" :width="variant === 'toolbar' ? 18 : 16" :height="variant === 'toolbar' ? 18 : 16" fill="currentColor" aria-hidden="true">
        <circle cx="5" cy="12" r="1.9" /><circle cx="12" cy="12" r="1.9" /><circle cx="19" cy="12" r="1.9" />
      </svg>
    </button>
    <Teleport to="body">
      <div
        v-if="open"
        :id="menuId"
        ref="menu"
        class="am-menu"
        role="menu"
        :aria-label="menuLabel"
        :style="place"
        @keydown="onMenuKey"
      >
        <template v-for="(entry, i) in props.items" :key="i">
          <div v-if="entry === 'divider'" class="am-divider" role="separator"></div>
          <button
            v-else
            type="button"
            :role="entry.checked === undefined ? 'menuitem' : 'menuitemradio'"
            tabindex="-1"
            class="am-item"
            :class="{ danger: entry.danger, unavailable: !!entry.unavailable, choice: entry.checked !== undefined }"
            :aria-checked="entry.checked"
            :aria-disabled="entry.unavailable ? 'true' : undefined"
            :aria-labelledby="`${menuId}-${i}`"
            :aria-describedby="note(entry) ? `${menuId}-${i}-note` : undefined"
            @click="choose(entry)"
          >
            <span :id="`${menuId}-${i}`" class="am-label">{{ entry.label }}</span>
            <span v-if="note(entry)" :id="`${menuId}-${i}-note`" class="am-note">{{ note(entry) }}</span>
            <svg v-if="entry.checked" class="am-check" viewBox="0 0 16 16" width="14" height="14" fill="currentColor" aria-hidden="true"><path d="M13.78 4.22a.75.75 0 0 1 0 1.06l-7.25 7.25a.75.75 0 0 1-1.06 0L2.22 9.28a.75.75 0 0 1 1.06-1.06L6 10.94l6.72-6.72a.75.75 0 0 1 1.06 0z"/></svg>
          </button>
        </template>
      </div>
    </Teleport>
  </span>
</template>

<style scoped>
.am { display: inline-flex; flex-shrink: 0; }
.am-trigger {
  display: inline-flex; align-items: center; justify-content: center;
  background: none; color: var(--text2); cursor: pointer; padding: 0;
  transition: background .12s, color .12s, border-color .12s;
}
.am-trigger svg { display: block; }
.am-trigger.toolbar {
  width: 34px; height: 34px; border: 1px solid var(--border); border-radius: 8px;
}
.am-trigger.toolbar:hover,
.am-trigger.toolbar[aria-expanded="true"] { color: var(--text); border-color: var(--text2); background: var(--surface2); }
.am-trigger.row {
  width: 24px; height: 24px; border: none; border-radius: 5px;
}
.am-trigger.row:hover,
.am-trigger.row[aria-expanded="true"] { color: var(--text); background: var(--surface3); }
.am-trigger.chip {
  gap: 4px; height: 26px; padding: 0 7px 0 9px; border: 1px solid var(--border); border-radius: 6px;
  font: inherit; font-size: 12px; font-weight: 600;
}
.am-trigger.chip:hover,
.am-trigger.chip[aria-expanded="true"] { color: var(--text); background: var(--surface2); }
@media (max-width: 768px) {
  .am-trigger.toolbar { width: 32px; height: 32px; }
}

.am-menu {
  position: fixed; z-index: 900;
  min-width: 190px; max-width: min(300px, calc(100vw - 16px));
  max-height: calc(100vh - 16px); overflow-y: auto;
  padding: 5px;
  background: var(--surface); border: 1px solid var(--border); border-radius: 10px;
  box-shadow: 0 12px 40px rgba(0,0,0,.28);
  animation: amPop .1s ease;
}
@keyframes amPop { from { opacity: 0; transform: translateY(-3px); } to { opacity: 1; transform: none; } }
@media (prefers-reduced-motion: reduce) { .am-menu { animation: none; } }
.am-item {
  display: flex; flex-direction: column; align-items: flex-start; gap: 2px;
  width: 100%; padding: 7px 10px; border: none; border-radius: 7px;
  background: none; color: var(--text); font: inherit; font-size: 13px; text-align: left;
  cursor: pointer;
}
.am-item:hover,
.am-item:focus { background: var(--hover); }
.am-item:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }
.am-label { font-weight: 500; }
.am-item.choice { position: relative; padding-right: 32px; }
.am-check { position: absolute; right: 10px; top: 9px; color: var(--accent); }
.am-note { font-size: 11.5px; line-height: 1.35; color: var(--text2); }
.am-item:hover .am-note,
.am-item:focus .am-note { color: var(--text); }
.am-item.danger { color: var(--red); }
.am-item.danger:hover,
.am-item.danger:focus { background: color-mix(in srgb, var(--red) 12%, transparent); }
.am-item.unavailable { cursor: default; }
.am-item.unavailable .am-label { color: var(--text3); }
.am-divider { height: 1px; margin: 5px 4px; background: var(--border); }
</style>
