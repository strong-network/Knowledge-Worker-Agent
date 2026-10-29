// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import type { SearchTerm } from '../api'

// Finds the words a search matched, as the server's index does: words are
// runs of letters and digits, compared without case or accents.
const WORD = /[\p{L}\p{N}\p{Co}]+/gu

export function fold(s: string): string {
  return s.normalize('NFD').replace(/\p{M}/gu, '').toLowerCase()
}

/** The [start, end) offsets of the words in text that match a term. */
export function matchRanges(text: string, terms: SearchTerm[]): Array<[number, number]> {
  const want = terms.map(t => ({ text: fold(t.text), prefix: !!t.prefix })).filter(t => t.text)
  if (!want.length) return []
  const out: Array<[number, number]> = []
  for (const m of text.matchAll(WORD)) {
    const w = fold(m[0])
    if (want.some(t => (t.prefix ? w.startsWith(t.text) : w === t.text))) {
      out.push([m.index!, m.index! + m[0].length])
    }
  }
  return out
}

/** text cut into runs, each marked if it's a match. */
export function highlightSegments(text: string, terms: SearchTerm[]): Array<{ text: string; hit: boolean }> {
  const out: Array<{ text: string; hit: boolean }> = []
  let at = 0
  for (const [start, end] of matchRanges(text, terms)) {
    if (start > at) out.push({ text: text.slice(at, start), hit: false })
    out.push({ text: text.slice(start, end), hit: true })
    at = end
  }
  if (at < text.length) out.push({ text: text.slice(at), hit: false })
  return out
}

const HIGHLIGHT = 'kwa-search'

/**
 * Highlights the matches inside roots without changing their DOM, so content
 * Vue renders is left alone. Returns how many it found. Browsers without the
 * CSS Custom Highlight API show none.
 */
export function highlightIn(roots: Iterable<Element>, terms: SearchTerm[]): number {
  clearHighlights()
  const reg = (globalThis as any).CSS?.highlights
  const Highlight = (globalThis as any).Highlight
  if (!reg || !Highlight) return 0
  const ranges: Range[] = []
  for (const root of roots) {
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      for (const [start, end] of matchRanges(n.nodeValue || '', terms)) {
        const r = document.createRange()
        r.setStart(n, start)
        r.setEnd(n, end)
        ranges.push(r)
      }
    }
  }
  if (ranges.length) reg.set(HIGHLIGHT, new Highlight(...ranges))
  return ranges.length
}

export function clearHighlights() {
  ;(globalThis as any).CSS?.highlights?.delete(HIGHLIGHT)
}
