<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { useSessionsStore } from '../../stores/sessions'
import { useChatStore } from '../../stores/chat'
import GoalsPanelView from './GoalsPanelView.vue'

// The owner's "Plan & progress" panel: the view, fed from the stores.
const sessionsStore = useSessionsStore()
const chatStore = useChatStore()
</script>

<template>
  <GoalsPanelView :live-calls="chatStore.toolCalls" :messages="sessionsStore.currentMessages" />
</template>
