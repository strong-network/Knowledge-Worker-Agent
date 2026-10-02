// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Helpers for the skills the agent loads mid-turn.
//
// When the model decides a skill applies, it calls opencode's `skill` tool with
// `{"name": "<skill>"}` and receives that skill's SKILL.md body back. That is a
// different kind of event from reading a file or running a command: it says
// which body of expertise the answer is grounded in, which is exactly what a
// user wants confirmed. So these calls are pulled out of the "Tools (n)" list
// and shown in their own band — see MessageBubble.

export function isSkillTool(name?: string): boolean {
  return (name || '').toLowerCase() === 'skill'
}

// The skill's name, taken from the tool's input. Returns '' when the input is
// absent or unparseable, which the caller renders as a nameless entry rather
// than dropping the fact that a skill was loaded at all.
export function skillNameFromArgs(args?: string): string {
  if (!args) return ''
  try {
    const parsed = JSON.parse(args)
    if (parsed && typeof parsed === 'object') {
      // `name` is what opencode sends; the others are cheap insurance against
      // a provider that renames the field.
      for (const key of ['name', 'skill', 'id']) {
        const val = (parsed as Record<string, unknown>)[key]
        if (typeof val === 'string' && val.trim()) return val.trim()
      }
    }
  } catch {}
  return ''
}

// True when the user armed this skill in the composer rather than the model
// choosing it. Both end up applied to the answer, so this changes no more than
// the wording of the chip's explanation — but "the skill you asked for was
// found" and "the agent went and got this" are different reassurances.
export function isForcedSkill(args?: string): boolean {
  if (!args) return false
  try {
    const parsed = JSON.parse(args)
    return !!parsed && typeof parsed === 'object' && (parsed as Record<string, unknown>).forced === true
  } catch {
    return false
  }
}
