<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { fetchBackendStatus, type MigrationInfo } from '../api'

// The one-folder migration's state, for a banner: the move announced, a reason
// it can't run, a failed run, or a database an older version left behind.
const info = ref<MigrationInfo | null>(null)
const dismissed = ref(false)
let timer: number | undefined

const words = ['zero', 'one', 'two', 'three', 'four', 'five', 'six', 'seven', 'eight', 'nine']
function minutes(n?: number): string {
  if (!n || n < 1) return 'a few minutes'
  if (n === 1) return 'about one minute'
  return `about ${n < 10 ? words[n] : n} minutes`
}

async function refresh() {
  try {
    const s = await fetchBackendStatus()
    info.value = s.migration ?? null
  } catch { /* the app shows its own message when the backend is down */ }
}

const copy = computed<{ title: string; detail: string; more?: string } | null>(() => {
  const m = info.value
  if (!m) return null
  const reason = m.reasons?.[0]
  const cantMove = m.undo ? "Files can't move back yet" : "Files can't move into one folder yet"
  switch (m.state) {
    case 'planned':
      return {
        title: 'Your files will move into one folder',
        detail: `The next update moves your chats, projects and settings into Knowledge_Worker_Agent. ` +
          `It takes ${minutes(m.minutes)}, while Knowledge Worker Agent is unavailable, and needs ${m.need} of free disk space. ` +
          `Don't stop or restart the workspace while it runs.`,
      }
    case 'blocked':
      if (m.short) {
        return {
          title: cantMove,
          detail: `Knowledge Worker Agent needs ${m.need} of free disk space, and ${m.free} is free. ` +
            `Free up at least ${m.short}, then restart the workspace. Nothing has changed, and you can keep working. ` +
            `The space is used for backups, which are deleted after 30 days.`,
        }
      }
      return {
        title: cantMove,
        detail: 'Nothing has changed, and you can keep working. Knowledge Worker Agent tries again at the next start.',
        more: reason,
      }
    case 'failed':
      return {
        title: m.undo ? 'Moving your files back failed' : 'Moving your files failed',
        detail: "Everything was set back, and nothing is lost. Knowledge Worker Agent doesn't try again until the next update.",
        more: reason,
      }
    case 'stray':
      return {
        title: "Some chats aren't shown",
        detail: `A database has appeared at ${m.path} since your files moved, so an older version of Knowledge Worker Agent probably ran. Chats made there aren't shown here.`,
      }
  }
  return null
})

onMounted(() => {
  refresh()
  // The dry run's report arrives a little after startup.
  timer = window.setInterval(refresh, 60000)
})
onBeforeUnmount(() => window.clearInterval(timer))
</script>

<template>
  <div v-if="copy && !dismissed" class="migration-banner" :class="info?.state" role="status">
    <div class="mb-msg">
      <strong>{{ copy.title }}</strong>
      <span>{{ copy.detail }}</span>
      <span v-if="copy.more" class="mb-more">{{ copy.more }}</span>
    </div>
    <button class="mb-close" aria-label="Dismiss" @click="dismissed = true">×</button>
  </div>
</template>

<style scoped>
.migration-banner {
  display: flex; align-items: flex-start; gap: 12px; flex-shrink: 0;
  padding: 10px 16px;
  background: rgba(3, 169, 241, 0.10);
  border-bottom: 1px solid rgba(3, 169, 241, 0.35);
  color: var(--text);
}
.migration-banner.blocked, .migration-banner.stray {
  background: rgba(255, 193, 7, 0.10);
  border-bottom-color: rgba(255, 193, 7, 0.35);
}
.migration-banner.failed {
  background: rgba(248, 81, 73, 0.10);
  border-bottom-color: rgba(248, 81, 73, 0.35);
}
.mb-msg { display: flex; flex-direction: column; gap: 2px; flex: 1; min-width: 0; }
.mb-msg strong { font-size: 13px; }
.mb-msg span { font-size: 12px; color: var(--text2); }
.mb-msg .mb-more { color: var(--text3); overflow-wrap: anywhere; }
.mb-close {
  background: none; border: none; color: var(--text2); cursor: pointer;
  font-size: 18px; line-height: 1; padding: 0 4px;
}
.mb-close:hover { color: var(--text); }
</style>
