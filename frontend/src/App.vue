<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, watch, onMounted, onBeforeUnmount } from 'vue'
import { useUiStore } from './stores/ui'
import { useSessionsStore } from './stores/sessions'
import { useUploadsStore } from './stores/uploads'
import { useChatStore } from './stores/chat'
import { useProvidersStore } from './stores/providers'
import { useShareStore } from './stores/share'
import { BACKEND_UNAVAILABLE_EVENT, checkBackendReady, fetchActiveStreams } from './api'
import MigrationBanner from './components/MigrationBanner.vue'
import { useActivityMonitor } from './composables/useActivityMonitor'
import AppSidebar from './components/sidebar/AppSidebar.vue'
import ChatView from './components/chat/ChatView.vue'
import ProjectView from './components/chat/ProjectView.vue'
import ScheduledTasksView from './components/scheduled/ScheduledTasksView.vue'
import ModalHost from './components/modals/ModalHost.vue'
import ToastNotification from './components/ToastNotification.vue'
import BackendDownPage from './components/BackendDownPage.vue'
import BackendDisconnectedPopup from './components/BackendDisconnectedPopup.vue'

const ui = useUiStore()
const sessionsStore = useSessionsStore()
const uploads = useUploadsStore()
const chatStore = useChatStore()
const providers = useProvidersStore()

// Report typing/click activity as platform ACTIVE/IDLE heartbeats.
// Registers its own mount/unmount lifecycle hooks.
useActivityMonitor()

let dragDepth = 0
let backendHealthHandle: number | undefined
const backendDown = ref(false)
const backendDownError = ref('')
const backendDisconnected = ref(false)
const backendDisconnectedError = ref('')
const appBootstrapped = ref(false)

function hasFiles(e: DragEvent): boolean {
  const types = e.dataTransfer?.types
  if (!types) return false
  for (let i = 0; i < types.length; i++) {
    if (types[i] === 'Files') return true
  }
  return false
}

function onWindowDragEnter(e: DragEvent) {
  if (!hasFiles(e)) return
  e.preventDefault()
  dragDepth++
  uploads.dragOver = true
}

function onWindowDragOver(e: DragEvent) {
  if (!hasFiles(e)) return
  e.preventDefault()
  if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy'
}

function onWindowDragLeave(e: DragEvent) {
  if (!hasFiles(e)) return
  dragDepth = Math.max(0, dragDepth - 1)
  if (dragDepth === 0) uploads.dragOver = false
}

async function onWindowDrop(e: DragEvent) {
  if (!hasFiles(e)) return
  e.preventDefault()
  dragDepth = 0
  uploads.dragOver = false
  if (e.dataTransfer?.files && e.dataTransfer.files.length > 0) {
    await uploads.handleFiles(Array.from(e.dataTransfer.files))
  }
}

async function checkBackend(): Promise<boolean> {
  try {
    await checkBackendReady()
    backendDownError.value = ''
    return true
  } catch (err) {
    backendDownError.value = err instanceof Error && err.message
      ? err.message
      : 'Backend is not reachable'
    return false
  }
}

function stopBackendHealthMonitor() {
  if (backendHealthHandle) {
    window.clearInterval(backendHealthHandle)
    backendHealthHandle = undefined
  }
}

function showBackendDown(err?: unknown) {
  if (err instanceof Error && err.message) {
    backendDownError.value = err.message
  } else if (typeof err === 'string' && err) {
    backendDownError.value = err
  }
  backendDown.value = true
  providers.stop()
  stopBackendHealthMonitor()
}

function showBackendDisconnected(err?: unknown) {
  if (err instanceof Error && err.message) {
    backendDisconnectedError.value = err.message
  } else if (typeof err === 'string' && err) {
    backendDisconnectedError.value = err
  }
  backendDisconnected.value = true
  providers.stop()
  stopBackendHealthMonitor()
}

function handleBackendUnavailable(err?: unknown) {
  if (appBootstrapped.value) {
    showBackendDisconnected(err)
  } else {
    showBackendDown(err)
  }
}

function onBackendUnavailable(event: Event) {
  const detail = (event as CustomEvent<{ message?: string }>).detail
  handleBackendUnavailable(detail?.message || 'Backend is not reachable')
}

function startBackendHealthMonitor() {
  if (backendHealthHandle) return
  backendHealthHandle = window.setInterval(async () => {
    if (!(await checkBackend())) {
      handleBackendUnavailable(backendDownError.value || 'Backend is not reachable')
    }
  }, 5000)
}

