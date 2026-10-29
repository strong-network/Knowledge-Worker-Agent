<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// A single open document: the reading/editing surface.
//
// Extracted from WorkspaceFilePanel so the same viewer serves both the docked
// Files widget and the pop-out document window — the iframe sandbox
// rules and CSV/markdown handling below are subtle enough that a second copy
// would drift.
//
// The host owns *which* file is open (and any unsaved-changes prompt before
// changing it); this component owns everything about showing and editing it.
import { ref, computed, watch, onBeforeUnmount, nextTick } from 'vue'
import { ownerFileApi, type FileApi, type FileVersion } from '../../api/files'
import { fetchOfficeStatus, installOffice, type OfficeInstallStatus, type PreviewLink } from '../../api'
import { renderMarkdownChanges, highlightCode } from '../../composables/useMarkdown'
import { parseCsv } from '../../utils/csv'
import { triggerDownload } from '../../utils/download'
import { diffItems, lineChanges, splitLines } from '../../utils/linediff'
import { changeMarks, changeMarksTheme, setChanges } from '../../utils/cmChanges'
import ActionMenu, { type MenuEntry } from '../ActionMenu.vue'
import githubLightCss from 'github-markdown-css/github-markdown-light.css?inline'
import githubDarkCss from 'github-markdown-css/github-markdown-dark.css?inline'
import { EditorView, basicSetup } from 'codemirror'
import { EditorState } from '@codemirror/state'
import { oneDark } from '@codemirror/theme-one-dark'
import { javascript } from '@codemirror/lang-javascript'
import { python } from '@codemirror/lang-python'
import { html } from '@codemirror/lang-html'
import { css } from '@codemirror/lang-css'
import { markdown } from '@codemirror/lang-markdown'
import { json } from '@codemirror/lang-json'
import { yaml } from '@codemirror/lang-yaml'

// Inject GitHub markdown CSS once.
;(function injectGithubMarkdownCss() {
  if (typeof document === 'undefined') return
  if (document.getElementById('gh-md-styles')) return
  const scope = (cssText: string, theme: 'light' | 'dark') =>
    cssText.replace(/\.markdown-body/g, `[data-theme="${theme}"] .markdown-body`)
  const style = document.createElement('style')
  style.id = 'gh-md-styles'
  style.textContent = scope(githubLightCss, 'light') + '\n' + scope(githubDarkCss, 'dark')
  document.head.appendChild(style)
})()

// api defaults to the owner's; a guest page passes its own.
const props = defineProps<{ path: string; name: string; api?: FileApi }>()
const files = computed(() => props.api || ownerFileApi)
const emit = defineEmits<{ (e: 'update:dirty', value: boolean): void }>()

const dirty = ref(false)
const viewMode = ref<'code' | 'markdown' | 'image' | 'pdf' | 'html' | 'csv' | 'office' | 'unsupported'>('code')
const imageDataUrl = ref('')
const pdfUrl = ref('')
const htmlUrl = ref('')
const editorContainer = ref<HTMLElement | null>(null)
const markdownPreviewEl = ref<HTMLElement | null>(null)
const editorContent = ref('')
let editorView: EditorView | null = null
let originalContent = ''

watch(dirty, (v) => emit('update:dirty', v))

const ext = (name: string) => name.split('.').pop()?.toLowerCase() || ''
const isMarkdown = computed(() => ['md', 'markdown', 'mdx'].includes(ext(props.name)))
const isHtml = computed(() => ['html', 'htm'].includes(ext(props.name)))
// .tsv comes along for free: the parser detects the delimiter, so tab-separated
// files are the same table with a different separator.
const isCsv = computed(() => ['csv', 'tsv'].includes(ext(props.name)))
// PowerPoint, Word and Excel files show as a PDF the server converts with
// LibreOffice. Only the owner's API offers it; a guest gets the download.
const officeExts = ['pptx', 'ppt', 'docx', 'doc', 'xlsx', 'xls']
const isOffice = (name: string) => !!files.value.officePreviewUrl && officeExts.includes(ext(name))
// These types open rendered and can be flipped to source, so the header button
// is shared rather than duplicated per type.
const hasPreview = computed(() => isMarkdown.value || isHtml.value || isCsv.value)
const isPreviewing = computed(
  () => viewMode.value === 'markdown' || viewMode.value === 'html' || viewMode.value === 'csv',
)

const renderedMarkdown = computed(() =>
  isMarkdown.value ? renderMarkdownChanges(shownText.value, against.value, showDeletions.value) : '',
)

// Parsed from the live buffer rather than from disk, so edits made in the
// source view show up in the table as soon as you switch back — the same
// immediacy markdown has. (The HTML preview can't do this: it is an iframe
// served from disk, which is why that one asks you to save first.)
const csvTable = computed(() =>
  isCsv.value ? parseCsv(shownText.value) : null,
)

// ── Versions ──
// A text file's versions, oldest first, and the one shown: null is the file as
// it is now. Changes are counted and marked against the version before the one
// shown.
const versions = ref<FileVersion[]>([])
const shownV = ref<number | null>(null)
const olderText = ref('')
const prevText = ref<string | null>(null)
const highlight = ref(true)
const announced = ref('')
// Texts of versions other than the latest, which no longer change.
const versionTexts = new Map<number, string>()

