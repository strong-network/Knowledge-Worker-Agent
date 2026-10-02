<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useUiStore } from '../../stores/ui'
import { useSessionsStore } from '../../stores/sessions'
import {
  fetchTidyReview, cleanupTidyItems, snoozeTidyChats,
  type TidyChat, type TidyOrphan,
} from '../../api'
import { formatRelativeTime, formatAbsoluteTime, formatSize } from '../../utils/time'

// Workspace hygiene.
//
// This is a review, not a deadline. Nothing in this list is scheduled for
// deletion, nothing expires, and closing the modal without doing anything is a
// valid outcome — the same chats are simply offered again next time. Every
// destructive action here is one the user selected item by item.

const ui = useUiStore()
const sessions = useSessionsStore()

const loading = ref(true)
const error = ref('')
const enabled = ref(true)
const chats = ref<TidyChat[]>([])
const orphans = ref<TidyOrphan[]>([])
const total = ref(0)
const reviewDays = ref(30)

// Selection is keyed rather than held on the rows, so a refresh can replace the
// list without stranding stale selections.
const selected = ref(new Set<string>())
const busy = ref(false)
const confirming = ref(false)

const chatKey = (c: TidyChat) => `chat:${c.id}`
const orphanKey = (o: TidyOrphan) => `orphan:${o.path}`

function isSelected(key: string) { return selected.value.has(key) }

function toggle(key: string) {
  const next = new Set(selected.value)
  next.has(key) ? next.delete(key) : next.add(key)
  selected.value = next
  // Any change to the selection invalidates a confirmation of the old one.
  confirming.value = false
}

// Chats the user has starred are split out rather than mixed in. Starring is
// the one signal of importance they have already given us, so a bulk action
// aimed at clutter should not be able to reach these by accident.
const plainChats = computed(() => chats.value.filter(c => !c.starred))
const starredChats = computed(() => chats.value.filter(c => c.starred))

// The starred group is listed last: the ordinary clutter is what the user came
// to clear, and reaching the chats they marked as important should take a
// deliberate scroll past everything else.
const chatGroups = computed(() => [
  {
    id: 'idle',
    name: 'Idle chats',
    hint: '',
    starred: false,
    chats: plainChats.value,
    keys: plainChats.value.map(chatKey),
  },
  {
    id: 'starred',
    name: '★ Starred',
    hint: "You marked these as important. They're idle too — listed separately so you can decide on them on their own.",
    starred: true,
    chats: starredChats.value,
    keys: starredChats.value.map(chatKey),
  },
].filter(g => g.chats.length > 0))

/**
 * Select-all is per section, deliberately. A single one spanning every section
 * would make separating starred chats purely cosmetic: the one click most
 * likely to be used would still sweep them in, which is the whole thing we are
 * trying to prevent.
 */
function sectionState(keys: string[]) {
  const all = keys.length > 0 && keys.every(k => selected.value.has(k))
  return {
    all,
    some: !all && keys.some(k => selected.value.has(k)),
  }
}

function toggleSection(keys: string[]) {
  const next = new Set(selected.value)
  if (keys.every(k => next.has(k))) {
    keys.forEach(k => next.delete(k))
  } else {
    keys.forEach(k => next.add(k))
  }
  selected.value = next
  confirming.value = false
}

const orphanKeys = computed(() => orphans.value.map(orphanKey))

const selectedChats = computed(() => chats.value.filter(c => isSelected(chatKey(c))))
const selectedOrphans = computed(() => orphans.value.filter(o => isSelected(orphanKey(o))))
const selectedCount = computed(() => selectedChats.value.length + selectedOrphans.value.length)

