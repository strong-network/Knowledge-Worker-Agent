// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

export interface SessionConfig {
  backend?: 'opencode'
  model?: string
  mode?: string
  workspace?: string
  workdir?: string
  system_prompt?: string
  context_files?: string[]
  permissions?: Record<string, boolean>
  yolo?: boolean
  agent?: string
}

export interface AgentInfo {
  // id is the opencode-resolvable agent identifier (file stem); this is what
  // must be sent as config.agent. name is the human-friendly display label.
  id: string
  name: string
  description: string
  filename: string
  // Which tier the agent came from: 'assigned' means the central config repo
  // gave it to this workspace, 'personal' means it lives in the user's own
  // OpenCode config. Pickers group on this so an agent nobody assigned cannot
  // pass for one IT did. Older servers omit it, so treat it as optional.
  source?: 'assigned' | 'personal'
}

// SavedPrompt is a user-owned starter chip. agentKind is the friendly
// selector choice ('default' | 'thinking' | 'custom'); agentId is the custom
// agent's opencode id when agentKind === 'custom' (empty otherwise). There is
// no raw model field — the model comes from the chosen agent/preset.
export interface SavedPrompt {
  id: string
  name: string
  prompt: string
  agentKind: 'default' | 'thinking' | 'custom'
  agentId: string
  position: number
}

interface SavedPromptWire {
  id: string
  name: string
  prompt: string
  agent_kind: string
  agent_id: string
  position: number
}

function fromWireSavedPrompt(w: SavedPromptWire): SavedPrompt {
  const kind = w.agent_kind === 'thinking' || w.agent_kind === 'custom' ? w.agent_kind : 'default'
  return {
    id: w.id,
    name: w.name,
    prompt: w.prompt,
    agentKind: kind,
    agentId: w.agent_id || '',
    position: w.position ?? 0,
  }
}

export interface Session {
  id: string
  name: string
  model: string
  mode: string
  workspace: string
  backend: 'opencode'
  agent?: string
  created_at: string
  updated_at: string
  pinned?: boolean
  favorite?: boolean
  draft?: string
  config?: SessionConfig
  // Number of messages in the session (populated by list endpoints).
  messages?: number
  // Projects: the project this chat belongs to ('' for loose chats) and the
  // project-scoped star flag (distinct from the global `favorite`).
  project_id?: string
  project_starred?: boolean
  // Scheduled tasks: the scheduled task this chat was created by ('' for normal chats).
  // Populated by list endpoints; used to exclude task runs from Recent.
  task_id?: string
  // Cross-session retrieval: how many day notes summarise this chat. Notes survive deletion by
  // default, so the delete confirmation uses this to say what is at stake
  // instead of asking abstractly. 0 unless day notes are enabled.
  note_count?: number
  // deprecated marks a legacy GitHub Copilot session. Such sessions are
  // read-only: the composer is disabled and the chat endpoint rejects turns.
  deprecated?: boolean
  // Chat sharing: shared now, how many people have opened it, and how many are here.
  shared?: boolean
  guest_count?: number
  present?: number
}

/**
 * Per-turn token/cost report. Produced by the backend `usage` SSE frame and
 * also stored on the assistant message, so a reloaded conversation can show
 * the same figures the live stream showed.
 */
export interface UsageInfo {
  tokens_in?: number
  tokens_in_reported?: boolean
  tokens_out?: number
  cost?: number
  premium_reqs?: number
  tool_calls?: number
  files_modified?: number
  lines_added?: number
  lines_removed?: number
}

export interface Message {
  role: 'user' | 'assistant'
  content: string
  // RFC3339 UTC, from the server (backend: created_at). Absent on messages
  // recorded before the server started returning it.
  createdAt?: string
  usage?: UsageInfo
  // A guest who wrote a user message. Absent means the owner.
  authorId?: string
  authorName?: string
  // The chat turn that wrote the message; the same id is on the turn's live
  // frames, so a turn already shown is recognised. Absent on older rows.
  turnId?: string
  toolCalls?: Array<{
    name: string
    callId: string
    args?: string
    status: 'running' | 'done' | 'error' | 'blocked'
    output?: string
  }>
}

export interface HistoryResponse {
  messages: Message[]
}

function toBackendSessionConfig(config: SessionConfig & { name?: string }): Record<string, unknown> {
  const payload: Record<string, unknown> = { ...config }
  if (config.name) {
    payload.label = config.name
    delete payload.name
  }
  if (config.workspace) {
    payload.workdir = config.workspace
    delete payload.workspace
  }
  return payload
}

export interface FileEntry {
  name: string
  path: string
  is_dir: boolean
  size: number
  mod_time: string
}

export interface FileViewResponse {
  name: string
  path: string
  type: 'text' | 'binary' | 'image'
  content: string
  size: number
  mime?: string
}

export interface ModelStats {
  summary: {
    total_sessions: number
    active_sessions: number
    total_requests: number
    total_tool_calls: number
    avg_tool_calls_per_session: number
    avg_tool_calls_per_active_session: number
    avg_tool_calls_per_request: number
  }
  models: Array<{
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
  }>
  daily_cost?: Array<{
    day: string
    cost: number
    requests: number
  }>
}

// ── API Client ──

const BASE = ''
export const BACKEND_UNAVAILABLE_EVENT = 'kwa-backend-unavailable'

// The one-folder migration's state, when it has something to say; sizes come
// formatted for people.
export interface MigrationInfo {
  state: 'running' | 'planned' | 'blocked' | 'failed' | 'stray' | string
  phase?: string
  done?: number
  total?: number
  minutes?: number
  need?: string
  free?: string
  short?: string
  reasons?: string[]
  path?: string
  undo?: boolean
}

export interface BackendStatus {
  status: 'starting' | 'ready' | 'degraded' | 'migrating' | string
  opencode_ready: boolean
  migration?: MigrationInfo
}

export class BackendUnavailableError extends Error {
  readonly backendUnavailable = true

  constructor(message: string) {
    super(message)
    this.name = 'BackendUnavailableError'
  }
}

export function isBackendUnavailableError(err: unknown): err is BackendUnavailableError {
  return err instanceof BackendUnavailableError ||
    (typeof err === 'object' && err !== null && (err as { backendUnavailable?: boolean }).backendUnavailable === true)
}

export function notifyBackendUnavailable(message: string) {
  if (typeof window === 'undefined') return
  window.dispatchEvent(new CustomEvent(BACKEND_UNAVAILABLE_EVENT, {
    detail: { message },
  }))
}

function backendUnavailable(message: string): BackendUnavailableError {
  const err = new BackendUnavailableError(message)
  notifyBackendUnavailable(message)
  return err
}

function isBackendDownStatus(status: number): boolean {
  return status === 0 || status === 404 || status === 502 || status === 503 || status === 504
}

function errorMessage(err: unknown, fallback: string): string {
  return err instanceof Error && err.message ? err.message : fallback
}

async function readJSONResponse<T>(res: Response, label: string): Promise<T> {
  const contentType = res.headers.get('content-type') || ''
  if (!contentType.includes('application/json')) {
    throw backendUnavailable(`${label} did not return backend JSON`)
  }
  return res.json() as Promise<T>
}

export async function fetchBackendStatus(): Promise<BackendStatus> {
  let res: Response
  try {
    res = await fetch(`${BASE}/api/status?t=${Date.now()}`, { cache: 'no-store' })
  } catch (err) {
    throw backendUnavailable(errorMessage(err, 'Backend is not reachable'))
  }
  if (!res.ok) {
    throw backendUnavailable(`Backend status check returned ${res.status}`)
  }
  const status = await readJSONResponse<BackendStatus>(res, 'Backend status check')
  if (!status || typeof status.status !== 'string' || typeof status.opencode_ready !== 'boolean') {
    throw backendUnavailable('Backend status check returned an unexpected response')
  }
  return status
}

export async function checkBackendReady(): Promise<BackendStatus> {
  const status = await fetchBackendStatus()
  if (status.status === 'migrating') {
    // Every page is the migration's status page meanwhile.
    window.location.reload()
  }
  if (status.status !== 'ready' || !status.opencode_ready) {
    throw backendUnavailable(`Backend is ${status.status || 'not ready'}`)
  }
  return status
}

// Tells the server someone is using the UI, so the workspace is not paused
// while the user reads or types between chat turns. Best-effort: the request
// carries no payload and a failure is never surfaced.
export function reportActivity(): void {
  fetch(`${BASE}/api/activity`, { method: 'POST', keepalive: true }).catch(() => {
    /* keep-alive is best-effort; never disrupt the app */
  })
}

export async function fetchSessions(): Promise<Session[]> {
  const res = await fetch(`${BASE}/api/sessions`)
  if (!res.ok) throw new Error(`Failed to fetch sessions: ${res.statusText}`)
  const data = await res.json()
  return data.map((s: any) => ({
    id: s.session_id || s.id,
    name: s.label || s.name || '',
    model: s.model || '',
    mode: s.mode || '',
    workspace: s.workdir || s.workspace || '',
    backend: 'opencode',
    agent: s.agent || '',
    created_at: s.created_at || '',
    updated_at: s.updated_at || '',
    pinned: s.pinned || false,
    favorite: s.favorite || false,
    draft: typeof s.draft === 'string' ? s.draft : '',
    messages: s.messages || 0,
    deprecated: s.deprecated === true,
    project_id: s.project_id || '',
    project_starred: s.project_starred === true,
    task_id: s.task_id || '',
    note_count: s.note_count || 0,
    shared: s.shared === true,
    guest_count: s.guest_count || 0,
    present: s.present || 0,
  }))
}

