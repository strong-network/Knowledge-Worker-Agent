// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Diagrams, pictures and file links in the agent's replies, and diagrams in a
// Markdown file's preview.
//
// A ```mermaid block is drawn as a diagram and a ```svg block as an image,
// each with Copy source and Download. Images resolve against the chat's
// folder; one outside it isn't shown, and one from the web shows only when
// the user asks, since loading it tells that server the chat was read.
//
// A link to a file in the chat's folder opens it beside the chat. So does
// inline code naming one, such as `report.md`, once a listing of its folder
// shows the file is there.
//
// Drawing happens in two steps. renderReply rewrites the reply's Markdown
// before it is sanitized, while its images can't load yet. hydrateReply then
// draws the diagrams into the page. Each one is shown as an <img> of its SVG,
// so nothing in it can run a script, fetch a file or restyle the page.

import DOMPurify from 'dompurify'
import type { FileEntry } from '../api'
import { renderMarkdown } from '../composables/useMarkdown'
import { triggerDownload } from './download'

// How a reply reaches the chat's files: the owner by absolute path under the
// chat's folder, a guest by path inside it.
export interface ChatFiles {
  // The chat's folder as the agent knows it, or '' when paths are relative
  // to it (a guest's).
  root: string
  rawUrl(path: string): string
  downloadUrl(path: string): string
  // Opens a file beside the chat. Without it, file names in a reply aren't links.
  open?(path: string): void
  // A folder's entries, to tell whether code in a reply names a file.
  browse?(path: string): Promise<FileEntry[]>
}

type Kind = 'mermaid' | 'svg'
type Theme = 'light' | 'dark'

const KINDS: Record<string, Kind> = { 'language-mermaid': 'mermaid', 'language-svg': 'svg' }

// Rendered Markdown of a reply, with its visuals ready to draw. While the
// reply streams, a diagram whose block hasn't closed yet shows its source.
export function renderReply(text: string, files: ChatFiles | undefined, streaming: boolean): string {
  const open = streaming && fenceOpen(text)
  return renderMarkdown(text, (root) => {
    boxVisuals(root, open)
    root.querySelectorAll('img').forEach((img) => placeImage(img, files))
    if (files?.open) linkFiles(root, files)
  })
}

// Draws the ```mermaid and ```svg blocks of Markdown already in the page, such
// as a file's preview. Call it again after the Markdown is replaced.
export function drawVisuals(root: HTMLElement) {
  boxVisuals(root, false)
  hydrateReply(root)
}

// Puts a box for its visual in place of each ```mermaid or ```svg block; with
// openLast, the last one's source shows, as a block that hasn't closed yet.
// Removed blocks in a preview's change marks stay as they were.
function boxVisuals(root: ParentNode, openLast: boolean) {
  const blocks = root.querySelectorAll('pre')
  blocks.forEach((pre, i) => {
    const code = pre.querySelector(':scope > code')
    const kind = code && Array.from(code.classList).map((c) => KINDS[c]).find(Boolean)
    if (!code || !kind || pre.closest('.md-deleted')) return
    pre.replaceWith(visualBox(kind, code.textContent ?? '', openLast && i === blocks.length - 1))
  })
}

