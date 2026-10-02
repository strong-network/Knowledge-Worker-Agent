<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { computed, watch, nextTick } from 'vue'
import { SLASH_COMMANDS } from '../../utils/constants'

const props = defineProps<{ filter: string; activeIndex: number }>()
const emit = defineEmits<{ select: [cmd: string]; close: [] }>()

const filtered = computed(() => {
  const f = props.filter.toLowerCase()
  return SLASH_COMMANDS.filter(c => c.cmd.includes(f) || c.desc.toLowerCase().includes(f))
})

watch(() => props.activeIndex, () => {
  nextTick(() => {
    const el = document.querySelector('.slash-item.active')
    if (el) el.scrollIntoView({ block: 'nearest' })
  })
})
</script>

<template>
  <div id="slash-picker" class="open">
    <div class="slash-header">Commands</div>
    <div class="slash-list">
      <div
        v-for="(cmd, idx) in filtered"
        :key="cmd.cmd"
        class="slash-item"
        :class="{ active: idx === activeIndex }"
        @click="emit('select', cmd.cmd)"
      >
        <span class="slash-icon">{{ cmd.icon }}</span>
        <span class="slash-cmd">{{ cmd.cmd }}</span>
        <span class="slash-desc">{{ cmd.desc }}</span>
      </div>
      <div v-if="!filtered.length" style="padding:12px;color:var(--text3);font-size:12px;text-align:center">
        No matching commands
      </div>
    </div>
  </div>
</template>

<style scoped>
#slash-picker {
  position: absolute; bottom: calc(100% + 4px); left: 20px; right: 20px;
  max-width: 860px; margin: 0 auto; background: var(--surface);
  border: 1px solid var(--border); border-radius: 10px;
  box-shadow: 0 8px 30px rgba(0,0,0,.4); z-index: 100; overflow: hidden;
}
.slash-header { padding: 8px 12px; font-size: 11px; color: var(--text2); border-bottom: 1px solid var(--border); font-weight: 600; letter-spacing: .05em; text-transform: uppercase; }
.slash-list { max-height: 300px; overflow-y: auto; }
.slash-item {
  display: flex; align-items: center; gap: 10px; padding: 9px 14px;
  cursor: pointer; transition: background .1s; border-bottom: 1px solid var(--border);
}
.slash-item:last-child { border-bottom: none; }
.slash-item:hover, .slash-item.active { background: var(--surface2); }
.slash-cmd { font-family: var(--mono); font-size: 13px; font-weight: 700; color: var(--accent2); min-width: 100px; flex-shrink: 0; }
.slash-desc { font-size: 12px; color: var(--text2); }
.slash-icon { font-size: 15px; flex-shrink: 0; }
</style>
