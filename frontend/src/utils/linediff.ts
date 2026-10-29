// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// What changed between two versions of a file, line by line (or row by row),
// and word by word within a line, as the file viewer counts and highlights it.

export interface Changes {
  // Lines that are new rather than changed.
  added: number
  // Lines of the earlier version that were removed or changed.
  removed: number
  // Indexes into the later version of the lines added or changed.
  marked: Set<number>
  // Lines a to a + aLen of the earlier version became b to b + bLen of the
  // later. Empty when the versions were too big to compare exactly.
  hunks: Hunk[]
}

export interface Hunk { a: number; aLen: number; b: number; bLen: number }

// Beyond this many cells the exact comparison is too slow to wait for, and a
// cheaper one that only notices lines the earlier version didn't have is used.
const EXACT_LIMIT = 4_000_000

// A file's lines. The newline that ends the last line doesn't start another.
export function splitLines(text: string): string[] {
  const lines = text.replace(/\r\n?/g, '\n').split('\n')
  if (lines.length > 1 && lines[lines.length - 1] === '') lines.pop()
  return lines
}

export function diffItems(before: string[], after: string[]): Changes {
  let start = 0
  while (start < before.length && start < after.length && before[start] === after[start]) start++
  let endA = before.length, endB = after.length
  while (endA > start && endB > start && before[endA - 1] === after[endB - 1]) { endA--; endB-- }
  const a = before.slice(start, endA)
  const b = after.slice(start, endB)
  const out: Changes = { added: 0, removed: 0, marked: new Set(), hunks: [] }
  if (!a.length && !b.length) return out
  if (a.length * b.length > EXACT_LIMIT) return roughly(a, b, start, out)

  // Longest common subsequence, then a walk that pairs the runs between matches.
  const w = b.length + 1
  const lcs = new Uint32Array((a.length + 1) * w)
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      lcs[i * w + j] = a[i] === b[j] ? lcs[(i + 1) * w + j + 1] + 1 : Math.max(lcs[(i + 1) * w + j], lcs[i * w + j + 1])
    }
  }
  let i = 0, j = 0, gone = 0, come = 0
  const settle = () => {
    if (gone || come) out.hunks.push({ a: start + i - gone, aLen: gone, b: start + j - come, bLen: come })
    out.removed += gone
    out.added += Math.max(0, come - gone)
    gone = come = 0
  }
  while (i < a.length || j < b.length) {
    if (i < a.length && j < b.length && a[i] === b[j]) {
      settle()
      i++; j++
    } else if (j < b.length && (i >= a.length || lcs[i * w + j + 1] >= lcs[(i + 1) * w + j])) {
      out.marked.add(start + j)
      come++; j++
    } else {
      gone++; i++
    }
  }
  settle()
  return out
}

function roughly(a: string[], b: string[], start: number, out: Changes): Changes {
  const left = new Map<string, number>()
  for (const line of a) left.set(line, (left.get(line) || 0) + 1)
  b.forEach((line, j) => {
    const n = left.get(line) || 0
    if (n) left.set(line, n - 1)
    else out.marked.add(start + j)
  })
  let gone = 0
  for (const n of left.values()) gone += n
  out.removed = gone
  out.added = Math.max(0, out.marked.size - gone)
  return out
}

// A stretch of text both versions have, or text the earlier version had where
// the later one has other text (either may be empty).
export type Segment = { same: string } | { removed: string; added: string }

// Words, numbers, runs of spaces, and every other character on its own, so
// "$1,200" to "$1,250" changes only "200".
const TOKEN = /[\p{L}\p{M}]+|\p{N}+|\s+|[^\p{L}\p{M}\p{N}\s]/gu
const WORD_LIMIT = 250_000
// With less than this share of their text in common, two versions are a
// rewrite: marked word by word they'd be mostly marks. Short texts (a list
// item, a cell) are always marked by their words.
const KEEP_SHARE = 0.4
const SHORT = 20

