<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref } from 'vue'
import type { QuestionEvent } from '../../stores/chat'

const props = defineProps<{ question: QuestionEvent }>()
const emit = defineEmits<{ answer: [value: string] }>()
const freeformInput = ref('')
const answered = ref(false)

function selectChoice(choice: string) {
  answered.value = true
  emit('answer', choice)
}

function submitFreeform() {
  if (!freeformInput.value.trim()) return
  answered.value = true
  emit('answer', freeformInput.value.trim())
}
</script>

<template>
  <div class="question-card">
    <div class="question-header">❓ Question</div>
    <div class="question-body">{{ question.question }}</div>
    <div class="question-choices" v-if="question.choices?.length">
      <button
        v-for="choice in question.choices"
        :key="choice"
        class="choice-btn"
        :disabled="answered"
        @click="selectChoice(choice)"
      >{{ choice }}</button>
    </div>
    <div class="question-input-row" v-if="question.allow_freeform !== false">
      <input
        v-model="freeformInput"
        class="question-input"
        placeholder="Type your answer…"
        :disabled="answered"
        @keydown.enter="submitFreeform"
      />
      <button class="question-send-btn" :disabled="answered || !freeformInput.trim()" @click="submitFreeform">
        Send
      </button>
    </div>
  </div>
</template>

<style scoped>
.question-card {
  margin: 10px 24px; padding: 14px 16px; border-radius: 12px;
  background: var(--surface2); border: 1px solid var(--accent2);
  max-width: 860px; margin-left: auto; margin-right: auto;
}
.question-header { font-size: 12px; font-weight: 700; color: var(--accent2); margin-bottom: 8px; letter-spacing: .04em; }
.question-body { font-size: 14px; line-height: 1.5; margin-bottom: 12px; white-space: pre-wrap; word-break: break-word; }
.question-choices { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 12px; }
.choice-btn {
  padding: 7px 14px; border-radius: 8px; border: 1px solid var(--accent);
  background: transparent; color: var(--accent); font-size: 13px; cursor: pointer;
  transition: background .15s, color .15s;
}
.choice-btn:hover { background: var(--accent); color: #fff; }
.choice-btn:disabled { opacity: .5; cursor: not-allowed; }
.question-input-row { display: flex; gap: 8px; }
.question-input {
  flex: 1; padding: 8px 12px; border-radius: 8px; border: 1px solid var(--border);
  background: var(--surface3); color: var(--text); font-size: 13px;
}
.question-input:focus { outline: none; border-color: var(--accent); }
.question-input:disabled { opacity: .5; }
.question-send-btn {
  padding: 8px 14px; border-radius: 8px; border: none;
  background: var(--accent); color: #fff; font-size: 13px; cursor: pointer;
}
.question-send-btn:disabled { opacity: .5; cursor: not-allowed; }
</style>
