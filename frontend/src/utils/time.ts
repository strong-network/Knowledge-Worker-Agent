// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

const MINUTE = 60_000
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

function plural(n: number, unit: string): string {
  return `${n} ${unit}${n === 1 ? '' : 's'} ago`
}

/**
 * How long ago something happened, e.g. "Just now", "2 minutes ago",
 * "6 hours ago", "10 days ago".
 *
 * `at` is an RFC3339 timestamp from the server; `now` is injected so the
 * caller can drive re-rendering from a shared clock (see useNow) and so this
 * stays a pure function. Returns '' for a missing or unparseable timestamp —
 * showing nothing beats inventing a time.
 */
export function formatRelativeTime(at?: string, now: number = Date.now()): string {
  if (!at) return ''
  const then = new Date(at).getTime()
  if (Number.isNaN(then)) return ''

  // Clock skew between server and browser can put a just-written message
  // slightly in the future; don't render "in -3 seconds".
  const elapsed = Math.max(0, now - then)

  if (elapsed < 45_000) return 'Just now'
  if (elapsed < HOUR) return plural(Math.max(1, Math.round(elapsed / MINUTE)), 'minute')
  if (elapsed < DAY) return plural(Math.round(elapsed / HOUR), 'hour')
  if (elapsed < 30 * DAY) return plural(Math.round(elapsed / DAY), 'day')
  if (elapsed < 365 * DAY) return plural(Math.round(elapsed / (30 * DAY)), 'month')
  return plural(Math.round(elapsed / (365 * DAY)), 'year')
}

/** Full date and time, for the tooltip behind a relative timestamp. */
export function formatAbsoluteTime(at?: string): string {
  if (!at) return ''
  const d = new Date(at)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleString()
}

export function formatRelativeDate(dateStr: string): string {
  const date = new Date(dateStr)
  const now = new Date()
  const diffMs = now.getTime() - date.getTime()
  const diffDays = Math.floor(diffMs / (1000 * 60 * 60 * 24))

  if (diffDays === 0) return 'Today'
  if (diffDays === 1) return 'Yesterday'
  if (diffDays < 7) return `${diffDays} days ago`
  return date.toLocaleDateString()
}

export function formatSize(bytes: number): string {
  if (bytes < 1024) return bytes + ' B'
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB'
  return (bytes / (1024 * 1024)).toFixed(1) + ' MB'
}
