<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { QuestionEvent } from '../../stores/chat'

const props = defineProps<{ question: QuestionEvent }>()
const emit = defineEmits<{ answer: [value: string] }>()
const freeformInput = ref('')
const answered = ref(false)

const asked = computed(() => props.question.questions || [])
const picked = ref<string[][]>([])
const typed = ref<string[]>([])
watch(asked, (qs) => {
  picked.value = qs.map(() => [])
  typed.value = qs.map(() => '')
  answered.value = false
}, { immediate: true })

// A header ends with a colon, unless it ends in punctuation already.
function headerLabel(header: string): string {
  const h = header.trim().replace(/:$/, '')
  return /[?!.]$/.test(h) ? h : `${h}:`
}

function answersTo(i: number): string[] {
  const own = (typed.value[i] || '').trim()
  return own ? [...picked.value[i], own] : [...picked.value[i]]
}
const complete = computed(() => asked.value.every((_, i) => answersTo(i).length > 0))

function pick(i: number, label: string) {
  if (answered.value) return
  if (asked.value[i].multiple) {
    const on = picked.value[i].includes(label)
    picked.value[i] = on ? picked.value[i].filter(l => l !== label) : [...picked.value[i], label]
    return
  }
  picked.value[i] = [label]
  typed.value[i] = ''
  // One question with one answer: choosing it is answering.
  if (asked.value.length === 1) sendAnswers()
}

function onType(i: number) {
  if (!asked.value[i].multiple && typed.value[i].trim()) picked.value[i] = []
}

function sendAnswers() {
  if (answered.value || !complete.value) return
  answered.value = true
  const lines = asked.value.map((q, i) => `- ${q.question.trim()} → ${answersTo(i).join(', ')}`)
  const lead = asked.value.length > 1 ? 'My answers to your questions:' : 'My answer to your question:'
  emit('answer', `${lead}\n${lines.join('\n')}`)
}

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
  <div v-if="asked.length" class="question-card" role="group" :aria-label="asked.length > 1 ? 'Questions from the assistant' : 'A question from the assistant'">
    <div class="question-header">❓ {{ asked.length > 1 ? 'Questions' : 'Question' }}</div>
    <div v-for="(q, i) in asked" :key="i" class="question-item">
      <div class="question-body">
        <strong v-if="q.header">{{ headerLabel(q.header) }}</strong> {{ q.question }}
      </div>
      <div class="question-choices" v-if="q.options?.length">
        <button
          v-for="o in q.options"
          :key="o.label"
          class="choice-btn"
          :class="{ selected: picked[i]?.includes(o.label) }"
          :aria-pressed="picked[i]?.includes(o.label) ? 'true' : 'false'"
          :title="o.description || undefined"
          :disabled="answered"
          @click="pick(i, o.label)"
        >{{ o.label }}</button>
      </div>
      <input
        v-model="typed[i]"
        class="question-input"
        :placeholder="q.options?.length ? 'Or type your own answer…' : 'Type your answer…'"
        :aria-label="`Your answer: ${q.header || q.question}`"
        :disabled="answered"
        @input="onType(i)"
        @keydown.enter="sendAnswers"
      />
    </div>
    <div class="question-send-row">
      <button class="question-send-btn" :disabled="answered || !complete" @click="sendAnswers">
        {{ asked.length > 1 ? 'Send answers' : 'Send answer' }}
      </button>
    </div>
  </div>
  <div v-else class="question-card">
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
.choice-btn:hover, .choice-btn.selected { background: var(--accent); color: #fff; }
.choice-btn:disabled { opacity: .5; cursor: not-allowed; }
.question-item { margin-bottom: 14px; }
.question-item .question-body { margin-bottom: 8px; }
.question-item .question-choices { margin-bottom: 8px; }
.question-item .question-input { width: 100%; box-sizing: border-box; }
.question-send-row { display: flex; justify-content: flex-end; }
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
