<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useUiStore } from '../../stores/ui'
import { useSessionsStore } from '../../stores/sessions'
import { useChatStore } from '../../stores/chat'
import { fetchSummarizeCandidates, runSummarize, type SummarizeCandidate } from '../../api'
import { formatRelativeDate } from '../../utils/time'

// Summarize a project: pick chats (recent ones pre-selected), then kick off an
// agent turn that consolidates their important context into PROJECT_SUMMARY.md
// in the project's shared workspace. The turn runs in the background (the
// backend auto-commits + pushes when it finishes); the modal closes and opens
// the summarization chat so the user watches progress live instead of waiting.
const ui = useUiStore()
const sessionsStore = useSessionsStore()
const chatStore = useChatStore()

const projectId = computed(() => ui.activeProjectId || '')

const candidates = ref<SummarizeCandidate[]>([])
const selected = ref<Set<string>>(new Set())
const instructions = ref('')

const loading = ref(true)
const running = ref(false)
const error = ref('')

const selectedCount = computed(() => selected.value.size)
const canRun = computed(() => selectedCount.value > 0 && !running.value)
const allSelected = computed(
  () => candidates.value.length > 0 && selected.value.size === candidates.value.length,
)

async function load() {
  if (!projectId.value) return
  loading.value = true
  error.value = ''
  try {
    const list = await fetchSummarizeCandidates(projectId.value)
    candidates.value = list
    // Pre-select chats active within the last week.
    selected.value = new Set(list.filter(c => c.active_last_week).map(c => c.session_id))
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not load chats'
  } finally {
    loading.value = false
  }
}
onMounted(load)

function toggle(id: string) {
  const s = new Set(selected.value)
  if (s.has(id)) s.delete(id)
  else s.add(id)
  selected.value = s
}

function toggleAll() {
  if (allSelected.value) selected.value = new Set()
  else selected.value = new Set(candidates.value.map(c => c.session_id))
}