export async function createSession(config: SessionConfig & { name?: string }): Promise<Session> {
  const payload = toBackendSessionConfig(config)

  const res = await fetch(`${BASE}/api/sessions/new`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  })
  if (!res.ok) throw new Error(`Failed to create session: ${res.statusText}`)
  const data = await res.json()
  // Backend returns { session_id, config }; map to Session
  return {
    id: data.session_id,
    name: data.config?.label || config.name || '',
    model: data.config?.model || config.model || '',
    mode: data.config?.mode || config.mode || '',
    workspace: data.config?.workdir || config.workspace || '',
    backend: 'opencode',
    agent: data.config?.agent || config.agent || '',
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    pinned: false,
    favorite: false,
  }
}

/**
 * Deletes a chat.
 *
 * Cross-session retrieval: day notes describing the chat are KEPT by default, so routine cleanup
 * does not erase the record of what was worked on. Pass deleteNotes to remove
 * them in the same action -- the consent path for deleting a chat *because of
 * what is in it*, where a surviving summary would defeat the deletion.
 */
export async function deleteSession(id: string, deleteNotes = false): Promise<void> {
  const q = deleteNotes ? '?notes=delete' : ''
  const res = await fetch(`${BASE}/api/sessions/${id}${q}`, { method: 'DELETE' })
  if (!res.ok) throw new Error(`Failed to delete session: ${res.statusText}`)
}

// auto marks a rename made on the user's behalf (the placeholder derived from
// the first prompt). A deliberate rename — one the user typed — leaves it unset,
// which tells the server never to overwrite the name with a generated title.
export async function renameSession(id: string, name: string, auto = false): Promise<void> {
  const res = await fetch(`${BASE}/api/sessions/${id}/rename`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(auto ? { name, auto: true } : { name }),
  })
  if (!res.ok) throw new Error(`Failed to rename session: ${res.statusText}`)
}

export async function pinSession(id: string, pinned: boolean): Promise<void> {
  const res = await fetch(`${BASE}/api/sessions/${id}/pin`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ pinned }),
  })
  if (!res.ok) throw new Error(`Failed to pin session: ${res.statusText}`)
}

export async function favoriteSession(id: string, favorite: boolean): Promise<void> {
  const res = await fetch(`${BASE}/api/sessions/${id}/favorite`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ favorite }),
  })
  if (!res.ok) throw new Error(`Failed to favorite session: ${res.statusText}`)
}

export async function getSessionDraft(sessionId: string): Promise<string> {
  const res = await fetch(`${BASE}/api/sessions/${sessionId}/draft`)
  if (!res.ok) throw new Error(`Failed to fetch draft: ${res.statusText}`)
  const data = await res.json()
  return typeof data?.draft === 'string' ? data.draft : ''
}

export async function saveSessionDraft(sessionId: string, draft: string): Promise<void> {
  const res = await fetch(`${BASE}/api/sessions/${sessionId}/draft`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ draft }),
  })
  if (!res.ok) throw new Error(`Failed to save draft: ${res.statusText}`)
}

// Per-chat MCP connector selection.
export interface SessionConnector {
  name: string
  selected: boolean
  status: string
}

export async function getSessionMcp(sessionId: string): Promise<SessionConnector[]> {
  const res = await fetch(`${BASE}/api/sessions/${sessionId}/mcp`)
  if (!res.ok) throw new Error(`Failed to fetch connectors: ${res.statusText}`)
  const data = await res.json()
  return Array.isArray(data?.connectors) ? data.connectors : []
}

export async function setSessionMcp(
  sessionId: string,
  selection: Record<string, boolean>,
): Promise<SessionConnector[]> {
  const res = await fetch(`${BASE}/api/sessions/${sessionId}/mcp`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ selection }),
  })
  if (!res.ok) throw new Error(`Failed to update connectors: ${res.statusText}`)
  const data = await res.json()
  return Array.isArray(data?.connectors) ? data.connectors : []
}

export async function fetchHistory(sessionId: string): Promise<HistoryResponse> {
  const res = await fetch(`${BASE}/api/history?session_id=${sessionId}`)
  if (!res.ok) throw new Error(`Failed to fetch history: ${res.statusText}`)
  const data = await res.json()
  // Backend returns messages array directly, wrap it
  const raw = Array.isArray(data) ? data : (data?.messages ?? [])
  return { messages: (raw as any[]).map(toMessage) }
}

// Translates a stored message to the frontend shape (backend created_at →
// createdAt, per this codebase's naming split).
function toMessage(m: any): Message {
  return {
    role: m.role,
    content: m.content,
    createdAt: m.created_at || undefined,
    usage: m.usage || undefined,
    authorId: m.author_id || undefined,
    authorName: m.author_name || undefined,
    turnId: m.turn_id || undefined,
  }
}

export async function getSessionConfig(sessionId: string): Promise<SessionConfig> {
  const res = await fetch(`${BASE}/api/sessions/${sessionId}/config`)
  if (!res.ok) throw new Error(`Failed to get session config: ${res.statusText}`)
  const cfg = await res.json()
  if (cfg?.workdir && !cfg.workspace) cfg.workspace = cfg.workdir
  return cfg
}

export async function updateSessionConfig(sessionId: string, config: SessionConfig): Promise<void> {
  const payload = toBackendSessionConfig(config)
  const res = await fetch(`${BASE}/api/sessions/${sessionId}/config`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  })
  if (!res.ok) throw new Error(`Failed to update session config: ${res.statusText}`)
}

export async function undoLastTurn(sessionId: string): Promise<void> {
  const res = await fetch(`${BASE}/api/sessions/${sessionId}/last`, { method: 'DELETE' })
  if (!res.ok) throw new Error(`Failed to undo: ${res.statusText}`)
}

export async function exportSession(sessionId: string): Promise<any> {
  const res = await fetch(`${BASE}/api/sessions/${sessionId}/export`)
  if (!res.ok) throw new Error(`Failed to export session: ${res.statusText}`)
  return res.json()
}

// PermissionResponse is the user's decision on an interactive permission
// request raised by the backend.
export type PermissionResponse = 'once' | 'always' | 'reject'

// respondPermission answers a pending permission request for a session's
// in-flight turn. Returns false if the backend reports no active turn (404).
export async function respondPermission(
  sessionId: string,
  permissionId: string,
  response: PermissionResponse,
): Promise<boolean> {
  const res = await fetch(`${BASE}/api/sessions/${sessionId}/permission`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ permission_id: permissionId, response }),
  })
  if (!res.ok) {
    if (res.status === 404) return false
    throw new Error(`Failed to respond to permission: ${res.statusText}`)
  }
  return true
}

export async function fetchModels(): Promise<string[]> {
  const res = await fetch(`${BASE}/api/models`)
  if (!res.ok) throw new Error(`Failed to fetch models: ${res.statusText}`)
  const data = await res.json()
  return Array.isArray(data) ? data : []
}

export async function fetchAgents(): Promise<AgentInfo[]> {
  const res = await fetch(`${BASE}/api/agents`)
  if (!res.ok) throw new Error(`Failed to fetch agents: ${res.statusText}`)
  const data = await res.json()
  return Array.isArray(data) ? data : []
}

// ── Skills ─────────────────────────────────────────────────────────
// The list is OpenCode's own enumeration, proxied by the server, so the client
// never scans or maintains one. SKILL.md bodies are stripped server-side: the
// picker only needs a name and a description, and the body is injected into
// the turn on send.

export interface SkillInfo {
  id: string
  name: string
  description: string
}

// `available` distinguishes "this workspace has no skills" from "we could not
// ask" — usually because OpenCode is still starting. They need different copy;
// telling someone with a dozen skills that they have none is worse than saying
// nothing yet.
export async function fetchSkills(sessionId?: string): Promise<{ skills: SkillInfo[]; available: boolean }> {
  const qs = sessionId ? `?session_id=${encodeURIComponent(sessionId)}` : ''
  const res = await fetch(`${BASE}/api/skills${qs}`)
  if (!res.ok) return { skills: [], available: false }
  const data = await res.json()
  return {
    skills: Array.isArray(data?.skills) ? data.skills : [],
    available: data?.available !== false,
  }
}

// ── Saved prompts ──────────────────────────────────────────────────
// The New Chat starter chips are a user-owned, persisted set. The wire format
// uses snake_case agent fields; these helpers map to/from the camelCase client
// shape (mirroring the workdir/agent naming split elsewhere in this module).

export async function fetchSavedPrompts(): Promise<SavedPrompt[]> {
  const res = await fetch(`${BASE}/api/saved-prompts`)
  if (!res.ok) throw new Error(`Failed to fetch saved prompts: ${res.statusText}`)
  const data = await res.json()
  const list = (data?.saved_prompts ?? []) as SavedPromptWire[]
  return Array.isArray(list) ? list.map(fromWireSavedPrompt) : []
}

export interface SavedPromptInput {
  name: string
  prompt: string
  agentKind: 'default' | 'thinking' | 'custom'
  agentId?: string
}

