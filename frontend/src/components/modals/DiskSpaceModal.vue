<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { fetchBackendStatus, type LowDiskInfo } from '../../api'
import { useUiStore } from '../../stores/ui'

// How to get more disk space. Knowledge Worker Agent can't resize the disk:
// the platform changes a workspace's specification only from the console.
const ui = useUiStore()
const disk = ref<LowDiskInfo | null>(null)

onMounted(async () => {
  try {
    disk.value = (await fetchBackendStatus()).disk ?? null
  } catch { /* the steps don't depend on it */ }
})

const workspace = computed(() => (disk.value?.workspace ? `"${disk.value.workspace}"` : 'this workspace'))
</script>

<template>
  <div class="modal disk-modal" @click.stop>
    <h2>Get more disk space</h2>
    <p v-if="disk" class="modal-subtitle">{{ disk.free }} of {{ disk.total }} is free on this workspace's disk.</p>

    <h3>Increase the disk</h3>
    <p class="dm-text">Knowledge Worker Agent can't change the size of the disk, but you can in the SecurSpaces console:</p>
    <ol class="dm-steps">
      <li>
        Open the SecurSpaces console.
        <a v-if="disk?.console_url" class="dm-link" :href="disk.console_url" target="_blank" rel="noopener noreferrer">Open the console ↗</a>
      </li>
      <li>In your workspaces, open the menu for {{ workspace }}, and select <strong>Edit</strong>.</li>
      <li>Under <strong>Workspace Specifications</strong>, increase the storage, and save your changes.</li>
    </ol>
    <p class="dm-note">If you can't increase it, your administrator sets the size of your workspace. Ask them for more storage.</p>

    <h3>Or free up space</h3>
    <div class="dm-actions">
      <button class="btn btn-ghost" @click="ui.openModal('diagnostics')">See what's using space</button>
      <button class="btn btn-ghost" @click="ui.openModal('tidy-up')">Tidy up old chats</button>
    </div>

    <div class="modal-footer">
      <button class="btn btn-primary" @click="ui.closeModal()">Close</button>
    </div>
  </div>
</template>

<style scoped>
.disk-modal { max-width: 520px; }
.disk-modal h3 { margin: 18px 0 6px; font-size: 14px; font-weight: 650; }
.dm-text, .dm-note { margin: 0 0 8px; font-size: 13px; line-height: 1.5; color: var(--text2); }
.dm-note { font-size: 12px; }
.dm-steps { margin: 0 0 10px; padding-left: 20px; font-size: 13px; line-height: 1.6; }
.dm-steps li { margin-bottom: 4px; }
.dm-link {
  display: inline-block; margin-left: 6px;
  color: var(--accent); font-weight: 600; text-decoration: none;
}
.dm-link:hover { text-decoration: underline; }
.dm-actions { display: flex; flex-wrap: wrap; gap: 8px; }
</style>
