<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useUiStore } from '../../stores/ui'
import { useSessionsStore } from '../../stores/sessions'
import { useProvidersStore } from '../../stores/providers'
import { useConnectorsStore } from '../../stores/connectors'
import { useToolboxStore } from '../../stores/toolbox'
import { providerOf, resolvePreset, shortModelName } from '../../utils/models'
import {
  fetchDiagnostics, fetchDiagnosticsDisk, fetchDiagnosticsLog, diagnosticsLogDownloadUrl,
  fetchRefreshState, startConfigRefresh,
  type DiagnosticsReport, type DiagnosticsDisk, type LogSource, type LogTail,
} from '../../api'

const ui = useUiStore()
const sessions = useSessionsStore()
const providersStore = useProvidersStore()
const connectorsStore = useConnectorsStore()
const toolboxStore = useToolboxStore()

type Tab = 'overview' | 'config' | 'log'
const tab = ref<Tab>('overview')

const report = ref<DiagnosticsReport | null>(null)
const reportError = ref('')
const disk = ref<DiagnosticsDisk | null>(null)
const diskError = ref('')

async function loadReport() {
  reportError.value = ''
  try {
    report.value = await fetchDiagnostics()
  } catch (e) {
    reportError.value = e instanceof Error ? e.message : String(e)
  }
}

// The disk walk takes a few seconds, so it loads on its own and never holds
// up the rest of the overview.
let diskLoad: Promise<void> = Promise.resolve()
function loadDisk() {
  diskError.value = ''
  diskLoad = fetchDiagnosticsDisk()
    .then((d) => { disk.value = d })
    .catch((e) => { diskError.value = e instanceof Error ? e.message : String(e) })
  return diskLoad
}

// ── Formatting ──
function fmtBytes(n: number): string {
  if (!n) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(units.length - 1, Math.floor(Math.log(n) / Math.log(1024)))
  return `${(n / 1024 ** i).toFixed(i ? 1 : 0)} ${units[i]}`
}
function fmtDuration(seconds: number): string {
  const d = Math.floor(seconds / 86400), h = Math.floor((seconds % 86400) / 3600), m = Math.floor((seconds % 3600) / 60)
  if (d) return `${d} d ${h} h`
  if (h) return `${h} h ${m} min`
  return `${m} min`
}
function fmtTime(iso: string | undefined): string {
  if (!iso) return ''
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) || d.getFullYear() < 2000 ? '' : d.toLocaleString()
}

const KIND_LABELS: Record<string, string> = {
  agents: 'Agents', skills: 'Skills', context: 'Context files', mcp: 'Connectors', provider: 'Model providers', vocabulary: 'Vocabularies',
}

// Text shared by the tabs and the copied report, so the two say the same thing.
const HIDDEN_NOTE = 'An assigned agent, skill or connector replaces one of yours with the same name.'
const APPLIED_NOTE = 'The configuration is applied when Knowledge Worker Agent starts, and when you refresh it.'
const NO_PROJECT = 'No central configuration: this workspace has no project set (KWA_PROJECT_ID).'
const NO_COMMIT = "Unknown: the source isn't a git repository of its own"
const STATUS_UNCHECKED = "Connection status hasn't been checked yet."
const runningFor = (r: DiagnosticsReport) => `${fmtDuration(r.app.uptime_seconds)} (since ${fmtTime(r.app.started_at)})`
const backendLabel = (r: DiagnosticsReport) => (r.app.backend === 'serve' ? 'As a server' : 'Once per turn')
const pinWarning = (r: DiagnosticsReport) => (r.opencode.matches_pin ? '' : `⚠ Expected ${r.opencode.pinned}`)
const signedIn = (authenticated: boolean) => (authenticated ? 'signed in' : 'not signed in')
const connectorsSummary = (k: DiagnosticsReport['connectors']) =>
  `${k.total} configured, ${k.on} on, ${k.connected} connected${k.assigned ? `; ${k.assigned} assigned by the central configuration` : ''}.`
