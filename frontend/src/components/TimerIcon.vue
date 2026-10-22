<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// The timer that marks a temporary chat; with off, a struck-through one for Keep chat.
withDefaults(defineProps<{ size?: number; off?: boolean }>(), { size: 12, off: false })
</script>

<template>
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" :width="size" :height="size" aria-hidden="true">
    <path d="M10 2h4M12 14l3-3" />
    <circle cx="12" cy="14" r="8" />
    <path v-if="off" d="M3 3l18 18" />
  </svg>
</template>
