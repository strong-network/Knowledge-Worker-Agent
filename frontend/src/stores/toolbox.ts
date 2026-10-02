// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Toolbox store.
//
// The Toolbox modal shows skills and connectors as one browsable library, so
// this store's job is to normalise three unrelated sources into a single item
// shape and own the filter state the rail and the grid both read from.
//
//   GET /api/skills?session_id=          skills, plus the `available` flag
//   stores/connectors.ts                 the global list + derived sign-in state
//   GET /api/sessions/{id}/mcp           per-chat selection + live connection
//   GET /api/projects/{id}/mcp           …the project-level equivalent
//
// The connector half deliberately goes through `stores/connectors` rather than
// re-fetching `/api/mcp/servers` and `/api/mcp/status`: that store already
// holds the list, the batched auth map and the GitHub token check, and a second
// copy of them here would mean two answers to "is this signed in?".

import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'
import {
  fetchSkills, getProjectMcp, getSessionMcp, setSessionMcp,
  type SessionConnector, type SkillInfo,
} from '../api'
import { useConnectorsStore } from './connectors'
import { useSessionsStore } from './sessions'
import {
  connectorEndpoint, displayName, type ConnectorStatus,
} from '../utils/connectorStatus'

export type ToolboxType = 'all' | 'skill' | 'connector'

export interface ToolboxItem {
  kind: 'skill' | 'connector'
  // Stable identity. Skills use their skill id; connectors use the server name,
  // which is what every connector endpoint keys on.
  id: string
  name: string
  // Always non-empty. Real prose when it exists, the connector's endpoint as a
  // stand-in until it does — the only thing on a card that tells two
  // similarly-named connectors apart, and the thing that keeps the card's
  // content-driven height exercised before any prose is written.
  description: string
  // Which of those two it is, so the card can render a placeholder in the muted
  // treatment instead of passing it off as a description someone wrote. When
  // prose arrives the text and the treatment change together.
  descriptionKind: 'prose' | 'endpoint'
  // Unset today. The rail's CATEGORY group is appended only when at least one
  // item carries this, so setting it is the entire change needed to make the
  // group appear.
  category?: string
  // An array rather than a preformatted string: a tool count is a `push`, not
  // string surgery, and the separator lives in one place (the card renders
  // `meta.join(' · ')`). Empty today.
  meta: string[]
  selected: boolean
  // Sign-in state, from the connectors modal's classifier. Connectors only. Because the
  // Toolbox lists only globally-enabled connectors this is always one of
  // 'on' | 'needs-signin' | 'needs-setup' — 'off' and 'blocked' cannot reach it.
  status?: ConnectorStatus
  // opencode's live view of the connection for *this* chat ('connected',
  // 'pending', 'error', …). A separate axis from `status`: a connector can be
  // signed in and still be failing right now, and the card shows those
  // differently (amber remedy vs. red "not working"). Connectors only.
  liveStatus?: string
  // True for connectors inside a project chat, where the selection is
  // project-level and shared by every chat in it. Skills are
  // never read-only — they are armed per message, not stored.
  readOnly: boolean
}

interface Filters {
  query: string
  type: ToolboxType
  activeOnly: boolean
  category: string
}

// The whole search index. Description and category are in it from the start so
// that the moment either gains content it becomes searchable with no other
// edit — which is what makes intent search ("battlecard", "meeting notes")
// a content change rather than a code change.
function searchText(item: ToolboxItem): string {
  return [
    item.name,
    item.id,
    item.description,
    item.category || '',
    item.meta.join(' '),
  ].join(' ').toLowerCase()
}

// One predicate for the grid and the rail counts both, so they cannot disagree
// about what a filter means.
function matches(item: ToolboxItem, f: Filters): boolean {
  if (f.type !== 'all' && item.kind !== f.type) return false
  if (f.activeOnly && !item.selected) return false
  if (f.category && item.category !== f.category) return false
  const q = f.query.trim().toLowerCase()
  if (q && !searchText(item).includes(q)) return false
  return true
}

