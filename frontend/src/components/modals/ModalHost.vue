<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref } from 'vue'
import { useUiStore } from '../../stores/ui'
import { useModalDialog } from '../../composables/useModalDialog'
import NewChatModal from './NewChatModal.vue'
import ModelPicker from './ModelPicker.vue'
import StatsModal from './StatsModal.vue'
import SessionSettings from './SessionSettings.vue'
import ConnectorsModal from '../connectors/ConnectorsModal.vue'
import ToolboxModal from '../toolbox/ToolboxModal.vue'
import SourceControlModal from './SourceControlModal.vue'
import OpencodeLoginModal from './OpencodeLoginModal.vue'
import VertexLoginModal from './VertexLoginModal.vue'
import AccountsModal from './AccountsModal.vue'
import FolderPickerModal from './FolderPickerModal.vue'
import GithubRepoModal from './GithubRepoModal.vue'
import CreateProjectModal from './CreateProjectModal.vue'
import ProjectSettingsModal from './ProjectSettingsModal.vue'
import SummarizeModal from './SummarizeModal.vue'
import AgentBuilderModal from './AgentBuilderModal.vue'
import ScheduledTaskModal from './ScheduledTaskModal.vue'
import TidyUpModal from './TidyUpModal.vue'
import ShareModal from './ShareModal.vue'

const ui = useUiStore()

const overlay = ref<HTMLElement | null>(null)

// Every modal below is a `.modal` panel inside the one overlay, so the shared
// dialog behaviour is installed once here rather than eighteen times. The panel
// is found by class instead of by template ref because the modals are separate
// components; keying off `ui.activeModal` re-runs it when one modal opens
// another, which a couple of them do.
useModalDialog(() => overlay.value?.querySelector<HTMLElement>('.modal'), {
  key: () => ui.activeModal,
  onClose: () => ui.closeModal(),
})

// Only a click that also began on the backdrop closes the modal. A press inside
// the dialog released outside it — a text selection, or content that moved
// under the pointer — lands its click on the backdrop too.
let pressedOnOverlay = false

function closeOnOverlay(e: MouseEvent) {
  const onOverlay = (e.target as HTMLElement).classList.contains('modal-overlay')
  if (onOverlay && pressedOnOverlay) ui.closeModal()
  pressedOnOverlay = false
}
</script>

<template>
  <div
    v-if="ui.activeModal"
    ref="overlay"
    class="modal-overlay"
    @pointerdown="pressedOnOverlay = $event.target === $event.currentTarget"
    @click="closeOnOverlay"
  >
    <NewChatModal v-if="ui.activeModal === 'new-chat'" />
    <ModelPicker v-if="ui.activeModal === 'model-picker'" />
    <StatsModal v-if="ui.activeModal === 'stats'" />
    <SessionSettings v-if="ui.activeModal === 'session-settings'" />
    <ConnectorsModal v-if="ui.activeModal === 'mcp-servers'" />
    <ToolboxModal v-if="ui.activeModal === 'toolbox'" />
    <SourceControlModal v-if="ui.activeModal === 'source-control'" />
    <OpencodeLoginModal v-if="ui.activeModal === 'opencode-login'" />
    <VertexLoginModal v-if="ui.activeModal === 'vertex-login'" />
    <AccountsModal v-if="ui.activeModal === 'accounts'" />
    <FolderPickerModal v-if="ui.activeModal === 'folder-picker'" />
    <GithubRepoModal v-if="ui.activeModal === 'github-repo'" />
    <CreateProjectModal v-if="ui.activeModal === 'create-project'" />
    <ProjectSettingsModal v-if="ui.activeModal === 'project-settings'" />
    <SummarizeModal v-if="ui.activeModal === 'summarize'" />
    <AgentBuilderModal v-if="ui.activeModal === 'agent-builder'" />
    <ScheduledTaskModal v-if="ui.activeModal === 'scheduled-task'" />
    <TidyUpModal v-if="ui.activeModal === 'tidy-up'" />
    <ShareModal v-if="ui.activeModal === 'share'" />
  </div>
</template>