async function savedPromptError(res: Response): Promise<Error> {
  let msg = res.statusText
  try {
    const body = await res.json()
    if (body?.error) msg = body.error
  } catch { /* keep statusText */ }
  return new Error(msg)
}

export async function createSavedPrompt(input: SavedPromptInput): Promise<SavedPrompt> {
  const res = await fetch(`${BASE}/api/saved-prompts`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      name: input.name,
      prompt: input.prompt,
      agent_kind: input.agentKind,
      agent_id: input.agentId || '',
    }),
  })
  if (!res.ok) throw await savedPromptError(res)
  return fromWireSavedPrompt(await res.json())
}

export async function updateSavedPrompt(id: string, input: SavedPromptInput): Promise<SavedPrompt> {
  const res = await fetch(`${BASE}/api/saved-prompts/${id}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      name: input.name,
      prompt: input.prompt,
      agent_kind: input.agentKind,
      agent_id: input.agentId || '',
    }),
  })
  if (!res.ok) throw await savedPromptError(res)
  return fromWireSavedPrompt(await res.json())
}

export async function deleteSavedPrompt(id: string): Promise<void> {
  const res = await fetch(`${BASE}/api/saved-prompts/${id}`, { method: 'DELETE' })
  if (!res.ok && res.status !== 204) throw await savedPromptError(res)
}

export async function reorderSavedPrompts(ids: string[]): Promise<SavedPrompt[]> {
  const res = await fetch(`${BASE}/api/saved-prompts/reorder`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ids }),
  })
  if (!res.ok) throw await savedPromptError(res)
  const data = await res.json()
  const list = (data?.saved_prompts ?? []) as SavedPromptWire[]
  return Array.isArray(list) ? list.map(fromWireSavedPrompt) : []
}

export async function fetchModelStats(): Promise<ModelStats> {
  const res = await fetch(`${BASE}/api/model-stats`)
  if (!res.ok) throw new Error(`Failed to fetch model stats: ${res.statusText}`)
  return res.json()
}

export interface VersionInfo {
  app: string
  buildTime: string
  version: string // opencode CLI version
}

export async function fetchVersionInfo(): Promise<VersionInfo> {
  const res = await fetch(`${BASE}/api/version`)
  if (!res.ok) return { app: 'unknown', buildTime: 'unknown', version: 'unknown' }
  return res.json()
}

// File APIs
export interface BrowseResponse {
  path: string
  parent: string
  // A folder's mod_time is the newest change at or under it.
  dirs: Array<{ name: string; path: string; mod_time?: string }>
  files: Array<{ name: string; path: string; size: number; mod_time?: string }>
}

export interface UploadedFile {
  name: string
  path: string
  size: number
}

export interface UploadResponse {
  ok: boolean
  uploaded: UploadedFile[]
}

export async function browseFiles(path: string): Promise<FileEntry[]> {
  const res = await fetch(`${BASE}/api/browse?path=${encodeURIComponent(path)}`)
  if (!res.ok) throw new Error(`Failed to browse: ${res.statusText}`)
  return browseEntries(await res.json())
}

// browseEntries flattens a browse answer into one list, folders first.
export function browseEntries(data: BrowseResponse): FileEntry[] {
  const entries: FileEntry[] = []
  for (const d of data.dirs) {
    entries.push({ name: d.name, path: d.path, is_dir: true, size: 0, mod_time: d.mod_time ?? '' })
  }
  for (const f of data.files) {
    entries.push({ name: f.name, path: f.path, is_dir: false, size: f.size, mod_time: f.mod_time ?? '' })
  }
  return entries
}

export async function viewFile(path: string): Promise<FileViewResponse> {
  const res = await fetch(`${BASE}/api/files/view?path=${encodeURIComponent(path)}`)
  if (!res.ok) throw new Error(`Failed to view file: ${res.statusText}`)
  return res.json()
}

export async function saveFile(path: string, content: string): Promise<void> {
  const res = await fetch(`${BASE}/api/files/save`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path, content }),
  })
  if (!res.ok) throw new Error(`Failed to save file: ${res.statusText}`)
}

export async function createDirectory(path: string, unique = false): Promise<string> {
  const res = await fetch(`${BASE}/api/files/directory`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path, unique }),
  })
  if (!res.ok) throw new Error(`Failed to create folder: ${res.statusText}`)
  const data = await res.json()
  return data.path
}

export async function deletePath(path: string): Promise<void> {
  const res = await fetch(`${BASE}/api/files/delete`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path }),
  })
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(text || `Failed to delete: ${res.statusText}`)
  }
}

export async function renamePath(src: string, dest: string): Promise<string> {
  const res = await fetch(`${BASE}/api/files/rename`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ src, dest }),
  })
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(text || `Failed to rename: ${res.statusText}`)
  }
  const data = await res.json()
  return data.path
}

export async function uploadFiles(files: FileList | File[], targetDir?: string): Promise<UploadResponse> {
  const formData = new FormData()
  if (targetDir) formData.append('target_dir', targetDir)
  for (let i = 0; i < files.length; i++) {
    formData.append('files', files[i])
  }
  const res = await fetch(`${BASE}/api/files/upload`, { method: 'POST', body: formData })
  if (!res.ok) throw new Error(`Failed to upload: ${res.statusText}`)
  return res.json()
}

export function downloadFileUrl(path: string): string {
  return `${BASE}/api/files/download?path=${encodeURIComponent(path)}`
}

export function rawFileUrl(path: string): string {
  return `${BASE}/api/files/raw?path=${encodeURIComponent(path)}`
}

// SSE streaming for chat
// `skill` is a skill armed in the composer for this one message. It
// travels with the prompt rather than in session config so that a turn which
// does not name a skill cannot have one.
export function streamChat(
  sessionId: string,
  prompt: string,
  onEvent: (event: string, data: any) => void,
  onDone: () => void,
  onError: (err: Error) => void,
  skill?: string,
): AbortController {
  return sseStream(`${BASE}/api/chat`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ session_id: sessionId, prompt, skill: skill || undefined }),
  }, onEvent, onDone, onError)
}

// attachChat reconnects to an in-flight (or recently-finished) chat stream
// for a session. Used after a page reload so work continues to display.
export function attachChat(
  sessionId: string,
  onEvent: (event: string, data: any) => void,
  onDone: () => void,
  onError: (err: Error) => void,
): AbortController {
  return sseStream(`${BASE}/api/sessions/${sessionId}/stream`, { method: 'GET' }, onEvent, onDone, onError)
}

// The open chat's live connection: every turn from now on, whoever
// started it. onDone means the connection closed.
export function followEvents(
  sessionId: string,
  onEvent: (event: string, data: any) => void,
  onDone: () => void,
  onError: (err: Error) => void,
): AbortController {
  return sseStream(`${BASE}/api/sessions/${encodeURIComponent(sessionId)}/events`, { method: 'GET' }, onEvent, onDone, onError)
}

export async function fetchActiveStreams(): Promise<string[]> {
  try {
    const res = await fetch(`${BASE}/api/sessions/active`)
    if (!res.ok) return []
    const data = await res.json()
    return Array.isArray(data.sessions) ? data.sessions : []
  } catch {
    return []
  }
}

// stopChat asks the server to stop an in-flight turn for a session (kills the
// backend process / aborts the remote opencode session and clears any queued
// follow-ups). Aborting the browser's SSE connection alone does NOT stop
// generation, because the server keeps the process alive so work survives a
// reload. Best-effort: failures are swallowed.
export async function stopChat(sessionId: string): Promise<void> {
  try {
    await fetch(`${BASE}/api/sessions/${sessionId}/stop`, { method: 'POST' })
  } catch {
    /* best-effort */
  }
}

// Server-side prompt queue ----------------------------------------------------

export interface ServerQueuedPrompt {
  id: string
  prompt: string
  enqueued_at: string
  // A guest who queued it; absent for the owner.
  author_name?: string
}

export interface ServerQueueState {
  session_id: string
  queue: ServerQueuedPrompt[]
  streaming: boolean
}

export async function fetchSessionQueue(sessionId: string): Promise<ServerQueueState | null> {
  try {
    const res = await fetch(`${BASE}/api/sessions/${sessionId}/queue`)
    if (!res.ok) return null
    return (await res.json()) as ServerQueueState
  } catch {
    return null
  }
}

export async function addToSessionQueue(
  sessionId: string,
  prompt: string,
  skill?: string,
): Promise<{ ok: boolean; queued: boolean; id?: string; queue_len: number } | null> {
  try {
    const res = await fetch(`${BASE}/api/sessions/${sessionId}/queue`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ prompt, skill: skill || undefined }),
    })
    if (!res.ok) return null
    return await res.json()
  } catch {
    return null
  }
}

export async function removeFromSessionQueue(sessionId: string, id: string): Promise<boolean> {
  try {
    const res = await fetch(`${BASE}/api/sessions/${sessionId}/queue/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    })
    return res.ok
  } catch {
    return false
  }
}

export async function clearSessionQueue(sessionId: string): Promise<boolean> {
  try {
    const res = await fetch(`${BASE}/api/sessions/${sessionId}/queue`, { method: 'DELETE' })
    return res.ok
  } catch {
    return false
  }
}

