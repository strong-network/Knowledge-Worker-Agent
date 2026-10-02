<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useUiStore } from '../../stores/ui'
import { fetchModelStats } from '../../api'
import { compactNumber, fullNumber } from '../../utils/format'

const ui = useUiStore()

interface ModelStat {
  model: string
  requests: number
  premium_reqs: number
  tokens_in: number
  tokens_out: number
  total_input: number
  total_output: number
  tool_calls: number
  files_modified: number
  lines_added: number
  lines_removed: number
  cost: number
  count: number
}

interface UsageSummary {
  total_sessions: number
  active_sessions: number
  total_requests: number
  total_tool_calls: number
  avg_tool_calls_per_session: number
  avg_tool_calls_per_active_session: number
  avg_tool_calls_per_request: number
}

interface DailyCost {
  day: string
  cost: number
  requests: number
}

const stats = ref<ModelStat[]>([])
const dailyCost = ref<DailyCost[]>([])
const summary = ref<UsageSummary>({
  total_sessions: 0,
  active_sessions: 0,
  total_requests: 0,
  total_tool_calls: 0,
  avg_tool_calls_per_session: 0,
  avg_tool_calls_per_active_session: 0,
  avg_tool_calls_per_request: 0,
})

onMounted(async () => {
  try {
    const data = await fetchModelStats()
    stats.value = data.models || []
    dailyCost.value = data.daily_cost || []
    summary.value = data.summary || summary.value
  } catch (e) {
    console.error(e)
  }
})

// Total request cost across all models (rows report a USD cost when the
// provider supplies one).
const totalCost = computed(() => stats.value.reduce((sum, s) => sum + (s.cost || 0), 0))

// Largest single-day cost in the window, for scaling the mini bars (min 1 cent
// so an all-zero week doesn't divide by zero).
const maxDailyCost = computed(() => Math.max(...dailyCost.value.map(d => d.cost || 0), 0.01))

// Sum of the daily-cost window (what's shown in the breakdown header).
const dailyCostTotal = computed(() => dailyCost.value.reduce((sum, d) => sum + (d.cost || 0), 0))

// Short weekday + day-of-month label, e.g. "Mon 7". Falls back to the raw
// YYYY-MM-DD if it can't be parsed.
function dayLabel(day: string): string {
  const d = new Date(day + 'T00:00:00')
  if (isNaN(d.getTime())) return day
  return d.toLocaleDateString(undefined, { weekday: 'short', day: 'numeric' })
}

function maxTokens(field: 'total_output'): number {
  return Math.max(...stats.value.map(s => s[field]), 1)
}

function formatAverage(value: number): string {
  return (value || 0).toFixed(1)
}

function formatCost(value: number): string {
  const v = value || 0
  if (v > 0 && v < 0.01) return `$${v.toFixed(4)}`
  return `$${v.toFixed(2)}`
}

// Whether a model row has any file-change data to show. The opencode backend
// doesn't report file/line changes (only the legacy Copilot sessions do), so
// for opencode rows these are always 0 — we hide the files/+lines/-lines pills
// in that case rather than show a misleading "0 files / +0 lines".
function hasFileChanges(s: ModelStat): boolean {
  return (s.files_modified || 0) > 0 || (s.lines_added || 0) > 0 || (s.lines_removed || 0) > 0
}
</script>

<template>
  <div class="modal stats-modal" @click.stop>
    <h2>📊 Usage Statistics</h2>
    <p class="modal-subtitle">Sessions, tool calls, token usage, cost, and file changes</p>
    <div class="summary-grid">
      <div class="summary-card">
        <span class="summary-label">Sessions</span>
        <strong>{{ fullNumber(summary.total_sessions) }}</strong>
        <small>{{ fullNumber(summary.active_sessions) }} active</small>
      </div>
      <div class="summary-card">
        <span class="summary-label">Requests</span>
        <strong>{{ compactNumber(summary.total_requests) }}</strong>
        <small>completed chats</small>
      </div>
      <div class="summary-card">
        <span class="summary-label">Tool calls</span>
        <strong>{{ compactNumber(summary.total_tool_calls) }}</strong>
        <small>{{ formatAverage(summary.avg_tool_calls_per_request) }} / request</small>
      </div>
      <div class="summary-card">
        <span class="summary-label">Avg/session</span>
        <strong>{{ formatAverage(summary.avg_tool_calls_per_session) }}</strong>
        <small>{{ formatAverage(summary.avg_tool_calls_per_active_session) }} / active</small>
      </div>
      <div class="summary-card">
        <span class="summary-label">Total cost</span>
        <strong>{{ formatCost(totalCost) }}</strong>
        <small>across all models</small>
      </div>
    </div>
    <div v-if="!stats.length" style="text-align:center;color:var(--text3);padding:20px">No model usage data yet</div>

    <div v-if="dailyCost.length" class="daily-cost">
      <div class="daily-cost-head">
        <span class="daily-cost-title">Cost — last 7 days</span>
        <span class="daily-cost-total">{{ formatCost(dailyCostTotal) }}</span>
      </div>
      <div class="daily-cost-rows">
        <div v-for="d in dailyCost" :key="d.day" class="dc-row">
          <span class="dc-day">{{ dayLabel(d.day) }}</span>
          <div class="dc-track">
            <div class="dc-fill" :style="{ width: Math.max((d.cost / maxDailyCost) * 100, d.cost > 0 ? 2 : 0) + '%' }"></div>
          </div>
          <span class="dc-amount" :class="{ zero: !d.cost }">{{ formatCost(d.cost) }}</span>
        </div>
      </div>
    </div>
    <div v-for="s in stats" :key="s.model" class="stat-model-row">
      <div class="stat-model-name">{{ s.model }} <span style="color:var(--text3);font-size:11px">({{ s.count }} requests)</span></div>
      <div class="stat-bar-row">
        <span class="stat-bar-label">Output</span>
        <div class="stat-bar-track"><div class="stat-bar-fill premium" :style="{ width: (s.total_output / maxTokens('total_output') * 100) + '%' }"></div></div>
        <span class="stat-bar-count">{{ compactNumber(s.total_output) }} tok</span>
      </div>
      <div class="stat-change-row">
        <span v-if="s.cost" class="change-pill cost">💲 {{ formatCost(s.cost) }}</span>
        <span class="change-pill tools">🛠 {{ compactNumber(s.tool_calls) }} tool call{{ s.tool_calls === 1 ? '' : 's' }}</span>
        <template v-if="hasFileChanges(s)">
          <span class="change-pill files">📄 {{ compactNumber(s.files_modified) }} file{{ s.files_modified === 1 ? '' : 's' }}</span>
          <span class="change-pill added">+{{ compactNumber(s.lines_added) }} lines</span>
          <span class="change-pill removed">-{{ compactNumber(s.lines_removed) }} lines</span>
        </template>
      </div>
    </div>
    <div class="modal-footer">
      <button class="btn btn-ghost" @click="ui.closeModal()">Close</button>
    </div>
  </div>