async function bootstrapApp() {
  ui.initTheme()
  if (!(await checkBackend())) {
    showBackendDown(backendDownError.value || 'Backend is not reachable')
    return
  }

  providers.start()
  await sessionsStore.loadModels()
  await sessionsStore.loadAgents()
  await sessionsStore.loadSessions()
  // Not awaited: a badge must never hold up the first paint, and a failure
  // here should cost nothing but the badge.
  void ui.refreshTidyCount()
  // Auto-select first session or create one
  if (sessionsStore.sessions.length > 0 && !sessionsStore.currentSessionId) {
    await sessionsStore.switchSession(sessionsStore.sessions[0].id)
  } else if (sessionsStore.sessions.length === 0) {
    await sessionsStore.createSession({ name: 'New Chat' })
  }

  // Re-attach to any sessions whose work is still running on the
  // server (survives page reload). The dispatcher kept the process alive
  // and buffered all events; replay+live-stream them now.
  try {
    const active = await fetchActiveStreams()
    for (const sid of active) {
      if (!chatStore.isStreamingSession(sid)) {
        chatStore.attachStream(sid)
      }
    }
  } catch (err) {
    console.warn('[App] Failed to fetch active streams:', err)
  }
  // Keep the open chat live: turns started by anyone else — a guest, another
  // tab, the queue — arrive on its /events connection.
  watch(() => sessionsStore.currentSessionId, (id) => chatStore.followSession(id), { immediate: true })
  // Shared chats the owner does not have open still need watching.
  useShareStore().start()

  window.addEventListener('dragenter', onWindowDragEnter)
  window.addEventListener('dragover', onWindowDragOver)
  window.addEventListener('dragleave', onWindowDragLeave)
  window.addEventListener('drop', onWindowDrop)
  appBootstrapped.value = true
  startBackendHealthMonitor()
}

onMounted(async () => {
  window.addEventListener(BACKEND_UNAVAILABLE_EVENT, onBackendUnavailable)
  try {
    await bootstrapApp()
  } catch (err) {
    console.error('[App] Backend initialization failed:', err)
    handleBackendUnavailable(err)
  }
})

onBeforeUnmount(() => {
  stopBackendHealthMonitor()
  window.removeEventListener(BACKEND_UNAVAILABLE_EVENT, onBackendUnavailable)
  window.removeEventListener('dragenter', onWindowDragEnter)
  window.removeEventListener('dragover', onWindowDragOver)
  window.removeEventListener('dragleave', onWindowDragLeave)
  window.removeEventListener('drop', onWindowDrop)
})
</script>

<template>
  <BackendDownPage v-if="backendDown" :initial-error="backendDownError" />
  <template v-else>
    <AppSidebar />
    <div id="sidebar-overlay" :class="{ show: ui.sidebarOpen }" @click="ui.closeSidebar()" />
    <main id="main">
      <MigrationBanner />
      <ProjectView v-if="ui.activeView === 'project'" />
      <ScheduledTasksView v-else-if="ui.activeView === 'scheduled'" />
      <ChatView v-else />
    </main>
    <ModalHost />
    <ToastNotification />
    <BackendDisconnectedPopup v-if="backendDisconnected" :error="backendDisconnectedError" />
    <div v-if="uploads.dragOver" id="global-drop-overlay">
      <div class="drop-card">
        <div class="drop-icon">📎</div>
        <div class="drop-title">Drop files to attach</div>
        <div class="drop-sub">They will be uploaded to the current chat</div>
      </div>
    </div>
  </template>
</template>

<style scoped>
#main {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  min-width: 0;
}

#sidebar-overlay {
  display: none;
  position: fixed;
  inset: 0;
  z-index: 299;
  background: rgba(0, 0, 0, .45);
  backdrop-filter: blur(2px);
}
#sidebar-overlay.show { display: block; }

#global-drop-overlay {
  position: fixed;
  inset: 0;
  z-index: 9999;
  background: rgba(3, 169, 241, 0.10);
  border: 3px dashed var(--accent);
  display: flex;
  align-items: center;
  justify-content: center;
  pointer-events: none;
  backdrop-filter: blur(2px);
}
.drop-card {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 14px;
  padding: 28px 40px;
  text-align: center;
  box-shadow: 0 10px 40px rgba(0,0,0,.25);
}
.drop-icon { font-size: 42px; margin-bottom: 8px; }
.drop-title { font-size: 18px; font-weight: 600; color: var(--text); }
.drop-sub { font-size: 13px; color: var(--text2); margin-top: 4px; }

@media (max-width: 768px) {
  #main { margin-left: 0; }
}
</style>
