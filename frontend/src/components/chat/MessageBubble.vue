<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, onMounted, onUpdated, nextTick } from 'vue'
import { renderMarkdown, highlightCode } from '../../composables/useMarkdown'
import { formatRelativeTime, formatAbsoluteTime } from '../../utils/time'
import { useNow } from '../../composables/useNow'
import { toolActionLabel, toolSubject } from '../../utils/toolLabels'
import { formatTokens, formatCost } from '../../utils/format'
import { isTodoTool } from '../../utils/todos'
import { isSkillTool, skillNameFromArgs, isForcedSkill } from '../../utils/skills'
import type { ToolCall, UsageInfo } from '../../stores/chat'

function formatToolOutput(output: any): string {
  if (!output) return ''
  try {
    const obj = typeof output === 'string' ? JSON.parse(output) : output
    if (obj && typeof obj === 'object') {
      if (typeof obj.content === 'string') return obj.content
      if (typeof obj.detailedContent === 'string') return obj.detailedContent
    }
  } catch {}
  return typeof output === 'string' ? output : JSON.stringify(output, null, 2)
}

// The opened row is where the exact call is inspected, so the arguments are
// laid out rather than left as the single-line JSON they arrive as.
function formatToolArgs(args?: string): string {
  if (!args) return ''
  try {
    return JSON.stringify(JSON.parse(args), null, 2)
  } catch {
    return args
  }
}

const props = defineProps<{
  role: 'user' | 'assistant'
  content: string
  streaming?: boolean
  stepCount?: number
  toolCalls?: ToolCall[]
  createdAt?: string
  usage?: UsageInfo | null
  // Who a user message is from, when that is not simply "You" (a guest of a shared chat).
  authorLabel?: string
}>()

// The running label has two phases. Until the agent has taken a step or called
// a tool it is still deciding what to do, which is worth distinguishing from
// actually doing it — a long pause before any tool appears reads as stuck
// otherwise.
const busyLabel = computed(() =>
  (props.stepCount || 0) > 0 || (props.toolCalls?.length || 0) > 0
    ? 'Working on it...'
    : 'Thinking about it...',
)

const now = useNow()
const relativeTime = computed(() => formatRelativeTime(props.createdAt, now.value))
const absoluteTime = computed(() => formatAbsoluteTime(props.createdAt))

// Only for a finished assistant turn: mid-stream the numbers are still moving,
// and a user message has none. `usage` is absent for turns recorded before it
// was stored, which simply renders no bar.
const showUsage = computed(() =>
  props.role === 'assistant' && !props.streaming && !!props.usage,
)

const bubbleEl = ref<HTMLElement | null>(null)

// A long-running turn can rack up dozens of tool calls, and an open list that
// long buries the answer it belongs to. Both lists below therefore show a
// summary by default and open on click.
const toolsExpanded = ref(false)
const connectorsExpanded = ref(false)
const expandedTools = ref<Set<string>>(new Set())

// Skills the agent loaded for this turn, lifted out of the tool list. A `skill`
// call answers a different question from the rest — not "what did it do" but
// "what did it know to apply" — and buried as one row among a dozen it was easy
// to miss entirely. Shown as its own band, it confirms at a glance that the
// relevant expertise was picked up.
const skillCalls = computed(() =>
  (props.toolCalls || [])
    .filter(t => isSkillTool(t.name))
    .map(t => ({
      name: skillNameFromArgs(t.args),
      status: t.status,
      callId: t.callId,
      forced: isForcedSkill(t.args),
    })),
)

// Tools contributed by an MCP server, split out of the general list. Reaching
// into Jira or GitHub is a different kind of act from reading a local file —
// it leaves the workspace — so which connectors a turn touched is worth being
// able to see without opening anything.
const connectorTools = computed(() =>
  (props.toolCalls || []).filter(t => !!t.mcpServer && !isTodoTool(t.name)),
)

// The connector names, in the order they were first used, for the bar's label.
const connectorNames = computed(() => {
  const seen: string[] = []
  for (const t of connectorTools.value) {
    if (t.mcpServer && !seen.includes(t.mcpServer)) seen.push(t.mcpServer)
  }
  return seen
})

