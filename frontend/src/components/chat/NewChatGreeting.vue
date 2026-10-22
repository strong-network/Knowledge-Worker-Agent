<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { computed } from 'vue'
import { useMe } from '../../composables/useMe'
import TimerIcon from '../TimerIcon.vue'
import { TEMPORARY_ABOUT } from '../../utils/temporary'

// temporary: the chat about to start is a temporary chat.
defineProps<{ temporary?: boolean }>()

// The headline on a new chat. Addressing the user by name makes the blank
// page feel like it is waiting for them rather than for anyone.
const { firstName } = useMe()

// The name arrives asynchronously and may never arrive at all (no
// OWNER_FULL_NAME set). Both readings have to stand on their own, so the
// name is a suffix rather than something the sentence is built around —
// no "there," no placeholder, no visible swap when it loads.
const headline = computed(() =>
  firstName.value ? `What can I help with, ${firstName.value}?` : 'What can I help with?'
)
</script>

<template>
  <div v-if="temporary" class="nc-temporary">
    <span class="nc-timer" aria-hidden="true"><TimerIcon :size="20" /></span>
    <h1 class="nc-greeting">Temporary chat</h1>
    <p class="nc-about">{{ TEMPORARY_ABOUT }}</p>
  </div>
  <h1 v-else class="nc-greeting">{{ headline }}</h1>
</template>

<style scoped>
/* Large and light rather than large and bold: this is an invitation, not a
   page title, and at this size weight reads as shouting. Scales with the
   viewport so it stays proportionate in a narrow window without wrapping
   into two lines at the widths people actually use. */
.nc-greeting {
  margin: 0;
  padding: 0 24px;
  font-size: clamp(22px, 2.6vw, 32px);
  font-weight: 500;
  line-height: 1.2;
  letter-spacing: -0.02em;
  color: var(--text);
  text-align: center;
  text-wrap: balance;
}
.nc-temporary {
  display: flex; flex-direction: column; align-items: center; gap: 10px;
  padding: 0 16px;
}
.nc-timer {
  display: inline-flex; align-items: center; justify-content: center;
  width: 44px; height: 44px; border-radius: 12px;
  border: 1px dashed var(--text3); color: var(--text2);
}
.nc-about {
  margin: 0; max-width: 440px;
  font-size: 14px; line-height: 1.5; color: var(--text2); text-align: center;
}
</style>