function sseStream(
  url: string,
  init: RequestInit,
  onEvent: (event: string, data: any) => void,
  onDone: () => void,
  onError: (err: Error) => void,
): AbortController {
  const controller = new AbortController()

  fetch(url, { ...init, signal: controller.signal })
    .then(async (res) => {
      if (!res.ok) {
        const message = `Stream failed: ${res.statusText || res.status}`
        if (isBackendDownStatus(res.status)) {
          onError(backendUnavailable(message))
        } else {
          onError(new Error(message))
        }
        return
      }
      const contentType = res.headers.get('content-type') || ''
      if (!contentType.includes('text/event-stream')) {
        onError(backendUnavailable('Chat stream endpoint did not return backend events'))
        return
      }
      const reader = res.body!.getReader()
      const decoder = new TextDecoder()
      let buffer = ''
      let currentEvent = 'message'

      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        buffer += decoder.decode(value, { stream: true })

        const lines = buffer.split('\n')
        buffer = lines.pop() || ''

        for (const line of lines) {
          if (line.startsWith('event: ')) {
            currentEvent = line.slice(7).trim()
          } else if (line.startsWith('data: ')) {
            const dataStr = line.slice(6)
            try {
              const data = JSON.parse(dataStr)
              onEvent(currentEvent, data)
            } catch {
              onEvent(currentEvent, dataStr)
            }
          } else if (line === '') {
            // Empty line = end of SSE event block, reset for next event
            currentEvent = 'message'
          }
        }
      }
      onDone()
    })
    .catch((err) => {
      if (err.name !== 'AbortError') {
        onError(backendUnavailable(errorMessage(err, 'Backend is not reachable')))
      }
    })

  return controller
}

export interface MeInfo {
  name: string
  email: string
  workspace_id?: string
  apps_domain?: string
  // Server-wide default backend for new sessions (always "opencode").
  default_backend?: 'opencode'
  // The base folder, where new chats, clones and the file view start.
  base_dir?: string
  // Chat sharing: whether chats can be shared here. Without it the Share action is
  // absent, not disabled.
  sharing_available?: boolean
  // Dictation: whether dictation works here. Without it the Dictate button is
  // absent, not disabled.
  voice_available?: boolean
}
export async function fetchMe(): Promise<MeInfo> {
  const res = await fetch(`${BASE}/api/me`)
  if (!res.ok) return { name: '', email: '' }
  return res.json()
}

// ── Sharing ──

export interface ShareParticipant {
  id: string
  name: string
  first_seen: string
  last_seen: string
  present: boolean
}

export interface ShareState {
  shared: boolean
  share_url: string
  online: boolean
  present: number
  participants: ShareParticipant[]
  // Why the link cannot be shown, when the chat is shared.
  problem?: string
  // What people in the chat may do; both off by default.
  allow_permissions: boolean
  allow_files: boolean
  // The folder guests see with files on, or why files can't be turned on.
  files_folder?: string
  files_unavailable?: string
}

export interface ShareOptions {
  allow_permissions?: boolean
  allow_files?: boolean
}

export interface ShareMember {
  id: number
  email: string
}

export interface ShareAudience {
  user_ids: number[]
  project: boolean
}

export interface AudienceState {
  members: ShareMember[]
  audience: ShareAudience
  app: boolean
}

export interface ShareActivity {
  session_id: string
  present: number
  streaming: boolean
  waiting: boolean
}

// The share routes answer failures with {error}, written for the owner.
async function shareCall<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  let data: any = null
  try { data = await res.json() } catch { /* not JSON */ }
  if (!res.ok) throw new Error(data?.error || `Sharing request failed (${res.status})`)
  return data as T
}

const sharePath = (id: string) => `/api/sessions/${encodeURIComponent(id)}/share`

export const getShare = (id: string) => shareCall<ShareState>('GET', sharePath(id))
export const startShare = (id: string) => shareCall<ShareState>('PUT', sharePath(id))
export const stopShare = (id: string) => shareCall<ShareState>('DELETE', sharePath(id))
export const setShareOptions = (id: string, o: ShareOptions) => shareCall<ShareState>('PUT', sharePath(id), o)
export const getAudience = () => shareCall<AudienceState>('GET', '/api/share/audience')
export const putAudience = (a: ShareAudience) => shareCall<AudienceState>('PUT', '/api/share/audience', a)

export async function fetchShareActivity(): Promise<ShareActivity[]> {
  const data = await shareCall<{ sessions: ShareActivity[] }>('GET', '/api/share/activity')
  return data.sessions || []
}

// ── Git sync ──

export interface GitStatus {
  is_repo: boolean
  root?: string
  branch?: string
  upstream?: string
  ahead: number
  behind: number
  dirty: boolean
  has_remote: boolean
  last_message?: string
  last_sha?: string
  remote?: string
}

export interface GitOpResult {
  success: boolean
  output: string
  error?: string
  status: GitStatus
}

export async function fetchGitStatus(path: string): Promise<GitStatus> {
  const res = await fetch(`${BASE}/api/git/status?path=${encodeURIComponent(path)}`)
  if (!res.ok) {
    return { is_repo: false, ahead: 0, behind: 0, dirty: false, has_remote: false }
  }
  return res.json()
}

export async function gitPull(path: string): Promise<GitOpResult> {
  const res = await fetch(`${BASE}/api/git/pull`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path }),
  })
  return res.json()
}

export async function gitPush(path: string): Promise<GitOpResult> {
  const res = await fetch(`${BASE}/api/git/push`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path }),
  })
  return res.json()
}

// gitFetch contacts the remote (git fetch --prune) and returns the recomputed
// status, so status.behind reflects the LIVE remote (plain fetchGitStatus only
// reads the cached tracking ref). Used by the periodic remote-refresh.
export async function gitFetch(path: string): Promise<GitOpResult> {
  const res = await fetch(`${BASE}/api/git/fetch`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path }),
  })
  return res.json()
}

export interface GitFileChange {
  path: string
  old_path?: string
  xy: string
  status: string
  staged: boolean
}

export async function fetchGitChanges(path: string): Promise<{ changes: GitFileChange[]; root: string }> {
  const res = await fetch(`${BASE}/api/git/changes?path=${encodeURIComponent(path)}`)
  if (!res.ok) return { changes: [], root: '' }
  return res.json()
}

export async function gitCommit(path: string, message: string, push = false): Promise<GitOpResult> {
  const res = await fetch(`${BASE}/api/git/commit`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path, message, push, all: true }),
  })
  return res.json()
}

export interface GitCloneResult {
  success: boolean
  workspace?: string
  output: string
  error?: string
  // Set when the workspace refused the host before cloning.
  blocked?: 'no_token' | 'restricted'
  host?: string
  // The SecurSpaces page where a token for `host` is deployed.
  token_url?: string
}

export interface RepoInfo {
  name: string
  path: string
  branch?: string
  remote?: string
}

// discoverRepos lists local git repositories found under the base workspace
// (directories containing a .git folder), for the "Connect a repository" picker.
export async function discoverRepos(): Promise<RepoInfo[]> {
  const res = await fetch(`${BASE}/api/git/repos`)
  if (!res.ok) return []
  const data = await res.json().catch(() => ({ repos: [] }))
  return Array.isArray(data.repos) ? data.repos : []
}

// cloneRepo clones a git repository into a fresh folder under the base
// workspace and returns that folder to use as the chat's working directory.
export async function cloneRepo(url: string, name?: string): Promise<GitCloneResult> {
  const res = await fetch(`${BASE}/api/git/clone`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ url, name: name || '' }),
  })
  const data = await res.json().catch(() => ({ success: false, output: '', error: `HTTP ${res.status}` }))
  return data as GitCloneResult
}

// ── MCP servers ──

export interface McpServer {
  name: string
  type?: string
  transport?: string
  command?: string
  args?: string[]
  url?: string
  env?: Record<string, string>
  headers?: Record<string, string>
  timeout?: number
  // Whether opencode connects this server at all. Disabled servers stay
  // listed but none of their tools are exposed to the assistant.
  enabled?: boolean
  // How the server authenticates, classified server-side (internal/mcp
  // AuthKind): 'none' (anonymous), 'local' (runs on this machine), 'oauth',
  // 'copilot', 'setup', or absent when unknown. Descriptive only — it never
  // affects whether a server is enabled.
  auth?: 'none' | 'local' | 'oauth' | 'copilot' | 'setup' | 'api-key' | string
  // Central connectors: true when the server comes from an artifact your organization
  // assigned, rather than from your own config. Provisioned servers can be
  // turned on and off (that choice lives in your Global and survives a
  // restart) but not edited or removed — the platform config dir is wiped and
  // recreated on every boot, so either action would silently undo itself.
  provisioned?: boolean
  // Run by Knowledge Worker Agent itself and rewritten at every start: switchable, not editable.
  owned?: boolean
  // Admin-authored text from the artifact. For 'setup' servers, where there is
  // no sign-in flow to fall back on, this is the only route forward.
  label?: string
  help?: string
  helpUrl?: string
  // Connector API keys: whether an 'api-key' server has your key stored. The key itself is
  // never sent back.
  key_saved?: boolean
  [key: string]: any
}

export interface McpAddRequest {
  name: string
  transport?: 'stdio' | 'http' | 'sse'
  command?: string
  args?: string[]
  url?: string
  env?: Record<string, string>
  headers?: Record<string, string>
  timeout?: number
}