const diskSummary = (d: DiagnosticsDisk) => `${fmtBytes(d.free_bytes)} free of ${fmtBytes(d.total_bytes)}`
const diskSize = (it: DiagnosticsDisk['items'][number]) => `${it.partial ? 'at least ' : ''}${fmtBytes(it.bytes)}`
const oldNameNote = (from: string) => `(read from the old name ${from})`
const oldSettingsLine = (names: string[]) => `⚠ Old setting names, which a later release stops reading: ${names.join(', ')}`
const sourceKind = (kind: string) => (kind === 'local' ? 'local folder' : 'git repository')
const keptNote = (dir: string) => `Files from an earlier release that would have been removed. In ${dir}.`
const staleLine = (reason = '') => `⚠ The repository couldn't be updated, so an earlier copy is in use: ${reason}`
const managedLabel = (enabled: boolean) =>
  enabled ? 'Managed centrally' : 'Not managed centrally (KWA_CENTRAL_CONFIG is off): agents, skills and context still apply'

// ── Overview ──
const statusLabel = computed(() => {
  const s = report.value?.app.status
  return s === 'ready' ? 'Ready' : s === 'degraded' ? 'Degraded: opencode could not be set up' : s === 'starting' ? 'Starting' : s || ''
})

const modeLabel = computed(() => {
  const p = report.value?.providers
  if (!p) return ''
  if (p.mode === 'central') return 'Assigned by the central configuration'
  if (p.mode === 'fallback') return `Built in, because the central configuration assigns none${p.fallback_reason ? ` (${p.fallback_reason})` : ''}`
  return 'Built in'
})

function reachLabel(r: string, note?: string): string {
  if (r === 'ok') return 'reachable'
  if (r === 'unreachable') return note ? `unreachable: ${note}` : 'unreachable'
  return 'not checked yet'
}

// What Default and Thinking resolve to, computed the way the composer does, so
// this shows what a chat actually gets.
const presetRows = computed(() => (['default', 'thinking'] as const).map((preset) => {
  const model = resolvePreset(preset, sessions.models, providersStore.presets)
  const nominated = providersStore.presets?.[preset]
  return {
    label: preset === 'default' ? 'Default' : 'Thinking',
    model: model || 'none available',
    source: !model ? '' : nominated === model ? 'set by the central configuration' : 'built-in choice',
  }
}))

const presetsFor = (providerId: string) => presetRows.value.filter((r) => providerOf(r.model) === providerId)
// Shown on their own when no listed provider owns the model, e.g. none available.
const unplacedPresets = computed(() => {
  const ids = new Set(report.value?.providers.items.map((p) => p.id))
  return presetRows.value.filter((r) => !ids.has(providerOf(r.model)))
})

// ── Configuration ──
// The run whose configuration is in use, or, when none has applied, the latest attempt.
const lastRun = computed(() => report.value?.config.applied ?? report.value?.config.last_run ?? null)
// A later attempt that failed and left the applied configuration in place.
const failedLine = computed(() => {
  const c = report.value?.config
  if (!c?.applied || !c.last_run || c.last_run.ok || c.last_run.at === c.applied.at) return ''
  return `⚠ The last attempt, ${fmtTime(c.last_run.at)}, failed: ${c.last_run.error}. The configuration applied ${fmtTime(c.applied.at)} is still in use.`
})
const runOutcome = computed(() => {
  const r = lastRun.value
  if (!r) return { label: 'Not run in this session', tone: 'muted' }
  if (!r.ok) return { label: `Failed: ${r.error}`, tone: 'bad' }
  if (r.skipped) return { label: `Skipped: ${r.skip_reason}`, tone: 'muted' }
  if (r.stale) return { label: 'Applied from an earlier copy', tone: 'warn' }
  return { label: 'Applied', tone: 'good' }
})
const countRows = computed(() => {
  const counts = lastRun.value?.counts ?? {}
  return Object.keys(KIND_LABELS).filter((k) => k in counts).map((k) => ({ label: KIND_LABELS[k], n: counts[k] }))
})
const versionInUse = computed(() => {
  const c = lastRun.value?.commit
  return c ? `${c.hash.slice(0, 12)} · ${fmtTime(c.date)} · ${c.subject}` : ''
})
const lastAppliedAt = computed(() => {
  const r = lastRun.value
  return r ? `${fmtTime(r.at)} (${(r.duration_ms / 1000).toFixed(1)} s): ` : ''
})

