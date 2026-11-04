<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// A circled "i" in front of a note: drawn, as not every font has the character.
withDefaults(defineProps<{ size?: number }>(), { size: 13 })
</script>

<template>
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" :width="size" :height="size" aria-hidden="true">
    <circle cx="12" cy="12" r="10" />
    <path d="M12 11v6M12 7.5v.01" />
  </svg>
</template>
