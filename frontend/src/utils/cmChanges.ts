// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Marks changes in the file viewer's editor: new or rewritten lines whole, other
// lines by their words, and, when asked, what was removed.
import { StateEffect, StateField, type Range, type Text } from '@codemirror/state'
import { Decoration, EditorView, WidgetType, type DecorationSet } from '@codemirror/view'
import type { LineChanges } from './linediff'

function note(text: string): HTMLElement {
  const span = document.createElement('span')
  span.className = 'sr-only'
  span.textContent = text
  return span
}

class Note extends WidgetType {
  constructor(readonly text: string) { super() }
  toDOM() { return note(this.text) }
  eq(other: Note) { return other.text === this.text }
}

class GoneText extends WidgetType {
  constructor(readonly text: string) { super() }
  toDOM() {
    const del = document.createElement('del')
    del.className = 'cm-gone-text'
    del.append(note('Deleted: '), this.text)
    return del
  }
  eq(other: GoneText) { return other.text === this.text }
}

class GoneLines extends WidgetType {
  constructor(readonly lines: string[]) { super() }
  toDOM() {
    const box = document.createElement('div')
    box.className = 'cm-gone-lines'
    for (const text of this.lines) {
      const line = document.createElement('del')
      line.className = 'cm-gone-line'
      line.append(note('Deleted: '), text)
      box.append(line)
    }
    return box
  }
  eq(other: GoneLines) { return other.lines.join('\n') === this.lines.join('\n') }
}

const wholeLine = Decoration.line({ class: 'cm-changed-line' })
const wordsLine = Decoration.line({ class: 'cm-changed-words' })
const words = Decoration.mark({ class: 'cm-changed-text' })
const wholeNote = Decoration.widget({ widget: new Note('Added or changed: '), side: -1 })
const wordsNote = Decoration.widget({ widget: new Note('Changed: '), side: -1 })

// Sets the changes to mark, and whether to show what was removed.
export const setChanges = StateEffect.define<{ changes: LineChanges | null; deletions: boolean }>()

export const changeMarks = StateField.define<DecorationSet>({
  create: () => Decoration.none,
  update(marks, tr) {
    marks = marks.map(tr.changes)
    for (const e of tr.effects) {
      if (e.is(setChanges)) marks = build(tr.state.doc, e.value.changes, e.value.deletions)
    }
    return marks
  },
  provide: (f) => EditorView.decorations.from(f),
})

function build(doc: Text, changes: LineChanges | null, deletions: boolean): DecorationSet {
  if (!changes) return Decoration.none
  const out: Range<Decoration>[] = []
  for (const c of changes.lines) {
    if (c.line >= doc.lines) continue
    const line = doc.line(c.line + 1)
    out.push((c.whole ? wholeLine : wordsLine).range(line.from), (c.whole ? wholeNote : wordsNote).range(line.from))
    if (c.whole) continue
    for (const [s, e] of c.added) {
      if (s < e && e <= line.length) out.push(words.range(line.from + s, line.from + e))
    }
    if (!deletions) continue
    for (const r of c.removed) {
      if (r.at <= line.length) out.push(Decoration.widget({ widget: new GoneText(r.text), side: -1 }).range(line.from + r.at))
    }
  }
  if (deletions) {
    for (const g of changes.gone) {
      const end = g.before >= doc.lines
      const at = end ? doc.length : doc.line(g.before + 1).from
      out.push(Decoration.widget({ widget: new GoneLines(g.lines), block: true, side: end ? 1 : -1 }).range(at))
    }
  }
  return Decoration.set(out, true)
}

// Additions are underlined and removals struck through, so neither relies on colour.
export const changeMarksTheme = EditorView.baseTheme({
  '.cm-changed-line': {
    backgroundColor: 'rgba(46, 160, 67, 0.16)',
    boxShadow: 'inset 3px 0 0 #2ea043',
  },
  '.cm-changed-words': { boxShadow: 'inset 3px 0 0 #2ea043' },
  '.cm-changed-text': {
    backgroundColor: 'rgba(46, 160, 67, 0.3)',
    textDecoration: 'underline 2px #2ea043',
    textUnderlineOffset: '3px',
    borderRadius: '2px',
  },
  '.cm-gone-lines': {
    backgroundColor: 'rgba(248, 81, 73, 0.12)',
    boxShadow: 'inset 3px 0 0 #f85149',
  },
  '.cm-gone-line': {
    display: 'block',
    minHeight: '1.4em',
    padding: '0 2px 0 6px',
    whiteSpace: 'pre-wrap',
    wordBreak: 'break-word',
    textDecoration: 'line-through #f85149',
    opacity: '0.8',
  },
  '.cm-gone-text': {
    backgroundColor: 'rgba(248, 81, 73, 0.22)',
    textDecoration: 'line-through #f85149',
    borderRadius: '2px',
    opacity: '0.85',
  },
})