// ── Log ──
const logSource = ref<LogSource>('kwa')
const logFilter = ref<'all' | 'problems'>('all')
const logQuery = ref('')
const log = ref<LogTail | null>(null)
const logError = ref('')
const logLoading = ref(false)
const logEl = ref<HTMLElement | null>(null)
const bodyEl = ref<HTMLElement | null>(null)

const PROBLEM = /⚠|\b(error|errors|failed|failure|panic|fatal|warn|warning)\b|level=(error|warn)/i

async function loadLog() {
  logLoading.value = true
  logError.value = ''
  try {
    log.value = await fetchDiagnosticsLog(logSource.value)
    requestAnimationFrame(() => { if (logEl.value) logEl.value.scrollTop = logEl.value.scrollHeight })
  } catch (e) {
    logError.value = e instanceof Error ? e.message : String(e)
  } finally {
    logLoading.value = false
  }
}
watch(logSource, loadLog)
watch(tab, (t) => {
  bodyEl.value?.scrollTo(0, 0)
  if (t === 'log' && !log.value) loadLog()
})

const shownLines = computed(() => {
  const q = logQuery.value.trim().toLowerCase()
  return (log.value?.lines ?? [])
    .filter((l) => logFilter.value === 'all' || PROBLEM.test(l))
    .filter((l) => !q || l.toLowerCase().includes(q))
})

// ── Copy report ──
const copying = ref(false)