// Whether the text ends inside a fenced code block.
function fenceOpen(text: string): boolean {
  return (text.match(/^[ \t]*(`{3,}|~{3,})/gm)?.length ?? 0) % 2 === 1
}

function el<K extends keyof HTMLElementTagNameMap>(tag: K, className: string, text = ''): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag)
  e.className = className
  if (text) e.textContent = text
  return e
}

function actionButton(action: string, label: string): HTMLButtonElement {
  const b = el('button', 'kwa-visual-btn', label)
  b.type = 'button'
  b.dataset.action = action
  return b
}

function visualBox(kind: Kind, source: string, open: boolean): HTMLElement {
  const box = el('div', 'kwa-visual')
  box.dataset.kind = kind
  box.dataset.state = open ? 'open' : 'new'
  const pre = el('pre', 'kwa-visual-source')
  pre.append(el('code', '', source))
  const bar = el('div', 'kwa-visual-bar')
  bar.append(actionButton('copy', 'Copy source'), actionButton('download', 'Download'))
  box.append(el('div', 'kwa-visual-view'), el('div', 'kwa-visual-note', open ? '' : 'Drawing…'), pre, bar)
  return box
}

function placeImage(img: HTMLImageElement, files: ChatFiles | undefined) {
  const src = (img.getAttribute('src') ?? '').trim()
  const alt = img.getAttribute('alt') ?? ''
  if (/^data:image\//i.test(src)) {
    img.removeAttribute('srcset')
    return
  }
  if (/^(https?:)?\/\//i.test(src)) {
    let host = ''
    try {
      host = new URL(src, location.href).host
    } catch {
      img.replaceWith(el('span', 'kwa-image-note', `Image not shown: ${alt || src}`))
      return
    }
    const b = el('button', 'kwa-web-image', `Show image from ${host}`)
    b.type = 'button'
    b.title = alt ? `${alt}\n${src}` : src
    b.dataset.src = src
    b.dataset.alt = alt
    img.replaceWith(b)
    return
  }
  const path = files ? folderPath(src, files.root) : null
  if (path === null) {
    img.replaceWith(el('span', 'kwa-image-note', `Image not shown: ${src || alt} isn't in this chat's folder.`))
    return
  }
  const shown = document.createElement('img')
  shown.src = files!.rawUrl(path)
  shown.alt = alt
  if (img.title) shown.title = img.title
  const box = el('span', 'kwa-file-image')
  box.dataset.path = src
  box.append(shown)
  // A link around the image already leads somewhere; a button inside it can't.
  if (!img.closest('a')) {
    const bar = el('span', 'kwa-visual-bar')
    const dl = actionButton('download-file', 'Download')
    dl.dataset.href = files!.downloadUrl(path)
    bar.append(dl)
    box.append(bar)
  }
  img.replaceWith(box)
}

// The path to ask the file API for, or null when src isn't a file in the
// chat's folder. root '' means the API takes paths relative to the folder,
// so an absolute path can't be placed.
export function folderPath(src: string, root: string): string | null {
  let p = src.replace(/[?#].*$/, '')
  if (/^file:\/\//i.test(p)) p = p.slice('file://'.length)
  else if (/^[a-z][a-z0-9+.-]*:/i.test(p)) return null
  try {
    p = decodeURIComponent(p)
  } catch {
    return null
  }
  const base = root.replace(/\/+$/, '')
  if (p.startsWith('/')) {
    if (!base || !p.startsWith(base + '/')) return null
    p = p.slice(base.length + 1)
  }
  const parts: string[] = []
  for (const part of p.split('/')) {
    if (part === '' || part === '.') continue
    if (part === '..') {
      if (!parts.length) return null
      parts.pop()
    } else parts.push(part)
  }
  if (!parts.length) return null
  return base ? `${base}/${parts.join('/')}` : parts.join('/')
}

// Whether inline code is worth looking up as a file: a name with an
// extension, or a path.
function namesFile(text: string): boolean {
  if (!text || text.length > 300 || /[\n<>"|?*#\\]/.test(text) || text.endsWith('/')) return false
  return /\.[a-z0-9]{1,12}$/i.test(text) || text.includes('/')
}

function fileLink(path: string, root: string): HTMLButtonElement {
  const b = el('button', 'kwa-file-link')
  b.type = 'button'
  b.dataset.path = path
  const base = root.replace(/\/+$/, '')
  b.title = `Open ${base && path.startsWith(base + '/') ? path.slice(base.length + 1) : path}`
  return b
}

// A link to a file in the chat's folder becomes a button that opens it. So
// does code naming a file its folder's listing holds; code whose folder
// hasn't been listed lately is marked for checkMentions.
function linkFiles(root: DocumentFragment, files: ChatFiles) {
  root.querySelectorAll('a[href]').forEach((a) => {
    const path = folderPath(a.getAttribute('href') ?? '', files.root)
    if (path === null) return
    const b = fileLink(path, files.root)
    b.append(...a.childNodes)
    a.replaceWith(b)
  })
  root.querySelectorAll<HTMLElement>('code').forEach((code) => {
    if (code.closest('pre, a, button')) return
    const text = code.textContent?.trim() ?? ''
    const path = namesFile(text) ? folderPath(text, files.root) : null
    if (path === null) return
    const listed = isListedFile(path)
    if (listed) wrapInLink(code, path, files.root)
    else if (listed === undefined) code.dataset.file = path
  })
}

function wrapInLink(code: HTMLElement, path: string, root: string) {
  const b = fileLink(path, root)
  code.replaceWith(b)
  b.append(code)
}

// The names of the files in each folder looked up, and when.
const listings = new Map<string, { files: Set<string>; at: number }>()
// Folders being listed.
const looking = new Map<string, Promise<void>>()
// How long a listing answers for a name it doesn't hold.
const LISTING_TTL = 10_000

function splitPath(path: string): [dir: string, name: string] {
  const i = path.lastIndexOf('/')
  return i < 0 ? ['.', path] : [path.slice(0, i) || '/', path.slice(i + 1)]
}

const listedLately = (dir: string) => Date.now() - (listings.get(dir)?.at ?? 0) < LISTING_TTL

// Whether path is a file as far as its folder's listing says: undefined when
// it isn't there and the folder hasn't been listed lately.
function isListedFile(path: string): boolean | undefined {
  const [dir, name] = splitPath(path)
  if (listings.get(dir)?.files.has(name)) return true
  return listedLately(dir) ? false : undefined
}

// A turn may have written files since: names not found are looked up again.
export function forgetMissingFiles() {
  for (const l of listings.values()) l.at = 0
}

// Lists the folders of the code linkFiles marked, and makes the code that
// names a file a link.
function checkMentions(root: HTMLElement, files: ChatFiles) {
  const dirs = new Set<string>()
  root.querySelectorAll<HTMLElement>('code[data-file]').forEach((code) => dirs.add(splitPath(code.dataset.file!)[0]))
  for (const dir of dirs) {
    let done = looking.get(dir)
    if (!done && !listedLately(dir)) {
      done = files.browse!(dir)
        .catch(() => [] as FileEntry[])
        .then((list) => {
          listings.set(dir, { files: new Set(list.filter((e) => !e.is_dir).map((e) => e.name)), at: Date.now() })
        })
        .finally(() => looking.delete(dir))
      looking.set(dir, done)
    }
    void (done ?? Promise.resolve()).then(() => {
      root.querySelectorAll<HTMLElement>('code[data-file]').forEach((code) => {
        const path = code.dataset.file!
        if (splitPath(path)[0] !== dir) return
        delete code.dataset.file
        if (isListedFile(path)) wrapInLink(code, path, files.root)
      })
    })
  }
}

// Web images the user has asked to see, so they stay shown when the reply
// renders again.
const shownWeb = new Set<string>()

function showWebImage(b: HTMLElement) {
  const src = b.dataset.src ?? ''
  if (!/^(https?:)?\/\//i.test(src)) return
  shownWeb.add(src)
  const img = document.createElement('img')
  img.referrerPolicy = 'no-referrer'
  img.alt = b.dataset.alt ?? ''
  img.src = src
  b.replaceWith(img)
}

// Draws what renderReply prepared. Safe to call after every update: it
// leaves alone what it has already done.
export function hydrateReply(root: HTMLElement, files?: ChatFiles) {
  root.querySelectorAll<HTMLElement>('.kwa-visual[data-state="new"]').forEach(draw)
  root.querySelectorAll<HTMLElement>('.kwa-web-image').forEach((b) => {
    if (shownWeb.has(b.dataset.src ?? '')) showWebImage(b)
  })
  root.querySelectorAll<HTMLImageElement>('.kwa-file-image > img:not([data-watched])').forEach((img) => {
    img.dataset.watched = '1'
    const missing = () => {
      const box = img.parentElement
      if (!box) return
      box.replaceWith(el('span', 'kwa-image-note', `Image not available: ${box.dataset.path ?? ''}`))
    }
    if (img.complete && img.naturalWidth === 0) missing()
    else img.addEventListener('error', missing, { once: true })
  })
  if (files?.browse) checkMentions(root, files)
}

// Copy source, Download, Show image and file links, handled for the whole reply.
export function onReplyClick(e: MouseEvent, files?: ChatFiles) {
  const target = e.target as HTMLElement | null
  const file = target?.closest<HTMLElement>('.kwa-file-link')
  if (file) {
    files?.open?.(file.dataset.path ?? '')
    return
  }
  const web = target?.closest<HTMLElement>('.kwa-web-image')
  if (web) {
    showWebImage(web)
    return
  }
  const btn = target?.closest<HTMLButtonElement>('.kwa-visual-btn')
  if (!btn) return
  if (btn.dataset.action === 'download-file') {
    triggerDownload(btn.dataset.href ?? '')
    return
  }
  const box = btn.closest<HTMLElement>('.kwa-visual')
  if (!box) return
  const kind = box.dataset.kind as Kind
  const source = sourceOf(box)
  if (btn.dataset.action === 'copy') {
    navigator.clipboard.writeText(source).then(
      () => flash(btn, 'Copied'),
      () => flash(btn, "Couldn't copy"),
    )
  } else if (btn.dataset.action === 'download') {
    // Always the light version: a download usually ends up on a white page.
    drawn(kind, source, 'light').then(
      (svg) => {
        const url = URL.createObjectURL(new Blob([svg], { type: 'image/svg+xml' }))
        triggerDownload(url, kind === 'mermaid' ? 'diagram.svg' : 'image.svg')
        setTimeout(() => URL.revokeObjectURL(url), 10_000)
      },
      () => flash(btn, "Couldn't download"),
    )
  }
}

function flash(btn: HTMLButtonElement, text: string) {
  const label = btn.dataset.label ?? btn.textContent ?? ''
  btn.dataset.label = label
  btn.textContent = text
  setTimeout(() => {
    btn.textContent = label
  }, 1500)
}

function sourceOf(box: HTMLElement): string {
  return box.querySelector('.kwa-visual-source code')?.textContent ?? ''
}

function theme(): Theme {
  return document.documentElement.getAttribute('data-theme') === 'dark' ? 'dark' : 'light'
}

let ticket = 0

function draw(box: HTMLElement) {
  const kind = box.dataset.kind as Kind
  const mine = String(++ticket)
  box.dataset.ticket = mine
  // On a theme change the old drawing stays until the new one is ready.
  if (box.dataset.state !== 'done') box.dataset.state = 'pending'
  if (kind === 'mermaid') watchTheme()
  drawn(kind, sourceOf(box), theme()).then(
    (svg) => {
      if (box.dataset.ticket !== mine) return
      const img = document.createElement('img')
      img.alt = kind === 'mermaid' ? 'Diagram' : 'Image'
      const size = svgSize(svg)
      if (size) {
        img.width = size.width
        img.height = size.height
      }
      img.src = 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(svg)
      box.querySelector('.kwa-visual-view')?.replaceChildren(img)
      box.dataset.state = 'done'
    },
    (err: unknown) => {
      if (box.dataset.ticket !== mine) return
      const first = (err instanceof Error ? err.message : String(err)).split('\n')[0].slice(0, 200)
      const what = kind === 'mermaid' ? "Couldn't draw this diagram" : "Couldn't show this SVG"
      const note = box.querySelector('.kwa-visual-note')
      if (note) note.textContent = first ? `${what}: ${first}` : `${what}.`
      box.dataset.state = 'failed'
    },
  )
}

let watching = false

// Diagrams follow the app's theme.
function watchTheme() {
  if (watching) return
  watching = true
  new MutationObserver(() => {
    document.querySelectorAll<HTMLElement>('.kwa-visual[data-kind="mermaid"][data-state="done"]').forEach(draw)
  }).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
}

const cache = new Map<string, Promise<string>>()

// The visual as standalone SVG markup. Replies render again as they stream
// and when they scroll back into a chat, so each drawing is kept.
function drawn(kind: Kind, source: string, t: Theme): Promise<string> {
  const key = `${kind}\n${kind === 'mermaid' ? t : ''}\n${source}`
  let p = cache.get(key)
  if (!p) {
    p = kind === 'mermaid' ? drawMermaid(source, t) : Promise.resolve().then(() => cleanSvg(source))
    cache.set(key, p)
    p.catch(() => cache.delete(key))
  }
  return p
}

type Mermaid = (typeof import('mermaid'))['default']
let mermaidLoad: Promise<Mermaid> | null = null
let queue: Promise<unknown> = Promise.resolve()
let seq = 0

async function drawMermaid(source: string, t: Theme): Promise<string> {
  // Loaded on first use: it is large, and most chats never draw a diagram.
  mermaidLoad ??= import('mermaid').then(
    (m) => m.default,
    (err) => {
      mermaidLoad = null
      throw err
    },
  )
  const mermaid = await mermaidLoad
  // mermaid keeps its settings globally, so drawings take turns.
  const run = queue.then(async () => {
    mermaid.initialize({
      startOnLoad: false,
      securityLevel: 'strict',
      suppressErrorRendering: true,
      theme: t === 'dark' ? 'dark' : 'default',
      // mermaid's default is ELK, which the build leaves out (noElk.ts).
      layout: 'dagre',
      // Labels as SVG text, not HTML, so a downloaded diagram's labels show
      // in other programs too.
      htmlLabels: false,
    })
    const { svg } = await mermaid.render(`kwa-mermaid-${++seq}`, source)
    return standalone(svg)
  })
  queue = run.catch(() => {})
  return run
}

const svgPurify = DOMPurify(window)
// Only references within the drawing and embedded images: a downloaded copy
// opened from disk must not reach out either.
svgPurify.addHook('afterSanitizeAttributes', (node) => {
  for (const name of ['href', 'xlink:href']) {
    const v = node.getAttribute(name)?.trim()
    if (v !== undefined && !v.startsWith('#') && !/^data:image\/(png|jpe?g|gif|webp);/i.test(v)) {
      node.removeAttribute(name)
    }
  }
})

function cleanSvg(source: string): string {
  const frag = svgPurify.sanitize(source, {
    USE_PROFILES: { svg: true, svgFilters: true },
    RETURN_DOM_FRAGMENT: true,
  })
  const svg = frag.querySelector('svg')
  if (!svg) throw new Error('there is no <svg> element')
  return standalone(svg)
}

// SVG as a file: namespaced, and sized from its viewBox when it gives no
// size of its own, since an image has no container to fill.
function standalone(svg: string | Element): string {
  let root: Element | null = null
  if (typeof svg === 'string') {
    const tpl = document.createElement('template')
    tpl.innerHTML = svg
    root = tpl.content.querySelector('svg')
  } else root = svg
  if (!root) throw new Error('there is no <svg> element')
  const box = (root.getAttribute('viewBox') ?? '').split(/[\s,]+/).map(Number)
  const plain = /^\s*[\d.]+(px)?\s*$/
  if (box.length === 4 && box[2] > 0 && box[3] > 0) {
    if (!plain.test(root.getAttribute('width') ?? '')) root.setAttribute('width', String(box[2]))
    if (!plain.test(root.getAttribute('height') ?? '')) root.setAttribute('height', String(box[3]))
  }
  ;(root as SVGElement).style?.removeProperty('max-width')
  return new XMLSerializer().serializeToString(root)
}

function svgSize(svg: string): { width: number; height: number } | null {
  const m = svg.match(/^<svg\b[^>]*>/)
  if (!m) return null
  const w = m[0].match(/\swidth="([\d.]+)(px)?"/)
  const h = m[0].match(/\sheight="([\d.]+)(px)?"/)
  if (!w || !h) return null
  return { width: Math.round(Number(w[1])), height: Math.round(Number(h[1])) }
}