async function run() {
  if (!canRun.value) return
  running.value = true
  error.value = ''
  try {
    const res = await runSummarize(
      projectId.value,
      Array.from(selected.value),
      instructions.value.trim() || undefined,
    )
    const sid = res.session_id
    // Close the modal and open the summarization chat so the user watches the
    // agent work live. The turn continues in the background and the server
    // auto-commits + pushes the summary when it finishes.
    ui.closeModal()
    ;(window as any).showToast?.('Summarizing… writing ' + (res.file || 'PROJECT_SUMMARY.md'))
    if (sid) {
      ui.showChat()
      // Refresh the list so the new "Project summary" chat is present, then
      // switch to it and attach to its live stream.
      await sessionsStore.loadSessions()
      await sessionsStore.switchSession(sid)
      if (!chatStore.isStreamingSession(sid)) {
        chatStore.attachStream(sid)
      }
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Summarize failed'
    running.value = false
  }
}
</script>

<template>
  <div class="modal summarize-modal" @click.stop style="width:560px">
    <h2>Summarize project</h2>
    <p class="modal-subtitle">
      Consolidate the important context from selected chats into
      <code>PROJECT_SUMMARY.md</code>. This runs in the background and is
      committed &amp; pushed automatically when it finishes.
    </p>

    <!-- Loading -->
    <div v-if="loading" class="sm-state">
      <span class="sm-spinner" aria-hidden="true" />
      Loading chats…
    </div>

    <!-- Empty -->
    <div v-else-if="!candidates.length" class="sm-empty">
      No chats with content in this project yet.
    </div>

    <template v-else>
      <div class="sm-list-head">
        <label class="sm-selectall">
          <input type="checkbox" :checked="allSelected" @change="toggleAll" :disabled="running" />
          <span>Select all</span>
        </label>
        <span class="sm-count">{{ selectedCount }} selected</span>
      </div>

      <div class="sm-list">
          <label
            v-for="c in candidates"
            :key="c.session_id"
            class="sm-item"
            :class="{ checked: selected.has(c.session_id) }"
          >
            <input
              type="checkbox"
              :checked="selected.has(c.session_id)"
              @change="toggle(c.session_id)"
              :disabled="running"
            />
            <span class="sm-item-body">
              <span class="sm-item-title">{{ c.title }}</span>
              <span class="sm-item-meta">
                {{ c.messages }} message{{ c.messages === 1 ? '' : 's' }} ·
                {{ formatRelativeDate(c.updated_at) }}
                <span v-if="c.active_last_week" class="sm-badge">this week</span>
              </span>
            </span>
          </label>
        </div>

      <label class="sm-label">Extra instructions <span class="sm-opt">(optional)</span></label>
      <textarea
        v-model="instructions"
        class="sm-textarea"
        rows="2"
        placeholder="e.g. focus on open decisions and action items"
        :disabled="running"
      ></textarea>
    </template>

    <p v-if="error" class="sm-error">{{ error }}</p>

    <div class="modal-footer">
      <button class="btn btn-ghost" @click="ui.closeModal()" :disabled="running">Cancel</button>
      <button class="btn btn-primary" @click="run" :disabled="!canRun">
        {{ running ? 'Starting…' : `Summarize ${selectedCount || ''}`.trim() }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.summarize-modal code { font-family: var(--mono); font-size: 12px; }
.sm-state {
  display: flex; align-items: center; justify-content: center; gap: 10px;
  padding: 26px; color: var(--text2); font-size: 13px;
  border: 1px solid var(--border); border-radius: 8px; margin-top: 4px;
}
.sm-empty {
  padding: 22px; text-align: center; color: var(--text3); font-size: 13px;
  border: 1px solid var(--border); border-radius: 8px; margin-top: 4px;
}
.sm-spinner {
  display: inline-block; width: 16px; height: 16px;
  border: 2px solid var(--border); border-top-color: var(--accent);
  border-radius: 50%; animation: sm-spin .8s linear infinite;
}
@keyframes sm-spin { to { transform: rotate(360deg); } }

.sm-list-head {
  display: flex; align-items: center; justify-content: space-between;
  margin: 8px 2px 6px;
}
.sm-selectall { display: flex; align-items: center; gap: 8px; font-size: 13px; color: var(--text); cursor: pointer; }
.sm-count { font-size: 12px; color: var(--text2); }
.sm-list {
  border: 1px solid var(--border); border-radius: 10px; overflow-y: auto;
  max-height: 300px;
}
.sm-item {
  display: flex; align-items: flex-start; gap: 10px;
  padding: 10px 12px; border-bottom: 1px solid var(--border); cursor: pointer;
  transition: background .1s;
}
.sm-item:last-child { border-bottom: none; }
.sm-item:hover { background: var(--surface2); }
.sm-item.checked { background: rgba(3,169,241,.06); }
.sm-item input { margin-top: 2px; flex-shrink: 0; }
.sm-item-body { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.sm-item-title { font-size: 14px; font-weight: 600; color: var(--text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sm-item-meta { font-size: 12px; color: var(--text2); display: flex; align-items: center; gap: 6px; }
.sm-badge {
  font-size: 10px; font-weight: 600; color: var(--accent);
  background: rgba(3,169,241,.12); border-radius: 999px; padding: 1px 7px;
}
.sm-label { display: block; font-size: 12px; font-weight: 600; color: var(--text2); margin: 14px 0 5px; }
.sm-opt { font-weight: 400; color: var(--text3); }
.sm-textarea {
  width: 100%; padding: 9px 11px; font-size: 13px; resize: vertical;
  border: 1px solid var(--border); border-radius: 8px;
  background: var(--surface); color: var(--text); font-family: inherit;
}
.sm-textarea:focus { outline: none; border-color: var(--accent); }
.sm-error { color: var(--red); font-size: 12px; margin-top: 10px; }
</style>
