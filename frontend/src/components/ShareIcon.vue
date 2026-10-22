<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// One share icon for sharing a chat and sharing a file.
withDefaults(defineProps<{ size?: number }>(), { size: 11 })
</script>

<template>
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" :width="size" :height="size" aria-hidden="true">
    <circle cx="18" cy="5" r="2.6" /><circle cx="6" cy="12" r="2.6" /><circle cx="18" cy="19" r="2.6" />
    <path d="M8.3 10.8l7.4-4.4M8.3 13.2l7.4 4.4" />
  </svg>
</template>