export async function listMcpServers(): Promise<McpServer[]> {
  const res = await fetch(`${BASE}/api/mcp/servers`)
  if (!res.ok) throw new Error(`Failed to list MCP servers: ${res.statusText}`)
  const data = await res.json()
  return data.servers || []
}

export async function addMcpServer(req: McpAddRequest): Promise<void> {
  const res = await fetch(`${BASE}/api/mcp/servers`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(err.error || `Failed to add MCP server`)
  }
}

// Edit a connector in place. Unlike addMcpServer it keeps the connector's
// on/off state, and it refuses a name that isn't configured.
export async function updateMcpServer(name: string, req: Omit<McpAddRequest, 'name'>): Promise<void> {
  const res = await fetch(`${BASE}/api/mcp/servers/${encodeURIComponent(name)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(err.error || `Failed to update MCP server`)
  }
}

// Check a server before adding it. Never mutates — "Add connector"
// stays disabled until this succeeds, so the user can't store an address that
// was going to fail and then have to work out why from a row that reads "off".
//
// The payload is deliberately the same shape as McpAddRequest: the command line
// is split here, once, so the probe and the add can't disagree about how it
// tokenizes.
export interface McpProbeRequest {
  transport?: 'stdio' | 'http' | 'sse'
  url?: string
  headers?: Record<string, string>
  command?: string
  args?: string[]
  env?: Record<string, string>
}

export interface McpProbeResult {
  ok: boolean
  // The name the server reports for itself. Used to pre-fill the name field.
  name?: string
  version?: string
  tools: string[]
  // Reachable, but answered 401/403 — it will ask for sign-in once turned on.
  // Not a failure: it is worth adding.
  requires_auth: boolean
  // A sentence for the user. Never the server's response body.
  error?: string
}

export async function probeMcpServer(req: McpProbeRequest): Promise<McpProbeResult> {
  const res = await fetch(`${BASE}/api/mcp/probe`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    return { ok: false, tools: [], requires_auth: false, error: err.error || 'Check failed' }
  }
  return res.json()
}

export async function removeMcpServer(name: string): Promise<void> {
  const res = await fetch(`${BASE}/api/mcp/servers/${encodeURIComponent(name)}`, {
    method: 'DELETE',
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(err.error || `Failed to remove MCP server`)
  }
}

// Enable or disable a configured MCP server. Disabling keeps it in the list
// but stops opencode connecting it, so its tools are hidden from the assistant.
export async function setMcpServerEnabled(name: string, enabled: boolean): Promise<void> {
  const res = await fetch(`${BASE}/api/mcp/servers/${encodeURIComponent(name)}/enabled`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ enabled }),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(err.error || `Failed to update MCP server`)
  }
}

// ── MCP personal API keys ──

async function mcpKeyCall(method: 'PUT' | 'DELETE', name: string, key?: string): Promise<void> {
  const res = await fetch(`${BASE}/api/mcp/servers/${encodeURIComponent(name)}/key`, {
    method,
    headers: key === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: key === undefined ? undefined : JSON.stringify({ key }),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(err.error || 'The key could not be changed.')
  }
}

export const saveMcpServerKey = (name: string, key: string) => mcpKeyCall('PUT', name, key)
export const removeMcpServerKey = (name: string) => mcpKeyCall('DELETE', name)

// ── MCP OAuth sign-in / sign-out ──
// Drives `opencode mcp auth|logout <name>`. The auth flow prints an
// authorization URL and blocks waiting for the browser callback; the frontend
// starts it, shows the URL, and polls /auth/info until done.

export interface McpAuthInfo {
  server: string
  running: boolean
  done: boolean
  success: boolean
  verify_url?: string
  output?: string
  error?: string
  started_at?: string
}

// Reads one server's authenticated state. `fresh` forces a live check rather
// than the cache — needed right after a sign-in, when the cache can still be
// answering from before the token existed.
export async function fetchMcpServerStatus(
  name: string,
  opts?: { fresh?: boolean },
): Promise<{ server: string; authenticated: boolean }> {
  const q = opts?.fresh ? '?fresh=1' : ''
  const res = await fetch(`${BASE}/api/mcp/servers/${encodeURIComponent(name)}/status${q}`, { cache: 'no-store' })
  if (!res.ok) return { server: name, authenticated: false }
  return res.json()
}

// fetchAllMcpStatus returns the authenticated state of every known server in a
// single request. Prefer it over calling fetchMcpServerStatus in a loop:
// each per-server read can trigger its own `opencode mcp list`, so a list of N
// servers used to cost N CLI invocations to answer a question one covers.
//
// Keys are lowercased server names, matching the server-side cache. A server
// missing from the map has no known status — it is either disabled (opencode
// neither connects nor lists a disabled server) or not yet checked.
export async function fetchAllMcpStatus(opts?: { fresh?: boolean }): Promise<Record<string, boolean>> {
  const res = await fetch(`${BASE}/api/mcp/status${opts?.fresh ? '?fresh=1' : ''}`, { cache: 'no-store' })
  if (!res.ok) return {}
  const data = await res.json().catch(() => ({}))
  const out: Record<string, boolean> = {}
  for (const [name, value] of Object.entries(data.servers || {})) {
    out[name.toLowerCase()] = !!(value as { authenticated?: boolean })?.authenticated
  }
  return out
}

// Starts the flow. The two refusals — 409 when the connector is switched off,
// 400 for GitHub, which authenticates via the Copilot login and has no OAuth
// flow at all — answer `{error}` rather than an info object, so they are thrown
// with the server's own wording. Both messages already tell the user what to do
// instead; replacing them with "Sign-in failed" would be strictly worse.
export async function startMcpAuth(name: string): Promise<McpAuthInfo> {
  const res = await fetch(`${BASE}/api/mcp/servers/${encodeURIComponent(name)}/auth/start`, { method: 'POST' })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(data?.error || `Could not start sign-in (HTTP ${res.status})`)
  return data as McpAuthInfo
}

export async function fetchMcpAuthInfo(name: string): Promise<McpAuthInfo> {
  const res = await fetch(`${BASE}/api/mcp/servers/${encodeURIComponent(name)}/auth/info`)
  return res.json()
}

export async function cancelMcpAuth(name: string): Promise<McpAuthInfo> {
  const res = await fetch(`${BASE}/api/mcp/servers/${encodeURIComponent(name)}/auth/cancel`, { method: 'POST' })
  return res.json()
}

// submitMcpAuthCallback delivers the pasted OAuth callback URL to opencode's
// local listener server-side (the browser can't reach 127.0.0.1 on the server),
// completing the sign-in.
export async function submitMcpAuthCallback(
  name: string,
  callbackUrl: string,
): Promise<{ ok: boolean; status_code?: number; info?: McpAuthInfo; error?: string }> {
  const res = await fetch(`${BASE}/api/mcp/servers/${encodeURIComponent(name)}/auth/callback`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ url: callbackUrl }),
  })
  return res.json()
}

export async function logoutMcpServer(name: string): Promise<{ ok: boolean; output?: string; error?: string }> {
  const res = await fetch(`${BASE}/api/mcp/servers/${encodeURIComponent(name)}/logout`, { method: 'POST' })
  return res.json()
}

// ── Sign-in (GitHub device flow) ──
//
// The backend drives the sign-in over a PTY (GitHub device flow) and exposes a
// LoginInfo shape the login modal renders against /api/opencode/auth/*. This is
// the only sign-in flow.

export interface LoginInfo {
  running: boolean
  done: boolean
  success: boolean
  verify_url?: string
  user_code?: string
  output?: string
  error?: string
  started_at?: string
}

// ── Model providers ──
//
// One endpoint describes every provider the workspace offers, built-in or
// declared by the central config repo, so the Accounts modal, the model picker
// and the connected-state banner all read the same list instead of each
// hard-coding the two built-ins.

// How a provider is authenticated. Determines which Accounts row is rendered:
// the two built-ins keep their own login flows, `user-key` gets a key field,
// and `managed` is read-only status because the platform supplies the key.
export type ProviderAuthType = 'oauth-device' | 'gcloud-adc' | 'user-key' | 'managed'

// Why a provider is not authenticated. `secret-not-provisioned` is the one that
// matters: telling a user to sign in to a managed provider is advice they
// cannot act on.
export type ProviderReason = 'not-signed-in' | 'no-key' | 'secret-not-provisioned'

export interface ModelProvider {
  id: string
  label: string
  authType: ProviderAuthType
  authenticated: boolean
  reason?: ProviderReason
  /** Built-ins keep their bespoke sign-in flows; config-declared ones do not. */
  builtin: boolean
  /** Admin-authored help text shown on the row. */
  help?: string
  /** Optional admin-authored link to the vendor's own instructions. Server-side
   *  sanitized to https:// before it reaches us. */
  helpUrl?: string
  /** Whether the provider answered when last probed. Independent of
   *  `authenticated`: valid credentials and a blocked network path look
   *  identical to an auth check. Absent means never probed. */
  reachability?: 'ok' | 'unreachable'
  /** The provider's own failure message — the only part a user can act on. */
  reachabilityNote?: string
  /** RFC 3339, absent when never probed. */
  reachabilityCheckedAt?: string
  /** Whether this workspace can discard the provider's credentials itself. */
  canSignOut?: boolean
}

export interface ProviderRegistry {
  /**
   * `builtin` — the shipped providers. `central` — declared by the config repo.
   * `fallback` — central was requested but nothing usable resolved, so the
   * built-ins are being offered instead and `fallbackReason` says why.
   */
  mode: 'builtin' | 'central' | 'fallback'
  providers: ModelProvider[]
  fallbackReason?: string
  /**
   * Default/Thinking nominations already resolved by the server against the
   * discovered model list. Empty in built-in mode, where the frontend's Claude
   * heuristic takes over.
   */
  presets: Record<string, string>
}

export async function fetchProviders(): Promise<ProviderRegistry> {
  let res: Response
  try {
    res = await fetch(`${BASE}/api/providers`, { cache: 'no-store' })
  } catch (err) {
    throw backendUnavailable(errorMessage(err, 'Backend is not reachable'))
  }
  if (!res.ok) {
    if (isBackendDownStatus(res.status)) {
      throw backendUnavailable(`Provider list returned ${res.status}`)
    }
    return { mode: 'builtin', providers: [], presets: {} }
  }
  return readJSONResponse<ProviderRegistry>(res, 'Provider list')
}

export async function setProviderKey(id: string, key: string): Promise<{ error?: string }> {
  const res = await fetch(`${BASE}/api/providers/${encodeURIComponent(id)}/key`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ key }),
  })
  return res.json()
}

export async function removeProviderKey(id: string): Promise<{ error?: string }> {
  const res = await fetch(`${BASE}/api/providers/${encodeURIComponent(id)}/key`, {
    method: 'DELETE',
  })
  return res.json()
}

export async function signOutProvider(id: string): Promise<{ error?: string }> {
  const res = await fetch(`${BASE}/api/providers/${encodeURIComponent(id)}/signout`, {
    method: 'POST',
  })
  return res.json()
}

export interface ProviderProbeResult {
  ok?: boolean
  reachability?: 'ok' | 'unreachable'
  reachabilityNote?: string
  error?: string
}

// Re-tests one provider now. Slow by nature — it makes a real request to the
// provider — so the caller should show a pending state rather than block.
export async function probeProvider(id: string): Promise<ProviderProbeResult> {
  const res = await fetch(`${BASE}/api/providers/${encodeURIComponent(id)}/probe`, {
    method: 'POST',
  })
  return res.json()
}

// The per-provider status fetchers that used to live here are gone: the
// registry above reports every provider's authenticated state in one call, so
// polling one endpoint per provider would have meant adding a request — and a
// store — for each new provider, which is the coupling the registry removes. The login
// flows below are unchanged; only status reporting moved.

export async function startOpencodeLogin(): Promise<LoginInfo> {
  const res = await fetch(`${BASE}/api/opencode/auth/login/start`, { method: 'POST' })
  return res.json()
}

export async function fetchOpencodeLoginInfo(): Promise<LoginInfo> {
  const res = await fetch(`${BASE}/api/opencode/auth/login/info`)
  return res.json()
}

export async function cancelOpencodeLogin(): Promise<LoginInfo> {
  const res = await fetch(`${BASE}/api/opencode/auth/login/cancel`, { method: 'POST' })
  return res.json()
}

// ── Sign-in with Google Cloud (Vertex AI / gcloud ADC) ──
//
// The backend drives `gcloud auth application-default login` over stdin/stdout
// pipes: it surfaces a consent URL (verify_url), then waits (awaiting_code) for
// the verification code the user pastes from their browser, which we POST back
// to /api/vertex/auth/login/code. This authenticates opencode's google-vertex
// provider and coexists with the GitHub sign-in above.

export interface VertexLoginInfo extends LoginInfo {
  awaiting_code?: boolean
}

export async function startVertexLogin(): Promise<VertexLoginInfo> {
  const res = await fetch(`${BASE}/api/vertex/auth/login/start`, { method: 'POST' })
  return res.json()
}

export async function fetchVertexLoginInfo(): Promise<VertexLoginInfo> {
  const res = await fetch(`${BASE}/api/vertex/auth/login/info`)
  return res.json()
}

export async function submitVertexLoginCode(code: string): Promise<VertexLoginInfo> {
  const res = await fetch(`${BASE}/api/vertex/auth/login/code`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ code }),
  })
  return res.json()
}

export async function cancelVertexLogin(): Promise<VertexLoginInfo> {
  const res = await fetch(`${BASE}/api/vertex/auth/login/cancel`, { method: 'POST' })
  return res.json()
}

// ── GitHub MCP token (Personal Access Token) ──
//
// The GitHub MCP server needs a properly-scoped token to read/write PRs, issues,
// and repos. The login token only carries read:user, so we let the user supply a
// PAT. The backend prefers env → stored PAT → login token, and reports which is
// active plus the token's validated scopes.

export type GitHubTokenSource = 'env' | 'stored' | 'login' | 'none'

export interface GitHubPatStatus {
  source: GitHubTokenSource
  has_stored: boolean
  login?: string
  scopes: string[]
  can_read_prs: boolean
  error?: string
}

export async function fetchGitHubPatStatus(): Promise<GitHubPatStatus> {
  const res = await fetch(`${BASE}/api/github/pat/status`, { cache: 'no-store' })
  return res.json()
}

export async function setGitHubPat(token: string): Promise<{ ok?: boolean; error?: string; check?: any }> {
  const res = await fetch(`${BASE}/api/github/pat`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ token }),
  })
  return res.json()
}

export async function clearGitHubPat(): Promise<{ ok?: boolean; error?: string; source?: GitHubTokenSource }> {
  const res = await fetch(`${BASE}/api/github/pat`, { method: 'DELETE' })
  return res.json()
}

// ── Projects ────────────────────────────────────────────────────────
export interface Project {
  id: string
  name: string
  description: string
  workspace_path: string
  repo_url: string
  instructions: string
  data_sources: string[]
  mcp_selection?: Record<string, boolean>
  chat_count: number
  created_at: string
  updated_at: string
}

export async function fetchProjects(): Promise<Project[]> {
  const res = await fetch(`${BASE}/api/projects`)
  if (!res.ok) throw new Error(`Failed to fetch projects: ${res.statusText}`)
  const data = await res.json()
  return Array.isArray(data.projects) ? data.projects : []
}

export async function fetchProject(id: string): Promise<Project> {
  const res = await fetch(`${BASE}/api/projects/${id}`)
  if (!res.ok) throw new Error(`Failed to fetch project: ${res.statusText}`)
  return res.json()
}

// getProjectMcp returns the project's shared connector selection + live status.
// Shape matches SessionConnector.
export async function getProjectMcp(projectId: string): Promise<SessionConnector[]> {
  const res = await fetch(`${BASE}/api/projects/${projectId}/mcp`)
  if (!res.ok) throw new Error(`Failed to fetch project connectors: ${res.statusText}`)
  const data = await res.json()
  return Array.isArray(data?.connectors) ? data.connectors : []
}

// setProjectMcp updates the project's shared connector selection. The change
// applies to every chat in the project.
export async function setProjectMcp(
  projectId: string,
  selection: Record<string, boolean>,
): Promise<SessionConnector[]> {
  const res = await fetch(`${BASE}/api/projects/${projectId}/mcp`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ selection }),
  })
  if (!res.ok) throw new Error(`Failed to update project connectors: ${res.statusText}`)
  const data = await res.json()
  return Array.isArray(data?.connectors) ? data.connectors : []
}

export async function createProject(input: {
  name: string
  description?: string
  repo_url?: string
}): Promise<Project> {
  const res = await fetch(`${BASE}/api/projects`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) {
    throw new Error(data?.error || `Failed to create project: ${res.statusText}`)
  }
  return data as Project
}

export async function updateProject(
  id: string,
  patch: Partial<Pick<Project, 'name' | 'description' | 'instructions' | 'data_sources'>>,
): Promise<Project> {
  const res = await fetch(`${BASE}/api/projects/${id}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(patch),
  })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(data?.error || `Failed to update project: ${res.statusText}`)
  return data as Project
}