// Todo (`todowrite`) calls are deliberately excluded from this list. The plan
// they carry is already rendered — live and in full — by the Plan & progress
// panel, so a second copy here was duplicate information that also inflated the
// "Tools (n)" count with rows the user has no reason to open. The calls stay in
// the store untouched; that panel is what reads them.
//
// Skill and connector calls are excluded for the same reason: their own bars
// render them, and counting them here would report work this list no longer
// shows.
const displayTools = computed(() =>
  (props.toolCalls || []).filter(
    t => !isTodoTool(t.name) && !isSkillTool(t.name) && !t.mcpServer,
  ),
)

// Collapsed, the tools bar keeps the most recent row on screen: that is the one
// that says what the agent is doing right now, and it is what a reader watching
// a running turn actually wants. The connectors bar collapses all the way,
// because its header already names every server it would show.
//
// Both bars are built from one description so they render through a single row
// template below — they differ in what they hold, not in how a row looks.
interface ToolRow {
  tool: ToolCall
  key: string
  // The plain-language action, e.g. "Running a command".
  label: string
  // What it acted on, lifted out of the raw arguments.
  subject: string
  // The exact tool name. Plain language is the default, not a replacement for
  // the truth, so this stays reachable on hover and in the opened row.
  exact: string
}

interface ToolBar {
  key: 'connectors' | 'tools'
  label: string
  // What the line says while shut. The bar has to be worth reading closed,
  // otherwise it is just a row of chevrons.
  value: string
  // The subject of `value`, when it has one — set apart so it can be rendered
  // as the machine text it is, and so it is the part that truncates.
  detail: string
  expanded: boolean
  rows: ToolRow[]
}

// An MCP tool arrives namespaced as `<server>_<tool>` (`atlassian_getJiraIssue`).
// Inside the connectors bar the server is already named in the header, so the
// prefix is repeated noise on every row — it is dropped there and only there.
function bareToolName(name: string, server?: string): string {
  if (!server) return name
  const prefix = server + '_'
  return name.toLowerCase().startsWith(prefix.toLowerCase())
    ? name.slice(prefix.length)
    : name
}

// A row's identity, so that opening one keeps it open as the list grows and the
// collapsed window slides. `callId` is the real identity; the positional
// fallback only matters for rows that arrived without one.
function rowsOf(bar: ToolBar['key'], tools: ToolCall[], strip: boolean): ToolRow[] {
  return tools.map((tool, i) => {
    const bare = strip ? bareToolName(tool.name, tool.mcpServer) : tool.name
    return {
      tool,
      key: tool.callId || `${bar}#${i}`,
      label: toolActionLabel(bare),
      subject: toolSubject(bare, tool.args),
      exact: tool.name,
    }
  })
}

const toolBars = computed<ToolBar[]>(() => {
  const bars: ToolBar[] = []

  if (connectorTools.value.length) {
    const all = rowsOf('connectors', connectorTools.value, true)
    bars.push({
      key: 'connectors',
      label: 'Connectors',
      // The servers stay named whether the bar is open or shut: which systems
      // a turn reached into is the whole point of this line.
      value: connectorNames.value.join(', '),
      detail: '',
      expanded: connectorsExpanded.value,
      rows: connectorsExpanded.value ? all : [],
    })
  }

  if (displayTools.value.length) {
    const all = rowsOf('tools', displayTools.value, false)
    const newest = all[all.length - 1]
    bars.push({
      key: 'tools',
      label: `Tools (${all.length})`,
      // Shut, the line describes the newest call — what the agent is doing
      // right now. Open, the rows below say it in full, so the summary would
      // only repeat itself.
      value: toolsExpanded.value
        ? ''
        : newest.tool.status === 'running'
          ? `${newest.label}…`
          : newest.label,
      detail: toolsExpanded.value ? '' : newest.subject,
      expanded: toolsExpanded.value,
      rows: toolsExpanded.value ? all : [],
    })
  }

  return bars
})

function toggleBar(key: ToolBar['key']) {
  if (key === 'connectors') connectorsExpanded.value = !connectorsExpanded.value
  else toolsExpanded.value = !toolsExpanded.value
}