// What the user is about to lose, spelled out before they confirm. A count of
// chats says little; the messages inside them is the part that matters.
const selectedMessages = computed(() =>
  selectedChats.value.reduce((n, c) => n + c.message_count, 0),
)
const selectedBytes = computed(() =>
  selectedChats.value.reduce((n, c) => n + c.size_bytes, 0) +
  selectedOrphans.value.reduce((n, o) => n + o.size_bytes, 0),
)
// Called out separately in the confirmation. Deleting something you marked as
// important is the one mistake here worth interrupting for.
const selectedStarred = computed(() => selectedChats.value.filter(c => c.starred).length)

const nothingToReview = computed(() =>
  !loading.value && !error.value && chats.value.length === 0 && orphans.value.length === 0,
)

/** Go duration ("720h0m0s") → whole days, for the subtitle. */
function durationDays(d: string): number {
  const m = /^(\d+(?:\.\d+)?)h/.exec(d)
  return m ? Math.round(parseFloat(m[1]) / 24) : 30
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const data = await fetchTidyReview()
    enabled.value = data.enabled
    chats.value = data.items
    orphans.value = data.orphans
    total.value = data.total
    reviewDays.value = durationDays(data.review_after)
    selected.value = new Set()
    confirming.value = false
    // The badge and the list must agree; this is the only place that knows
    // the count changed.
    ui.tidyCount = data.total
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

async function deleteSelected() {
  if (!selectedCount.value || busy.value) return
  busy.value = true
  try {
    const { results, removed } = await cleanupTidyItems([
      ...selectedChats.value.map(c => ({ kind: 'chat' as const, id: c.id })),
      ...selectedOrphans.value.map(o => ({ kind: 'orphan' as const, path: o.path })),
    ])
    const failed = results.filter(r => !r.removed)
    // The server reports per item, so a partial result is normal and must be
    // said out loud rather than rounded up to "done".
    if (failed.length) {
      ;(window as any).showToast?.(
        `Removed ${removed}; ${failed.length} could not be removed (${failed[0].error || 'unknown reason'})`,
      )
    } else {
      ;(window as any).showToast?.(`Removed ${removed} item${removed === 1 ? '' : 's'}`)
    }
    // The sidebar still lists the chats we just deleted.
    await sessions.loadSessions()
    await load()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

async function keepSelected() {
  const ids = selectedChats.value.map(c => c.id)
  if (!ids.length || busy.value) return
  busy.value = true
  try {
    await snoozeTidyChats(ids)
    ;(window as any).showToast?.(
      `Keeping ${ids.length} chat${ids.length === 1 ? '' : 's'} — we'll ask again in ${reviewDays.value} days`,
    )
    await load()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="modal tidy-modal" @click.stop>
    <h2>🧹 Tidy up</h2>
    <p class="modal-subtitle">
      Chats you haven't opened in {{ reviewDays }} days. Nothing is deleted unless you choose it.
    </p>

    <div v-if="loading" class="tidy-note">Looking…</div>
    <div v-else-if="error" class="tidy-error">{{ error }}</div>
    <div v-else-if="!enabled" class="tidy-note">
      Workspace hygiene is turned off for this workspace.
    </div>
    <div v-else-if="nothingToReview" class="tidy-empty">
      <div class="tidy-empty-ico">✨</div>
      <div>Nothing to review — your workspace is tidy.</div>
    </div>

    <template v-else>
      <div class="tidy-toolbar">
        <!-- The list is capped, so say how much is not on screen: a user who
             clears it should know whether they are finished. It counts the
             whole review, not one section, so it lives here. -->
        <span v-if="total > chats.length" class="tidy-more">
          Showing the {{ chats.length }} oldest of {{ total }} idle chats
        </span>
        <span v-else></span>
        <span class="tidy-count">{{ selectedCount }} selected</span>
      </div>

      <section
        v-for="g in chatGroups"
        :key="g.id"
        class="tidy-section"
        :class="{ 'tidy-section-starred': g.starred }"
      >
        <h3 class="tidy-head">
          <label class="tidy-check">
            <input
              type="checkbox"
              :checked="sectionState(g.keys).all"
              :indeterminate.prop="sectionState(g.keys).some"
              @change="toggleSection(g.keys)"
            />
            <span class="tidy-head-name">{{ g.name }}</span>
          </label>
        </h3>
        <p v-if="g.hint" class="tidy-hint">{{ g.hint }}</p>
        <div v-for="c in g.chats" :key="c.id" class="tidy-row">
          <label class="tidy-check">
            <input type="checkbox" :checked="isSelected(chatKey(c))" @change="toggle(chatKey(c))" />
          </label>
          <div class="tidy-main">
            <div class="tidy-label">
              <span v-if="c.starred" class="tidy-star" aria-label="Starred">★</span>{{ c.label }}
            </div>
            <div class="tidy-meta">
              <span :title="formatAbsoluteTime(c.last_activity)">
                Last used {{ formatRelativeTime(c.last_activity) }}
              </span>
              <span>·</span>
              <span>{{ c.message_count }} message{{ c.message_count === 1 ? '' : 's' }}</span>
              <template v-if="c.size_bytes">
                <span>·</span>
                <span>{{ formatSize(c.size_bytes) }}</span>
              </template>
            </div>
          </div>
        </div>
      </section>

      <section v-if="orphans.length" class="tidy-section">
        <h3 class="tidy-head">
          <label class="tidy-check">
            <input
              type="checkbox"
              :checked="sectionState(orphanKeys).all"
              :indeterminate.prop="sectionState(orphanKeys).some"
              @change="toggleSection(orphanKeys)"
            />
            <span class="tidy-head-name">Leftover folders</span>
          </label>
        </h3>
        <p class="tidy-hint">
          Workspace folders with no chat attached, left behind by an older bug.
          Deleting them removes the files inside.
        </p>
        <div v-for="o in orphans" :key="o.path" class="tidy-row">
          <label class="tidy-check">
            <input type="checkbox" :checked="isSelected(orphanKey(o))" @change="toggle(orphanKey(o))" />
          </label>
          <div class="tidy-main">
            <div class="tidy-label mono">{{ o.name }}</div>
            <div class="tidy-meta">
              <span :title="o.path">{{ o.path }}</span>
              <span>·</span>
              <span>{{ formatSize(o.size_bytes) }}</span>
              <template v-if="o.modified_at">
                <span>·</span>
                <span :title="formatAbsoluteTime(o.modified_at)">
                  modified {{ formatRelativeTime(o.modified_at) }}
                </span>
              </template>
            </div>
          </div>
        </div>
      </section>

      <!-- Deletion is irreversible, so the total is restated at the moment of
           confirming rather than only at the moment of selecting. -->
      <div v-if="confirming" class="tidy-confirm">
        <strong>Delete {{ selectedCount }} item{{ selectedCount === 1 ? '' : 's' }}?</strong>
        <span>
          {{ selectedChats.length }} chat{{ selectedChats.length === 1 ? '' : 's' }}
          <template v-if="selectedMessages">
            containing {{ selectedMessages }} message{{ selectedMessages === 1 ? '' : 's' }}
          </template>
          <template v-if="selectedOrphans.length">
            , {{ selectedOrphans.length }} folder{{ selectedOrphans.length === 1 ? '' : 's' }}
          </template>
          <template v-if="selectedBytes">
            , {{ formatSize(selectedBytes) }} on disk
          </template>
          . This can't be undone.
        </span>
        <!-- Named on its own line rather than folded into the sentence above:
             it is the one detail in this summary that should stop someone. -->
        <span v-if="selectedStarred" class="tidy-confirm-starred">
          ★ Includes {{ selectedStarred }} starred chat{{ selectedStarred === 1 ? '' : 's' }}.
        </span>
      </div>
    </template>

    <div class="modal-footer">
      <button class="btn btn-ghost" @click="ui.closeModal()">Close</button>
      <template v-if="!loading && enabled && !nothingToReview && !error">
        <button
          class="btn btn-ghost"
          :disabled="!selectedChats.length || busy"
          :title="`Ask me again in ${reviewDays} days`"
          @click="keepSelected"
        >
          Keep for now
        </button>
        <button
          v-if="!confirming"
          class="btn btn-danger"
          :disabled="!selectedCount || busy"
          @click="confirming = true"
        >
          Delete selected
        </button>
        <template v-else>
          <button class="btn btn-ghost" :disabled="busy" @click="confirming = false">Cancel</button>
          <button class="btn btn-danger" :disabled="busy" @click="deleteSelected">
            {{ busy ? 'Deleting…' : 'Yes, delete' }}
          </button>
        </template>
      </template>
    </div>
  </div>
</template>

<style scoped>
.tidy-modal { max-width: 640px; }
.tidy-note, .tidy-error { padding: 18px 4px; font-size: 13px; color: var(--text2); }
.tidy-error { color: var(--red); }
.tidy-empty { text-align: center; padding: 32px 0 12px; color: var(--text2); font-size: 13px; }
.tidy-empty-ico { font-size: 28px; margin-bottom: 8px; }
.tidy-toolbar {
  display: flex; align-items: center; justify-content: space-between; gap: 12px;
  padding: 8px 0; border-bottom: 1px solid var(--border); margin-bottom: 4px;
}
.tidy-count { font-size: 12px; color: var(--text2); font-variant-numeric: tabular-nums; }
.tidy-check {
  display: inline-flex; align-items: center; gap: 8px;
  font-size: 12px; color: var(--text2); cursor: pointer; flex-shrink: 0;
}
.tidy-check input { cursor: pointer; accent-color: var(--accent); }
.tidy-section { margin-top: 14px; }
/* The starred group is set apart rather than merely labelled: at a glance it
   should read as a different pile, not the next few rows of the same one. */
.tidy-section-starred {
  border-top: 1px solid var(--border);
  padding-top: 12px; margin-top: 18px;
}
.tidy-head {
  display: flex; align-items: center; gap: 8px;
  font-size: 12px; font-weight: 600; color: var(--text2);
  text-transform: uppercase; letter-spacing: .04em; margin: 0 0 6px;
}
.tidy-head-name { font-weight: 600; color: var(--text2); }
.tidy-star { color: var(--orange, #d29922); margin-right: 5px; }
.tidy-more { text-transform: none; letter-spacing: 0; font-weight: 400; color: var(--text3); font-size: 12px; }
.tidy-hint { font-size: 12px; color: var(--text3); margin: 0 0 8px; }
.tidy-row {
  display: flex; align-items: flex-start; gap: 10px;
  padding: 8px 6px; border-radius: 8px;
}
.tidy-row:hover { background: var(--hover); }
.tidy-main { min-width: 0; flex: 1; }
.tidy-label {
  font-size: 13px; color: var(--text); font-weight: 500;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.tidy-label.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
.tidy-meta {
  display: flex; gap: 6px; flex-wrap: wrap;
  font-size: 11px; color: var(--text3); margin-top: 2px;
}
.tidy-meta > span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tidy-confirm {
  display: flex; flex-direction: column; gap: 4px;
  margin-top: 14px; padding: 10px 12px; border-radius: 8px;
  border: 1px solid var(--red); background: var(--diff-del, rgba(239,68,68,.08));
  font-size: 12px; color: var(--text2);
}
.tidy-confirm strong { color: var(--text); font-size: 13px; }
.tidy-confirm-starred { color: var(--text); font-weight: 600; }
/* Deletion is the only irreversible action here, so it is the only one styled
   to look like one. There is no shared .btn-danger yet. */
.btn-danger { background: var(--red); color: #fff; border: 1px solid var(--red); }
.btn-danger:hover:not(:disabled) { filter: brightness(1.08); }
.btn:disabled { opacity: .5; cursor: default; }
</style>
