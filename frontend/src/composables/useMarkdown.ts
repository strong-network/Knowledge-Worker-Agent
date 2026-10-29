// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import { marked, type Tokens, type TokensList } from 'marked'
import DOMPurify from 'dompurify'
import { align, diffItems, diffWords, isRewrite, similarity, wordMarks } from '../utils/linediff'
import hljs from 'highlight.js/lib/core'
import javascript from 'highlight.js/lib/languages/javascript'
import typescript from 'highlight.js/lib/languages/typescript'
import python from 'highlight.js/lib/languages/python'
import bash from 'highlight.js/lib/languages/bash'
import json from 'highlight.js/lib/languages/json'
import yaml from 'highlight.js/lib/languages/yaml'
import css from 'highlight.js/lib/languages/css'
import xml from 'highlight.js/lib/languages/xml'
import go from 'highlight.js/lib/languages/go'
import sql from 'highlight.js/lib/languages/sql'
import markdown from 'highlight.js/lib/languages/markdown'
import dockerfile from 'highlight.js/lib/languages/dockerfile'
import diff from 'highlight.js/lib/languages/diff'

// Register languages
hljs.registerLanguage('javascript', javascript)
hljs.registerLanguage('js', javascript)
hljs.registerLanguage('typescript', typescript)
hljs.registerLanguage('ts', typescript)
hljs.registerLanguage('python', python)
hljs.registerLanguage('bash', bash)
hljs.registerLanguage('sh', bash)
hljs.registerLanguage('shell', bash)
hljs.registerLanguage('json', json)
hljs.registerLanguage('yaml', yaml)
hljs.registerLanguage('yml', yaml)
hljs.registerLanguage('css', css)
hljs.registerLanguage('html', xml)
hljs.registerLanguage('xml', xml)
hljs.registerLanguage('go', go)
hljs.registerLanguage('golang', go)
hljs.registerLanguage('sql', sql)
hljs.registerLanguage('markdown', markdown)
hljs.registerLanguage('md', markdown)
hljs.registerLanguage('dockerfile', dockerfile)
hljs.registerLanguage('diff', diff)

// Configure marked
marked.setOptions({
  breaks: true,
  gfm: true,
})

// Force every rendered link to open in a new tab. Runs after DOMPurify has
// sanitized attributes so it also applies to links whose markup we didn't
// author. rel="noopener noreferrer" prevents the opened tab from accessing
// window.opener and avoids referrer leakage.
DOMPurify.addHook('afterSanitizeAttributes', (node) => {
  if (node.tagName === 'A') {
    node.setAttribute('target', '_blank')
    node.setAttribute('rel', 'noopener noreferrer')
  }
})

export function renderMarkdown(text: string): string {
  if (!text) return ''
  const raw = marked.parse(text) as string
  // A form in an answer, or in text it quotes, could send what the user types to another site.
  return DOMPurify.sanitize(raw, { FORBID_TAGS: ['form'] })
}

const sanitize = (html: string) => DOMPurify.sanitize(html, { FORBID_TAGS: ['form'] })

// What a Markdown file's changes are compared and marked by: a block, a list
// item or a table row.
interface Unit {
  kind: 'block' | 'li' | 'tr'
  // The source it's compared by.
  key: string
  // What it renders to, and that as HTML.
  nodes: Node[]
  html: string
  // Whether its words can be marked: not in code, raw HTML or a rule.
  words: boolean
  // The box around a block once it's marked.
  box?: HTMLElement
}

const WORDY = new Set(['paragraph', 'heading', 'blockquote', 'text'])

function layout(text: string): { root: HTMLElement; units: Unit[] } {
  const tokens = marked.lexer(text.replace(/\r\n?/g, '\n'))
  const root = document.createElement('div')
  const units: Unit[] = []
  for (const token of tokens) {
    const tpl = document.createElement('template')
    const html = marked.parser(Object.assign([token], { links: tokens.links }) as TokensList)
    tpl.innerHTML = html
    const nodes = Array.from(tpl.content.childNodes)
    root.append(...nodes)
    const el = nodes.find((n): n is HTMLElement => n.nodeType === Node.ELEMENT_NODE)
    if (token.type === 'space' || (!el && !nodes.some(n => n.textContent?.trim()))) continue
    if (token.type === 'list' && el) {
      const items = Array.from(el.children).filter(c => c.tagName === 'LI')
      ;(token as Tokens.List).items.forEach((item, i) => {
        // Without its number, so an item renumbered by one removed above it is the same.
        const key = item.raw.trimEnd().replace(/^\s*(?:[*+-]|\d+[.)])\s*/, '')
        if (items[i]) units.push({ kind: 'li', key, nodes: [items[i]], html: items[i].outerHTML, words: true })
      })
      continue
    }
    if (token.type === 'table' && el) {
      // The header row, the separator row, then a line per body row.
      const lines = token.raw.replace(/\n+$/, '').split('\n')
      el.querySelectorAll('tr').forEach((row, i) => {
        units.push({ kind: 'tr', key: lines[i ? i + 1 : 0] ?? '', nodes: [row], html: row.outerHTML, words: true })
      })
      continue
    }
    units.push({ kind: 'block', key: token.raw.replace(/\n+$/, ''), nodes, html, words: WORDY.has(token.type) })
  }
  return { root, units }
}

