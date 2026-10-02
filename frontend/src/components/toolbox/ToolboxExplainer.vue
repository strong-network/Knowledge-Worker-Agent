<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// The explainer band. Shown only when TYPE is Skills or Connectors,
// hidden on Everything so it does not nag people who already know.
//
// The Skills copy is not the spec's. "Turn one on and you get a consistent
// result" implies the choice sticks, which is the opposite of what a skill does
// — so it ends by saying the lifetime out loud. That sentence and the
// card's *Use for next message* label are two of the three places carrying the
// mixed-lifetime idea; the footer is the third.
import { computed } from 'vue'

const props = defineProps<{ type: 'skill' | 'connector' }>()
const emit = defineEmits<{ (e: 'manage'): void }>()

const isSkill = computed(() => props.type === 'skill')
</script>

<template>
  <aside class="tb-explainer">
    <span class="tb-ex-icon" :class="`k-${type}`" aria-hidden="true">
      <svg v-if="isSkill" viewBox="0 0 24 24" fill="none" stroke="currentColor"
           stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round">
        <path d="M14.7 6.3a4 4 0 0 0 5 5L15 16l-4-4 3.7-5.7Z" />
        <path d="m11 12-6.3 6.3a1.8 1.8 0 0 0 2.5 2.5L13.5 15" />
      </svg>
      <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor"
           stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round">
        <path d="M9 2v6M15 2v6" />
        <path d="M6 8h12v3a6 6 0 0 1-12 0V8Z" />
        <path d="M12 17v5" />
      </svg>
    </span>

    <div class="tb-ex-text">
      <h3>{{ isSkill ? 'Skills' : 'Connectors' }}</h3>
      <p v-if="isSkill">
        A skill is a packaged way of working — instructions, house style and output
        format the agent follows for one kind of task. Pick one and it applies to your
        next message, then clears.
      </p>
      <p v-else>
        A connector gives the agent live access to one of your systems — Jira, GitHub,
        Slack, the web. Turn one on and answers are grounded in your real data instead
        of what the model remembers.
      </p>
    </div>

    <!-- Only connectors get a route out. The Toolbox picks tools for this chat;
         adding, removing and globally enabling them is management, and this is
         the one visible road to it. Skills have no equivalent surface. -->
    <button v-if="!isSkill" type="button" class="tb-ex-btn" @click="emit('manage')">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"
           stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
        <path d="M4 6h10M18 6h2M4 12h2M10 12h10M4 18h10M18 18h2" />
        <circle cx="16" cy="6" r="2" /><circle cx="8" cy="12" r="2" /><circle cx="16" cy="18" r="2" />
      </svg>
      Manage connectors
    </button>
  </aside>
</template>

<style scoped>
.tb-explainer {
  display: flex; align-items: flex-start; gap: 12px;
  margin: 16px 16px 0;
  padding: 13px 15px;
  border-radius: 14px;
  background: var(--surface2);
  border: 1px solid var(--border);
}

.tb-ex-icon {
  width: 32px; height: 32px; flex-shrink: 0;
  display: flex; align-items: center; justify-content: center;
}
.tb-ex-icon svg { width: 17px; height: 17px; }
.tb-ex-icon.k-skill {
  border-radius: 10px;
  color: var(--accent);
  background: color-mix(in srgb, var(--accent) 14%, transparent);
}
.tb-ex-icon.k-connector {
  border-radius: 50%;
  color: var(--accent2);
  background: color-mix(in srgb, var(--accent2) 14%, transparent);
}

.tb-ex-text { flex: 1; min-width: 0; }
.tb-ex-text h3 { margin: 0 0 3px; font-size: 13px; font-weight: 700; color: var(--text); }
.tb-ex-text p { margin: 0; font-size: 12.5px; line-height: 1.5; color: var(--text2); text-wrap: pretty; }

.tb-ex-btn {
  display: inline-flex; align-items: center; gap: 6px; flex-shrink: 0;
  background: var(--surface); border: 1px solid var(--border); border-radius: 8px;
  padding: 7px 12px; cursor: pointer;
  font-family: inherit; font-size: 12.5px; color: var(--text2);
}
.tb-ex-btn:hover { color: var(--text); background: var(--surface3); }
.tb-ex-btn svg { width: 15px; height: 15px; color: var(--accent2); }

@media (max-width: 760px) {
  .tb-explainer { flex-wrap: wrap; }
  .tb-ex-btn { margin-left: 44px; }
}
</style>
