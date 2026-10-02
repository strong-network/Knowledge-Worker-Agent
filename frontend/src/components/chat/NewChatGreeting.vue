<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { computed } from 'vue'
import { useMe } from '../../composables/useMe'

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
  <h1 class="nc-greeting">{{ headline }}</h1>
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
</style>
