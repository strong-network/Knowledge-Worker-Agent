// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Connector classification.
//
// Everything the connectors modal shows is derived from two sources: the
// server list (`GET /api/mcp/servers`) and the batched auth status
// (`GET /api/mcp/status`). Nothing here is persisted, and nothing here talks to
// the network — these are pure functions so the store and the row components
// can't disagree about what a connector's state is.

import type { McpServer } from '../api'

// The states a connector can be in. They are mutually exclusive and
// exhaustive: `deriveStatus` always returns exactly one.
export type ConnectorStatus = 'on' | 'needs-signin' | 'needs-key' | 'needs-setup' | 'blocked' | 'off'

// The three sections the list is grouped into. Several statuses share a group:
// "needs sign-in" and "needs setup" are different remedies for the same
// user-facing problem, and a blocked connector is still just off.
export type ConnectorGroup = 'on' | 'attention' | 'off'

export function isRemote(s: McpServer): boolean {
  const t = (s.transport || s.type || '').toLowerCase()
  return t === 'http' || t === 'sse' || t === 'remote' || !!s.url
}

// GitHub's hosted MCP server has no sign-in flow we can drive: it does not
// support browser OAuth (no dynamic client registration — `opencode mcp auth
// github` fails), and the GitHub Copilot login token, though it connects and
// lists all 47 tools, is rejected with "insufficient scopes" on every actual
// repository call. A personal access token with repo scope is the only thing
// that works, so the row's remedy is always "add a token", never "sign in".
// Keep in sync with mcp.GitHubDefault and opencodeauth.GitHubMCPToken.
export function isGithub(s: McpServer): boolean {
  return s.name.trim().toLowerCase() === 'github'
}

// Servers classified 'none' connect anonymously and 'local' run on this
// machine — neither has an account to sign in to.
export function isAnonymous(s: McpServer): boolean {
  return s.auth === 'none'
}

export function isLocal(s: McpServer): boolean {
  return s.auth === 'local' || !isRemote(s)
}

// Central connectors: a server your organization assigned. It is defined in the
// platform-owned config dir, which the materializer wipes and recreates on
// every boot — so editing or removing it here would appear to work and then
// silently undo itself at the next restart. Enabling and disabling stay
// available: those write to your own Global, which is never touched.
export function isProvisioned(s: McpServer): boolean {
  return s.provisioned === true
}

// A connector Knowledge Worker Agent runs itself (recall, obsidian): its definition
// is rewritten at every start, so, like a provisioned one, it can't be edited.
export function isOwned(s: McpServer): boolean {
  return s.owned === true
}

// A 'setup' server needs a per-tenant URL or a hand-issued API token, and there
// is no sign-in flow that can supply either — its help text is the only route
// forward.
export function needsSetup(s: McpServer): boolean {
  return s.auth === 'setup'
}

// Connector API keys: an 'api-key' server connects with the user's own key, pasted into the
// connectors modal. There is no sign-in flow for it.
export function needsKey(s: McpServer): boolean {
  return s.auth === 'api-key'
}

// needsAuth reports whether a server has an account to sign in to at all.
// Anonymous servers connect without credentials and local ones run on this
// machine, so neither has a sign-in state worth showing.
export function needsAuth(s: McpServer): boolean {
  return !isAnonymous(s) && !isLocal(s)
}

// oauthCapable reports whether `opencode mcp auth <name>` can actually sign
// this server in. 'setup' servers and GitHub are excluded: offering them a
// sign-in button would send the user into a flow that always fails. A server
// with no catalogue auth kind is included — a remote server the user added by
// hand is exactly the case where opencode's own answer is all we have.
export function oauthCapable(s: McpServer): boolean {
  return needsAuth(s) && !isGithub(s) && !needsSetup(s) && !needsKey(s)
}

// A provisioned artifact may carry an admin-authored label. Fall back to the
// server name, which is also what `opencode mcp auth` keys on.
export function displayName(s: McpServer): string {
  return (s.label || '').trim() || s.name
}

export function describeServer(s: McpServer): string {
  if (s.url) return `${s.transport || 'http'} → ${s.url}`
  if (s.command) {
    const args = (s.args || []).join(' ')
    return `stdio → ${s.command}${args ? ' ' + args : ''}`
  }
  if (s.type) return s.type
  return ''
}