const latestV = computed(() => versions.value[versions.value.length - 1]?.v ?? 0)
const viewingV = computed(() => shownV.value ?? latestV.value)
const older = computed(() => shownV.value !== null)
const viewed = computed(() => versions.value.find(x => x.v === viewingV.value))
const prevV = computed(() => {
  const i = versions.value.findIndex(x => x.v === viewingV.value)
  return i > 0 ? versions.value[i - 1].v : 0
})
const showVersions = computed(
  () => !!files.value.versions && versions.value.length > 1 && ['code', 'markdown', 'csv'].includes(viewMode.value),
)
// What the text views show: the buffer, or an older version.
const shownText = computed(() => (older.value ? olderText.value : editorContent.value || originalContent))

const changes = computed(() =>
  showVersions.value && prevText.value !== null ? diffItems(splitLines(prevText.value), splitLines(shownText.value)) : null,
)
const anyChanges = computed(() => !!changes.value && (changes.value.added > 0 || changes.value.removed > 0))
const marks = computed(() => (highlight.value && changes.value ? changes.value.marked : new Set<number>()))
// Remembered: it's how you like to read changes, not something about one file.
const showDeletions = ref(localStorage.getItem('showDeletions') === '1')
watch(showDeletions, (on) => localStorage.setItem('showDeletions', on ? '1' : '0'))
// The version the text views mark changes against, while highlighting is on.
const against = computed(() => (highlight.value && changes.value ? prevText.value : null))
const lineMarks = computed(() =>
  against.value !== null && changes.value ? lineChanges(splitLines(against.value), splitLines(shownText.value), changes.value) : null,
)
// A table's rows are compared as rows, so a quoted line break doesn't shift them.
const csvMarks = computed(() => {
  if (!marks.value.size || !csvTable.value || prevText.value === null) return new Set<number>()
  const key = (r: string[]) => JSON.stringify(r)
  return diffItems(parseCsv(prevText.value).rows.map(key), csvTable.value.rows.map(key)).marked
})

const lines = (n: number) => `${n} ${n === 1 ? 'line' : 'lines'}`
const changesText = computed(() => {
  const c = changes.value
  if (!c) return ''
  const since = `Since v${prevV.value}: `
  if (c.added && c.removed) return `${since}${lines(c.added)} added, ${c.removed} removed or changed`
  if (c.added) return `${since}${lines(c.added)} added`
  if (c.removed) return `${since}${lines(c.removed)} removed or changed`
  return `${since}no changes`
})

function byWhom(x: FileVersion): string {
  switch (x.author) {
    case 'agent': return 'by the agent'
    case 'you': return 'by you'
    case 'guest': return `by ${x.name || 'a guest'}`
    default: return ''
  }
}
function when(at: string): string {
  const d = new Date(at)
  const time = d.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })
  return d.toDateString() === new Date().toDateString()
    ? time
    : `${d.toLocaleDateString([], { month: 'short', day: 'numeric' })}, ${time}`
}
const metaText = computed(() => {
  const x = viewed.value
  if (!x) return ''
  return [x.v === 1 ? 'Created' : 'Updated', when(x.at), byWhom(x)].filter(Boolean).join(' ')
})
const olderNote = computed(() =>
  versions.value.some(x => x.v > viewingV.value && x.author === 'agent')
    ? 'The agent has changed this file since.'
    : 'This file has changed since.',
)
const versionItems = computed<MenuEntry[]>(() =>
  [...versions.value].reverse().map((x) => {
    const who = byWhom(x)
    return {
      label: x.v === latestV.value ? `v${x.v} · Latest` : `v${x.v}`,
      description: [when(x.at), who && who[0].toUpperCase() + who.slice(1)].filter(Boolean).join(' · '),
      checked: x.v === viewingV.value,
      action: () => { void showVersion(x.v === latestV.value ? null : x.v) },
    }
  }),
)

async function versionText(v: number): Promise<string | null> {
  const known = versionTexts.get(v)
  if (known !== undefined) return known
  try {
    const text = await files.value.versionText!(props.path, v)
    if (v !== latestV.value) versionTexts.set(v, text)
    return text
  } catch {
    return null
  }
}

async function loadPrev() {
  const path = props.path
  const v = prevV.value
  const text = v ? await versionText(v) : null
  if (props.path === path && prevV.value === v) prevText.value = text
}

// Re-reads the list, which takes a version of any change first. A newer
// version while the file is followed is announced; an older one shown stays.
async function loadVersions() {
  const get = files.value.versions
  if (!get) return
  const path = props.path
  let list: FileVersion[]
  try { list = await get(path) } catch { return }
  if (props.path !== path) return
  const before = latestV.value
  versions.value = list
  if (before && latestV.value > before && !older.value) announced.value = `"${props.name}" was updated to v${latestV.value}`
  if (older.value && !list.some(x => x.v === shownV.value)) shownV.value = null
  await loadPrev()
}

async function showVersion(v: number | null) {
  const path = props.path
  if (v !== null) {
    const text = await versionText(v)
    if (props.path !== path) return
    if (text === null) { ;(window as any).showToast?.(`Couldn't open v${v}. Try again.`); return }
    olderText.value = text
  }
  shownV.value = v
  await loadPrev()
  if (viewMode.value === 'code') initEditor(shownText.value, props.name)
}
watch([renderedMarkdown, viewMode], () => {
  if (viewMode.value !== 'markdown') return
  nextTick(() => { if (markdownPreviewEl.value) highlightCode(markdownPreviewEl.value) })
})

// A cache-busted raw URL. The iframe serves the file from disk, and without
// this a re-open after an edit would show the browser's cached copy.
function previewUrl(path: string): string {
  return `${files.value.rawUrl(path)}&v=${Date.now()}`
}

