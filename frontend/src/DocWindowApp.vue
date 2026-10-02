<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// Root of the pop-out document window.
//
// This is the same SPA, booted with `?doc=<path>`: main.ts mounts this instead
// of the chat app. It deliberately does *not* run the chat app's bootstrap —
// that loads models, agents and sessions and will create a session if none
// exist, none of which a document viewer should be doing.
//
// It talks to the chat window over a BroadcastChannel: the chat window sends
// files to show and tells it when the agent has written to disk.
import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import { useUiStore } from './stores/ui'
import DocumentView from './components/chat/DocumentView.vue'
import ToastNotification from './components/ToastNotification.vue'
import { connectDocWindow } from './composables/useDocWindow'

const ui = useUiStore()

const path = ref(new URLSearchParams(window.location.search).get('doc') || '')
const docView = ref<InstanceType<typeof DocumentView> | null>(null)
const dirty = ref(false)

const name = computed(() => path.value.split('/').filter(Boolean).pop() || 'Document')
const dir = computed(() => {
  const i = path.value.lastIndexOf('/')
  return i > 0 ? path.value.slice(0, i) : '/'
})

function setPath(next: string) {
  if (!next || next === path.value) return
  path.value = next
  // Keep the URL in step so reloading this window reopens the same document
  // rather than whichever file it happened to be opened with.
  window.history.replaceState(null, '', `/?doc=${encodeURIComponent(next)}`)
}

// The window's title is how you find it in a taskbar or a window switcher,
// which is exactly the journey this feature exists to make bearable.
watch(name, (n) => { document.title = `${n} — Knowledge Worker Agent` }, { immediate: true })

// Theme lives in localStorage, which is shared across windows of the same
// origin — but only this window's own writes update its store, so mirror the
// chat window's changes rather than sitting in the wrong theme until reload.
function onStorage(e: StorageEvent) {
  if (e.key !== 'theme') return
  if (e.newValue === 'dark' || e.newValue === 'light') ui.setTheme(e.newValue)
}

let disconnect: (() => void) | null = null

onMounted(() => {
  ui.initTheme()
  window.addEventListener('storage', onStorage)
  disconnect = connectDocWindow({
    currentPath: () => path.value,
    onOpen: (next) => {
      // An unsaved buffer here is the user's work; a click over in the file
      // tree shouldn't silently discard it.
      if (dirty.value && !confirm(`Unsaved changes in ${name.value}. Discard?`)) return
      setPath(next)
    },
    onRefresh: () => docView.value?.reload(),
  })
})

onBeforeUnmount(() => {
  window.removeEventListener('storage', onStorage)
  disconnect?.()
})

// A reload button matters more here than in the panel: this window has no chat
// of its own, so if the link to the chat window is broken it has nothing else
// telling it the file changed.
function reload() { docView.value?.reopen() }
</script>

<template>
  <div class="dw">
    <div class="dw-bar">
      <span class="dw-app">Knowledge Worker Agent</span>
      <span class="dw-dir" :title="path">{{ dir }}</span>
      <button class="dw-ico" @click="ui.toggleTheme()" :title="ui.theme === 'dark' ? 'Light theme' : 'Dark theme'" aria-label="Toggle theme">
        <svg v-if="ui.theme === 'dark'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="15" height="15"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" stroke-linecap="round"/></svg>
        <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="15" height="15"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z" stroke-linecap="round" stroke-linejoin="round"/></svg>
      </button>
    </div>

    <DocumentView
      v-if="path"
      ref="docView"
      :path="path"
      :name="name"
      @update:dirty="dirty = $event"
    >
      <template #trailing>
        <button class="dw-ico" @click="reload" title="Reload from disk" aria-label="Reload from disk">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="15" height="15"><path d="M21 12a9 9 0 1 1-2.64-6.36M21 3v6h-6" stroke-linecap="round" stroke-linejoin="round"/></svg>
        </button>
      </template>
    </DocumentView>

    <div v-else class="dw-empty">
      <div class="dw-empty-title">No document</div>
      <div class="dw-empty-sub">Pick a file in the chat window and it will open here.</div>
    </div>

    <ToastNotification />
  </div>
</template>

<style scoped>
.dw {
  position: fixed; inset: 0;
  display: flex; flex-direction: column;
  background: var(--surface);
  color: var(--text);
}
/* A slim identity strip: a bare document in its own window is otherwise hard
   to place, especially once a few of them are open. */
.dw-bar {
  display: flex; align-items: center; gap: 10px;
  padding: 6px 10px; flex-shrink: 0;
  border-bottom: 1px solid var(--border);
  background: var(--surface2);
  font-size: 12px;
}
.dw-app { font-weight: 700; color: var(--text2); white-space: nowrap; }
.dw-dir {
  flex: 1; min-width: 0;
  color: var(--text3); font-family: var(--mono); font-size: 11.5px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  /* No `direction: rtl` to ellipsis the head instead of the tail: it moves the
     leading "/" of an absolute path to the end, so /tmp/x renders as tmp/x/.
     The full path is on the element's title anyway. */
}
.dw-ico {
  width: 26px; height: 26px; display: flex; align-items: center; justify-content: center;
  background: none; border: none; color: var(--text2); border-radius: 6px; cursor: pointer;
  transition: all .12s; flex-shrink: 0;
}
.dw-ico:hover { background: var(--surface3); color: var(--text); }
.dw-empty {
  flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: 6px; text-align: center;
}
.dw-empty-title { font-size: 15px; font-weight: 600; }
.dw-empty-sub { font-size: 12px; color: var(--text2); }
</style>