function getRendered(): string {
  if (props.role === 'user') return ''
  return renderMarkdown(props.content)
}

function toggleToolDetail(key: string) {
  if (expandedTools.value.has(key)) {
    expandedTools.value.delete(key)
  } else {
    expandedTools.value.add(key)
  }
}

// A skill that loaded needs no adornment — the line already says it was used.
// The states worth marking are the ones where it wasn't, and they read better
// spelled out than as a glyph the user has to decode.
function skillSuffix(status: ToolCall['status']): string {
  switch (status) {
    case 'running':
      return '…'
    case 'done':
      return ''
    case 'blocked':
      return ' (needs approval)'
    default:
      return ' (not loaded)'
  }
}

// Hover text for a skill. The line shows only the name, so the status gets
// spelled out here rather than left to be guessed. A skill the user armed
// the user armed reads differently from one the agent picked: the first
// confirms their request was honoured, the second reports a decision they did
// not make.
function skillStatusLabel(status: ToolCall['status'], name: string, forced: boolean): string {
  const subject = name ? `Skill "${name}"` : 'Skill'
  switch (status) {
    case 'running':
      return `${subject} is loading`
    case 'done':
      return forced
        ? `${subject} was requested for this message and applied to this answer`
        : `${subject} was selected by the agent and applied to this answer`
    case 'blocked':
      return `${subject} was not loaded — the action needs approval`
    default:
      return forced
        ? `${subject} was requested for this message but could not be loaded`
        : `${subject} could not be loaded`
  }
}

onUpdated(() => {
  nextTick(() => {
    if (bubbleEl.value && props.role === 'assistant') {
      highlightCode(bubbleEl.value)
    }
  })
})

onMounted(() => {
  if (bubbleEl.value && props.role === 'assistant') {
    highlightCode(bubbleEl.value)
  }
})
</script>