// The report follows the Overview and Configuration tabs section by section,
// with the same labels and wording, so it reads like the dialog.
function reportText(r: DiagnosticsReport, problems: string[]): string {
  const out: string[] = []
  const blank = () => { if (out.length && out[out.length - 1] !== '') out.push('') }
  const part = (title: string) => { blank(); out.push(`=== ${title} ===`); out.push('') }
  const section = (title: string) => { blank(); out.push(title) }
  const text = (s: string) => out.push(`  ${s}`)
  const bullet = (s: string) => out.push(`  • ${s}`)
  const subBullet = (s: string) => out.push(`    ◦ ${s}`)
  const rows = (pairs: [string, string][]) => {
    const w = Math.max(...pairs.map(([k]) => k.length))
    for (const [k, v] of pairs) out.push(`  ${k.padEnd(w)}  ${v}`)
  }

  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone
  out.push('Knowledge Worker Agent diagnostics')
  out.push(`Generated ${fmtTime(r.generated_at)}${zone ? ` (${zone})` : ''}`)

  part('Overview')
  section('Knowledge Worker Agent')
  rows([
    ['Version', r.app.version],
    ['Built', r.app.build_time],
    ['Status', statusLabel.value],
    ['Running for', runningFor(r)],
    ['Chats run opencode', backendLabel(r)],
  ])
  section('opencode')
  rows([
    ['Version', [r.opencode.version || 'Not found', pinWarning(r)].filter(Boolean).join(' ')],
    ['Program', r.opencode.bin],
  ])
  section('Model providers')
  text(`${modeLabel.value}. ${r.providers.model_count} models available.`)
  for (const p of r.providers.items) {
    bullet(`${p.label || p.id}: ${signedIn(p.authenticated)}, ${reachLabel(p.reachability, p.note)}, ${p.models} models`)
    for (const row of presetsFor(p.id)) subBullet(`${row.label}: ${shortModelName(row.model)} (${row.source})`)
  }
  for (const row of unplacedPresets.value) bullet(`${row.label}: ${row.model}${row.source ? ` (${row.source})` : ''}`)
  section('Connectors')
  text(connectorsSummary(r.connectors))
  if (r.connectors.not_connected.length) text(`On but not connected: ${r.connectors.not_connected.join(', ')}`)
  if (!r.connectors.status_known) text(STATUS_UNCHECKED)
  section('Disk')
  if (disk.value) {
    text(diskSummary(disk.value))
    if (disk.value.items.length) rows(disk.value.items.map((it): [string, string] => [it.label, diskSize(it)]))
  } else {
    text(diskError.value || 'Not measured')
  }
  section('Settings')
  if (r.settings.length) rows(r.settings.map((s): [string, string] => [s.name, s.from ? `${s.value} ${oldNameNote(s.from)}` : s.value]))
  else text('None set.')
  if (r.old_settings.length) text(oldSettingsLine(r.old_settings))
  section('Locations')
  rows(r.paths.filter((x) => x.path).map((x): [string, string] => [x.label, x.path]))

  part('Configuration')
  const c = r.config
  const run = c.last_run
  if (!c.project) {
    text(NO_PROJECT)
  } else {
    rows([
      ['Project', c.project],
      ['Source', [c.source || 'not set', c.source_kind ? `(${sourceKind(c.source_kind)})` : ''].filter(Boolean).join(' ')],
      ['Version in use', versionInUse.value || NO_COMMIT],
      ['Last applied', `${lastAppliedAt.value}${runOutcome.value.label}`],
      ['Model providers and connectors', managedLabel(c.enabled)],
    ])
    if (run?.stale) text(staleLine(run.stale_reason))
    if (failedLine.value) text(failedLine.value)
    text(APPLIED_NOTE)
    if (refreshLabel.value) text(refreshLabel.value)
    if (countRows.value.length) {
      section('Applied')
      rows(countRows.value.map((row): [string, string] => [row.label, String(row.n)]))
    }
    if (run?.warnings.length) {
      section('Warnings')
      for (const w of run.warnings) bullet(w)
    }
  }
  section('Your own items hidden by assigned ones')
  text(HIDDEN_NOTE)
  if (c.hidden.length) for (const h of c.hidden) bullet(`${h.kind} ${h.name}`)
  else text('None.')
  section('Old default files kept because you edited them')
  text(keptNote(r.opencode.config_dir))
  if (c.kept_defaults.length) for (const f of c.kept_defaults) bullet(f)
  else text('None.')

  if (problems.length) {
    part('Recent warnings and errors (Knowledge Worker Agent log)')
    for (const l of problems) text(l.trim())
  }
  return out.join('\n') + '\n'
}

async function copyReport() {
  if (!report.value || copying.value) return
  copying.value = true
  try {
    await diskLoad
    let problems: string[] = []
    try {
      problems = (await fetchDiagnosticsLog('kwa', 2000)).lines.filter((l) => PROBLEM.test(l)).slice(-20)
    } catch { /* the report is still useful without them */ }
    const text = reportText(report.value, problems)
    try {
      await navigator.clipboard.writeText(text)
    } catch {
      const ta = document.createElement('textarea')
      ta.value = text
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      ta.remove()
    }
    ;(window as any).showToast?.('Diagnostics report copied')
  } finally {
    copying.value = false
  }
}

// ── Refresh configuration ──
// The server runs it in the background; poll its state until it finishes.
const refreshState = computed(() => report.value?.config.refresh ?? null)
const refreshError = ref('')
let refreshTimer: ReturnType<typeof setTimeout> | null = null

async function startRefresh() {
  refreshError.value = ''
  try {
    const st = await startConfigRefresh()
    if (report.value) report.value.config.refresh = st
    pollRefresh()
  } catch (e) {
    refreshError.value = e instanceof Error ? e.message : String(e)
  }
}