function note(text: string): HTMLElement {
  const span = document.createElement('span')
  span.className = 'sr-only md-note'
  span.textContent = text
  return span
}

// The text of nodes as compared, a line break for each <br>, and where each
// piece of it is. Removed text and screen reader notes put in aren't part of it.
function pieces(nodes: Node[]): { node: Node; start: number; len: number }[] {
  const out: { node: Node; start: number; len: number }[] = []
  let at = 0
  const visit = (n: Node) => {
    if (n.nodeType === Node.TEXT_NODE) {
      const len = (n as Text).data.length
      out.push({ node: n, start: at, len })
      at += len
    } else if (n.nodeName === 'BR') {
      out.push({ node: n, start: at, len: 1 })
      at += 1
    } else if (n.nodeType === Node.ELEMENT_NODE && !(n as Element).matches('.md-note, .md-del')) {
      n.childNodes.forEach(visit)
    }
  }
  nodes.forEach(visit)
  return out
}

const textOf = (nodes: Node[]) =>
  pieces(nodes).map(p => (p.node.nodeType === Node.TEXT_NODE ? (p.node as Text).data : '\n')).join('')

// A row's cells are compared one by one; anything else as a whole.
function parts(u: Unit): Node[][] {
  return u.kind === 'tr' ? Array.from((u.nodes[0] as Element).children).map(c => [c]) : [u.nodes]
}

function insertDeleted(nodes: Node[], at: number, text: string) {
  const ps = pieces(nodes)
  const p = ps.find(x => at < x.start + x.len) ?? ps[ps.length - 1]
  if (!p) return
  const del = document.createElement('del')
  del.className = 'md-del'
  del.append(note('Deleted: '), text)
  if (at >= p.start + p.len) p.node.parentNode?.insertBefore(del, p.node.nextSibling)
  else if (at === p.start) p.node.parentNode?.insertBefore(del, p.node)
  else (p.node as Text).splitText(at - p.start).before(del)
}

function wrapAdded(nodes: Node[], from: number, to: number) {
  let first = true
  for (const p of pieces(nodes)) {
    if (p.start + p.len <= from || p.start >= to || p.node.nodeType !== Node.TEXT_NODE) continue
    let node = p.node as Text
    const s = Math.max(from, p.start) - p.start, e = Math.min(to, p.start + p.len) - p.start
    if (!node.data.slice(s, e).trim()) continue
    if (e < node.data.length) node.splitText(e)
    if (s > 0) node = node.splitText(s)
    const ins = document.createElement('ins')
    ins.className = 'md-ins'
    node.replaceWith(ins)
    ins.append(node)
    if (first) ins.prepend(note('Added: '))
    first = false
  }
}

// md-changed marks a unit whole; md-touched puts a bar beside one whose words are marked.
function flag(u: Unit, cls: string, text: string) {
  if (u.kind !== 'block') {
    const el = u.nodes[0] as HTMLElement
    el.classList.add(cls)
    ;((u.kind === 'tr' ? el.firstElementChild : el) ?? el).prepend(note(text))
    return
  }
  const box = document.createElement('div')
  box.className = cls
  u.nodes[0].parentNode?.insertBefore(box, u.nodes[0])
  box.append(note(text), ...u.nodes)
  u.box = box
}

// False when the words can't be marked: code, a rewrite, or only formatting changed.
function markWords(u: Unit, was: Unit, deletions: boolean): boolean {
  if (u.kind !== was.kind || !u.words || !was.words) return false
  const now = parts(u), before = parts(was)
  if (now.length !== before.length) return false
  const segs = now.map((p, i) => diffWords(textOf(before[i]), textOf(p)))
  if (segs.every(s => s.every(x => 'same' in x))) return false
  // A row is compared cell by cell, so a rewritten cell marks only that cell.
  if (u.kind === 'tr' ? segs.every(isRewrite) : isRewrite(segs[0])) return false
  segs.forEach((s, i) => {
    const { added, removed } = wordMarks(s)
    if (deletions) for (const r of removed) insertDeleted(now[i], r.at, r.text)
    for (const [from, to] of added) wrapAdded(now[i], from, to)
  })
  flag(u, 'md-touched', 'Changed: ')
  return true
}