// deleteProject removes a project. deleteChats also deletes the project's chats
// (default: they become loose chats). The shared workspace directory is left
// on disk regardless.
export async function deleteProject(id: string, deleteChats = false): Promise<void> {
  const q = deleteChats ? '?delete_chats=true' : ''
  const res = await fetch(`${BASE}/api/projects/${id}${q}`, { method: 'DELETE' })
  if (!res.ok) throw new Error(`Failed to delete project: ${res.statusText}`)
}

export async function fetchProjectChats(id: string): Promise<Session[]> {
  const res = await fetch(`${BASE}/api/projects/${id}/chats`)
  if (!res.ok) throw new Error(`Failed to fetch project chats: ${res.statusText}`)
  const data = await res.json()
  const list = Array.isArray(data.sessions) ? data.sessions : []
  return list.map((s: any) => ({
    id: s.session_id || s.id,
    name: s.label || s.name || '',
    model: s.model || '',
    mode: s.mode || '',
    workspace: s.workdir || s.workspace || '',
    backend: 'opencode',
    agent: s.agent || '',
    created_at: s.created_at || '',
    updated_at: s.updated_at || '',
    pinned: s.pinned || false,
    favorite: s.favorite || false,
    draft: typeof s.draft === 'string' ? s.draft : '',
    messages: s.messages || 0,
    deprecated: s.deprecated === true,
    project_id: id,
    project_starred: s.project_starred === true,
  }))
}

