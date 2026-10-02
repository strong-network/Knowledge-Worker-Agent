<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref } from 'vue'
import type { PermissionRequest } from '../../stores/chat'
import type { PermissionResponse } from '../../api'

// allowAlways is false for a guest of a shared chat: "always" is a standing
// rule in the owner's workspace, which outlasts the share.
withDefaults(defineProps<{ permission: PermissionRequest; allowAlways?: boolean }>(), { allowAlways: true })
const emit = defineEmits<{ respond: [value: PermissionResponse] }>()
const answered = ref(false)

function respond(value: PermissionResponse) {
  if (answered.value) return
  answered.value = true
  emit('respond', value)
}
</script>

<template>
  <div class="permission-card">
    <div class="permission-header">🔐 Permission requested</div>
    <div class="permission-body">
      The agent wants to use
      <code class="permission-name">{{ permission.permission || 'a tool' }}</code>.
    </div>
    <ul class="permission-patterns" v-if="permission.patterns?.length">
      <li v-for="p in permission.patterns" :key="p"><code>{{ p }}</code></li>
    </ul>
    <div class="permission-actions">
      <button class="perm-btn allow" :disabled="answered" @click="respond('once')">Allow once</button>
      <button v-if="allowAlways" class="perm-btn always" :disabled="answered" @click="respond('always')">Always allow</button>
      <button class="perm-btn reject" :disabled="answered" @click="respond('reject')">Deny</button>
    </div>
  </div>
</template>

<style scoped>
.permission-card {
  margin: 10px 24px; padding: 14px 16px; border-radius: 12px;
  background: var(--surface2); border: 1px solid var(--accent);
  max-width: 860px; margin-left: auto; margin-right: auto;
}
.permission-header { font-size: 12px; font-weight: 700; color: var(--accent); margin-bottom: 8px; letter-spacing: .04em; }
.permission-body { font-size: 14px; line-height: 1.5; margin-bottom: 8px; word-break: break-word; }
.permission-name { padding: 1px 6px; border-radius: 6px; background: var(--surface3); font-size: 13px; }
.permission-patterns { margin: 0 0 12px; padding-left: 18px; font-size: 13px; color: var(--text-muted); }
.permission-patterns li { margin: 2px 0; }
.permission-patterns code { background: var(--surface3); padding: 1px 5px; border-radius: 5px; }
.permission-actions { display: flex; flex-wrap: wrap; gap: 8px; }
.perm-btn {
  padding: 7px 14px; border-radius: 8px; border: 1px solid var(--border);
  background: transparent; font-size: 13px; cursor: pointer;
  transition: background .15s, color .15s, border-color .15s;
}
.perm-btn:disabled { opacity: .5; cursor: not-allowed; }
.perm-btn.allow { border-color: var(--accent); color: var(--accent); }
.perm-btn.allow:hover { background: var(--accent); color: #fff; }
.perm-btn.always { border-color: var(--green, #3fb950); color: var(--green, #3fb950); }
.perm-btn.always:hover { background: var(--green, #3fb950); color: #fff; }
.perm-btn.reject { border-color: var(--red, #f85149); color: var(--red, #f85149); }
.perm-btn.reject:hover { background: var(--red, #f85149); color: #fff; }
</style>
