<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref } from 'vue'

const message = ref('')
const visible = ref(false)
let timeout: ReturnType<typeof setTimeout> | null = null

function show(msg: string, duration = 3500) {
  message.value = msg
  visible.value = true
  if (timeout) clearTimeout(timeout)
  timeout = setTimeout(() => { visible.value = false }, duration)
}

// Expose globally
;(window as any).showToast = show
defineExpose({ show })
</script>

<template>
  <div id="toast" :class="{ show: visible }">{{ message }}</div>
</template>
