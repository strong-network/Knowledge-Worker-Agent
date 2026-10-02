<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, onBeforeUnmount, watch } from 'vue'
import { useUiStore } from '../../stores/ui'
import { useProvidersStore } from '../../stores/providers'
import { useNotifications } from '../../composables/useNotifications'
import { fetchVersionInfo } from '../../api'

// A bottom-of-sidebar Settings popover menu. Order:
//   Accounts, Theme, Alerts, MCP, Agents, Stats, Tidy up
const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ close: [] }>()

const ui = useUiStore()
const providers = useProvidersStore()
const notifications = useNotifications()

// App version (KS: version file), lazily loaded the first time the menu opens.
const appVersion = ref('')
let versionLoaded = false
async function loadVersion() {
  if (versionLoaded) return
  versionLoaded = true
  try {
    const info = await fetchVersionInfo()
    appVersion.value = info.app
  } catch {
    versionLoaded = false
  }
}

// The badge tracks whether ANY provider is usable, not specifically GitHub
// Copilot — a workspace signed in to Vertex only, or to a config-declared
// provider, used to read "Sign in" while working perfectly well.
const accountsLabel = computed(() =>
  providers.anyAuthenticated ? 'Connected' : 'Sign in',
)
const accountsDot = computed(() => (providers.anyAuthenticated ? 'ok' : 'off'))

const alertsLabel = computed(() => {
  if (!notifications.notificationsSupported) return 'n/a'
  if (notifications.notificationPermission.value === 'denied') return 'Blocked'
  return notifications.notificationsEnabled.value ? 'On' : 'Off'
})

function close() {
  emit('close')
}

function openModal(id: string) {
  ui.openModal(id)
  close()
}

function toggleTheme() {
  ui.toggleTheme()
  // Keep the menu open so the user can see the effect / toggle again.
}

async function toggleAlerts() {
  const enabled = await notifications.enable()
  ;(window as any).showToast?.(
    enabled ? 'Chat notifications enabled' : 'Notifications are blocked in this browser',
  )
}

// Close on outside click / Escape while open.
function onPointerDown(e: MouseEvent) {
  const t = e.target as HTMLElement
  if (!t.closest('.settings-menu') && !t.closest('.settings-trigger')) close()
}
function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') close()
}
watch(() => props.open, (open) => {
  if (open) {
    loadVersion()
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeydown)
  } else {
    document.removeEventListener('pointerdown', onPointerDown)
    document.removeEventListener('keydown', onKeydown)
  }
})
onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onPointerDown)
  document.removeEventListener('keydown', onKeydown)
})
</script>

<template>
  <div v-if="open" class="settings-menu" role="menu">
    <button class="sm-item" role="menuitem" @click="openModal('accounts')">
      <span class="sm-ico">👤</span>
      <span class="sm-name">Accounts</span>
      <span class="sm-status"><span class="acc-dot" :class="accountsDot" />{{ accountsLabel }}</span>
    </button>
    <button class="sm-item" role="menuitem" @click="toggleTheme">
      <span class="sm-ico">{{ ui.theme === 'dark' ? '🌙' : '☀️' }}</span>
      <span class="sm-name">Theme</span>
      <span class="sm-status">{{ ui.theme === 'dark' ? 'Dark' : 'Light' }}</span>
    </button>
    <button
      class="sm-item"
      role="menuitem"
      :disabled="!notifications.notificationsSupported || notifications.notificationPermission.value === 'denied'"
      @click="toggleAlerts"
    >
      <span class="sm-ico">🔔</span>
      <span class="sm-name">Alerts</span>
      <span class="sm-status">{{ alertsLabel }}</span>
    </button>
    <button class="sm-item" role="menuitem" @click="openModal('mcp-servers')">
      <span class="sm-ico">🔌</span>
      <span class="sm-name">Connectors</span>
    </button>
    <button class="sm-item" role="menuitem" @click="openModal('agent-builder')">
      <span class="sm-ico">🤖</span>
      <span class="sm-name">Agents</span>
    </button>
    <button class="sm-item" role="menuitem" @click="openModal('stats')">
      <span class="sm-ico">📊</span>
      <span class="sm-name">Stats</span>
    </button>
    <button class="sm-item" role="menuitem" @click="openModal('tidy-up')">
      <span class="sm-ico">🧹</span>
      <span class="sm-name">Tidy up</span>
      <span v-if="ui.tidyCount" class="sm-status">{{ ui.tidyCount }} to review</span>
    </button>

    <div class="sm-version" role="note">
      <span class="sm-ver-label">Version</span>
      <span class="sm-ver-value">{{ appVersion || '…' }}</span>
    </div>
  </div>
</template>

<style scoped>
.settings-menu {
  position: absolute; bottom: calc(100% + 8px); left: 12px; right: 12px;
  background: var(--surface); border: 1px solid var(--border);
  border-radius: 12px; box-shadow: 0 12px 40px rgba(0,0,0,.28);
  padding: 6px; z-index: 80; animation: smPop .12s ease;
}
@keyframes smPop { from { opacity: 0; transform: translateY(4px); } to { opacity: 1; transform: translateY(0); } }
.sm-item {
  display: flex; align-items: center; gap: 10px; width: 100%;
  padding: 9px 10px; border: none; background: none; cursor: pointer;
  border-radius: 8px; font-size: 13px; color: var(--text); text-align: left;
  font-family: inherit; transition: background .1s;
}
.sm-item:hover:not(:disabled) { background: var(--hover); }
.sm-item:disabled { opacity: .5; cursor: not-allowed; }
.sm-ico { font-size: 15px; width: 20px; text-align: center; flex-shrink: 0; }
.sm-name { flex: 1; font-weight: 500; }
.sm-status {
  display: inline-flex; align-items: center; gap: 5px;
  font-size: 11px; color: var(--text2); flex-shrink: 0;
}
.acc-dot { width: 7px; height: 7px; border-radius: 50%; display: inline-block; }
.acc-dot.ok  { background: #22c55e; }
.acc-dot.off { background: #ef4444; }
.sm-version {
  display: flex; align-items: center; justify-content: space-between;
  padding: 8px 10px 4px; margin-top: 4px;
  border-top: 1px solid var(--border);
  font-size: 11px; color: var(--text3);
}
.sm-ver-value { font-variant-numeric: tabular-nums; color: var(--text2); }
</style>
