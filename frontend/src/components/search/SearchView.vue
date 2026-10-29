<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useSearchStore, type SearchRange } from '../../stores/search'
import { useUiStore } from '../../stores/ui'
import { highlightSegments } from '../../utils/searchHighlight'
import { formatRelativeTime } from '../../utils/time'
import type { ChatSearchHit } from '../../api'
import TimerIcon from '../TimerIcon.vue'

const search = useSearchStore()
const ui = useUiStore()
const inputEl = ref<HTMLInputElement | null>(null)

const RANGES: Array<{ value: SearchRange; label: string }> = [
  { value: 'any', label: 'Any time' },
  { value: 'week', label: 'Past week' },
  { value: 'month', label: 'Past month' },
  { value: 'year', label: 'Past year' },
  { value: 'custom', label: 'Custom range' },
]

// Searches as you type, once you pause.
let timer: ReturnType<typeof setTimeout> | undefined
function schedule() {
  clearTimeout(timer)
  timer = setTimeout(() => search.run(), 250)
}
function runNow() {
  clearTimeout(timer)
  void search.run()
}
watch(() => [search.range, search.customFrom, search.customTo], () => {
  if (search.query.trim()) runNow()
})

onMounted(() => {
  inputEl.value?.focus()
  // Back from a result: show the results again, brought up to date.
  if (search.query.trim()) runNow()
})
onBeforeUnmount(() => clearTimeout(timer))

const today = new Date().toLocaleDateString('en-CA')

// Matches allowing for typos come after the exact ones, under a heading.
const closeStart = computed(() => search.chats.findIndex(c => !c.exact))

function segments(text: string, hit: ChatSearchHit) {
  return highlightSegments(text, search.termsFor(hit))
}

function who(hit: ChatSearchHit): string {
  const m = hit.message
  if (!m) return 'Title match'
  return m.role === 'user' ? m.author_name || 'You' : 'Knowledge Worker Agent'
}

function when(at?: string): string {
  if (!at) return ''
  const d = new Date(at)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}

function more(hit: ChatSearchHit): string {
  const n = hit.message ? hit.matches - 1 : 0
  return n > 0 ? `${n} more ${n === 1 ? 'match' : 'matches'} in this chat` : ''
}
</script>

<template>
  <div id="search-view">
    <div class="sv-header">
      <button
        class="hamburger-btn"
        @click="ui.toggleSidebar()"
        :aria-label="ui.sidebarOpen ? 'Close menu' : 'Open menu'"
        title="Menu"
      >
        <span></span><span></span><span></span>
      </button>
      <h1>Search chats</h1>
    </div>

    <div class="sv-body">
      <div class="sv-controls">
        <div class="sv-field">
          <svg class="sv-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16" aria-hidden="true">
            <circle cx="11" cy="11" r="7"/><path d="M21 21l-4.3-4.3" stroke-linecap="round"/>
          </svg>
          <input
            id="search-input"
            ref="inputEl"
            v-model="search.query"
            type="search"
            placeholder="Search titles and messages"
            aria-label="Search chats"
            autocomplete="off"
            spellcheck="false"
            @input="schedule"
            @keydown.enter="runNow"
          />
        </div>
        <select v-model="search.range" class="sv-range" aria-label="Time range">
          <option v-for="r in RANGES" :key="r.value" :value="r.value">{{ r.label }}</option>
        </select>
      </div>
      <div v-if="search.range === 'custom'" class="sv-dates">
        <label>From <input v-model="search.customFrom" type="date" :max="search.customTo || today" /></label>
        <label>To <input v-model="search.customTo" type="date" :min="search.customFrom" :max="today" /></label>
      </div>
      <p class="sv-tip">Finds words in your chats, including similar spellings. Put a phrase in quotes to find it exactly.</p>

      <p class="sv-status" role="status">
        <template v-if="search.error">{{ search.error }}</template>
        <template v-else-if="search.loading && !search.chats.length">Searching…</template>
        <template v-else-if="search.searched && !search.total">
          No chats match “{{ search.searched }}”{{ search.range !== 'any' ? ' in this time range' : '' }}.
        </template>
        <template v-else-if="search.searched">
          {{ search.total }} {{ search.total === 1 ? 'chat' : 'chats' }}<template v-if="search.match === 'any'">.
            No chat has all the words, so these have some of them</template>
        </template>
      </p>

      <ul v-if="search.chats.length" class="sv-list" aria-label="Results">
        <li v-for="(hit, i) in search.chats" :key="hit.session_id">
          <div v-if="i === closeStart" class="sv-section">Close matches</div>
          <button class="sv-hit" @click="search.open(hit)">
            <span class="sv-title-row">
              <span class="sv-title"><template v-for="(s, k) in segments(hit.title || 'Untitled chat', hit)" :key="k"><mark v-if="s.hit">{{ s.text }}</mark><template v-else>{{ s.text }}</template></template></span>
              <span v-if="hit.temporary" class="sv-badge"><TimerIcon :size="11" />Temporary</span>
              <span v-if="hit.project" class="sv-project">{{ hit.project }}</span>
            </span>
            <span v-if="hit.message?.snippet || hit.excerpt" class="sv-snippet"><template v-for="(s, k) in segments(hit.message?.snippet || hit.excerpt || '', hit)" :key="k"><mark v-if="s.hit">{{ s.text }}</mark><template v-else>{{ s.text }}</template></template></span>
            <span class="sv-meta">
              <span>{{ who(hit) }}</span>
              <template v-if="hit.message">
                · <time :datetime="hit.message.created_at" :title="formatRelativeTime(hit.message.created_at)">{{ when(hit.message.created_at) }}</time>
              </template>
              <template v-else>
                · Last active <time :datetime="hit.updated_at" :title="formatRelativeTime(hit.updated_at)">{{ when(hit.updated_at) }}</time>
              </template>
              <template v-if="more(hit)"> · {{ more(hit) }}</template>
            </span>
          </button>
        </li>
      </ul>

      <button
        v-if="search.chats.length < search.total"
        class="sv-more"
        :disabled="search.loading"
        @click="search.run(true)"
      >
        {{ search.loading ? 'Loading…' : 'Show more' }}
      </button>
    </div>
  </div>