export function diffWords(before: string, after: string): Segment[] {
  const a = before.match(TOKEN) ?? []
  const b = after.match(TOKEN) ?? []
  let start = 0
  while (start < a.length && start < b.length && a[start] === b[start]) start++
  let endA = a.length, endB = b.length
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) { endA--; endB-- }

  const segs: Segment[] = []
  const same = (t: string) => {
    if (!t) return
    const last = segs[segs.length - 1]
    if (last && 'same' in last) last.same += t
    else segs.push({ same: t })
  }
  const edit = (removed: string, added: string) => {
    if (!removed && !added) return
    const last = segs[segs.length - 1]
    if (last && 'removed' in last) { last.removed += removed; last.added += added }
    else segs.push({ removed, added })
  }
  same(a.slice(0, start).join(''))
  const x = a.slice(start, endA), y = b.slice(start, endB)
  if (x.length * y.length > WORD_LIMIT) {
    edit(x.join(''), y.join(''))
  } else {
    const w = y.length + 1
    const lcs = new Uint32Array((x.length + 1) * w)
    for (let i = x.length - 1; i >= 0; i--) {
      for (let j = y.length - 1; j >= 0; j--) {
        lcs[i * w + j] = x[i] === y[j] ? lcs[(i + 1) * w + j + 1] + 1 : Math.max(lcs[(i + 1) * w + j], lcs[i * w + j + 1])
      }
    }
    let i = 0, j = 0
    while (i < x.length || j < y.length) {
      if (i < x.length && j < y.length && x[i] === y[j]) { same(x[i]); i++; j++ }
      else if (j < y.length && (i >= x.length || lcs[i * w + j + 1] >= lcs[(i + 1) * w + j])) { edit('', y[j]); j++ }
      else { edit(x[i], ''); i++ }
    }
  }
  same(a.slice(endA).join(''))

  // Two replacements a space apart read as one: "the quick" to "a slow".
  const out: Segment[] = []
  for (const s of segs) {
    const gap = out[out.length - 1], prev = out[out.length - 2]
    if ('removed' in s && s.removed && s.added && gap && 'same' in gap && !gap.same.trim() &&
        prev && 'removed' in prev && prev.removed && prev.added) {
      out.pop()
      prev.removed += gap.same + s.removed
      prev.added += gap.same + s.added
    } else {
      out.push(s)
    }
  }
  const solid = (t: string) => t.replace(/\s+/g, '').length
  const common = out.reduce((n, s) => n + ('same' in s ? solid(s.same) : 0), 0)
  const total = Math.max(solid(before), solid(after))
  if (total > SHORT && common / total < KEEP_SHARE) return [{ removed: before, added: after }]
  return out
}

// Whether the versions have nothing but spaces in common.
export function isRewrite(segs: Segment[]): boolean {
  return segs.every(s => !('same' in s) || !s.same.trim())
}

// Where a line's words changed: ranges of added text, and removed text with the
// position it was at, both in the later line.
export interface WordMarks {
  added: [number, number][]
  removed: { at: number; text: string }[]
}

export function wordMarks(segs: Segment[]): WordMarks {
  const out: WordMarks = { added: [], removed: [] }
  let at = 0
  for (const s of segs) {
    if ('same' in s) { at += s.same.length; continue }
    if (s.removed) out.removed.push({ at, text: s.removed })
    if (s.added) { out.added.push([at, at + s.added.length]); at += s.added.length }
  }
  return out
}

// How alike two texts are, from 0 (a rewrite) to 1 (the same).
export function similarity(before: string, after: string): number {
  if (before === after) return 1
  const segs = diffWords(before, after)
  if (isRewrite(segs)) return 0
  const solid = (t: string) => t.replace(/\s+/g, '').length
  const common = segs.reduce((n, s) => n + ('same' in s ? solid(s.same) : 0), 0)
  return common / Math.max(solid(before), solid(after), 1)
}

// A step through a run of changes: an earlier item paired with a later one,
// or either on its own.
export interface Step { a?: number; b?: number }

