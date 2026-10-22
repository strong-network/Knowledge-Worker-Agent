// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import type { Directive } from 'vue'

// v-tip="text": a tooltip that shows soon on hover and at once on keyboard
// focus, unlike the native title attribute. One element serves every control.

const HOVER_DELAY = 300
// After a tooltip hides, the next one shows without a delay for this long.
const WARM_FOR = 500

interface Tipped extends HTMLElement {
  _tipText?: string
  _tipOff?: () => void
}

let tipEl: HTMLDivElement | null = null
let current: Tipped | null = null
let pressed: Tipped | null = null
let timer: ReturnType<typeof setTimeout> | null = null
let hiddenAt = 0

function element(): HTMLDivElement {
  if (!tipEl) {
    tipEl = document.createElement('div')
    tipEl.id = 'kwa-tip'
    tipEl.className = 'kwa-tip'
    tipEl.setAttribute('role', 'tooltip')
    document.body.appendChild(tipEl)
  }
  return tipEl
}

function place(el: HTMLElement) {
  const tip = element()
  const r = el.getBoundingClientRect()
  const t = tip.getBoundingClientRect()
  const gap = 8
  let top = r.top - t.height - gap
  if (top < gap) top = r.bottom + gap
  const left = Math.min(Math.max(gap, r.left + r.width / 2 - t.width / 2), window.innerWidth - t.width - gap)
  tip.style.top = `${Math.round(top)}px`
  tip.style.left = `${Math.round(left)}px`
}

function show(el: Tipped) {
  if (!el._tipText || !el.isConnected) return
  const tip = element()
  current = el
  tip.textContent = el._tipText
  tip.classList.add('show')
  place(el)
  document.addEventListener('keydown', onKey, true)
  window.addEventListener('scroll', hide, true)
  window.addEventListener('resize', hide)
}

function hide() {
  if (timer) { clearTimeout(timer); timer = null }
  if (!current) return
  current = null
  hiddenAt = Date.now()
  tipEl?.classList.remove('show')
  document.removeEventListener('keydown', onKey, true)
  window.removeEventListener('scroll', hide, true)
  window.removeEventListener('resize', hide)
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') hide()
}

function schedule(el: Tipped, delay: number) {
  if (timer) clearTimeout(timer)
  if (delay <= 0) { show(el); return }
  timer = setTimeout(() => { timer = null; show(el) }, delay)
}

function setText(el: Tipped, text: string | null | undefined) {
  el._tipText = text || ''
  if (el._tipText && el._tipText !== el.getAttribute('aria-label')) el.setAttribute('aria-description', el._tipText)
  else el.removeAttribute('aria-description')
  if (current === el) {
    if (!el._tipText) { hide(); return }
    element().textContent = el._tipText
    place(el)
  }
}

export const vTip: Directive<Tipped, string | null | undefined> = {
  mounted(el, binding) {
    setText(el, binding.value)
    const enter = (e: PointerEvent) => {
      if (e.pointerType === 'touch' || pressed === el) return
      schedule(el, Date.now() - hiddenAt < WARM_FOR ? 0 : HOVER_DELAY)
    }
    const leave = () => {
      if (pressed === el) pressed = null
      if (current === el || timer) hide()
    }
    const focus = () => { if (el.matches(':focus-visible')) schedule(el, 0) }
    const down = () => { pressed = el; hide() }
    el.addEventListener('pointerenter', enter)
    el.addEventListener('pointerleave', leave)
    el.addEventListener('focus', focus)
    el.addEventListener('blur', leave)
    el.addEventListener('pointerdown', down)
    el._tipOff = () => {
      el.removeEventListener('pointerenter', enter)
      el.removeEventListener('pointerleave', leave)
      el.removeEventListener('focus', focus)
      el.removeEventListener('blur', leave)
      el.removeEventListener('pointerdown', down)
    }
  },
  updated(el, binding) {
    if (binding.value !== binding.oldValue) setText(el, binding.value)
  },
  beforeUnmount(el) {
    el._tipOff?.()
    if (pressed === el) pressed = null
    if (current === el) hide()
  },
}