// createProjectChat starts a chat inside a project (shared workspace, inherited
// context). Returns the new session id.
export async function createProjectChat(projectId: string): Promise<string> {
  const res = await fetch(`${BASE}/api/projects/${projectId}/chats`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: '{}',
  })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(data?.error || `Failed to create project chat: ${res.statusText}`)
  return data.session_id as string
}

export async function setProjectStar(sessionId: string, starred: boolean): Promise<void> {
  const res = await fetch(`${BASE}/api/sessions/${sessionId}/project-star`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ starred }),
  })
  if (!res.ok) throw new Error(`Failed to set project star: ${res.statusText}`)
}

// ── Project summarize ──

export interface SummarizeCandidate {
  session_id: string
  title: string
  messages: number
  updated_at: string
  active_last_week: boolean
}

export async function fetchSummarizeCandidates(projectId: string): Promise<SummarizeCandidate[]> {
  const res = await fetch(`${BASE}/api/projects/${projectId}/summarize/candidates`)
  const data = await res.json().catch(() => ({ chats: [] }))
  if (!res.ok) throw new Error(data?.error || `Failed to load chats: ${res.statusText}`)
  return Array.isArray(data.chats) ? data.chats : []
}

export interface SummarizeResult {
  ok: boolean
  session_id?: string
  file?: string
  chats_included?: number
  error?: string
}

export async function runSummarize(
  projectId: string,
  sessionIds: string[],
  instructions?: string,
): Promise<SummarizeResult> {
  const res = await fetch(`${BASE}/api/projects/${projectId}/summarize`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ session_ids: sessionIds, instructions: instructions || '' }),
  })
  const data = await res.json().catch(() => ({ ok: false, error: `HTTP ${res.status}` }))
  if (!res.ok) throw new Error(data?.error || `Summarize failed: ${res.statusText}`)
  return data as SummarizeResult
}

// ─────────────────────────────────────────────────────────────────────────────
// Agent Builder
//
// A separate management namespace (`/api/agent-builder/agents`) from the
// read-only selector feed (`/api/agents`). The backend owns the file's location
// and format; the frontend sends/receives one typed shape (AgentDraft) that
// mirrors internal/agents.agentPayload.
// ─────────────────────────────────────────────────────────────────────────────

// The three permission actions OpenCode recognizes.
export type PermissionAction = 'allow' | 'ask' | 'deny'

// The agent's OpenCode mode.
export type AgentMode = 'subagent' | 'primary' | 'all'

// The fixed set of 15 capability keys OpenCode's permission schema defines.
// Order matches internal/agentbuilder.PermissionCapabilities (canonical order).
export const AGENT_CAPABILITIES = [
  'read', 'edit', 'glob', 'grep', 'list', 'bash', 'task',
  'external_directory', 'todowrite', 'question', 'webfetch',
  'websearch', 'lsp', 'doom_loop', 'skill',
] as const
export type AgentCapability = (typeof AGENT_CAPABILITIES)[number]

// Permission block. `global` is the top-level "*" wildcard default (empty = no
// "*" key written, inherit defaults). `rules` holds per-capability overrides.
export interface AgentPermission {
  global?: PermissionAction | ''
  rules?: Partial<Record<AgentCapability, PermissionAction>>
}

// AgentDraft is the full editable agent shape sent to POST/PUT and returned by
// GET {id}. Optional numeric fields are null when unset (omitted from the file).
export interface AgentDraft {
  name: string
  description: string
  prompt: string
  model?: string
  mode?: AgentMode | ''
  temperature?: number | null
  top_p?: number | null
  steps?: number | null
  color?: string
  hidden?: boolean
  disable?: boolean
  permission?: AgentPermission | null
}

// AgentListItem is the summary row returned by the list endpoint and on save.
export interface AgentListItem {
  id: string
  name: string
  description: string
  filename: string
  hidden: boolean
  disable: boolean
}

// A single field-level validation problem (HTTP 422 body).
export interface AgentFieldError {
  field: string
  message: string
}

// AgentApiError carries the structured problem detail the builder API returns so
// the UI can surface field errors (422) and name collisions (409) precisely.
export class AgentApiError extends Error {
  status: number
  fields?: AgentFieldError[]
  slug?: string
  constructor(status: number, message: string, fields?: AgentFieldError[], slug?: string) {
    super(message)
    this.name = 'AgentApiError'
    this.status = status
    this.fields = fields
    this.slug = slug
  }
}

async function agentApiError(res: Response): Promise<AgentApiError> {
  const data = await res.json().catch(() => ({}))
  return new AgentApiError(
    res.status,
    data?.error || `Request failed: ${res.statusText}`,
    Array.isArray(data?.fields) ? data.fields : undefined,
    typeof data?.slug === 'string' ? data.slug : undefined,
  )
}

export async function listBuilderAgents(): Promise<AgentListItem[]> {
  const res = await fetch(`${BASE}/api/agent-builder/agents`)
  if (!res.ok) throw await agentApiError(res)
  const data = await res.json()
  return Array.isArray(data?.agents) ? data.agents : []
}

export async function getBuilderAgent(id: string): Promise<AgentDraft> {
  const res = await fetch(`${BASE}/api/agent-builder/agents/${encodeURIComponent(id)}`)
  if (!res.ok) throw await agentApiError(res)
  return (await res.json()) as AgentDraft
}

export async function createBuilderAgent(draft: AgentDraft): Promise<AgentListItem> {
  const res = await fetch(`${BASE}/api/agent-builder/agents`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(draft),
  })
  if (!res.ok) throw await agentApiError(res)
  return (await res.json()) as AgentListItem
}

export async function updateBuilderAgent(id: string, draft: AgentDraft): Promise<AgentListItem> {
  const res = await fetch(`${BASE}/api/agent-builder/agents/${encodeURIComponent(id)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(draft),
  })
  if (!res.ok) throw await agentApiError(res)
  return (await res.json()) as AgentListItem
}

export async function deleteBuilderAgent(id: string): Promise<void> {
  const res = await fetch(`${BASE}/api/agent-builder/agents/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })
  if (!res.ok) throw await agentApiError(res)
}

// Export triggers a browser download of the agent's `.md` (exactly what
// OpenCode consumes). Uses a blob so the Content-Disposition filename is honored.
export async function exportBuilderAgent(id: string, filename: string): Promise<void> {
  const res = await fetch(`${BASE}/api/agent-builder/agents/${encodeURIComponent(id)}/export`)
  if (!res.ok) throw await agentApiError(res)
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename || `${id}.md`
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

// ImportPreview mirrors internal/agents.ImportPreview: the pre-adopt summary of
// an uploaded file. `valid` is false (with `errors`) when the file is malformed.
export interface ImportPreview {
  name: string
  slug: string
  filename: string
  description: string
  prompt: string
  permission: AgentPermission | null
  requires: string
  model: string
  model_available: boolean
  collision: boolean
  valid: boolean
  errors: AgentFieldError[]
}

export async function previewImportAgent(
  filename: string, content: string, name?: string,
): Promise<ImportPreview> {
  const res = await fetch(`${BASE}/api/agent-builder/import/preview`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ filename, content, name: name || '' }),
  })
  if (!res.ok) throw await agentApiError(res)
  return (await res.json()) as ImportPreview
}

export async function commitImportAgent(
  filename: string, content: string, name?: string,
): Promise<AgentListItem> {
  const res = await fetch(`${BASE}/api/agent-builder/import`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ filename, content, name: name || '' }),
  })
  if (!res.ok) throw await agentApiError(res)
  return (await res.json()) as AgentListItem
}

// Writing assists: one-shot model turns that rewrite the prompt or description.
// Both return only the improved text; the caller decides whether to apply it.
export async function polishAgentPrompt(
  input: { name?: string; description?: string; prompt: string },
): Promise<string> {
  const res = await fetch(`${BASE}/api/agent-builder/assist/prompt`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
  if (!res.ok) throw await agentApiError(res)
  return ((await res.json()) as { text: string }).text
}

export async function improveAgentDescription(
  input: { name?: string; description: string; prompt?: string },
): Promise<string> {
  const res = await fetch(`${BASE}/api/agent-builder/assist/description`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
  if (!res.ok) throw await agentApiError(res)
  return ((await res.json()) as { text: string }).text
}

// Chat composer "Optimize my prompt": a one-shot model turn that
// rewrites the user's draft to be clearer and more specific, returning only the
// improved text. The caller replaces the composer draft with it; nothing is sent.
export async function optimizeChatPrompt(prompt: string): Promise<string> {
  const res = await fetch(`${BASE}/api/chat/assist/prompt`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ prompt }),
  })
  if (!res.ok) throw await agentApiError(res)
  return ((await res.json()) as { text: string }).text
}

// ─────────────────────────────────────────────────────────────────────────────
// Dictation
// ─────────────────────────────────────────────────────────────────────────────

// A failed dictation call. `code` is the server's `error` field (`in_use`,
// `no_speech`, `engine_failed`, `not_found`, …) or `network` when the server
// could not be reached. `text` is the final text the server kept when the
// engine failed part-way through.
export class DictationError extends Error {
  constructor(readonly code: string, readonly status: number, readonly text = '') {
    super(code)
    this.name = 'DictationError'
  }
}

