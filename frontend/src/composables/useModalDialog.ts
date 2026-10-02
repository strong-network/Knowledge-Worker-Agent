// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import { onBeforeUnmount, onMounted, watch } from 'vue'

/**
 * The keyboard behaviour every modal dialog needs, in one place: Tab stays
 * inside the dialog, Escape closes it, focus moves into it when it opens, and
 * back to whatever opened it when it closes.
 *
 * None of the app's dialogs did any of this. Tab walked straight out of the
 * dialog and on through the page behind it — which is still there, and on a
 * long chat is a very long way to walk — leaving keyboard and screen-reader
 * users somewhere they could not see and had no way back from. Escape did
 * nothing at all, in any of them, even though every one closes on a backdrop
 * click and so has nothing to lose by closing on a keystroke too.
 *
 * Dialogs stack: only the topmost one handles Escape and holds focus, so a
 * sheet layered over a modal closes the sheet and leaves the modal behind it
 * open rather than tearing both down at once.
 */

// `[tabindex]` catches anything opted in by hand; the tabIndex >= 0 filter
// below then discards the -1 ones, including a dialog's own root.
const FOCUSABLE = 'a[href],button,input,select,textarea,[contenteditable],[tabindex]'

interface Entry {
  el: () => HTMLElement | null | undefined
  canCloseOnEscape: () => boolean
  onClose: () => void
}

// Topmost last. Only the last entry is live; the rest are held open beneath it.
const stack: Entry[] = []

let titleSeq = 0

function isVisible(el: HTMLElement): boolean {
  // getClientRects() is empty for display:none and for hidden inputs, but a
  // visibility:hidden element still reports a box while being unfocusable.
  return el.getClientRects().length > 0 && getComputedStyle(el).visibility !== 'hidden'
}

function focusable(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE))
    .filter(el => !el.hasAttribute('disabled') && el.tabIndex >= 0 && isVisible(el))
}

/**
 * A dialog that exists to be typed into should arrive with the caret already in
 * the field, so the container is a fallback rather than the default. Anything
 * with no field at all — a confirmation, a table of numbers — takes focus on
 * the dialog itself, which announces its title and puts Tab at the start.
 */
function firstField(root: HTMLElement): HTMLElement | null {
  return focusable(root).find(el => /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName)) ?? null
}

/**
 * Fill in the dialog semantics from the markup rather than asking eighteen
 * components to repeat them. Anything already declared is left alone, so a
 * component that has said what it is keeps saying it.
 */
function describe(root: HTMLElement) {
  if (!root.hasAttribute('role')) root.setAttribute('role', 'dialog')
  if (!root.hasAttribute('aria-modal')) root.setAttribute('aria-modal', 'true')
  // Needed for the no-field case below: an element cannot take focus without it.
  if (!root.hasAttribute('tabindex')) root.tabIndex = -1
  if (root.hasAttribute('aria-label') || root.hasAttribute('aria-labelledby')) return
  const heading = root.querySelector<HTMLElement>('h1,h2,h3')
  if (!heading) return
  if (!heading.id) heading.id = `modal-dialog-title-${++titleSeq}`
  root.setAttribute('aria-labelledby', heading.id)
}

function onKeydown(e: KeyboardEvent) {
  if (e.isComposing) return
  const top = stack[stack.length - 1]
  if (!top) return
  const root = top.el()

  if (e.key === 'Escape') {
    // Handled even with no element to point at. A modal whose own root is
    // behind a `v-if` on data that never arrived leaves the overlay up with
    // nothing in it, and that is exactly when a way out is worth having.
    // Swallowed whether or not it closes anything: an Escape aimed at a dialog
    // must not also reach the handlers behind it and dismiss two things at once.
    e.stopPropagation()
    if (!top.canCloseOnEscape()) return
    e.preventDefault()
    top.onClose()
    return
  }

  if (e.key !== 'Tab' || !root) return
  const items = focusable(root)
  const active = document.activeElement as HTMLElement | null
  const inside = !!active && root.contains(active)
  if (!items.length) {
    e.preventDefault()
    root.focus()
    return
  }
  const first = items[0]!
  const last = items[items.length - 1]!
  if (e.shiftKey ? active === first || !inside : active === last || !inside) {
    e.preventDefault()
    ;(e.shiftKey ? last : first).focus()
  }
}

