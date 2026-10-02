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
import { ownerFileApi, type FileApi } from '../../api/files'
import { renderMarkdown, highlightCode } from '../../composables/useMarkdown'
import { parseCsv } from '../../utils/csv'
import { triggerDownload } from '../../utils/download'
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
const viewMode = ref<'code' | 'markdown' | 'image' | 'pdf' | 'html' | 'csv' | 'unsupported'>('code')
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
// These types open rendered and can be flipped to source, so the header button
// is shared rather than duplicated per type.
const hasPreview = computed(() => isMarkdown.value || isHtml.value || isCsv.value)
const isPreviewing = computed(
  () => viewMode.value === 'markdown' || viewMode.value === 'html' || viewMode.value === 'csv',
)

const renderedMarkdown = computed(() =>
  isMarkdown.value ? renderMarkdown(editorContent.value || originalContent) : '',
)

// Parsed from the live buffer rather than from disk, so edits made in the
// source view show up in the table as soon as you switch back — the same
// immediacy markdown has. (The HTML preview can't do this: it is an iframe
// served from disk, which is why that one asks you to save first.)
const csvTable = computed(() =>
  isCsv.value ? parseCsv(editorContent.value || originalContent) : null,
)
watch([renderedMarkdown, viewMode], () => {
  if (viewMode.value !== 'markdown') return
  nextTick(() => { if (markdownPreviewEl.value) highlightCode(markdownPreviewEl.value) })
})

// A cache-busted raw URL. The iframe serves the file from disk, and without
// this a re-open after an edit would show the browser's cached copy.
function previewUrl(path: string): string {
  return `${files.value.rawUrl(path)}&v=${Date.now()}`
}

// The version of the file the PDF, HTML or image view shows. Those render what
// was on disk when they opened, so reload() compares this with what is there
// now. Taken before the content is fetched: a write in between then costs one
// extra reload rather than a missed one.
let shownVersion = ''

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
  htmlUrl.value = ''
  editorContent.value = ''
  originalContent = ''
  shownVersion = ''
  dirty.value = false
  if (!path) { viewMode.value = 'code'; return }

  const e = ext(name)
  const richDoc = ['pptx', 'xlsx', 'docx'].includes(e)
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
      htmlUrl.value = previewUrl(path)
      viewMode.value = 'html'
    } else if (['csv', 'tsv'].includes(e)) {
      // Same reasoning: a table is what the data is, and quoted fields and
      // embedded commas make the raw text the harder way to read it.
      viewMode.value = 'csv'
    } else {
      viewMode.value = 'code'
    }
    if (viewMode.value === 'code') initEditor(data.content, name)
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
    if (viewMode.value === 'pdf') {
      // Straight to the new bytes, without blanking the frame first.
      shownVersion = version
      pdfUrl.value = previewUrl(path)
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
    if (editorView) {
      const cur = editorView.state.doc.toString()
      if (cur !== data.content) {
        editorView.dispatch({ changes: { from: 0, to: cur.length, insert: data.content } })
      }
    }
    dirty.value = false
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
    const state = EditorState.create({
      doc: content,
      extensions: [
        basicSetup,
        oneDark,
        langFor(filename),
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
  })
}

function teardownEditor() {
  if (editorView) { editorView.destroy(); editorView = null }
}

// Markdown/HTML/CSV preview ↔ source toggle.
function togglePreviewMode() {
  if (!hasPreview.value) return
  if (isPreviewing.value) {
    viewMode.value = 'code'
    initEditor(editorContent.value || originalContent, props.name)
    return
  }
  if (isHtml.value) {
    // The iframe renders the file from disk, so an unsaved buffer would
    // preview stale content. Say so rather than showing the old version as if
    // it were the new one.
    if (dirty.value) { ;(window as any).showToast?.('Save to see your changes in the preview') }
    htmlUrl.value = previewUrl(props.path)
    viewMode.value = 'html'
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
  } catch {
    ;(window as any).showToast?.('Save error')
  }
}

function downloadOpen() {
  if (props.path) triggerDownload(files.value.downloadUrl(props.path))
}

onBeforeUnmount(teardownEditor)

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
        <button
          v-if="hasPreview && (isPreviewing || viewMode === 'code')"
          class="fp-btn"
          :class="{ active: isPreviewing }"
          @click="togglePreviewMode"
          :title="isPreviewing ? 'Edit source' : 'Preview'"
        >{{ isPreviewing ? '‹ › Code' : '◉ Preview' }}</button>
        <button v-if="viewMode === 'code'" class="fp-btn save" :class="{ unsaved: dirty }" @click="save">Save</button>
        <button class="fp-ico" @click="downloadOpen" title="Download" aria-label="Download">⬇</button>
        <slot name="trailing" />
      </div>
    </div>

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
      <!--
        sandbox="" (no allow-* tokens) is load-bearing, not decoration: it gives the
        document an opaque origin AND blocks script execution. Scripts are the whole
        risk here — the panel renders files the agent wrote, and this API has no auth,
        so a script inside a previewed page could otherwise call it back and delete
        the user's files. The server sends a matching CSP on the same response, so the
        page also can't phone home through <img>/<link>. Do not add allow-scripts.
      -->
      <iframe
        v-else-if="viewMode === 'html'"
        class="fp-html"
        sandbox=""
        referrerpolicy="no-referrer"
        :src="htmlUrl"
        :title="name"
      ></iframe>
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
                <tr v-for="(row, r) in csvTable.rows" :key="r">
                  <td class="fp-csv-num">{{ r + 1 }}</td>
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
.fp-unsupported-sub { font-size: 12px; color: var(--text2); margin-bottom: 6px; }
</style>