// Past this many pairs to weigh, items are paired in order instead.
const ALIGN_LIMIT = 400

// Pairs the items of a run of changes, in order, each with the one most like
// it (sim 0: not alike). A removed item comes before one added in its place.
export function align(aLen: number, bLen: number, sim: (i: number, j: number) => number): Step[] {
  const steps: Step[] = []
  if (aLen * bLen > ALIGN_LIMIT) {
    for (let k = 0; k < Math.max(aLen, bLen); k++) steps.push({ a: k < aLen ? k : undefined, b: k < bLen ? k : undefined })
    return steps
  }
  const w = bLen + 1
  const best = new Float64Array((aLen + 1) * w)
  const sims = new Float64Array(aLen * bLen)
  for (let i = aLen - 1; i >= 0; i--) {
    for (let j = bLen - 1; j >= 0; j--) {
      const v = sim(i, j)
      sims[i * bLen + j] = v
      let most = Math.max(best[(i + 1) * w + j], best[i * w + j + 1])
      if (v > 0) most = Math.max(most, v + best[(i + 1) * w + j + 1])
      best[i * w + j] = most
    }
  }
  let i = 0, j = 0
  while (i < aLen || j < bLen) {
    const v = i < aLen && j < bLen ? sims[i * bLen + j] : 0
    if (v > 0 && best[i * w + j] === v + best[(i + 1) * w + j + 1]) steps.push({ a: i++, b: j++ })
    else if (i < aLen && (j >= bLen || best[i * w + j] === best[(i + 1) * w + j])) steps.push({ a: i++ })
    else steps.push({ b: j++ })
  }
  return steps
}

// A changed line of the later version: marked whole when it's new or
// rewritten, otherwise by its words.
export interface LineChange extends WordMarks {
  line: number
  whole: boolean
}

export interface LineChanges {
  lines: LineChange[]
  // Lines removed, to show above line `before` (the line count: at the end).
  gone: { before: number; lines: string[] }[]
}

// Runs of changes only blank lines apart, as one: otherwise the blank lines
// between paragraphs split an edited paragraph from its old self.
function joinAcrossBlankLines(hunks: Hunk[], before: string[]): Hunk[] {
  const out: Hunk[] = []
  for (const h of hunks) {
    const last = out[out.length - 1]
    if (last && before.slice(last.a + last.aLen, h.a).every(l => !l.trim())) {
      out[out.length - 1] = { a: last.a, aLen: h.a + h.aLen - last.a, b: last.b, bLen: h.b + h.bLen - last.b }
    } else {
      out.push({ ...h })
    }
  }
  return out
}

export function lineChanges(before: string[], after: string[], changes: Changes): LineChanges {
  const out: LineChanges = { lines: [], gone: [] }
  const whole = (line: number) => out.lines.push({ line, whole: true, added: [], removed: [] })
  if (!changes.hunks.length) {
    for (const line of [...changes.marked].sort((p, q) => p - q)) whole(line)
    return out
  }
  for (const h of joinAcrossBlankLines(changes.hunks, before)) {
    const was = before.slice(h.a, h.a + h.aLen), now = after.slice(h.b, h.b + h.bLen)
    let pending: string[] = []
    const flush = (at: number) => {
      if (pending.length) out.gone.push({ before: at, lines: pending })
      pending = []
    }
    for (const step of align(was.length, now.length, (i, j) => similarity(was[i], now[j]))) {
      if (step.b === undefined) { pending.push(was[step.a!]); continue }
      const line = h.b + step.b
      if (step.a !== undefined && was[step.a] === now[step.b]) { flush(line); continue }
      const segs = step.a === undefined ? null : diffWords(was[step.a], now[step.b])
      if (segs && !isRewrite(segs)) {
        flush(line)
        out.lines.push({ line, whole: false, ...wordMarks(segs) })
        continue
      }
      if (step.a !== undefined) pending.push(was[step.a])
      flush(line)
      whole(line)
    }
    flush(h.b + h.bLen)
  }
  return out
}