function onFocusIn(e: FocusEvent) {
  const top = stack[stack.length - 1]
  const root = top?.el()
  if (!top || !root) return
  if (e.target instanceof Node && root.contains(e.target)) return
  // Focus left by a route Tab does not cover — a click on the page behind, or
  // code elsewhere calling focus(). Put it back.
  ;(focusable(root)[0] ?? root).focus()
}

function listen(on: boolean) {
  const fn = on ? document.addEventListener : document.removeEventListener
  // Capture, so a dialog's Escape and Tab are settled before the document-level
  // handlers that close composer popovers and leave the file panel's full-screen
  // reader get a look at them.
  fn.call(document, 'keydown', onKeydown as EventListener, true)
  fn.call(document, 'focusin', onFocusIn as EventListener, true)
}

/**
 * Run `fn` once the dialog element exists. It usually already does, but a modal
 * whose root is itself behind a `v-if` on loaded data can arrive a frame or two
 * late, and silently skipping focus for those would be worse than waiting.
 */
function whenReady(get: () => HTMLElement | null | undefined, fn: (el: HTMLElement) => void) {
  let raf = 0
  let tries = 0
  const tick = () => {
    const el = get()
    if (el) return fn(el)
    if (++tries > 5) return
    raf = requestAnimationFrame(tick)
  }
  tick()
  return () => cancelAnimationFrame(raf)
}

export interface ModalDialogOptions {
  /** Close the dialog. Called when Escape is pressed. */
  onClose: () => void
  /**
   * Which dialog is showing: `null` when none is, and a different string when a
   * different one has replaced it. Omit it and the dialog counts as open for as
   * long as the calling component is mounted, which is what a `v-if`'d dialog
   * wants; `ModalHost` needs it because its own lifetime is the whole app's.
   */
  key?: () => string | null
  /** Escape is ignored while this returns false. Defaults to always closing. */
  canCloseOnEscape?: () => boolean
  /** What to focus on open, overriding the first-field-else-dialog default. */
  initialFocus?: () => HTMLElement | null | undefined
}

export function useModalDialog(
  el: () => HTMLElement | null | undefined,
  options: ModalDialogOptions
) {
  const entry: Entry = {
    el,
    canCloseOnEscape: options.canCloseOnEscape ?? (() => true),
    onClose: () => options.onClose(),
  }
  let opener: HTMLElement | null = null
  let cancelReady: (() => void) | null = null
  let open = false

  function enter() {
    if (open) return
    open = true
    opener = document.activeElement as HTMLElement | null
    stack.push(entry)
    if (stack.length === 1) listen(true)
    cancelReady = whenReady(el, root => {
      describe(root)
      ;(options.initialFocus?.() ?? firstField(root) ?? root).focus()
    })
  }

  function exit() {
    if (!open) return
    open = false
    cancelReady?.()
    cancelReady = null
    const i = stack.lastIndexOf(entry)
    if (i >= 0) stack.splice(i, 1)
    if (!stack.length) listen(false)
    const back = opener
    opener = null
    // Not if it has gone: a row's own button can be what opened the dialog that
    // deleted the row, and focusing a detached node drops focus onto the body.
    if (back?.isConnected) back.focus()
  }

  const key = options.key
  onMounted(() => {
    if (!key || key() !== null) enter()
  })
  if (key) {
    // `post` so the dialog is in the DOM by the time we go looking for it.
    watch(key, (now, before) => {
      if (before !== null) exit()
      if (now !== null) enter()
    }, { flush: 'post' })
  }
  onBeforeUnmount(exit)
}