async function pollRefresh() {
  if (refreshTimer) clearTimeout(refreshTimer)
  refreshTimer = setTimeout(async () => {
    refreshTimer = null
    let st = null
    try { st = await fetchRefreshState() } catch { /* try again below */ }
    if (st && report.value) report.value.config.refresh = st
    if (!st || st.running) { pollRefresh(); return }
    // Done: show what was applied, and bring the rest of the app up to date.
    await Promise.all([loadReport(), loadDisk()])
    sessions.loadModels()
    sessions.loadAgents()
    providersStore.refresh()
    if (connectorsStore.loaded) connectorsStore.load()
    toolboxStore.loadSkills()
  }, 1000)
}

const refreshLabel = computed(() => {
  const st = refreshState.value
  if (!st) return ''
  if (st.running) return `Refreshing: ${st.step || 'starting'}…`
  const at = fmtTime(st.finished_at)
  if (st.ok) return `Refreshed ${at}.`
  return failedLine.value ? `Refresh failed ${at}.` : `Refresh failed ${at}: ${st.error}`
})

onMounted(async () => {
  loadDisk()
  await loadReport()
  if (refreshState.value?.running) pollRefresh()
})
onBeforeUnmount(() => { if (refreshTimer) clearTimeout(refreshTimer) })

function refreshAll() {
  loadReport()
  loadDisk()
  if (tab.value === 'log') loadLog()
}
</script>