// ── HTML with scripts on ──
// The owner's HTML previews run their scripts on the preview's own origin, by a
// link from the server; without one, scripts are off and the server says why.
const scripted = ref<PreviewLink | null>(null)
const scriptedSrc = ref('')
const scriptsOff = ref('')
// No "loaded" from the frame in time: most likely an expired sign-in, whose
// page can't be framed, so offer the preview in a tab of its own.
const frameStuck = ref(false)
const scriptedFrame = ref<HTMLIFrameElement | null>(null)
let loadedTimer: number | undefined

function showScripted(link: PreviewLink) {
  scripted.value = link
  frameStuck.value = false
  window.clearTimeout(loadedTimer)
  // A new src reloads the frame, which serves the file as it is now.
  scriptedSrc.value = `${link.url}?v=${Date.now()}`
  loadedTimer = window.setTimeout(() => { frameStuck.value = true }, 10000)
}

function onPreviewMessage(e: MessageEvent) {
  const link = scripted.value
  if (!link || e.origin !== link.origin || e.source !== scriptedFrame.value?.contentWindow || e.data !== 'kwa-preview-loaded') return
  window.clearTimeout(loadedTimer)
  frameStuck.value = false
}
window.addEventListener('message', onPreviewMessage)

function clearHtml() {
  window.clearTimeout(loadedTimer)
  scripted.value = null
  scriptedSrc.value = ''
  scriptsOff.value = ''
  frameStuck.value = false
  htmlUrl.value = ''
}

async function showHtml(path: string) {
  clearHtml()
  const get = files.value.previewLink
  if (get) {
    try {
      const link = await get(path)
      if (props.path !== path) return
      showScripted(link)
      return
    } catch (e) {
      if (props.path !== path) return
      scriptsOff.value = e instanceof Error ? e.message : String(e)
    }
  }
  htmlUrl.value = previewUrl(path)
}

// The version of the file the PDF, HTML or image view shows. Those render what
// was on disk when they opened, so reload() compares this with what is there
// now. Taken before the content is fetched: a write in between then costs one
// extra reload rather than a missed one.
let shownVersion = ''

// ── Office preview ──
// 'office' is the view while there is no PDF to show yet: converting, the
// LibreOffice install, or a failure.
const officeState = ref<'converting' | 'missing' | 'installing' | 'failed' | ''>('')
const officeError = ref('')
const officeInstall = ref<OfficeInstallStatus | null>(null)
const officeSizes = ref({ download: 0, installed: 0 })
let installPoll: number | undefined

const installStage = computed(() => {
  const st = officeInstall.value
  const mb = (n?: number) => Math.round((n || 0) / 1e6)
  switch (st?.stage) {
    case 'downloading':
      return st.total ? `Downloading: ${mb(st.done)} of ${mb(st.total)} MB` : 'Downloading'
    case 'unpacking': return 'Unpacking'
    case 'checking': return 'Checking that it starts'
    default: return 'Starting'
  }
})

// Asks for the PDF, which converts the file if needed, and shows it. quiet
// keeps the current view up meanwhile, for a refresh after the file changed.
async function showOffice(path: string, quiet = false) {
  const url = files.value.officePreviewUrl?.(path)
  if (!url) { viewMode.value = 'unsupported'; return }
  if (!quiet) { viewMode.value = 'office'; officeState.value = 'converting' }
  let res: Response
  try {
    res = await fetch(url, { cache: 'no-store' })
  } catch {
    if (props.path !== path) return
    officeError.value = "Knowledge Worker Agent didn't respond. Try again in a moment."
    officeState.value = 'failed'; viewMode.value = 'office'
    return
  }
  if (props.path !== path) { res.body?.cancel(); return }
  if (res.ok) {
    // The PDF is converted and cached now; the frame fetches it from there.
    res.body?.cancel()
    officeState.value = ''
    pdfUrl.value = `${url}&v=${Date.now()}`
    viewMode.value = 'pdf'
    return
  }
  const body = await res.json().catch(() => ({}))
  if (props.path !== path) return
  if (body.missing) { await showOfficeSetup(); return }
  officeError.value = body.error || res.statusText
  officeState.value = 'failed'; viewMode.value = 'office'
}

// LibreOffice is missing: offer the install, or follow one already running.
async function showOfficeSetup() {
  viewMode.value = 'office'
  try {
    const st = await fetchOfficeStatus()
    officeSizes.value = { download: st.download_mb, installed: st.installed_mb }
    officeInstall.value = st.install
    if (st.install.installing) { officeState.value = 'installing'; pollInstall(); return }
    if (st.available) { await showOffice(props.path); return }
  } catch { /* the note still shows, without sizes */ }
  officeState.value = 'missing'
}

async function startInstall() {
  officeInstall.value = { installing: true, stage: 'starting' }
  officeState.value = 'installing'
  try {
    await installOffice()
  } catch {
    officeInstall.value = { installing: false, error: "The install didn't start. Try again." }
    officeState.value = 'missing'
    return
  }
  pollInstall()
}

function stopInstallPoll() {
  if (installPoll) { window.clearInterval(installPoll); installPoll = undefined }
}

function pollInstall() {
  stopInstallPoll()
  installPoll = window.setInterval(async () => {
    let st
    try { st = await fetchOfficeStatus() } catch { return }
    officeInstall.value = st.install
    if (st.install.installing) return
    stopInstallPoll()
    if (!isOffice(props.name)) return
    if (st.available) await showOffice(props.path)
    else officeState.value = 'missing'
  }, 1000)
}

