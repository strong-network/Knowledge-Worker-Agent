// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// With the installed app's title bar hidden (Chrome's window controls
// overlay), the window buttons are drawn on the theme colour, over the page's
// top row: give them the row's colour, in either theme. With the title bar
// shown, the page's own theme colours apply again.
export function matchTitleBarColor(): void {
  if (typeof window === 'undefined' || !window.matchMedia) return
  const hidden = window.matchMedia('(display-mode: window-controls-overlay)')
  const metas = Array.from(document.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]'))
  const own = metas.map((m) => m.content)
  const apply = () => {
    const row = hidden.matches ? getComputedStyle(document.documentElement).getPropertyValue('--surface').trim() : ''
    metas.forEach((m, i) => { m.content = row || own[i] })
  }
  apply()
  hidden.addEventListener('change', apply)
  new MutationObserver(apply).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
}