<template>
  <div class="modal diag-modal" @click.stop>
    <h2>🩺 Diagnostics</h2>
    <p class="modal-subtitle">
      For troubleshooting, on your own or with your administrator. <strong>Copy report</strong> puts the Overview and
      Configuration tabs on the clipboard as text, with recent log warnings; passwords, keys and tokens are left out.
    </p>

    <div class="modal-tabs" role="tablist">
      <button class="modal-tab" role="tab" :aria-selected="tab === 'overview'" :class="{ active: tab === 'overview' }" @click="tab = 'overview'">Overview</button>
      <button class="modal-tab" role="tab" :aria-selected="tab === 'config'" :class="{ active: tab === 'config' }" @click="tab = 'config'">Configuration</button>
      <button class="modal-tab" role="tab" :aria-selected="tab === 'log'" :class="{ active: tab === 'log' }" @click="tab = 'log'">Log</button>
    </div>

    <!-- Only this part scrolls, so the tabs and buttons stay in view. -->
    <div ref="bodyEl" class="diag-body">
      <p v-if="reportError" class="diag-error">Couldn't load diagnostics: {{ reportError }}</p>
      <p v-else-if="!report && tab !== 'log'" class="diag-muted">Loading…</p>

      <!-- Overview -->
      <div v-if="report && tab === 'overview'" class="diag-panel">
        <section>
          <h3>Knowledge Worker Agent</h3>
          <dl class="diag-dl">
            <dt>Version</dt><dd>{{ report.app.version }}</dd>
            <dt>Built</dt><dd>{{ report.app.build_time }}</dd>
            <dt>Status</dt><dd :class="{ 'diag-bad': report.app.status !== 'ready' }">{{ statusLabel }}</dd>
            <dt>Running for</dt><dd>{{ runningFor(report) }}</dd>
            <dt>Chats run opencode</dt><dd>{{ backendLabel(report) }}</dd>
          </dl>
        </section>

        <section>
          <h3>opencode</h3>
          <dl class="diag-dl">
            <dt>Version</dt>
            <dd>
              {{ report.opencode.version || 'Not found' }}
              <span v-if="!report.opencode.matches_pin" class="diag-warn">{{ pinWarning(report) }}</span>
            </dd>
            <dt>Program</dt><dd class="diag-path">{{ report.opencode.bin }}</dd>
          </dl>
        </section>

        <section>
          <h3>Model providers</h3>
          <p class="diag-line">{{ modeLabel }}. {{ report.providers.model_count }} models available.</p>
          <ul class="diag-list">
            <li v-for="p in report.providers.items" :key="p.id">
              <strong>{{ p.label || p.id }}</strong>:
              <span :class="p.authenticated ? 'diag-good' : 'diag-muted'">{{ signedIn(p.authenticated) }}</span>,
              <span :class="{ 'diag-bad': p.reachability === 'unreachable' }">{{ reachLabel(p.reachability, p.note) }}</span>,
              {{ p.models }} models
              <ul v-if="presetsFor(p.id).length" class="diag-sublist">
                <li v-for="row in presetsFor(p.id)" :key="row.label">
                  {{ row.label }}: {{ shortModelName(row.model) }} <span class="diag-muted">({{ row.source }})</span>
                </li>
              </ul>
            </li>
            <li v-for="row in unplacedPresets" :key="row.label">
              {{ row.label }}: {{ row.model }} <span v-if="row.source" class="diag-muted">({{ row.source }})</span>
            </li>
          </ul>
        </section>

        <section>
          <h3>Connectors</h3>
          <p class="diag-line">{{ connectorsSummary(report.connectors) }}</p>
          <p v-if="report.connectors.not_connected.length" class="diag-line diag-warn">
            On but not connected: {{ report.connectors.not_connected.join(', ') }}
          </p>
          <p v-if="!report.connectors.status_known" class="diag-line diag-muted">{{ STATUS_UNCHECKED }}</p>
        </section>

        <section>
          <h3>Disk</h3>
          <p v-if="diskError" class="diag-error">{{ diskError }}</p>
          <p v-else-if="!disk" class="diag-muted">Measuring…</p>
          <template v-else>
            <p class="diag-line" :class="{ 'diag-warn': disk.total_bytes && disk.free_bytes / disk.total_bytes < 0.1 }">
              {{ diskSummary(disk) }}
            </p>
            <dl class="diag-dl">
              <template v-for="it in disk.items" :key="it.label">
                <dt :title="it.path">{{ it.label }}</dt><dd>{{ diskSize(it) }}</dd>
              </template>
            </dl>
          </template>
        </section>

        <section>
          <h3>Settings</h3>
          <p v-if="!report.settings.length" class="diag-muted">None set.</p>
          <dl v-else class="diag-dl diag-settings">
            <template v-for="s in report.settings" :key="s.name">
              <dt>{{ s.name }}</dt>
              <dd>{{ s.value }} <span v-if="s.from" class="diag-warn">{{ oldNameNote(s.from) }}</span></dd>
            </template>
          </dl>
          <p v-if="report.old_settings.length" class="diag-line diag-warn">
            {{ oldSettingsLine(report.old_settings) }}
          </p>
        </section>

        <section>
          <h3>Locations</h3>
          <dl class="diag-dl">
            <template v-for="p in report.paths.filter((x) => x.path)" :key="p.label">
              <dt>{{ p.label }}</dt><dd class="diag-path">{{ p.path }}</dd>
            </template>
          </dl>
        </section>
      </div>

      <!-- Configuration -->
      <div v-if="report && tab === 'config'" class="diag-panel">
        <p v-if="!report.config.project" class="diag-line">{{ NO_PROJECT }}</p>
        <template v-else>
          <section>
            <dl class="diag-dl">
              <dt>Project</dt><dd>{{ report.config.project }}</dd>
              <dt>Source</dt>
              <dd class="diag-path">{{ report.config.source || 'not set' }}
                <span v-if="report.config.source_kind" class="diag-muted">({{ sourceKind(report.config.source_kind) }})</span>
              </dd>
              <dt>Version in use</dt>
              <dd v-if="lastRun?.commit">
                <code>{{ lastRun.commit.hash.slice(0, 12) }}</code> · {{ fmtTime(lastRun.commit.date) }} · {{ lastRun.commit.subject }}
              </dd>
              <dd v-else class="diag-muted">{{ NO_COMMIT }}</dd>
              <dt>Last applied</dt>
              <dd>
                {{ lastAppliedAt }}
                <span :class="`diag-${runOutcome.tone}`">{{ runOutcome.label }}</span>
              </dd>
              <dt>Model providers and connectors</dt>
              <dd>{{ managedLabel(report.config.enabled) }}</dd>
            </dl>
            <p v-if="lastRun?.stale" class="diag-callout diag-warn">
              {{ staleLine(lastRun.stale_reason) }}
            </p>
            <p v-if="failedLine" class="diag-callout diag-bad">{{ failedLine }}</p>
            <p class="diag-muted diag-small">{{ APPLIED_NOTE }}</p>
            <div class="diag-refresh">
              <button class="btn btn-secondary" :disabled="refreshState?.running" @click="startRefresh">
                {{ refreshState?.running ? 'Refreshing…' : 'Refresh configuration' }}
              </button>
              <span v-if="refreshError" class="diag-error" role="status">{{ refreshError }}</span>
              <span v-else-if="refreshLabel" role="status"
                :class="refreshState?.running ? 'diag-muted' : refreshState?.ok ? 'diag-good' : 'diag-bad'">{{ refreshLabel }}</span>
            </div>
          </section>

          <section v-if="countRows.length">
            <h3>Applied</h3>
            <dl class="diag-dl">
              <template v-for="row in countRows" :key="row.label">
                <dt>{{ row.label }}</dt><dd>{{ row.n }}</dd>
              </template>
            </dl>
          </section>

          <section v-if="lastRun?.warnings.length">
            <h3>Warnings</h3>
            <ul class="diag-list"><li v-for="w in lastRun.warnings" :key="w" class="diag-warn">{{ w }}</li></ul>
          </section>
        </template>

        <section>
          <h3>Your own items hidden by assigned ones</h3>
          <p class="diag-muted diag-small">{{ HIDDEN_NOTE }}</p>
          <p v-if="!report.config.hidden.length" class="diag-line">None.</p>
          <ul v-else class="diag-list">
            <li v-for="h in report.config.hidden" :key="h.kind + h.name">{{ h.kind }} <strong>{{ h.name }}</strong></li>
          </ul>
        </section>

        <section>
          <h3>Old default files kept because you edited them</h3>
          <p class="diag-muted diag-small">{{ keptNote(report.opencode.config_dir) }}</p>
          <p v-if="!report.config.kept_defaults.length" class="diag-line">None.</p>
          <ul v-else class="diag-list diag-path">
            <li v-for="f in report.config.kept_defaults" :key="f">{{ f }}</li>
          </ul>
        </section>
      </div>

      <!-- Log -->
      <div v-if="tab === 'log'" class="diag-panel diag-log-panel">
        <div class="diag-log-bar">
          <select v-model="logSource" class="form-select" aria-label="Log">
            <option value="kwa">Knowledge Worker Agent</option>
            <option value="opencode">opencode</option>
          </select>
          <select v-model="logFilter" class="form-select" aria-label="Lines">
            <option value="all">All lines</option>
            <option value="problems">Warnings and errors</option>
          </select>
          <input v-model="logQuery" class="form-input" type="search" placeholder="Search" aria-label="Search the log" />
          <button class="btn btn-ghost" :disabled="logLoading" @click="loadLog">Reload</button>
          <a class="btn btn-ghost" :href="diagnosticsLogDownloadUrl(logSource)" download>Download</a>
        </div>
        <p v-if="logError" class="diag-error">{{ logError }}</p>
        <p v-else-if="log" class="diag-muted diag-small">
          <template v-if="!log.paths.length">This log doesn't exist.</template>
          <template v-else>
            {{ shownLines.length }} of the newest {{ log.lines.length }} lines<template v-if="log.truncated">; older lines are in the download</template>.
            <span class="diag-path">{{ log.paths.join(', ') }}</span>
          </template>
        </p>
        <pre ref="logEl" class="diag-log" tabindex="0" aria-label="Log lines"><template v-if="logLoading && !log">Loading…</template><template v-else><span
          v-for="(l, i) in shownLines" :key="i" :class="{ 'diag-log-problem': PROBLEM.test(l) }">{{ l }}
  </span></template></pre>
      </div>
    </div>

    <div class="modal-footer">
      <button class="btn btn-ghost" @click="refreshAll">Refresh</button>
      <button class="btn btn-ghost" :disabled="!report || copying" @click="copyReport">{{ copying ? 'Copying…' : 'Copy report' }}</button>
      <button class="btn btn-primary" @click="ui.closeModal()">Close</button>
    </div>
  </div>