</template>

<style scoped>
/* Laid out like the Artifacts and Scheduled tasks views. */
#search-view { flex: 1; display: flex; flex-direction: column; overflow: hidden; min-width: 0; }
.sv-header {
  display: flex; align-items: center; gap: 10px;
  padding: 20px 24px 16px; border-bottom: 1px solid var(--border);
}
.sv-header h1 { font-size: 20px; font-weight: 700; color: var(--text); margin: 0; }

.sv-body { flex: 1; overflow-y: auto; padding: 18px 24px 32px; }
.sv-body > * { max-width: 860px; }

.sv-controls { display: flex; gap: 8px; }
.sv-field { position: relative; flex: 1; min-width: 0; display: flex; }
.sv-icon {
  position: absolute; left: 12px; top: 50%; transform: translateY(-50%);
  color: var(--text2); pointer-events: none;
}
#search-input {
  width: 100%; padding: 10px 12px 10px 38px;
  border: 1px solid var(--border); border-radius: 10px;
  background: var(--surface2); color: var(--text); font-size: 15px; font-family: inherit; outline: none;
}
#search-input:focus { border-color: var(--accent); }
#search-input::placeholder { color: var(--text2); }
.sv-range, .sv-dates input {
  padding: 8px 10px; border: 1px solid var(--border); border-radius: 10px;
  background: var(--surface2); color: var(--text); font-size: 13px; font-family: inherit;
}
/* The native arrow sits against the edge; draw one with room around it, as Diagnostics does. */
.sv-range {
  appearance: none;
  padding-right: 34px;
  background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 12 12'%3E%3Cpath d='M3 4.5l3 3 3-3' fill='none' stroke='%23888' stroke-width='1.5' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E");
  background-repeat: no-repeat;
  background-position: right 12px center;
  background-size: 12px;
  cursor: pointer;
}
.sv-range:focus, .sv-dates input:focus { outline: none; border-color: var(--accent); }
.sv-dates { display: flex; flex-wrap: wrap; gap: 12px; margin-top: 10px; }
.sv-dates label { display: flex; align-items: center; gap: 6px; font-size: 13px; color: var(--text2); }
.sv-tip { font-size: 12.5px; color: var(--text3); margin: 8px 0 0; }
.sv-status { font-size: 13px; color: var(--text2); margin: 16px 0 8px; min-height: 1.2em; }

.sv-list { list-style: none; margin: 0; padding: 0; }
.sv-section {
  font-size: 11px; font-weight: 600; color: var(--text3); text-transform: uppercase;
  letter-spacing: .06em; margin: 18px 0 8px;
}
.sv-hit {
  display: flex; flex-direction: column; gap: 4px; width: 100%;
  padding: 12px 14px; margin-bottom: 8px; text-align: left;
  border: 1px solid var(--border); border-radius: 12px; background: var(--surface);
  color: var(--text); font: inherit; cursor: pointer; transition: background .12s, border-color .12s;
}
.sv-hit:hover { background: var(--hover); }
.sv-hit:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
.sv-title-row { display: flex; align-items: center; gap: 8px; min-width: 0; }
.sv-title { font-size: 15px; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sv-badge {
  flex-shrink: 0; display: inline-flex; align-items: center; gap: 3px;
  font-size: 11px; color: var(--text2); border: 1px solid var(--border); border-radius: 5px; padding: 0 5px;
}
.sv-project { flex-shrink: 0; font-size: 12px; color: var(--text3); }
.sv-snippet {
  font-size: 13.5px; line-height: 1.5; color: var(--text2);
  display: -webkit-box; -webkit-line-clamp: 3; line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden;
}
.sv-meta { font-size: 12px; color: var(--text3); }
.sv-hit mark { background: var(--search-hit-bg); color: inherit; border-radius: 2px; padding: 0 1px; }

.sv-more {
  display: block; margin: 8px 0 0; padding: 8px 14px;
  border: 1px solid var(--border); border-radius: 8px; background: none;
  color: var(--text2); font: inherit; font-size: 13px; cursor: pointer;
}
.sv-more:hover:not(:disabled) { color: var(--text); background: var(--hover); }
.sv-more:disabled { opacity: .6; cursor: default; }

/* As in the chat header. */
.hamburger-btn {
  display: none;
  width: 38px; height: 38px; flex-shrink: 0; padding: 0;
  flex-direction: column; justify-content: center; align-items: center; gap: 4px;
  background: none; border: 1px solid var(--border); border-radius: 8px; cursor: pointer;
  transition: background .15s, border-color .15s;
}
.hamburger-btn:hover { background: var(--surface2); border-color: var(--text2); }
.hamburger-btn span { display: block; width: 18px; height: 2px; background: var(--text); border-radius: 2px; }

@media (max-width: 768px) {
  .hamburger-btn { display: inline-flex; }
  .sv-header { padding: 10px 12px; }
  .sv-body { padding: 14px 12px 24px; }
  .sv-controls { flex-direction: column; }
}
</style>
