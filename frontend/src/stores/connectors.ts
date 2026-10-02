// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Connectors store.
//
// Connector state used to live as refs inside McpServersModal, which meant
// every trip to the sign-in modal and back unmounted the modal, dropped the
// list, and re-probed every server's auth status. Holding it here keeps the
// list, the search box, the filter and the scroll position across that round
// trip, and lets the modal reopen instantly from cache while it revalidates.

import { defineStore } from 'pinia'
import { computed, nextTick, ref } from 'vue'
import {
  listMcpServers, addMcpServer, updateMcpServer, removeMcpServer, setMcpServerEnabled,
  logoutMcpServer, fetchAllMcpStatus, fetchGitHubPatStatus,
  saveMcpServerKey, removeMcpServerKey,
  type McpServer, type McpAddRequest, type GitHubPatStatus,
} from '../api'
import {
  deriveStatus, displayName, groupOf, isGithub, matchesQuery, statusLabel,
  type ConnectorGroup, type ConnectorStatus,
} from '../utils/connectorStatus'

export type ConnectorFilter = 'all' | ConnectorGroup

export interface DecoratedConnector {
  server: McpServer
  status: ConnectorStatus
  group: ConnectorGroup
}

function key(name: string): string {
  return name.trim().toLowerCase()
}

export const useConnectorsStore = defineStore('connectors', () => {
  const servers = ref<McpServer[]>([])
  // Lowercased server name → opencode reports it as authenticated.
  const authed = ref<Record<string, boolean>>({})
  const githubToken = ref<GitHubPatStatus | null>(null)

  const loaded = ref(false)
  const loading = ref(false)
  const error = ref('')

  const query = ref('')
  const filter = ref<ConnectorFilter>('all')

  // Per-row transient state. Keyed by server name so a slow toggle on one row
  // never blocks or mislabels another.
  const busyEnable = ref('')
  const busyAuth = ref('')
  const rowError = ref<Record<string, string>>({})
  // Rows where the user chose "Later" on the sign-in bar. The connector stays
  // amber in the list — this only hides the prompt, it does not pretend the
  // problem is solved.
  const dismissed = ref<Record<string, boolean>>({})

  // Whether the stored GitHub token can actually do repository work. The row's
  // status is deliberately capability-based rather than "is a token present":
  // a token that fails on every repo call should not be reported as ready.
  const githubReady = computed(() => githubToken.value?.can_read_prs === true)

  const decorated = computed<DecoratedConnector[]>(() =>
    servers.value.map(s => {
      // For GitHub the personal access token is the only credential that makes
      // the server usable, so it is the whole answer — deliberately *not* OR'd
      // with opencode's view. opencode reports GitHub as connected on the
      // Copilot login token, which lists all 47 tools and then fails every
      // actual repository call with "insufficient scopes"; trusting it would
      // report a broken connector as working.
      const authenticated = isGithub(s)
        ? githubReady.value
        : authed.value[key(s.name)] === true
      const status = deriveStatus(s, { authenticated, githubReady: githubReady.value })
      return { server: s, status, group: groupOf(status) }
    }),
  )

  // Counts describe the whole set, not the current search — they are the rail's
  // way of telling you what is there, including what your query is hiding.
  const counts = computed(() => {
    const c = { all: decorated.value.length, on: 0, attention: 0, off: 0 }
    for (const d of decorated.value) c[d.group]++
    return c
  })

  const visible = computed(() =>
    decorated.value.filter(d =>
      (filter.value === 'all' || d.group === filter.value)
      && matchesQuery(d.server, query.value)),
  )

  const groups = computed(() => {
    const order: ConnectorGroup[] = ['on', 'attention', 'off']
    return order
      .map(g => ({ key: g, items: visible.value.filter(d => d.group === g) }))
      .filter(g => g.items.length > 0)
  })

  const hasProvisioned = computed(() => servers.value.some(s => s.provisioned === true))

  // What the modal's live region says next. Turning a
  // connector on changes its status a moment later, somewhere other than where
  // focus is: the pill repaints and a bar appears, and a screen reader is told
  // none of it. The switch announces its own on/off; this announces the
  // consequence, which is the part that decides what the user has to do next.
  const announcement = ref('')

  function announce(message: string) {
    // Assistive tech ignores a live region whose text did not change, and
    // toggling the same row twice is an ordinary thing to do — so clear it
    // first and let the next frame carry the repeat.
    if (announcement.value === message) {
      announcement.value = ''
      void nextTick(() => { announcement.value = message })
      return
    }
    announcement.value = message
  }

  function setRowError(name: string, message: string) {
    if (message) rowError.value[name] = message
    else delete rowError.value[name]
  }

  async function loadStatuses(opts?: { fresh?: boolean }) {
    // One request for every server. This used to be a serial loop of
    // per-server reads, each of which could spawn its own `opencode mcp list`.
    try {
      authed.value = await fetchAllMcpStatus(opts)
    } catch {
      /* leave the previous map in place rather than inventing a status */
    }
  }

  async function loadGithubToken() {
    try {
      githubToken.value = await fetchGitHubPatStatus()
    } catch { /* leave unknown — the row then reads as needing a token */ }
  }

  // load fetches the list and its auth status. Existing data stays on screen
  // while it runs, so reopening the modal is instant and merely revalidates.
  async function load() {
    loading.value = true
    error.value = ''
    try {
      servers.value = await listMcpServers()
      loaded.value = true
      const statuses = loadStatuses()
      if (servers.value.some(isGithub)) await loadGithubToken()
      await statuses
    } catch (e: any) {
      error.value = e.message || String(e)
    } finally {
      loading.value = false
    }
  }

  async function ensureLoaded() {
    if (!loaded.value) await load()
    else void load()
  }

  // Enabling a server is what exposes its tools to the assistant. The switch is
  // optimistic: it flips immediately so the contextual bar can appear in the
  // same frame, and reverts with an in-row message if the write fails.
  async function toggle(s: McpServer) {
    const next = !s.enabled
    busyEnable.value = s.name
    setRowError(s.name, '')
    s.enabled = next
    if (next) delete dismissed.value[s.name]
    try {
      await setMcpServerEnabled(s.name, next)
      // A freshly enabled server is one opencode will now connect, so its auth
      // status can change. Re-read it rather than guessing.
      void loadStatuses().then(() => {
        // Announce the settled status, not the optimistic one: "on" and "on but
        // it needs a sign-in first" are different answers, and the second is
        // the one that asks something of the user.
        const d = decorated.value.find(x => x.server.name === s.name)
        const label = d ? statusLabel(d.status) : ''
        announce(next
          ? `${displayName(s)} turned on${label ? `. ${label}` : ''}.`
          : `${displayName(s)} turned off.`)
      })
    } catch (e: any) {
      s.enabled = !next
      const message = e.message || String(e)
      setRowError(s.name, message)
      announce(`${displayName(s)} could not be turned ${next ? 'on' : 'off'}. ${message}`)
    } finally {
      busyEnable.value = ''
    }
  }

  // Connector API keys: a key change is a connection change, so the status is re-read
  // rather than guessed.
  async function saveKey(s: McpServer, key: string) {
    await saveMcpServerKey(s.name, key)
    s.key_saved = true
    setRowError(s.name, '')
    await loadStatuses({ fresh: true })
  }

  async function removeKey(s: McpServer) {
    const title = displayName(s)
    if (!confirm(`Remove your ${title} key? The assistant can't use ${title} until you add one again.`)) return
    busyAuth.value = s.name
    try {
      await removeMcpServerKey(s.name)
      s.key_saved = false
      announce(`${title} key removed.`)
      await loadStatuses({ fresh: true })
    } catch (e: any) {
      setRowError(s.name, e.message || String(e))
    } finally {
      busyAuth.value = ''
    }
  }

  async function signOut(name: string) {
    if (!confirm(`Sign out of "${name}"? It will also be turned off, so you'll need to enable it and authorize again to use it.`)) return
    busyAuth.value = name
    setRowError(name, '')
    try {
      await logoutMcpServer(name)
      authed.value[key(name)] = false
      // Signing out disables the server server-side; mirror that locally so the
      // toggle doesn't briefly lie about the state.
      const s = servers.value.find(x => x.name === name)
      if (s) s.enabled = false
      ;(window as any).showToast?.(`Signed out of ${name}`)
      announce(`Signed out of ${name}. It has been turned off.`)
    } catch (e: any) {
      const message = e.message || String(e)
      setRowError(name, message)
      announce(`Could not sign out of ${name}. ${message}`)
    } finally {
      busyAuth.value = ''
    }
  }

  async function remove(name: string) {
    if (!confirm(`Remove connector "${name}"?`)) return
    setRowError(name, '')
    try {
      await removeMcpServer(name)
      ;(window as any).showToast?.(`Removed "${name}"`)
      await load()
      announce(`Removed ${name}.`)
    } catch (e: any) {
      const message = e.message || String(e)
      setRowError(name, message)
      announce(`Could not remove ${name}. ${message}`)
    }
  }

  async function add(req: McpAddRequest) {
    await addMcpServer(req)
    await load()
    // The cached status map cannot contain a server added a moment ago, and
    // absent reads as unauthenticated — so the new row would claim a sign-in is
    // needed whatever the truth. Force one live re-check. Deliberately not
    // awaited: the sheet has closed, and the row corrects itself when the
    // answer lands. Deliberately not faked from the probe either — the probe
    // says the address answers us, not that opencode could connect to it.
    void loadStatuses({ fresh: true })
  }

  async function update(name: string, req: Omit<McpAddRequest, 'name'>) {
    await updateMcpServer(name, req)
    setRowError(name, '')
    await load()
    // A new address or header can change whether it connects, and a new
    // address signs it out, so the cached answer no longer holds.
    void loadStatuses({ fresh: true })
  }

  return {
    servers, authed, githubToken, loaded, loading, error,
    query, filter, busyEnable, busyAuth, rowError, dismissed, announcement,
    githubReady, decorated, counts, visible, groups, hasProvisioned,
    load, ensureLoaded, loadStatuses, toggle, signOut, remove, add, update, setRowError, announce,
    saveKey, removeKey,
  }
})
