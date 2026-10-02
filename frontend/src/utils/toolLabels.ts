// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Tool names are the agent's vocabulary, not the user's. A row reading `bash`
// over a slab of raw JSON tells someone who did not build the agent nothing
// about what just happened — and these rows exist precisely to reassure a
// reader that sensible work is going on. So each call is rendered as a plain
// action ("Running a command") plus the one argument that says what it acted
// on, lifted out of the JSON it arrived in.
//
// The exact tool name is never lost: it is on the row's hover text and shown
// in full when the row is opened. Plain language is the default, not a
// replacement for the truth.

// Built-in tools, named as the actions they are. The keys are opencode's
// tool names, lowercased.
const TOOL_ACTIONS: Record<string, string> = {
  bash: 'Running a command',
  read: 'Reading a file',
  write: 'Writing a file',
  edit: 'Editing a file',
  patch: 'Editing a file',
  multiedit: 'Editing a file',
  grep: 'Searching in files',
  glob: 'Finding files',
  list: 'Listing a folder',
  ls: 'Listing a folder',
  webfetch: 'Reading a web page',
  websearch: 'Searching the web',
  task: 'Running a sub-task',
  todowrite: 'Updating the plan',
  todoread: 'Checking the plan',
  skill: 'Loading a skill',
  question: 'Asking a question',
  ask_user: 'Asking a question',
  list_mcp_resources: 'Listing connector resources',
  list_mcp_resource_templates: 'Listing connector resources',
  invalid: 'Unrecognised tool call',
}

// The argument worth showing, per built-in. Ordered: the first key present
// wins. Only the value is rendered — the action label has already said what
// kind of thing it is, so "Reading a file  main.go" needs no "filePath:".
const TOOL_SUBJECT_KEYS: Record<string, string[]> = {
  bash: ['command'],
  read: ['filePath', 'path'],
  write: ['filePath', 'path'],
  edit: ['filePath', 'path'],
  patch: ['filePath', 'path'],
  multiedit: ['filePath', 'path'],
  grep: ['pattern'],
  glob: ['pattern'],
  list: ['path'],
  ls: ['path'],
  webfetch: ['url'],
  websearch: ['query'],
  task: ['description', 'prompt'],
  skill: ['name'],
}

// Fallback for connector (MCP) tools, whose arguments follow no single
// convention. These are the identifying keys the real connectors in use
// actually carry — Jira issue keys and JQL, GitHub paths, queries and issue
// numbers — checked before the generic sweep below.
const GENERIC_SUBJECT_KEYS = [
  'command',
  'filePath',
  'path',
  'url',
  'query',
  'jql',
  'pattern',
  'issueIdOrKey',
  'issue_number',
  'pullNumber',
  'summary',
  'title',
  'name',
  'description',
  'server',
]

// Keys that identify plumbing rather than intent. They are frequently the
// first key in a connector's argument object (`cloudId` leads every Atlassian
// call), so without this the generic sweep would surface a UUID as the most
// interesting thing about a Jira lookup.
const NOISE_KEYS = new Set([
  'cloudid',
  'fields',
  'format',
  'expand',
  'limit',
  'offset',
  'perpage',
  'timeout',
  'responsecontentformat',
  'searchresultmode',
  'sort',
  'order',
  'state',
  'ref',
  'subagent_type',
])

// Splits an identifier into words: `gh__create_pull_request` and
// `getJiraIssue` both become readable without a lookup table, which matters
// because connector tool names are defined by third parties and cannot be
// enumerated here. Original capitalisation is preserved after the first word
// so "Jira" and "PR" survive intact rather than being flattened.
function humanizeIdentifier(name: string): string {
  const words = name
    .replace(/[_\-.]+/g, ' ')
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .replace(/\s+/g, ' ')
    .trim()
  if (!words) return name
  return words.charAt(0).toUpperCase() + words.slice(1)
}

// The plain-language action for a tool call. `bare` is the tool name with any
// connector prefix already removed.
export function toolActionLabel(bare: string): string {
  const key = bare.toLowerCase().trim()
  const known = TOOL_ACTIONS[key]
  if (known) return known
  // Connectors often group their tools behind a toolset prefix marked with a
  // double underscore — GitHub ships `gh__create_pull_request` and
  // `github-copilot-chat-standard__search_repositories`. That segment names the
  // toolset, not the action, and reads as noise ("Gh create pull request").
  const grouped = bare.lastIndexOf('__')
  const action = grouped === -1 ? bare : bare.slice(grouped + 2)
  return humanizeIdentifier(action || bare)
}

function parseArgs(args?: string): Record<string, unknown> | null {
  if (!args) return null
  try {
    const parsed = JSON.parse(args)
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed)
      ? (parsed as Record<string, unknown>)
      : null
  } catch {
    return null
  }
}

function asText(value: unknown): string {
  if (typeof value === 'string') return value
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  return ''
}

// The one argument worth putting on the row. Newlines are collapsed so a
// heredoc or multi-line script stays a single line; the row is a summary, and
// the untouched original is one click away in the opened detail.
export function toolSubject(bare: string, args?: string): string {
  if (!args) return ''
  const obj = parseArgs(args)
  // Not an object: some tools send a bare string, which is already the subject.
  if (!obj) return args.replace(/\s+/g, ' ').trim()

  const preferred = TOOL_SUBJECT_KEYS[bare.toLowerCase().trim()] || GENERIC_SUBJECT_KEYS
  for (const key of preferred) {
    const text = asText(obj[key])
    if (text) return text.replace(/\s+/g, ' ').trim()
  }

  // Nothing recognised. Rather than fall back to the raw JSON — the thing this
  // whole module exists to avoid — name the arguments that carry meaning.
  const parts: string[] = []
  for (const [key, value] of Object.entries(obj)) {
    if (NOISE_KEYS.has(key.toLowerCase())) continue
    const text = asText(value)
    if (!text || text.length > 60) continue
    parts.push(`${humanizeIdentifier(key).toLowerCase()}: ${text.replace(/\s+/g, ' ').trim()}`)
    if (parts.length === 2) break
  }
  return parts.join(', ')
}