// The transcript so far. `final` never changes once returned; `live` is the
// guess at the last few seconds and may. `limit` means the two-minute cap was
// reached and the server will take no more audio.
export interface DictationProgress {
  final: string
  live: string
  limit: boolean
}

async function dictationFetch(path: string, init: RequestInit): Promise<Response> {
  let res: Response
  try {
    res = await fetch(`${BASE}/api/voice/dictations${path}`, init)
  } catch (e) {
    if ((e as Error).name === 'AbortError') throw e
    throw new DictationError('network', 0)
  }
  if (res.ok) return res
  let body: { error?: string; text?: string } = {}
  try { body = await res.json() } catch { /* not JSON, e.g. a proxy's error page */ }
  throw new DictationError(body.error || `http_${res.status}`, res.status, body.text || '')
}

export async function startDictation(): Promise<string> {
  const res = await dictationFetch('', { method: 'POST' })
  return ((await res.json()) as { id: string }).id
}

// Sends 16 kHz mono 16-bit little-endian samples and returns the transcript
// so far. Uploads must not overlap.
export async function sendDictationAudio(id: string, samples: Int16Array<ArrayBuffer>): Promise<DictationProgress> {
  const res = await dictationFetch(`/${encodeURIComponent(id)}/audio`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/octet-stream' },
    body: samples,
  })
  return (await res.json()) as DictationProgress
}

export async function stopDictation(id: string): Promise<string> {
  const res = await dictationFetch(`/${encodeURIComponent(id)}/stop`, { method: 'POST' })
  return ((await res.json()) as { text: string }).text
}

// Best effort: the server discards an abandoned dictation by itself after
// ten seconds without audio. keepalive lets it outlive a closing tab.
export function discardDictation(id: string): void {
  fetch(`${BASE}/api/voice/dictations/${encodeURIComponent(id)}`, { method: 'DELETE', keepalive: true })
    .catch(() => { /* the idle timeout covers it */ })
}

// ─────────────────────────────────────────────────────────────────────────────
// Scheduled tasks
// ─────────────────────────────────────────────────────────────────────────────

// Repeat cadence for a scheduled task. Mirrors the backend Repeat constants.
export type TaskRepeat = 'none' | 'daily' | 'weekdays' | 'weekly'

export interface ScheduledTask {
  id: string
  name: string
  prompt: string
  workdir: string
  repeat: TaskRepeat
  // Model id (provider/model) for this task's runs; empty = workspace default.
  model: string
  first_run_at: string
  next_run_at: string
  enabled: boolean
  pending_catchup_at: string
  created_at: string
  updated_at: string
  run_count: number
  // Human-readable schedule summary computed by the backend.
  summary: string
}

export interface TaskRun {
  id: string
  task_id: string
  session_id: string
  // scheduled | catch-up | manual
  trigger: string
  // running | succeeded | failed
  status: string
  error: string
  opened: boolean
  scheduled_for: string
  created_at: string
  updated_at: string
}

// The create/update request body. Times are RFC3339 in workspace-local time.
export interface ScheduledTaskInput {
  name?: string
  prompt: string
  workdir?: string
  repeat: TaskRepeat
  // Model id (provider/model); omit or empty for the workspace default.
  model?: string
  first_run_at: string
  enabled?: boolean
}

async function scheduledTaskError(res: Response): Promise<Error> {
  try {
    const data = await res.json()
    if (data && typeof data.error === 'string') return new Error(data.error)
  } catch { /* fall through */ }
  return new Error(`Request failed: ${res.statusText}`)
}

export async function fetchScheduledTasks(): Promise<ScheduledTask[]> {
  const res = await fetch(`${BASE}/api/scheduled-tasks`)
  if (!res.ok) throw await scheduledTaskError(res)
  const data = await res.json()
  return (data.tasks || []) as ScheduledTask[]
}

export async function createScheduledTask(input: ScheduledTaskInput): Promise<ScheduledTask> {
  const res = await fetch(`${BASE}/api/scheduled-tasks`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
  if (!res.ok) throw await scheduledTaskError(res)
  return (await res.json()) as ScheduledTask
}

export async function updateScheduledTask(
  id: string, input: ScheduledTaskInput,
): Promise<ScheduledTask> {
  const res = await fetch(`${BASE}/api/scheduled-tasks/${id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
  if (!res.ok) throw await scheduledTaskError(res)
  return (await res.json()) as ScheduledTask
}

export async function deleteScheduledTask(id: string): Promise<void> {
  const res = await fetch(`${BASE}/api/scheduled-tasks/${id}`, { method: 'DELETE' })
  if (!res.ok) throw await scheduledTaskError(res)
}

export async function setScheduledTaskEnabled(
  id: string, enabled: boolean,
): Promise<ScheduledTask> {
  const res = await fetch(`${BASE}/api/scheduled-tasks/${id}/enabled`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ enabled }),
  })
  if (!res.ok) throw await scheduledTaskError(res)
  return (await res.json()) as ScheduledTask
}

export async function runScheduledTaskNow(id: string): Promise<void> {
  const res = await fetch(`${BASE}/api/scheduled-tasks/${id}/run`, { method: 'POST' })
  if (!res.ok) throw await scheduledTaskError(res)
}

// Resolve a pending missed run: 'run' executes it now, 'skip' discards it.
export async function resolveScheduledTaskPending(
  id: string, action: 'run' | 'skip',
): Promise<ScheduledTask> {
  const res = await fetch(`${BASE}/api/scheduled-tasks/${id}/pending`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ action }),
  })
  if (!res.ok) throw await scheduledTaskError(res)
  return (await res.json()) as ScheduledTask
}

export async function fetchScheduledTaskRuns(id: string): Promise<TaskRun[]> {
  const res = await fetch(`${BASE}/api/scheduled-tasks/${id}/runs`)
  if (!res.ok) throw await scheduledTaskError(res)
  const data = await res.json()
  return (data.runs || []) as TaskRun[]
}

export async function markTaskRunOpened(runId: string): Promise<void> {
  const res = await fetch(`${BASE}/api/scheduled-tasks/runs/${runId}/opened`, { method: 'POST' })
  if (!res.ok) throw await scheduledTaskError(res)
}

// ── Workspace hygiene ──

/** A chat that has been idle long enough to be worth a second look. */
export interface TidyChat {
  kind: 'chat'
  id: string
  label: string
  /** RFC3339 timestamp of the last activity — the thing the user decides on. */
  last_activity: string
  idle_days: number
  message_count: number
  /** Size of the folder that would be removed with it; 0 when there is none. */
  size_bytes: number
  /** Empty when deleting the chat would not remove any folder. */
  workdir: string
  /** The user's own "this one matters" flag — shown as a star in the sidebar. */
  starred: boolean
}

/** A chat folder left behind on disk with no chat to attribute it to. */
export interface TidyOrphan {
  path: string
  name: string
  size_bytes: number
  modified_at: string
}

export interface TidyReview {
  items: TidyChat[]
  orphans: TidyOrphan[]
  /** How many chats are shown. */
  count: number
  /** How many qualified in total — larger than count when the list is capped. */
  total: number
  limit: number
  /** The idle window, as a Go duration string (e.g. "720h0m0s"). */
  review_after: string
  enabled: boolean
}

export interface TidyCleanupResult {
  kind: string
  id?: string
  path?: string
  removed: boolean
  error?: string
}

/**
 * How many chats are waiting to be reviewed, for the sidebar badge.
 *
 * Separate from fetchTidyReview because the full list measures directories on
 * disk; the badge is fetched on every app load and must not pay for that.
 */
export async function fetchTidyCount(): Promise<number> {
  const res = await fetch(`${BASE}/api/tidyup/count`)
  if (!res.ok) throw new Error(`Failed to load the review count: ${res.statusText}`)
  const data = await res.json()
  return data.enabled === true ? (data.count || 0) : 0
}

export async function fetchTidyReview(): Promise<TidyReview> {
  const res = await fetch(`${BASE}/api/tidyup`)
  if (!res.ok) throw new Error(`Failed to load the review list: ${res.statusText}`)
  const data = await res.json()
  return {
    items: data.items || [],
    orphans: data.orphans || [],
    count: data.count || 0,
    total: data.total || 0,
    limit: data.limit || 0,
    review_after: data.review_after || '',
    enabled: data.enabled === true,
  }
}

/**
 * Delete the selected chats and orphaned folders.
 *
 * The server reports per item rather than failing the batch, so one item it
 * refuses (a folder already gone, or one since claimed by a chat) does not
 * silently discard the rest of the user's selection.
 */
export async function cleanupTidyItems(
  items: Array<{ kind: 'chat'; id: string } | { kind: 'orphan'; path: string }>,
): Promise<{ results: TidyCleanupResult[]; removed: number }> {
  const res = await fetch(`${BASE}/api/tidyup/cleanup`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ items }),
  })
  if (!res.ok) throw new Error(`Cleanup failed: ${res.statusText}`)
  const data = await res.json()
  return { results: data.results || [], removed: data.removed || 0 }
}

/** "Keep for now": defer these chats until the snooze window elapses. */
export async function snoozeTidyChats(ids: string[]): Promise<number> {
  const res = await fetch(`${BASE}/api/tidyup/keep`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ids }),
  })
  if (!res.ok) throw new Error(`Failed to defer: ${res.statusText}`)
  const data = await res.json()
  return data.snoozed || 0
}