async function versionOf(path: string): Promise<string> {
  try {
    const res = await fetch(files.value.rawUrl(path), { method: 'HEAD', cache: 'no-store' })
    if (!res.ok) return ''
    return `${res.headers.get('Last-Modified') ?? ''}|${res.headers.get('Content-Length') ?? ''}`
  } catch {
    return ''
  }
}

// ── Loading ──
async function load() {
  const path = props.path
  const name = props.name
  teardownEditor()
  imageDataUrl.value = ''
  pdfUrl.value = ''
  clearHtml()
  editorContent.value = ''
  originalContent = ''
  shownVersion = ''
  versions.value = []
  shownV.value = null
  olderText.value = ''
  prevText.value = null
  announced.value = ''
  versionTexts.clear()
  dirty.value = false
  officeState.value = ''
  officeError.value = ''
  stopInstallPoll()
  if (!path) { viewMode.value = 'code'; return }

  const e = ext(name)
  const richDoc = ['pptx', 'xlsx', 'docx'].includes(e)
  if (isOffice(name)) {
    viewMode.value = 'office'
    officeState.value = 'converting'
    const version = await versionOf(path)
    if (props.path !== path) return
    shownVersion = version
    await showOffice(path)
    return
  }
  if (e === 'pdf') {
    viewMode.value = 'pdf'
    const version = await versionOf(path)
    if (props.path !== path) return
    shownVersion = version
    pdfUrl.value = previewUrl(path)
    return
  }
  try {
    const version = await versionOf(path)
    const data = await files.value.view(path)
    // The host may have switched files while this was in flight.
    if (props.path !== path) return
    shownVersion = version

    if (data.type === 'image') {
      imageDataUrl.value = `data:${data.mime || guessMime(name)};base64,${data.content}`
      viewMode.value = 'image'
      return
    }
    if (data.type !== 'text' || richDoc) {
      // Rich office docs and other binaries: no in-UI renderer available →
      // graceful "can't preview, download instead" state.
      viewMode.value = 'unsupported'
      return
    }
    // Text: markdown/HTML/CSV → preview by default, else code.
    originalContent = data.content
    editorContent.value = data.content
    if (['md', 'markdown', 'mdx'].includes(e)) {
      viewMode.value = 'markdown'
    } else if (['html', 'htm'].includes(e)) {
      // Rendered by default: seeing the finished page is the point. The
      // source is one click away for anyone who wants to edit it.
      viewMode.value = 'html'
      await showHtml(path)
    } else if (['csv', 'tsv'].includes(e)) {
      // Same reasoning: a table is what the data is, and quoted fields and
      // embedded commas make the raw text the harder way to read it.
      viewMode.value = 'csv'
    } else {
      viewMode.value = 'code'
    }
    if (viewMode.value === 'code') initEditor(data.content, name)
    void loadVersions()
  } catch {
    ;(window as any).showToast?.('Failed to open file')
  }
}

watch(() => props.path, load, { immediate: true })

/**
 * Re-read the open file from disk, unless it has unsaved edits — the agent's
 * write wins over a stale buffer, but never over the user's own typing.
 * Called by the host after a turn writes files, and on its poll while one runs.
 *
 * Text views take the new content in place. The PDF, HTML and image views
 * render straight from disk, and reloading one throws away the reader's scroll
 * position, so they reload only when the file's version has actually moved:
 * the poll costs a HEAD request, and a long PDF stays readable while the agent
 * works on other files.
 */
async function reload() {
  if (!props.path || dirty.value) return
  const path = props.path
  if (viewMode.value === 'pdf' || viewMode.value === 'html' || viewMode.value === 'image') {
    const version = await versionOf(path)
    if (props.path !== path || dirty.value || !version || version === shownVersion) return
    if (viewMode.value === 'pdf' && isOffice(props.name)) {
      // Keeps the old PDF up until the new one is converted.
      shownVersion = version
      await showOffice(path, true)
      return
    }
    if (viewMode.value === 'pdf') {
      // Straight to the new bytes, without blanking the frame first.
      shownVersion = version
      pdfUrl.value = previewUrl(path)
      return
    }
    // The same link serves the new version, while it has a while to run.
    const link = scripted.value
    if (viewMode.value === 'html' && link && Date.parse(link.expires) - Date.now() > 60_000) {
      shownVersion = version
      showScripted(link)
      return
    }
    await load()
    return
  }
  if (viewMode.value !== 'code' && viewMode.value !== 'markdown' && viewMode.value !== 'csv') return
  try {
    const data = await files.value.view(path)
    if (props.path !== path) return
    if (data.type !== 'text' || data.content === originalContent) return
    originalContent = data.content
    editorContent.value = data.content
    if (editorView && !older.value) {
      const cur = editorView.state.doc.toString()
      if (cur !== data.content) {
        editorView.dispatch({ changes: { from: 0, to: cur.length, insert: data.content } })
      }
    }
    dirty.value = false
    void loadVersions()
  } catch { /* file may have been deleted */ }
}

/** Re-open from scratch, discarding the current view — the explicit "reload". */
async function reopen() {
  if (dirty.value && !confirm('Discard unsaved changes?')) return
  await load()
}

function guessMime(name: string): string {
  const e = ext(name)
  const map: Record<string, string> = {
    png: 'image/png', jpg: 'image/jpeg', jpeg: 'image/jpeg', gif: 'image/gif',
    webp: 'image/webp', svg: 'image/svg+xml', bmp: 'image/bmp', ico: 'image/x-icon', avif: 'image/avif',
  }
  return map[e] || 'application/octet-stream'
}