<template>
  <div class="msg-wrap">
    <div class="msg-meta">
      <div class="msg-avatar" :class="role">
        <template v-if="role === 'user'">U</template>
        <svg v-else width="13" height="13" viewBox="0 0 32 32" fill="none">
          <circle cx="12" cy="16" r="2.5" fill="white"/>
          <circle cx="20" cy="16" r="2.5" fill="white"/>
        </svg>
      </div>
      <div class="msg-role">{{ role === 'user' ? (authorLabel || 'You') : 'Knowledge Worker Agent' }}</div>
      <div v-if="relativeTime" class="msg-time" :title="absoluteTime">{{ relativeTime }}</div>
    </div>
    <!-- Skills, connectors and tools for this turn. These annotate the answer
         rather than being it, so they read as plain secondary lines above it —
         no icons, no boxes. Skills stay open (the line is the whole content);
         the other two open on click. -->
    <div v-if="skillCalls.length || toolBars.length" class="turn-bars">
      <div v-if="skillCalls.length" class="turn-bar skills">
        <span class="turn-bar-label">Skill{{ skillCalls.length > 1 ? 's' : '' }}:</span>
        <span class="turn-bar-value">
          <template v-for="(skill, i) in skillCalls" :key="skill.callId || i">
            <span
              class="skill-name"
              :class="skill.status"
              :title="skillStatusLabel(skill.status, skill.name, skill.forced)"
            >{{ skill.name || 'unnamed skill' }}{{ skillSuffix(skill.status) }}</span
            ><span v-if="i < skillCalls.length - 1">, </span>
          </template>
        </span>
      </div>

      <div v-for="bar in toolBars" :key="bar.key" class="turn-bar-group" :class="bar.key">
        <button
          type="button"
          class="turn-bar toggle"
          :aria-expanded="bar.expanded"
          @click="toggleBar(bar.key)"
        >
          <span class="turn-bar-label">{{ bar.label }}:</span>
          <span class="turn-bar-value">{{ bar.value }}</span>
          <span v-if="bar.detail" class="turn-bar-detail">{{ bar.detail }}</span>
          <span class="turn-bar-chevron">{{ bar.expanded ? '▾' : '▸' }}</span>
        </button>
        <div class="turn-bar-body" v-if="bar.rows.length">
          <div
            v-for="row in bar.rows"
            :key="row.key"
            class="tool-row"
            :class="{
              running: row.tool.status === 'running',
              'done-ok': row.tool.status === 'done',
              'done-err': row.tool.status === 'error',
              blocked: row.tool.status === 'blocked',
              expanded: expandedTools.has(row.key),
            }"
            :title="row.exact"
            @click="toggleToolDetail(row.key)"
          >
            <div class="tool-main">
              <div class="tool-name">
                {{ row.label }}
                <!-- Only worth showing when the bar covers more than one server;
                     with a single connector the header has already said it. -->
                <span v-if="bar.key === 'connectors' && connectorNames.length > 1" class="tool-server-tag">
                  {{ row.tool.mcpServer }}
                </span>
                <span v-if="row.tool.status === 'blocked'" class="tool-blocked-tag">needs approval</span>
              </div>
              <!-- Live subagent progress on a running `task` delegation row. -->
              <div
                v-if="row.tool.name === 'task' && row.tool.status === 'running'"
                class="tool-args-preview tool-progress"
              >
                subagent working<template v-if="row.tool.progressSteps"> (step {{ row.tool.progressSteps }})</template><template v-if="row.tool.progress"> — {{ row.tool.progress }}</template>
              </div>
              <div v-else-if="row.subject && !expandedTools.has(row.key)" class="tool-args-preview">{{ row.subject }}</div>
            </div>
            <div class="tool-status">
              <template v-if="row.tool.status === 'running'">⟳</template>
              <template v-else-if="row.tool.status === 'done'">✓</template>
              <template v-else-if="row.tool.status === 'blocked'">⊘</template>
              <template v-else>✗</template>
            </div>
            <div v-if="expandedTools.has(row.key)" class="tool-details" @click.stop>
              <div class="tool-detail-section">
                <div class="tool-detail-label">Tool</div>
                <pre class="tool-detail-content">{{ row.exact }}</pre>
              </div>
              <div v-if="row.tool.args" class="tool-detail-section">
                <div class="tool-detail-label">Inputs</div>
                <pre class="tool-detail-content">{{ formatToolArgs(row.tool.args) }}</pre>
              </div>
              <div v-if="row.tool.output" class="tool-detail-section">
                <div class="tool-detail-label">{{ row.tool.status === 'blocked' ? 'Reason' : 'Output' }}</div>
                <pre class="tool-detail-content">{{ formatToolOutput(row.tool.output) }}</pre>
              </div>
              <div v-if="row.tool.status === 'blocked'" class="tool-blocked-hint">
                This action requires approval. Actions set to "ask" are auto-rejected when running
                non-interactively. To allow it, enable auto-approve (YOLO) for this session or adjust the
                permission settings.
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <div
      class="msg-bubble"
      :class="role"
      ref="bubbleEl"
      v-if="role === 'user' || content || streaming"
    >
      <template v-if="role === 'user'">{{ content }}</template>
      <div v-else v-html="getRendered()"></div>
      <span v-if="streaming" class="typing-cursor">
        <span class="tspinner" aria-hidden="true"></span>
        <span class="tlabel">{{ busyLabel }}</span>
      </span>
    </div>

    <!-- What this turn cost. Shown per response, so scrolling back through a
         conversation still accounts for each one. -->
    <div v-if="showUsage" class="msg-usage">
      <span v-if="usage?.premium_reqs" class="ustat">
        <span class="dot" style="background:var(--orange)"></span>
        {{ usage.premium_reqs }} premium req{{ usage.premium_reqs > 1 ? 's' : '' }}
      </span>
      <span v-if="usage?.tokens_out" class="ustat">
        <span class="dot" style="background:var(--accent2)"></span>
        {{ formatTokens(usage.tokens_out) }} output tokens
      </span>
      <span v-if="usage?.tokens_in_reported" class="ustat">
        <span class="dot" style="background:var(--blue, #0969da)"></span>
        {{ formatTokens(usage.tokens_in) }} input tokens
      </span>
      <span v-else class="ustat">
        <span class="dot" style="background:var(--blue, #0969da)"></span>
        input tokens not reported
      </span>
      <span v-if="usage?.cost" class="ustat">
        <span class="dot" style="background:var(--green, #1a7f37)"></span>
        {{ formatCost(usage.cost) }}
      </span>
      <span v-if="usage?.tool_calls" class="ustat">
        <span class="dot" style="background:var(--purple, #8b5cf6)"></span>
        {{ usage.tool_calls }} tool call{{ usage.tool_calls > 1 ? 's' : '' }}
      </span>
      <span v-if="usage?.files_modified" class="ustat">
        <span class="dot" style="background:var(--green)"></span>
        {{ usage.files_modified }} file{{ usage.files_modified > 1 ? 's' : '' }} changed
      </span>
      <span v-if="usage?.lines_added" class="ustat">
        <span class="dot" style="background:var(--green)"></span>
        +{{ usage.lines_added }} lines
      </span>
      <span v-if="usage?.lines_removed" class="ustat">
        <span class="dot" style="background:var(--red)"></span>
        -{{ usage.lines_removed }} lines
      </span>
    </div>
  </div>
