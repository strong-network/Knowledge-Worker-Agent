// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Persistence for the sidebar's expand/collapse state.
//
// The sidebar is a tree the user arranges to suit how they work — Projects and
// Starred open, everything else folded away. Rebuilding that arrangement on
// every visit is busywork, so each node's state is remembered.
//
// localStorage rather than the server: this is per-browser furniture, not
// account data, and it must be readable synchronously during component setup.
// Anything async would render the default tree first and rearrange it a moment
// later, which reads as a glitch.
//
// Every access is wrapped: localStorage throws (not returns null) in Safari
// private mode and when the quota is full. A sidebar that fails to remember a
// caret is a shrug; a sidebar that throws during setup renders nothing at all.

function read(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

function write(key: string, value: string): void {
  try {
    localStorage.setItem(key, value)
  } catch {
    /* private mode / quota — the state simply isn't remembered */
  }
}

/** Read a persisted collapse flag, falling back when nothing is stored yet. */
export function loadFlag(key: string, fallback: boolean): boolean {
  const raw = read(key)
  if (raw === null) return fallback
  return raw === '1'
}

export function saveFlag(key: string, value: boolean): void {
  write(key, value ? '1' : '0')
}

// Sections whose default is adaptive (it depends on the user's data) need three
// states, not two: open, closed, and "no opinion — keep following the default".
// Storing a plain boolean would collapse that third case into a choice the user
// never made, permanently freezing a default that was meant to keep adapting.
export function loadOverride(key: string): boolean | null {
  const raw = read(key)
  if (raw !== '1' && raw !== '0') return null
  return raw === '1'
}

export function saveOverride(key: string, value: boolean | null): void {
  if (value === null) {
    try {
      localStorage.removeItem(key)
    } catch {
      /* nothing stored is the same as no override */
    }
    return
  }
  write(key, value ? '1' : '0')
}

/**
 * Read the set of expanded item ids (projects, scheduled tasks) as a lookup.
 * Stored as an array of the expanded ids only, so collapsing an item drops it
 * rather than accumulating `false` entries forever.
 */
export function loadExpandedIds(key: string): Record<string, boolean> {
  const raw = read(key)
  if (!raw) return {}
  try {
    const list = JSON.parse(raw)
    if (!Array.isArray(list)) return {}
    const out: Record<string, boolean> = {}
    for (const id of list) {
      if (typeof id === 'string' && id) out[id] = true
    }
    return out
  } catch {
    return {} // corrupt entry — start clean rather than break setup
  }
}

export function saveExpandedIds(key: string, map: Record<string, boolean>): void {
  write(key, JSON.stringify(Object.keys(map).filter(id => map[id])))
}