// The bare address a connector talks to: a URL for a remote server, the command
// line for a stdio one. Deliberately not `describeServer` — that one prefixes
// the transport (`http → …`, `stdio → …`), which is the right vocabulary for
// the management list and the wrong one for the Toolbox, where this text stands
// in for a description until real prose exists. Kept separate rather than
// changing `describeServer` so the shipped management row is untouched.
//
// Guaranteed non-empty: `describeServer`'s last branch returns `''` for a
// server with no url, no command and no type, which would collapse the card's
// description slot. Falls back to the display name instead.
export function connectorEndpoint(s: McpServer): string {
  if (s.url) return s.url
  if (s.command) {
    const args = (s.args || []).join(' ')
    return `${s.command}${args ? ' ' + args : ''}`
  }
  return (s.type || '').trim() || displayName(s)
}

// Letter monogram standing in for a product logo. The design calls for real
// logos; we don't ship any, and a wrong or missing logo reads worse than a
// letter, so the first character of the display name is used instead.
export function monogram(s: McpServer): string {
  const n = displayName(s).trim()
  return n ? n[0]!.toUpperCase() : '?'
}

export function helpText(s: McpServer): string {
  return (s.help || '').trim()
}

export interface StatusInputs {
  // Whether opencode reports the server as authenticated. For GitHub this is
  // folded together with the personal-access-token check by the caller, since
  // the token is what makes that server usable.
  authenticated: boolean
  // Whether the stored GitHub token can do repository work. Only consulted for
  // the GitHub row.
  githubReady: boolean
}

// deriveStatus maps a server plus its auth state onto exactly one status. The
// clauses are ordered and exhaustive.
export function deriveStatus(s: McpServer, { authenticated, githubReady }: StatusInputs): ConnectorStatus {
  const enabled = s.enabled === true
  if (enabled) {
    if (authenticated) return 'on'
    // Nothing to sign in to: anonymous servers connect without credentials and
    // local ones run on this machine. A server the user added by hand has no
    // catalogue auth kind, and if it is a local command that lands here too —
    // which is what stops an enabled custom stdio server reading as broken.
    if (!needsAuth(s)) return 'on'
    if (needsSetup(s)) return 'needs-setup'
    // No key yet, or one it did not connect with: either way the remedy is
    // the key, not a sign-in that cannot work for this server.
    if (needsKey(s)) return 'needs-key'
    // Anything remote that isn't signed in needs a sign-in, whether or not the
    // catalogue labelled it 'oauth'. Deliberately not treating an unknown auth
    // kind as "fine": opencode lists every server it connects, so an enabled
    // one missing from that list really is unauthenticated, and calling it
    // working would be a confident lie rather than a missing badge.
    return 'needs-signin'
  }
  // Turning GitHub on without a usable token is a guaranteed failure (opencode
  // gets a 400 from the endpoint and the row goes red), so it is called out
  // rather than sitting silently among the other off rows.
  if (isGithub(s) && !githubReady) return 'blocked'
  return 'off'
}

export function groupOf(status: ConnectorStatus): ConnectorGroup {
  if (status === 'on') return 'on'
  if (status === 'needs-signin' || status === 'needs-key' || status === 'needs-setup') return 'attention'
  return 'off'
}

export function statusLabel(status: ConnectorStatus): string {
  switch (status) {
    case 'on': return 'On · working'
    case 'needs-signin': return 'Sign-in needed'
    case 'needs-key': return 'Key needed'
    case 'needs-setup': return 'Needs setup'
    case 'blocked': return 'Off · needs a token'
    default: return 'Off'
  }
}

export const GROUP_LABELS: Record<ConnectorGroup, string> = {
  on: 'On · working',
  attention: 'Needs your attention',
  off: 'Off',
}

// Free-text search over everything a user might plausibly type: the name they
// see, the name the backend keys on, and the address or command in the
// secondary line.
export function matchesQuery(s: McpServer, query: string): boolean {
  const q = query.trim().toLowerCase()
  if (!q) return true
  const hay = [
    s.name,
    s.label || '',
    s.url || '',
    s.command || '',
    (s.args || []).join(' '),
  ].join(' ').toLowerCase()
  return hay.includes(q)
}
