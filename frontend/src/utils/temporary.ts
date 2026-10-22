// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Temporary chats: how long one has left, as the app shows it.

const MINUTE = 60_000

// Never less than a minute: a chat past its time stays until the server deletes it.
function minutesLeft(expiresAt: number, now: number): number {
  return Math.max(1, Math.ceil((expiresAt - now) / MINUTE))
}

/** "23 hr" or "42 min". */
export function timeLeft(expiresAt: number, now: number): string {
  const m = minutesLeft(expiresAt, now)
  return m < 60 ? `${m} min` : `${Math.round(m / 60)} hr`
}

/** "23 hours" or "1 minute": the same, for screen readers. */
export function timeLeftLong(expiresAt: number, now: number): string {
  const m = minutesLeft(expiresAt, now)
  if (m < 60) return `${m} ${m === 1 ? 'minute' : 'minutes'}`
  const h = Math.round(m / 60)
  return `${h} ${h === 1 ? 'hour' : 'hours'}`
}

export function lastHour(expiresAt: number, now: number): boolean {
  return expiresAt - now < 60 * MINUTE
}

/** When the chat is deleted, for the tooltip on its time left. */
export function deletionNote(expiresAt: number, now: number): string {
  const at = new Date(Math.max(expiresAt, now + MINUTE))
  const day = at.toDateString() === new Date(now).toDateString() ? 'today' : 'tomorrow'
  const time = at.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })
  return `Deleted ${day} at ${time} unless you or the agent add a message. Keep the chat to save it.`
}

const WORDS = ['zero', 'one', 'two', 'three', 'four', 'five', 'six', 'seven', 'eight', 'nine']

/** Two to nine in words, 10 and up in numerals. */
export function countWord(n: number): string {
  return WORDS[n] ?? String(n)
}

export const TEMPORARY_ABOUT = "This chat isn't saved to your history. It's deleted after 24 hours with no new messages from you or the agent."
export const TEMPORARY_SHORT = "Not saved to your history. Deleted after 24 hours with no new messages from you or the agent."