</template>

<style scoped>
/* Wider than the default 560px modal: five summary cards plus the per-model
   bars need the room, otherwise long values (e.g. a four-figure total cost)
   spill out of their card. */
.stats-modal { width: 780px; }
.summary-grid {
  /* The last card holds the total cost, which is the widest value by far — give
     it extra room so it doesn't wrap while the other four still fit. */
  display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)) minmax(0, 1.3fr); gap: 8px;
  margin: 12px 0 18px;
}
.summary-card {
  background: var(--surface2); border: 1px solid var(--border); border-radius: 10px;
  padding: 10px; min-width: 0; overflow: hidden;
}
.summary-label { display: block; font-size: 11px; color: var(--text2); margin-bottom: 4px; }
.summary-card strong {
  display: block; font-size: 20px; color: var(--text); line-height: 1.1;
  /* Numbers have no natural break opportunity, so without this they render
     past the card edge instead of wrapping. */
  overflow-wrap: anywhere; font-variant-numeric: tabular-nums;
}
.summary-card small { display: block; color: var(--text3); font-size: 11px; margin-top: 3px; overflow-wrap: anywhere; }
.stat-model-row { margin-bottom: 16px; }
.stat-model-name { font-size: 13px; font-weight: 600; color: var(--text); margin-bottom: 4px; }
.stat-bar-row { display: flex; align-items: center; gap: 8px; margin-bottom: 4px; }
.stat-bar-label { font-size: 11px; color: var(--text2); width: 60px; flex-shrink: 0; text-align: right; }
.stat-bar-track { flex: 1; height: 8px; background: var(--surface2); border-radius: 4px; overflow: hidden; }
.stat-bar-fill { height: 100%; background: var(--accent); border-radius: 4px; transition: width .3s ease; min-width: 2px; }
.stat-bar-fill.premium { background: var(--orange); }
.stat-bar-count { font-size: 11px; color: var(--text3); width: 70px; }
.stat-change-row { display: flex; gap: 8px; flex-wrap: wrap; margin: 8px 0 0 68px; }
.change-pill {
  padding: 3px 8px; border-radius: 999px; font-size: 11px; border: 1px solid var(--border);
  color: var(--text2); background: var(--surface2);
}
.change-pill.added { color: var(--green); background: var(--diff-add); }
.change-pill.removed { color: var(--red); background: var(--diff-del); }
.change-pill.files { color: var(--accent); }
.change-pill.tools { color: var(--purple, #8b5cf6); }
.change-pill.cost { color: var(--green, #1a7f37); font-weight: 600; }
.daily-cost {
  background: var(--surface2); border: 1px solid var(--border); border-radius: 10px;
  padding: 12px 14px; margin: 0 0 18px;
}
.daily-cost-head {
  display: flex; align-items: baseline; justify-content: space-between; margin-bottom: 10px;
}
.daily-cost-title { font-size: 12px; font-weight: 600; color: var(--text2); }
.daily-cost-total { font-size: 14px; font-weight: 700; color: var(--green, #1a7f37); }
.daily-cost-rows { display: flex; flex-direction: column; gap: 6px; }
.dc-row { display: flex; align-items: center; gap: 8px; }
.dc-day { font-size: 11px; color: var(--text2); width: 56px; flex-shrink: 0; }
.dc-track { flex: 1; height: 8px; background: var(--surface); border: 1px solid var(--border); border-radius: 4px; overflow: hidden; }
.dc-fill { height: 100%; background: var(--green, #1a7f37); border-radius: 4px; transition: width .3s ease; }
.dc-amount { font-size: 11px; color: var(--text); width: 64px; text-align: right; flex-shrink: 0; font-variant-numeric: tabular-nums; }
.dc-amount.zero { color: var(--text3); }
@media (max-width: 860px) {
  .summary-grid { grid-template-columns: repeat(3, minmax(0, 1fr)); }
}
@media (max-width: 560px) {
  .summary-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .summary-card strong { font-size: 18px; }
}
</style>