export const useToolboxStore = defineStore('toolbox', () => {
  const connectorsStore = useConnectorsStore()
  const sessionsStore = useSessionsStore()

  // ── Skills half ──────────────────────────────────────────────────────────
  const skills = ref<SkillInfo[]>([])
  const skillsLoading = ref(false)
  // Distinct from "the list is empty": false means we could not ask, normally
  // because opencode is still starting. `ListSkills` returns an empty array
  // *and* available:false on any failure, so without this flag "still starting"
  // and "genuinely none" are indistinguishable — and rendering the first as the
  // second tells someone with a dozen skills that they have none.
  const skillsAvailable = ref(true)
  // Composer-local and transient, exactly like the picker it replaces: a skill
  // belongs to the draft, not to the session, and is never persisted.
  const selectedSkillId = ref<string | null>(null)

  // ── Connector half ───────────────────────────────────────────────────────
  const selection = ref<SessionConnector[]>([])
  const connectorsLoading = ref(false)
  const connectorsError = ref('')
  const busy = ref<Record<string, boolean>>({})

  // ── Filters ──────────────────────────────────────────────────────────────
  const query = ref('')
  const type = ref<ToolboxType>('all')
  const activeOnly = ref(false)
  const category = ref('')

  const filters = computed<Filters>(() => ({
    query: query.value,
    type: type.value,
    activeOnly: activeOnly.value,
    category: category.value,
  }))

  const readOnlyConnectors = computed(() => !!sessionsStore.currentProjectId)

  const skillItems = computed<ToolboxItem[]>(() =>
    skills.value.map((s) => {
      const prose = (s.description || '').trim()
      return {
        kind: 'skill' as const,
        id: s.id,
        name: s.name || s.id,
        // Skills normally carry prose; falling back to the name keeps the
        // always-non-empty guarantee if one ever ships without it.
        description: prose || (s.name || s.id),
        descriptionKind: prose ? 'prose' as const : 'endpoint' as const,
        meta: [],
        selected: selectedSkillId.value === s.id,
        readOnly: false,
      }
    }),
  )

  const connectorItems = computed<ToolboxItem[]>(() => {
    const picked = new Map(selection.value.map((c) => [c.name, c]))
    // Only globally-enabled connectors appear. The Toolbox is a per-chat
    // control; turning one on for the workspace is a different decision, and it
    // lives behind "Manage connectors".
    return connectorsStore.decorated
      .filter((d) => d.server.enabled === true)
      .map((d) => {
        const chat = picked.get(d.server.name)
        return {
          kind: 'connector' as const,
          id: d.server.name,
          name: displayName(d.server),
          description: connectorEndpoint(d.server),
          descriptionKind: 'endpoint' as const,
          meta: [],
          selected: chat?.selected === true,
          status: d.status,
          liveStatus: chat?.status,
          readOnly: readOnlyConnectors.value,
        }
      })
  })

  const items = computed<ToolboxItem[]>(() => [...skillItems.value, ...connectorItems.value])

  // The armed skill as something renderable, with the id standing in for a name
  // we do not have yet. `/skill <name>` can arm a skill before the list has
  // loaded — and after the composer's local copy of the list is gone, this store
  // is the only place that knows — so without the fallback the pill would have
  // an armed skill and nothing to print. Lives here rather than in the composer
  // so every future reader gets the fallback instead of reinventing it.
  const selectedSkill = computed<{ id: string; name: string } | null>(() => {
    const id = selectedSkillId.value
    if (!id) return null
    const found = skillItems.value.find((i) => i.id === id)
    return found ? { id, name: found.name } : { id, name: id }
  })

  // How many connectors are on for this chat. Counted from `connectorItems`,
  // not from `selection`: the latter can name a connector that has since been
  // disabled workspace-wide, which D3 keeps out of the Toolbox — so counting it
  // would put a number on the composer pill that the modal cannot account for.
  const connectorsOn = computed(() => connectorItems.value.filter((i) => i.selected).length)

  // Selected first, then stable by name inside each band, so turning
  // something on moves it to the top instead of leaving the user to find it.
  const visible = computed<ToolboxItem[]>(() => {
    const f = filters.value
    return items.value
      .filter((i) => matches(i, f))
      .sort((a, b) => {
        if (a.selected !== b.selected) return a.selected ? -1 : 1
        return a.name.localeCompare(b.name)
      })
  })

  // A rail row's count is what you would see if you clicked it: every *other*
  // active dimension applied, its own released and set to the row's value.
  // Deliberately not "the set the grid currently renders" — that would zero
  // every count the moment a filter is applied, so picking TYPE=Skills would
  // make Connectors read 0, which is exactly the number you can no longer see
  // rather than the number that is there.
  function countFor(overrides: Partial<Filters>): number {
    const f = { ...filters.value, ...overrides }
    return items.value.filter((i) => matches(i, f)).length
  }

  const counts = computed(() => ({
    all: countFor({ type: 'all' }),
    skill: countFor({ type: 'skill' }),
    connector: countFor({ type: 'connector' }),
    active: countFor({ activeOnly: true }),
  }))

  // Categories are a deferred feature. The rail group appears by itself as
  // soon as any item carries one.
  const categories = computed(() => {
    const seen = new Set<string>()
    for (const i of items.value) if (i.category) seen.add(i.category)
    return [...seen].sort()
  })

  const hasCategories = computed(() => categories.value.length > 0)

  function categoryCount(name: string): number {
    return countFor({ category: name })
  }

  // Why the grid is empty, which picks the copy. Null when it isn't.
  //
  // 'filter' and 'none' are kept apart because they need opposite answers: a
  // filter combination that excluded everything is fixed by clearing it, while
  // an empty library is not — offering "Clear filters" there is a button that
  // visibly does nothing, and telling someone with no connectors at all that
  // "nothing matches this filter combination" blames a filter they never set.
  const emptyReason = computed<'query' | 'active' | 'filter' | 'none' | null>(() => {
    if (visible.value.length > 0) return null
    if (query.value.trim()) return 'query'
    if (activeOnly.value) return 'active'
    if (type.value !== 'all' || category.value) return 'filter'
    return 'none'
  })

  // What the skills half has to say about itself, deliberately independent of
  // `emptyReason`.
  //
  // The two halves load independently, so the state this exists for is
  // "connectors have landed, skills have not" — and in that state the grid is
  // *not* empty, so an empty-state component never mounts and the skills status
  // would render nowhere. That would make the split-loading work invisible: the
  // screen would be identical to a finished load that found no skills. The
  // failure case is worse, because it is permanent — a workspace where opencode
  // cannot start would show a Toolbox of connectors with no explanation and no
  // reachable Try again, which is exactly the "you have no skills" misreading
  // the three-state copy exists to prevent.
  //
  // Null once skills have arrived; the genuinely-empty case is the grid's own
  // empty state, not a notice.
  const skillsNotice = computed<'loading' | 'unavailable' | null>(() => {
    // Nothing to report when the user has said they are not looking at skills.
    if (type.value === 'connector') return null
    if (skillsLoading.value) return 'loading'
    if (!skillsAvailable.value) return 'unavailable'
    return null
  })

  const anyConnectors = computed(() => connectorItems.value.length > 0)

  function clearFilters() {
    query.value = ''
    type.value = 'all'
    activeOnly.value = false
    category.value = ''
  }

  // Called when the modal opens. The store outlives the component — ModalHost
  // mounts with `v-if`, so closing destroys the modal and keeps this state —
  // and not all of it should survive that.
  //
  // A query is a one-off lookup, finished once it has been used, and it is the
  // only filter that can hide the whole library behind "Nothing matches…";
  // reopening to a stale search reads as a bug. `type` and `activeOnly` are a
  // browsing stance: still visible in the rail, and one click to clear.
  //
  // This matters more than it would for the connectors modal, which does not
  // reset either. That surface is opened deliberately and has no programmatic
  // way to set its filter. The Toolbox is a composer control opened constantly,
  // and `/skill <query>` routes into it — so without this, a slash
  // command's filter leaks into every later open.
  //
  // Split into request-then-apply rather than one function the modal calls,
  // because the two callers run in the wrong order otherwise. `/skill foo` has
  // to set a query *and* open the modal; if the modal cleared the query itself
  // on mount it would wipe the one the slash command just asked for. So the
  // opener records what it wants here, and the modal consumes it below. The
  // default is an empty query, which means a plain `ui.openModal('toolbox')`
  // that never calls this still opens clean — the reset does not depend on
  // every caller remembering it.
  const pendingQuery = ref('')

  function prepareForOpen(opts?: { query?: string; type?: ToolboxType }) {
    pendingQuery.value = opts?.query ?? ''
    if (opts?.type) type.value = opts.type
  }

  // Called by the modal as it mounts. One-shot: consuming it resets the request
  // so the *next* open starts from a clean query rather than repeating this one.
  function applyPendingOpen() {
    query.value = pendingQuery.value
    pendingQuery.value = ''
  }

  // ── Loading ──────────────────────────────────────────────────────────────
  //
  // The two halves load independently and are never awaited together. The
  // server's skills cache makes the skills half *usually* fast, not reliably fast: a
  // miss that cannot be seeded — a client-chosen workdir, a repo clone, the
  // first chat in a fresh workspace — still blocks on an `opencode serve` start
  // for up to 60s, while connectors are a local config read plus a status map.
  // A single combined load with one loading flag would hold the connector cards
  // hostage to that worst case and render the modal empty while half its
  // content was ready immediately. Because the slow case is now rare, it is
  // also the kind of bug that would never show up in development.

  async function loadSkills() {
    skillsLoading.value = true
    try {
      const res = await fetchSkills(sessionsStore.currentSessionId || undefined)
      skills.value = res.skills
      skillsAvailable.value = res.available
    } catch {
      skills.value = []
      skillsAvailable.value = false
    } finally {
      skillsLoading.value = false
    }
  }

  async function loadConnectors() {
    connectorsLoading.value = true
    connectorsError.value = ''
    try {
      // The global list and auth map come from the connectors store, which
      // serves them from cache and revalidates in the background.
      const list = connectorsStore.ensureLoaded()
      const pid = sessionsStore.currentProjectId
      if (pid) {
        selection.value = await getProjectMcp(pid)
      } else {
        const sid = sessionsStore.currentSessionId
        selection.value = sid ? await getSessionMcp(sid) : []
      }
      await list
    } catch (e: any) {
      connectorsError.value = e?.message || String(e)
    } finally {
      connectorsLoading.value = false
    }
  }

  // ── Actions ──────────────────────────────────────────────────────────────

  // Switching chats resets everything that belongs to a chat rather than to the
  // workspace.
  //
  // This watcher is the price of moving the armed skill out of the composer. As
  // a component-local ref it died with `PromptInput`, so a skill could not
  // outlive the chat it was armed in; a Pinia store lives as long as the app, so
  // without an explicit reset a skill armed in one chat stays armed in the next.
  // That would break the per-message lifetime every piece of Toolbox copy
  // promises — the card control, the explainer and the footer all say a skill
  // applies to your next message and then clears.
  //
  // The connector selection is session-scoped too, and a stale one is just as
  // visible: the composer pill reads its count from here, so leaving the
  // previous chat's selection in place would show the new chat a number that
  // belongs to the old one. Reloading replaces it wholesale.
  watch(() => sessionsStore.currentSessionId, () => {
    selectedSkillId.value = null
    void loadConnectors()
  })

  // Exclusive, and not persisted anywhere: one skill applies to the next
  // message and then it is gone. Passing null clears it.
  function selectSkill(id: string | null) {
    selectedSkillId.value = id
  }

  function clearSkill() {
    selectedSkillId.value = null
  }

  async function toggleConnector(item: ToolboxItem) {
    if (item.kind !== 'connector' || item.readOnly) return
    const sid = sessionsStore.currentSessionId
    if (!sid || busy.value[item.id]) return
    const next = !item.selected
    busy.value = { ...busy.value, [item.id]: true }
    // Optimistic: the switch flips in the same frame, and the authoritative
    // list replaces this a moment later. On failure the local edit is dropped,
    // which puts the switch back where it was.
    const before = selection.value
    selection.value = selection.value.map((c) =>
      c.name === item.id ? { ...c, selected: next } : c)
    try {
      selection.value = await setSessionMcp(sid, { [item.id]: next })
    } catch {
      selection.value = before
      ;(window as any).showToast?.('Could not update connector')
    } finally {
      const b = { ...busy.value }
      delete b[item.id]
      busy.value = b
    }
  }

  // After a sign-in completes the cached status map still says the connector is
  // unauthenticated, so the card would keep its amber band and the flow would
  // read as broken. The sheet's close event is the only signal we get.
  function refreshConnectorStatuses() {
    void connectorsStore.loadStatuses({ fresh: true })
  }

  return {
    skills, skillsLoading, skillsAvailable, selectedSkillId,
    selection, connectorsLoading, connectorsError, busy,
    query, type, activeOnly, category,
    readOnlyConnectors, skillItems, connectorItems, items, visible,
    selectedSkill, connectorsOn,
    counts, categories, hasCategories, emptyReason, skillsNotice, anyConnectors,
    countFor, categoryCount, clearFilters, prepareForOpen, applyPendingOpen,
    loadSkills, loadConnectors,
    selectSkill, clearSkill, toggleConnector, refreshConnectorStatuses,
  }
})