</template>

<style scoped>
.diag-modal { width: 760px; display: flex; flex-direction: column; overflow: hidden; }
.diag-modal > :not(.diag-body) { flex: none; }
.diag-body { flex: 1 1 auto; min-height: 0; overflow-y: auto; display: flex; flex-direction: column; }
.diag-body > * { flex: none; }
.diag-body > .diag-log-panel { flex: 1 1 auto; min-height: 0; display: flex; flex-direction: column; }
.diag-log-panel > * { flex: none; }
/* Too short for fixed parts: scroll the whole dialog rather than hide its buttons. */
@media (max-height: 360px) {
  .diag-modal { overflow-y: auto; }
  .diag-body, .diag-body > .diag-log-panel { flex: none; overflow: visible; }
}
.diag-panel section { margin-bottom: 18px; }
.diag-panel h3 { font-size: 13px; font-weight: 700; margin: 0 0 6px; }
.diag-dl { display: grid; grid-template-columns: minmax(150px, max-content) 1fr; gap: 4px 14px; margin: 0; font-size: 13px; }
.diag-dl dt { color: var(--text2); }
.diag-dl dd { margin: 0; min-width: 0; overflow-wrap: anywhere; }
.diag-settings dt { font-family: var(--mono, ui-monospace, monospace); font-size: 12px; }
.diag-line { font-size: 13px; margin: 0 0 6px; }
.diag-list { margin: 0 0 6px; padding-left: 18px; font-size: 13px; }
.diag-sublist { margin: 2px 0 0; padding-left: 18px; list-style: circle; }
.diag-path { font-family: var(--mono, ui-monospace, monospace); font-size: 12px; overflow-wrap: anywhere; }
.diag-small { font-size: 12px; margin: 4px 0; }
.diag-muted { color: var(--text2); }
.diag-good { color: var(--green, #2e9d5b); }
.diag-warn { color: var(--yellow, #b7791f); }
.diag-bad, .diag-error { color: var(--red); }
.diag-error { font-size: 13px; }
.diag-refresh { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; margin: 10px 0 4px; font-size: 13px; }
.diag-callout { font-size: 13px; padding: 8px 10px; border: 1px solid currentColor; border-radius: 8px; margin: 8px 0; }
.diag-log-bar { display: flex; gap: 8px; align-items: center; margin-bottom: 8px; flex-wrap: wrap; }
/* The native arrow sits against the edge; draw one with room around it. */
.diag-log-bar .form-select {
  width: auto;
  appearance: none;
  padding-right: 34px;
  background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 12 12'%3E%3Cpath d='M3 4.5l3 3 3-3' fill='none' stroke='%23888' stroke-width='1.5' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E");
  background-repeat: no-repeat;
  background-position: right 12px center;
  background-size: 12px;
}
.diag-log-bar .form-input { flex: 1; min-width: 100px; }
.diag-log-bar .btn { padding: 7px 12px; }
.diag-log-bar a.btn { text-decoration: none; }
.diag-log {
  height: 46vh; min-height: 120px; overflow: auto; margin: 0;
  background: var(--surface2); border: 1px solid var(--border); border-radius: 8px;
  padding: 8px 10px; font-family: var(--mono, ui-monospace, monospace); font-size: 11.5px; line-height: 1.45;
  white-space: pre-wrap; overflow-wrap: anywhere;
}
.diag-log-panel > .diag-log { flex: 0 1 auto; }
.diag-log-problem { color: var(--red); }
</style>