function langFor(name: string) {
  const e = ext(name)
  switch (e) {
    case 'js': case 'jsx': case 'ts': case 'tsx':
      return javascript({ typescript: e.includes('ts'), jsx: e.includes('x') })
    case 'py': return python()
    case 'html': case 'vue': case 'svelte': return html()
    case 'css': case 'scss': return css()
    case 'md': case 'markdown': return markdown()
    case 'json': return json()
    case 'yaml': case 'yml': return yaml()
    // Delimited data has no syntax to highlight, and the JavaScript fallback
    // would colour arbitrary words as keywords.
    case 'csv': case 'tsv': return []
    default: return javascript()
  }
}

function initEditor(content: string, filename: string) {
  teardownEditor()
  nextTick(() => {
    if (!editorContainer.value) return
    // Prose wraps; code keeps its lines, as in most editors.
    const wrap = ['md', 'markdown', 'mdx', 'txt'].includes(ext(filename))
    // An older version can be read, not edited.
    const readOnly = older.value
    const state = EditorState.create({
      doc: content,
      extensions: [
        basicSetup,
        oneDark,
        langFor(filename),
        wrap ? EditorView.lineWrapping : [],
        readOnly ? [EditorState.readOnly.of(true), EditorView.editable.of(false)] : [],
        changeMarks,
        changeMarksTheme,
        EditorView.updateListener.of((u) => {
          if (u.docChanged) {
            const doc = u.state.doc.toString()
            editorContent.value = doc
            dirty.value = doc !== originalContent
          }
        }),
      ],
    })
    editorView = new EditorView({ state, parent: editorContainer.value })
    editorView.dispatch({ effects: setChanges.of({ changes: lineMarks.value, deletions: showDeletions.value }) })
  })
}

watch([lineMarks, showDeletions], ([changes, deletions]) => editorView?.dispatch({ effects: setChanges.of({ changes, deletions }) }))

function teardownEditor() {
  if (editorView) { editorView.destroy(); editorView = null }
}

// Markdown/HTML/CSV preview ↔ source toggle.
function togglePreviewMode() {
  if (!hasPreview.value) return
  if (isPreviewing.value) {
    viewMode.value = 'code'
    initEditor(shownText.value, props.name)
    return
  }
  if (isHtml.value) {
    // The iframe renders the file from disk, so an unsaved buffer would
    // preview stale content. Say so rather than showing the old version as if
    // it were the new one.
    if (dirty.value) { ;(window as any).showToast?.('Save to see your changes in the preview') }
    viewMode.value = 'html'
    void showHtml(props.path)
  } else if (isCsv.value) {
    viewMode.value = 'csv'
  } else {
    viewMode.value = 'markdown'
  }
  teardownEditor()
}

async function save() {
  if (!props.path || !editorView) return
  try {
    const content = editorView.state.doc.toString()
    await files.value.save(props.path, content)
    originalContent = content
    editorContent.value = content
    dirty.value = false
    // The HTML preview reads from disk, so point it at the new bytes.
    if (isHtml.value) htmlUrl.value = previewUrl(props.path)
    ;(window as any).showToast?.('File saved')
    void loadVersions()
  } catch {
    ;(window as any).showToast?.('Save error')
  }
}

function downloadOpen() {
  if (!props.path) return
  const v = shownV.value
  triggerDownload(v !== null && files.value.versionDownloadUrl ? files.value.versionDownloadUrl(props.path, v) : files.value.downloadUrl(props.path))
}

onBeforeUnmount(() => {
  teardownEditor()
  stopInstallPoll()
  window.clearTimeout(loadedTimer)
  window.removeEventListener('message', onPreviewMessage)
})

defineExpose({ reload, reopen, save })
</script>