// The top-level element a unit is in.
function topOf(u: Unit, root: HTMLElement): Node {
  let n: Node = u.box ?? u.nodes[0]
  while (n.parentNode && n.parentNode !== root) n = n.parentNode
  return n
}

// Shows removed units, struck through, before next (or after prev): a list
// item or table row among its own, anything else as a block of its own.
function insertGone(gone: Unit[], next: Unit | undefined, prev: Unit | undefined, root: HTMLElement) {
  let after: Element | null = null
  for (const old of gone) {
    const sibling = next?.kind === old.kind && old.kind !== 'block' ? next : prev?.kind === old.kind && old.kind !== 'block' ? prev : undefined
    if (sibling) {
      const el = document.createElement(old.kind)
      el.className = 'md-deleted'
      el.innerHTML = (old.nodes[0] as Element).innerHTML
      ;((old.kind === 'tr' ? el.firstElementChild : el) ?? el).prepend(note('Deleted: '))
      const at = sibling.nodes[0] as Element
      if (sibling === next) at.before(el)
      else { (after ?? at).after(el); after = el }
      continue
    }
    const box = document.createElement('div')
    box.className = 'md-deleted'
    box.innerHTML = old.kind === 'li' ? `<ul>${old.html}</ul>` : old.kind === 'tr' ? `<table><tbody>${old.html}</tbody></table>` : old.html
    box.prepend(note('Deleted: '))
    if (next) root.insertBefore(box, topOf(next, root))
    else root.append(box)
  }
}

// renderMarkdownChanges marks what changed since before: changed words, or
// whole blocks that are new or rewritten; with deletions, what was removed.
export function renderMarkdownChanges(text: string, before: string | null, deletions = false): string {
  if (!text || before === null || before === text) return renderMarkdown(text)
  const now = layout(text), was = layout(before)
  const diff = diffItems(was.units.map(u => u.key), now.units.map(u => u.key))
  if (!diff.hunks.length) {
    for (const i of diff.marked) flag(now.units[i], 'md-changed', 'Added or changed: ')
    return sanitize(now.root.innerHTML)
  }
  for (const h of diff.hunks) {
    const olds = was.units.slice(h.a, h.a + h.aLen), news = now.units.slice(h.b, h.b + h.bLen)
    const oldText = olds.map(u => textOf(u.nodes)), newText = news.map(u => textOf(u.nodes))
    const sim = (i: number, j: number) => (olds[i].kind === news[j].kind ? similarity(oldText[i], newText[j]) : 0)
    let pending: Unit[] = []
    const flush = (next: Unit | undefined, prev: Unit | undefined) => {
      if (deletions && pending.length) insertGone(pending, next, prev, now.root)
      pending = []
    }
    for (const step of align(olds.length, news.length, sim)) {
      if (step.b === undefined) { pending.push(olds[step.a!]); continue }
      const u = news[step.b], old = step.a === undefined ? undefined : olds[step.a]
      if (!old || !markWords(u, old, deletions)) {
        flag(u, 'md-changed', 'Added or changed: ')
        if (old) pending.push(old)
      }
      flush(u, now.units[h.b + step.b - 1])
    }
    flush(now.units[h.b + h.bLen], now.units[h.b + h.bLen - 1])
  }
  return sanitize(now.root.innerHTML)
}

export function highlightCode(el: HTMLElement) {
  el.querySelectorAll('pre code').forEach((block) => {
    // Removed code shows as it was, without a Copy button.
    if (block.closest('.md-deleted')) return
    const codeEl = block as HTMLElement
    // Try to detect language from class
    const langClass = Array.from(codeEl.classList).find(c => c.startsWith('language-'))
    const lang = langClass?.replace('language-', '') || ''

    if (lang && hljs.getLanguage(lang)) {
      hljs.highlightElement(codeEl)
    } else {
      hljs.highlightElement(codeEl)
    }

    // Wrap in header + code block structure
    const pre = codeEl.parentElement
    if (pre && !pre.querySelector('.pre-header')) {
      const header = document.createElement('div')
      header.className = 'pre-header'
      header.innerHTML = `
        <span class="pre-lang">${lang || 'code'}</span>
        <button class="copy-btn" onclick="navigator.clipboard.writeText(this.closest('pre').querySelector('code').textContent).then(()=>{this.textContent='Copied!';setTimeout(()=>this.textContent='Copy',1500)})">Copy</button>
      `
      pre.insertBefore(header, pre.firstChild)
    }
  })
}