</template>

<style scoped>
.msg-wrap {
  max-width: 860px; margin: 0 auto 20px; padding: 0 24px;
  animation: fadeUp .18s ease;
}
.msg-meta { display: flex; align-items: center; gap: 8px; margin-bottom: 7px; }
.msg-avatar {
  width: 26px; height: 26px; border-radius: 50%;
  display: flex; align-items: center; justify-content: center;
  font-size: 12px; flex-shrink: 0; font-weight: 700;
}
.msg-avatar.user { background: var(--accent); color: #fff; }
.msg-avatar.assistant { background: #03a9f1; }
.msg-role { font-size: 13px; font-weight: 600; }
.msg-time { font-size: 11px; color: var(--text2); margin-left: auto; }

.msg-usage {
  display: flex; gap: 12px; flex-wrap: wrap; padding: 8px 10px; margin-top: 10px;
  background: var(--surface2); border-radius: 6px; border: 1px solid var(--border);
}
.ustat { display: flex; align-items: center; gap: 5px; font-size: 12px; color: var(--text2); }
.ustat .dot { width: 7px; height: 7px; border-radius: 50%; flex-shrink: 0; }

.msg-bubble {
  font-size: 15px; line-height: 1.75; word-break: break-word;
  padding: 14px 18px; border-radius: 10px; border: 1px solid var(--border);
}
.msg-bubble.user { background: var(--user-bg); border-color: var(--user-border); white-space: pre-wrap; }
.msg-bubble.assistant { background: var(--surface); }

/* Markdown content */
.msg-bubble :deep(p) { margin-bottom: 10px; }
.msg-bubble :deep(p:last-child) { margin-bottom: 0; }
.msg-bubble :deep(h1), .msg-bubble :deep(h2), .msg-bubble :deep(h3), .msg-bubble :deep(h4) { margin: 16px 0 8px; font-weight: 600; line-height: 1.3; }
.msg-bubble :deep(h1) { font-size: 20px; }
.msg-bubble :deep(h2) { font-size: 17px; }
.msg-bubble :deep(h3) { font-size: 15px; }
.msg-bubble :deep(code:not(pre code)) { background: var(--surface2); border: 1px solid var(--border); padding: 2px 6px; border-radius: 4px; font-size: 13px; font-family: var(--mono); }
.msg-bubble :deep(pre) { background: var(--code-bg); border: 1px solid var(--border); border-radius: 8px; overflow: hidden; margin: 12px 0; }
.msg-bubble :deep(pre code) { display: block; font-family: var(--mono); font-size: 13px; background: none !important; padding: 14px !important; overflow-x: auto; line-height: 1.55; }
.msg-bubble :deep(ul), .msg-bubble :deep(ol) { padding-left: 24px; margin-bottom: 10px; }
.msg-bubble :deep(li) { margin-bottom: 4px; }
.msg-bubble :deep(blockquote) { border-left: 3px solid var(--accent2); padding-left: 14px; color: var(--text2); margin: 12px 0; }
.msg-bubble :deep(table) { border-collapse: collapse; width: 100%; margin: 12px 0; font-size: 14px; }
.msg-bubble :deep(th), .msg-bubble :deep(td) { border: 1px solid var(--border); padding: 7px 12px; text-align: left; }
.msg-bubble :deep(th) { background: var(--surface2); font-weight: 600; }
.msg-bubble :deep(a) { color: var(--blue); text-decoration: none; }
.msg-bubble :deep(a:hover) { text-decoration: underline; }
.msg-bubble :deep(hr) { border: none; border-top: 1px solid var(--border); margin: 16px 0; }

/* Shown while the turn is still running: one spinner and a short label. The
   tool list above reports the detail, so this only says which phase the turn
   is in. */
.typing-cursor {
  display: inline-flex; align-items: center; gap: 7px;
  margin-top: 6px;
  padding: 3px 10px; border-radius: 999px;
  background: rgba(3,169,241,.10);
  border: 1px solid rgba(3,169,241,.25);
  font-size: 12px; color: var(--text2); line-height: 1; vertical-align: middle;
}
.typing-cursor .tspinner {
  width: 11px; height: 11px; flex-shrink: 0;
  border: 2px solid rgba(3,169,241,.25);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: typingSpin .7s linear infinite;
}
.typing-cursor .tlabel {
  font-weight: 500;
  letter-spacing: .02em;
}
@media (prefers-reduced-motion: reduce) {
  .typing-cursor .tspinner {
    animation: none;
  }
}
@keyframes typingSpin {
  to { transform: rotate(360deg); }
}

/* Turn bars: skills, connectors, tools. These are annotations on the answer,
   not the answer, so they are set as quiet lines of secondary text rather than
   framed panels. Anything louder competes with the response for attention,
   which is exactly backwards. */
.turn-bars {
  display: flex; flex-direction: column;
  gap: 1px; margin-bottom: 8px;
}
.turn-bar {
  display: flex; align-items: baseline; gap: 6px; width: 100%;
  padding: 2px 2px; margin: 0;
  font: inherit; font-size: 12px; line-height: 1.5;
  color: var(--text3); text-align: left;
  background: none; border: none; border-radius: 5px;
}
button.turn-bar { cursor: pointer; transition: color .12s, background .12s; }
button.turn-bar:hover { color: var(--text2); background: var(--surface2); }
.turn-bar-label { font-weight: 600; white-space: nowrap; user-select: none; }
/* Never shrinks. Flex distributes an overflow across every shrinkable item in
   proportion to its size, so while a long command takes most of the loss, the
   short action label still lost enough to collapse to "R…" — the one part of
   the line that has to survive. Capped so that a pathologically long label
   (connector actions are derived from third-party tool names) still cannot
   crowd out the subject or push the chevron off the end. */
.turn-bar-value {
  color: var(--text2); flex: 0 0 auto; max-width: 60%;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
/* The subject of the summary. Given the flexible share of the line so it is
   the only thing that truncates when space runs out — losing the tail of a
   command is survivable, losing the action it belongs to is not. */
.turn-bar-detail {
  color: var(--text3); font-family: var(--mono); font-size: 11px;
  flex: 1 1 auto; min-width: 0;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
/* Pushed to the far edge so the chevrons of stacked bars line up, which is what
   makes them read as one control column rather than three stray marks. */
.turn-bar-chevron { margin-left: auto; font-size: 10px; color: var(--text3); }
/* The skills line has no subject competing for the row, so its value is free to
   use the width and to shrink into it — the cap above exists only to stop a
   label starving a sibling that isn't there. */
.turn-bar.skills .turn-bar-value { flex: 0 1 auto; max-width: none; min-width: 0; }
.turn-bar-body { padding: 3px 0 5px; display: flex; flex-direction: column; gap: 3px; }
.skill-name { font-family: var(--mono); font-size: 11.5px; }
.skill-name.error { color: rgba(248,81,73,.95); }
.skill-name.blocked { color: rgba(219,154,4,.95); }
/* Which connector a row came from, for bars that span more than one. */
.tool-server-tag {
  font-family: var(--mono); font-size: 10px; font-weight: 600;
  color: var(--text3); background: var(--surface3);
  padding: 1px 6px; border-radius: 999px; margin-left: 6px;
}

.tool-row {
  display: flex; align-items: center; flex-wrap: wrap; gap: 0; font-size: 12px;
  border-radius: 6px; overflow: hidden; border: 1px solid var(--border);
  background: var(--surface2); transition: border-color .2s;
  cursor: pointer;
}
.tool-row:hover { background: var(--surface3); }
.tool-row.running { border-color: var(--accent); animation: toolPulse 1.5s ease-in-out infinite; }
.tool-row.done-ok { border-color: rgba(63,185,80,.4); }
.tool-row.done-err { border-color: rgba(248,81,73,.4); }
/* Blocked / needs-approval tool call: amber to distinguish from a red error. */
.tool-row.blocked { border-color: rgba(219,154,4,.55); background: rgba(219,154,4,.08); }
.tool-row.blocked:hover { background: rgba(219,154,4,.14); }
.tool-blocked-tag {
  margin-left: 6px; padding: 0 6px; border-radius: 999px;
  font-size: 10px; font-weight: 700; text-transform: uppercase; letter-spacing: .03em;
  background: rgba(219,154,4,.18); color: #b8860b;
  border: 1px solid rgba(219,154,4,.45);
}
.tool-blocked-hint {
  margin-top: 8px; padding: 8px 10px; border-radius: 6px;
  background: rgba(219,154,4,.10); border: 1px solid rgba(219,154,4,.3);
  color: var(--text2); font-size: 11px; line-height: 1.5;
}
.tool-main { flex: 1; padding: 6px 10px; min-width: 0; }
.tool-name { color: var(--text); font-weight: 600; }
/* The action is written for a person, the subject is machine text — a path, a
   command, a query. Setting only the latter in mono keeps that distinction
   visible without any decoration. */
.tool-args-preview { color: var(--text2); font-family: var(--mono); font-size: 11px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; margin-top: 1px; }
.tool-progress { color: var(--accent); font-style: italic; }
.tool-status { padding: 6px 10px; display: flex; align-items: center; font-size: 13px; flex-shrink: 0; }

/* Expanded tool details */
.tool-details {
  width: 100%; padding: 8px 12px; border-top: 1px solid var(--border);
  background: var(--surface); cursor: default;
}
.tool-detail-section { margin-bottom: 8px; }
.tool-detail-section:last-child { margin-bottom: 0; }
.tool-detail-label { font-size: 10px; font-weight: 700; color: var(--text2); text-transform: uppercase; margin-bottom: 4px; letter-spacing: .05em; }
.tool-detail-content {
  font-size: 11px; font-family: var(--mono); color: var(--text);
  white-space: pre-wrap; word-break: break-all; max-height: 200px; overflow-y: auto;
  margin: 0; padding: 6px 8px; background: var(--surface2); border-radius: 4px;
}

@keyframes toolPulse {
  0%, 100% { opacity: 1; }
  50% { opacity: .7; }
}

@keyframes blink {
  50% { opacity: 0; }
}

@keyframes fadeUp {
  from { opacity: 0; transform: translateY(8px); }
  to { opacity: 1; transform: translateY(0); }
}

@media (max-width: 768px) {
  .msg-wrap { padding: 0 12px; margin-bottom: 14px; }
  .msg-usage { gap: 8px; padding: 6px 8px; }
  .ustat { font-size: 11px; }
  .msg-bubble { font-size: 14px; padding: 11px 13px; line-height: 1.6; }
  .msg-bubble :deep(pre code) { font-size: 12px; padding: 10px !important; }
  .msg-bubble :deep(code:not(pre code)) { font-size: 12px; }
  .msg-bubble :deep(table) { font-size: 12px; display: block; overflow-x: auto; white-space: nowrap; }
  .msg-bubble :deep(h1) { font-size: 18px; }
  .msg-bubble :deep(h2) { font-size: 16px; }
  .msg-meta { gap: 6px; }
  .msg-avatar { width: 22px; height: 22px; font-size: 11px; }
  .msg-role { font-size: 12px; }
  .msg-time { font-size: 10px; }
  .tool-row { font-size: 12px; }
}
</style>