<template>
  <div class="dv">
    <div class="fp-header editor">
      <!-- Host-supplied chrome: a back button in the docked panel, nothing in
           the document window (the OS provides its close control). -->
      <slot name="leading" />
      <span class="fp-title" :title="name">{{ name }}</span>
      <span v-if="dirty" class="fp-dirty" title="Unsaved changes">●</span>
      <div class="fp-header-actions">
        <ActionMenu
          v-if="showVersions"
          variant="chip"
          :trigger-text="`v${viewingV}`"
          :label="`Version v${viewingV}`"
          menu-label="Versions"
          :items="versionItems"
        />
        <button
          v-if="hasPreview && (isPreviewing || viewMode === 'code')"
          class="fp-btn"
          :class="{ active: isPreviewing }"
          @click="togglePreviewMode"
          :title="isPreviewing ? 'Edit source' : 'Preview'"
        >{{ isPreviewing ? '‹ › Code' : '◉ Preview' }}</button>
        <button v-if="viewMode === 'code' && !older" class="fp-btn save" :class="{ unsaved: dirty }" @click="save">Save</button>
        <button class="fp-ico" @click="downloadOpen" title="Download" aria-label="Download">⬇</button>
        <slot name="trailing" />
      </div>
    </div>

    <div v-if="showVersions" class="dv-versions">
      <span class="dv-meta"><strong>v{{ viewingV }}</strong> · {{ metaText }}</span>
      <span v-if="changes" class="dv-changes">{{ changesText }}</span>
      <span v-if="anyChanges" class="dv-toggles">
        <label class="dv-highlight">
          Highlight changes
          <span class="toggle"><input v-model="highlight" type="checkbox" role="switch" /><span class="slider"></span></span>
        </label>
        <label v-if="viewMode !== 'csv' && changes?.removed" class="dv-deletions" :class="{ off: !highlight }">
          Show deletions
          <span class="toggle"><input v-model="showDeletions" type="checkbox" role="switch" :disabled="!highlight" /><span class="slider"></span></span>
        </label>
      </span>
    </div>
    <div v-if="showVersions && older" class="dv-older" role="status">
      <span><strong>You're looking at v{{ viewingV }}.</strong> {{ olderNote }}</span>
      <button class="fp-btn" @click="showVersion(null)">Go to latest (v{{ latestV }})</button>
    </div>
    <p class="sr-only" aria-live="polite">{{ announced }}</p>

    <div class="fp-editor-body">
      <div v-show="viewMode === 'code'" ref="editorContainer" class="fp-cm"></div>
      <div v-if="viewMode === 'markdown'" ref="markdownPreviewEl" class="fp-md markdown-body" v-html="renderedMarkdown"></div>
      <div v-else-if="viewMode === 'image'" class="fp-image"><img :src="imageDataUrl" :alt="name" /></div>
      <!--
        Deliberately not sandboxed: any sandbox attribute (even
        allow-scripts allow-same-origin) makes Chrome refuse to hand the
        response to its built-in PDF viewer, and the panel shows a broken-file
        icon instead of the document. The viewer already runs PDF JavaScript
        in its own sandbox, so this is a viewer decision rather than an
        oversight. Verified in-browser before leaving it as-is.
      -->
      <iframe v-else-if="viewMode === 'pdf'" class="fp-pdf" :src="pdfUrl" :title="name"></iframe>
      <div v-else-if="viewMode === 'html'" class="fp-html-wrap">
        <p v-if="scriptsOff" class="fp-html-note" role="status">{{ scriptsOff }}</p>
        <p v-if="scripted && frameStuck" class="fp-html-note" role="status">
          The preview didn't load here. Your sign-in may have expired.
          <a class="fp-btn" :href="scripted.url" target="_blank" rel="noopener noreferrer">Open in a new tab</a>
        </p>
        <!-- No sandbox: the preview origin is the boundary, and the file runs sandboxed in its page
             there. A sandbox here would make that page's origin opaque, which its frame-ancestors refuses. -->
        <iframe
          v-if="scripted"
          ref="scriptedFrame"
          class="fp-html"
          referrerpolicy="no-referrer"
          :src="scriptedSrc"
          :title="name"
        ></iframe>
        <!--
          sandbox="" (no allow-* tokens) is load-bearing, not decoration: it gives the
          document an opaque origin AND blocks script execution. Scripts are the whole
          risk here — the panel renders files the agent wrote, and this API has no auth,
          so a script inside a previewed page could otherwise call it back and delete
          the user's files. The server sends a matching CSP on the same response, so the
          page also can't phone home through <img>/<link>. Do not add allow-scripts.
        -->
        <iframe
          v-else-if="htmlUrl"
          class="fp-html"
          sandbox=""
          referrerpolicy="no-referrer"
          :src="htmlUrl"
          :title="name"
        ></iframe>
      </div>
      <!--
        CSV renders as a table. Cells are bound as text, never v-html: a
        spreadsheet column is a perfectly ordinary place to find a string that
        looks like markup, and it should read as the characters it is.
      -->
      <div v-else-if="viewMode === 'csv'" class="fp-csv">
        <div v-if="!csvTable || !csvTable.columns" class="fp-csv-empty">This file has no rows to show.</div>
        <template v-else>
          <div class="fp-csv-scroll">
            <table class="fp-csv-table">
              <thead>
                <tr>
                  <th class="fp-csv-num"></th>
                  <th v-for="(cell, i) in csvTable.header" :key="i" :title="cell">{{ cell }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(row, r) in csvTable.rows" :key="r" :class="{ 'csv-changed': csvMarks.has(r) }">
                  <td class="fp-csv-num"><span v-if="csvMarks.has(r)" class="sr-only">Added or changed: </span>{{ r + 1 }}</td>
                  <td v-for="(cell, c) in row" :key="c" :title="cell">{{ cell }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <div v-if="csvTable.truncated" class="fp-csv-note">
            Showing the first {{ csvTable.rows.length.toLocaleString() }}
            of {{ csvTable.totalRows.toLocaleString() }} rows — switch to Code for the whole file.
          </div>
        </template>
      </div>
      <div v-else-if="viewMode === 'office'" class="fp-unsupported" aria-live="polite">
        <template v-if="officeState === 'converting'">
          <span class="fp-spinner" aria-hidden="true"></span>
          <div class="fp-unsupported-title">Preparing preview</div>
          <div class="fp-unsupported-sub">LibreOffice is converting the file. Large files take a few seconds.</div>
        </template>
        <template v-else-if="officeState === 'installing'">
          <span class="fp-spinner" aria-hidden="true"></span>
          <div class="fp-unsupported-title">Installing LibreOffice</div>
          <div class="fp-unsupported-sub">{{ installStage }}</div>
          <progress
            v-if="officeInstall?.stage === 'downloading' && officeInstall.total"
            class="fp-progress"
            :value="officeInstall.done || 0"
            :max="officeInstall.total"
          ></progress>
          <div class="fp-unsupported-sub">The preview opens when it's done.</div>
        </template>
        <template v-else-if="officeState === 'missing'">
          <div class="fp-unsupported-icon">🗂️</div>
          <div class="fp-unsupported-title">LibreOffice isn't installed</div>
          <div class="fp-unsupported-sub">
            Previewing PowerPoint, Word and Excel files needs LibreOffice.
            <template v-if="officeSizes.download">
              It's a {{ officeSizes.download }} MB download and uses about {{ officeSizes.installed }} MB
              in your home folder, so it stays installed when the workspace restarts.
            </template>
          </div>
          <div v-if="officeInstall?.error" class="fp-office-error">Install failed: {{ officeInstall.error }}</div>
          <div class="fp-office-actions">
            <button class="fp-btn save" @click="startInstall">{{ officeInstall?.error ? 'Try again' : 'Install LibreOffice' }}</button>
            <button class="fp-btn" @click="downloadOpen">Download file</button>
          </div>
        </template>
        <template v-else-if="officeState === 'failed'">
          <div class="fp-unsupported-icon">⚠️</div>
          <div class="fp-unsupported-title">Preview failed</div>
          <div class="fp-unsupported-sub">{{ officeError }}</div>
          <div class="fp-office-actions">
            <button class="fp-btn save" @click="showOffice(props.path)">Try again</button>
            <button class="fp-btn" @click="downloadOpen">Download file</button>
          </div>
        </template>
      </div>
      <div v-else-if="viewMode === 'unsupported'" class="fp-unsupported">
        <div class="fp-unsupported-icon">🗂️</div>
        <div class="fp-unsupported-title">Can't preview this file</div>
        <div class="fp-unsupported-sub">Download it to open in the right app.</div>
        <button class="fp-btn save" @click="downloadOpen">⬇ Download instead</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.dv { flex: 1 1 auto; min-height: 0; display: flex; flex-direction: column; overflow: hidden; }

.fp-header {
  display: flex; align-items: center; gap: 6px; padding: 8px 10px;
  min-height: var(--fp-header-h, auto);
  border-bottom: 1px solid var(--border); background: var(--surface); flex-shrink: 0;
}
.fp-title { font-size: 15px; font-weight: 700; flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.fp-header-actions { display: flex; align-items: center; gap: 2px; }
.fp-ico {
  width: 28px; height: 28px; display: flex; align-items: center; justify-content: center;
  background: none; border: none; color: var(--text2); border-radius: 6px; cursor: pointer;
  transition: all .12s; font-size: 13px;
}
.fp-ico:hover { background: var(--surface2); color: var(--text); }
.fp-btn {
  padding: 4px 9px; font-size: 12px; border: 1px solid var(--border); border-radius: 6px;
  background: var(--surface); color: var(--text2); cursor: pointer; transition: all .12s; white-space: nowrap;
}
.fp-btn:hover { background: var(--surface2); color: var(--text); }
.fp-btn.active { background: var(--accent); color: #fff; border-color: var(--accent); }
.fp-btn.save { background: var(--green); color: #fff; border-color: var(--green); }
.fp-btn.save:hover { opacity: .88; }
.fp-btn.save.unsaved { animation: pulse-save 1s infinite; }
.fp-dirty { color: var(--orange); font-size: 12px; }

.dv-versions {
  display: flex; align-items: center; flex-wrap: wrap; gap: 4px 14px; flex-shrink: 0;
  padding: 6px 12px; border-bottom: 1px solid var(--border); background: var(--surface);
  font-size: 12px; color: var(--text2);
}
.dv-meta strong { color: var(--text); font-weight: 600; }
.dv-toggles { display: inline-flex; align-items: center; flex-wrap: wrap; gap: 4px 16px; margin-left: auto; }
.dv-highlight, .dv-deletions { display: inline-flex; align-items: center; gap: 8px; color: var(--text); cursor: pointer; }
.dv-toggles .toggle input:focus-visible + .slider { outline: 2px solid var(--accent); outline-offset: 2px; }
.dv-deletions.off { color: var(--text3); cursor: default; }
.dv-deletions.off .slider { opacity: .5; cursor: default; }
.dv-older {
  display: flex; align-items: center; gap: 12px; flex-shrink: 0; padding: 8px 12px;
  border-bottom: 1px solid var(--border); font-size: 13px; color: var(--text);
  background: color-mix(in srgb, var(--accent) 10%, var(--surface));
}
.dv-older span { flex: 1; }
/* Added or changed: a bar and a tint, in the preview, the table and (cmChanges) the source. A block
   whose words changed gets the bar, and its added words the tint, underlined. Removed text is struck through. */
.fp-md :deep(.md-changed) {
  background: color-mix(in srgb, var(--green) 14%, transparent);
  box-shadow: inset 3px 0 0 var(--green);
}
.fp-md :deep(.md-touched) { box-shadow: inset 3px 0 0 var(--green); }
.fp-md :deep(.md-deleted) {
  background: color-mix(in srgb, var(--red) 10%, transparent);
  box-shadow: inset 3px 0 0 var(--red);
  color: var(--text2); text-decoration: line-through; text-decoration-color: var(--red);
  user-select: none;
}
.fp-md :deep(:is(div.md-changed, div.md-touched, div.md-deleted)) { margin: 0 -10px 16px; padding: 4px 10px 1px; border-radius: 0 4px 4px 0; }
.fp-md :deep(:is(div.md-changed, div.md-touched, div.md-deleted) > :last-child) { margin-bottom: 12px; }
.fp-md :deep(:is(li.md-changed, li.md-touched, li.md-deleted)) { padding-left: 6px; margin-left: -6px; }
/* Not a list item, so a removed item doesn't renumber the ones after it. */
.fp-md :deep(li.md-deleted) { display: block; }
.fp-md :deep(:is(tr.md-changed, tr.md-touched, tr.md-deleted)) { box-shadow: none; }
.fp-md :deep(tr.md-changed > *) { background: color-mix(in srgb, var(--green) 14%, var(--surface)); }
.fp-md :deep(tr.md-deleted > *) { background: color-mix(in srgb, var(--red) 10%, var(--surface)); }
.fp-md :deep(tr.md-changed > :first-child), .fp-md :deep(tr.md-touched > :first-child) { box-shadow: inset 3px 0 0 var(--green); }
.fp-md :deep(tr.md-deleted > :first-child) { box-shadow: inset 3px 0 0 var(--red); }
.fp-md :deep(ins.md-ins) {
  background: color-mix(in srgb, var(--green) 26%, transparent); border-radius: 2px;
  text-decoration: underline 2px var(--green); text-underline-offset: 3px;
}
.fp-md :deep(del.md-del) {
  background: color-mix(in srgb, var(--red) 16%, transparent); border-radius: 2px;
  color: var(--text2); text-decoration: line-through var(--red); user-select: none;
}
.fp-md :deep(.md-note) { user-select: none; }
.fp-csv-table tbody tr.csv-changed td { background: color-mix(in srgb, var(--green) 14%, var(--surface)); }
.fp-csv-table tbody tr.csv-changed .fp-csv-num { box-shadow: inset 3px 0 0 var(--green); }

.fp-editor-body { flex: 1; min-height: 0; display: flex; flex-direction: column; overflow: hidden; }
.fp-cm { flex: 1; overflow: auto; }
.fp-cm :deep(.cm-editor) { height: 100%; }
.fp-md { flex: 1; overflow: auto; padding: 20px 22px; font-size: 14px; }
.fp-image {
  flex: 1; display: flex; align-items: center; justify-content: center; overflow: auto; padding: 14px;
  background: repeating-conic-gradient(var(--surface2) 0 25%, var(--surface) 0 50%) 0 0/24px 24px;
}
.fp-image img { max-width: 100%; max-height: 100%; object-fit: contain; box-shadow: 0 4px 18px rgba(0,0,0,.25); border-radius: 4px; }
.fp-pdf { flex: 1; width: 100%; border: none; background: var(--surface2); }
/* White backing: pages assume a white canvas, and a dark panel behind a
   transparent body would make black body text unreadable. */
.fp-html { flex: 1; width: 100%; border: none; background: #fff; }
.fp-html-wrap { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.fp-html-note {
  margin: 0; padding: 8px 12px; font-size: 12px; line-height: 1.5; color: var(--text2);
  background: color-mix(in srgb, var(--orange) 12%, transparent); border-bottom: 1px solid var(--border);
  display: flex; align-items: center; gap: 10px; flex-wrap: wrap;
}
.fp-html-note .fp-btn { text-decoration: none; }

/* ── CSV table preview ── */
/* min-height: 0 is load-bearing *here*: this element's overflow is `visible`,
   so its automatic minimum size is its content, and without it the column grows
   to the full height of the table, overflows the panel (which clips), and
   nothing scrolls — every row below the fold becomes unreachable.
   The scroller below deliberately does *not* repeat it: `overflow: auto`
   already gives a flex item a zero automatic minimum size. Verified by deleting
   each in turn — parent removed => scrollTop stayed 0; scroller removed => no
   change at all. */
.fp-csv { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.fp-csv-scroll { flex: 1; overflow: auto; }
.fp-csv-empty { padding: 20px; text-align: center; color: var(--text3); font-size: 13px; }
.fp-csv-table {
  border-collapse: separate; /* separate keeps borders painted under a sticky header */
  border-spacing: 0;
  font-size: 12.5px;
  font-variant-numeric: tabular-nums; /* digits line up column-wise */
  white-space: nowrap;
}
.fp-csv-table th, .fp-csv-table td {
  border-right: 1px solid var(--border);
  border-bottom: 1px solid var(--border);
  padding: 5px 10px;
  text-align: left;
  /* Wide free-text columns would otherwise push every other column off screen;
     the full value stays available as the cell's tooltip. */
  max-width: 320px; overflow: hidden; text-overflow: ellipsis;
}
.fp-csv-table thead th {
  position: sticky; top: 0; z-index: 1; /* column names stay put while scrolling */
  background: var(--surface3);
  color: var(--text);
  font-weight: 600;
}
.fp-csv-table td { color: var(--text); }
.fp-csv-table tbody tr:nth-child(even) td { background: var(--surface2); }
.fp-csv-table tbody tr:hover td { background: var(--surface3); }
/* Row-number gutter: an index into the file, not data, so it is muted and
   excluded from selection to keep copied cells clean. */
.fp-csv-num {
  color: var(--text3); text-align: right; user-select: none;
  background: var(--surface2); font-size: 11px;
  position: sticky; left: 0; z-index: 2;
}
.fp-csv-table thead .fp-csv-num { z-index: 3; background: var(--surface3); }
.fp-csv-note {
  flex-shrink: 0; padding: 7px 12px; font-size: 11.5px; color: var(--text2);
  border-top: 1px solid var(--border); background: var(--surface2);
}
.fp-unsupported {
  flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: 8px; padding: 24px; text-align: center;
}
.fp-unsupported-icon { font-size: 40px; }
.fp-unsupported-title { font-size: 15px; font-weight: 600; color: var(--text); }
.fp-unsupported-sub { font-size: 12px; color: var(--text2); margin-bottom: 6px; max-width: 420px; }
.fp-office-actions { display: flex; gap: 8px; }
.fp-office-error { font-size: 12px; color: var(--red); max-width: 420px; margin-bottom: 6px; }
.fp-progress { width: 220px; accent-color: var(--accent); }
.fp-spinner {
  width: 22px; height: 22px; border: 2px solid var(--border); border-top-color: var(--accent);
  border-radius: 50%; animation: fp-spin .8s linear infinite;
}
@keyframes fp-spin { to { transform: rotate(360deg); } }
</style>
