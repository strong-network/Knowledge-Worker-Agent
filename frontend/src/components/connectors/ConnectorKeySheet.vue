<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// Add or replace a connector's personal API key. The key is pasted as
// it came, with no header syntax: the server's definition says how to send it.
// Saving checks the connection straight away, so the sheet can say whether the
// key worked rather than leaving the row to find out later.
import { computed, ref } from 'vue'
import type { McpServer } from '../../api'
import { useConnectorsStore } from '../../stores/connectors'
import { displayName, helpText } from '../../utils/connectorStatus'
import { useModalDialog } from '../../composables/useModalDialog'

const props = defineProps<{ server: McpServer }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const store = useConnectorsStore()
const sheet = ref<HTMLElement | null>(null)
useModalDialog(() => sheet.value, { onClose: () => emit('close') })

const title = computed(() => displayName(props.server))
// As it was when the sheet opened: saving sets key_saved, and the heading must not change mid-flow.
const replacing = props.server.key_saved === true
const help = computed(() => helpText(props.server) || `Paste the personal API key you were given for ${title.value}.`)

type Phase = 'entry' | 'checking' | 'connected' | 'saved' | 'not-connected'
const phase = ref<Phase>('entry')
const key = ref('')
const reveal = ref(false)
const problem = ref('')
const saving = ref(false)

async function save() {
  if (!key.value.trim() || saving.value) return
  problem.value = ''
  saving.value = true
  try {
    await store.saveKey(props.server, key.value)
  } catch (e: any) {
    problem.value = e.message || String(e)
    return
  } finally {
    saving.value = false
  }
  // Nothing on the page needs the key once it is stored.
  key.value = ''
  if (!props.server.enabled) {
    phase.value = 'saved'
    return
  }
  phase.value = 'checking'
  await store.loadStatuses({ fresh: true })
  phase.value = store.authed[props.server.name.trim().toLowerCase()] ? 'connected' : 'not-connected'
}

function again() {
  phase.value = 'entry'
  problem.value = ''
}
</script>

<template>
  <div ref="sheet" class="ks-sheet" role="dialog" aria-modal="true" aria-labelledby="ks-title">
    <header class="ks-head">
      <h3 id="ks-title">{{ replacing ? `Replace your ${title} key` : `Add your ${title} key` }}</h3>
      <p>{{ help }}</p>
    </header>

    <div class="ks-body">
      <form v-if="phase === 'entry'" class="ks-form" @submit.prevent="save">
        <label for="ks-key">API key</label>
        <div class="ks-field">
          <input
            id="ks-key"
            v-model="key"
            :type="reveal ? 'text' : 'password'"
            autocomplete="off"
            spellcheck="false"
            placeholder="Paste your key"
            autofocus
          />
          <button type="button" class="ks-btn-ghost" :aria-pressed="reveal" @click="reveal = !reveal">
            {{ reveal ? 'Hide' : 'Show' }}
          </button>
        </div>
        <p v-if="problem" class="ks-problem" role="alert">{{ problem }}</p>
        <p v-else class="ks-note">
          Stored in your workspace, readable only by you, and never shown again.
          <a v-if="server.helpUrl" :href="server.helpUrl" target="_blank" rel="noopener noreferrer">Where do I get my key? ↗</a>
        </p>
      </form>

      <div v-else-if="phase === 'checking'" class="ks-state" role="status">Checking the connection to {{ title }}…</div>

      <div v-else-if="phase === 'connected'" class="ks-ok" role="status">
        <span class="ks-tick" aria-hidden="true">✓</span>
        <div>
          <div class="ks-ok-title">{{ title }} is connected</div>
          <div class="ks-ok-sub">The assistant can use it now.</div>
        </div>
      </div>

      <div v-else-if="phase === 'saved'" class="ks-state" role="status">
        Key saved. Turn {{ title }} on to use it.
      </div>

      <div v-else class="ks-failed" role="alert">
        <strong>Your key is saved, but {{ title }} didn't connect with it</strong>
        <span>Check that you copied the whole key. If it's right, {{ title }} may be unreachable just now; try again later.</span>
      </div>
    </div>

    <footer class="ks-foot">
      <template v-if="phase === 'entry'">
        <button type="button" class="ks-btn-ghost" @click="emit('close')">Cancel</button>
        <button type="button" class="ks-btn" :disabled="saving || !key.trim()" @click="save">
          {{ saving ? 'Saving…' : 'Save key' }}
        </button>
      </template>
      <template v-else-if="phase === 'not-connected'">
        <button type="button" class="ks-btn-ghost" @click="emit('close')">Close</button>
        <button type="button" class="ks-btn" @click="again">Try another key</button>
      </template>
      <button v-else type="button" class="ks-btn" :disabled="phase === 'checking'" @click="emit('close')">Done</button>
    </footer>
  </div>
</template>

<style scoped>
/* The sign-in sheet's shell (ConnectorSignInSheet.vue), so the two read as one family. */
.ks-sheet {
  width: 520px; max-width: 100%; max-height: 100%;
  display: flex; flex-direction: column;
  background: var(--surface); border: 1px solid var(--border); border-radius: 12px;
  box-shadow: 0 18px 48px rgba(0, 0, 0, .45);
}
.ks-head { padding: 18px 20px 12px; border-bottom: 1px solid var(--border); }
.ks-head h3 { font-size: 15px; font-weight: 700; margin: 0 0 4px; }
.ks-head p { font-size: 12px; color: var(--text2); margin: 0; line-height: 1.5; }
.ks-body { padding: 16px 20px; overflow-y: auto; flex: 1; min-height: 0; }

.ks-form label { display: block; font-size: 12px; font-weight: 600; margin-bottom: 6px; }
.ks-field { display: flex; gap: 8px; align-items: center; }
.ks-field input {
  flex: 1; min-width: 0; box-sizing: border-box;
  background: var(--surface2); border: 1px solid var(--border); color: var(--text);
  border-radius: 7px; padding: 8px 10px; font-size: 12px; font-family: var(--mono); outline: none;
}
.ks-field input:focus { border-color: var(--accent); }
.ks-note { font-size: 11px; color: var(--text3); margin: 8px 0 0; line-height: 1.5; }
.ks-note a { color: var(--accent); text-decoration: none; margin-left: 4px; white-space: nowrap; }
.ks-note a:hover { text-decoration: underline; }
.ks-problem {
  font-size: 12px; line-height: 1.5; color: var(--text); margin: 8px 0 0;
  padding: 8px 10px; border-radius: 6px;
  background: color-mix(in srgb, var(--red) 9%, transparent);
  border: 1px solid color-mix(in srgb, var(--red) 35%, transparent);
}

.ks-state { font-size: 13px; color: var(--text2); padding: 4px 0; }
.ks-ok {
  display: flex; align-items: center; gap: 12px;
  border: 1px solid color-mix(in srgb, var(--green) 40%, transparent);
  background: color-mix(in srgb, var(--green) 9%, transparent);
  border-radius: 9px; padding: 14px;
}
.ks-tick {
  width: 32px; height: 32px; border-radius: 50%; flex-shrink: 0;
  display: flex; align-items: center; justify-content: center;
  font-size: 16px; font-weight: 700; color: #fff; background: var(--green);
}
.ks-ok-title { font-size: 13px; font-weight: 600; color: var(--text); }
.ks-ok-sub { font-size: 12px; color: var(--text2); margin-top: 1px; }
.ks-failed {
  display: flex; flex-direction: column; gap: 4px;
  border: 1px solid color-mix(in srgb, var(--red) 40%, transparent);
  background: color-mix(in srgb, var(--red) 9%, transparent);
  border-radius: 9px; padding: 12px 14px;
}
.ks-failed strong { font-size: 13px; color: var(--text); }
.ks-failed span { font-size: 12px; color: var(--text2); line-height: 1.55; }

.ks-foot {
  display: flex; align-items: center; justify-content: flex-end; gap: 8px;
  padding: 12px 20px; border-top: 1px solid var(--border);
  background: var(--surface2); border-radius: 0 0 12px 12px;
}
.ks-btn {
  background: var(--accent); color: #fff; border: 1px solid transparent;
  padding: 7px 16px; border-radius: 7px; cursor: pointer; font-size: 13px; font-weight: 600;
}
.ks-btn:hover:not(:disabled) { background: var(--accent-h); }
.ks-btn:disabled { opacity: .5; cursor: not-allowed; }
.ks-btn-ghost {
  background: var(--surface); border: 1px solid var(--border); color: var(--text2);
  padding: 7px 14px; border-radius: 7px; cursor: pointer; font-size: 13px;
}
.ks-btn-ghost:hover:not(:disabled) { color: var(--text); background: var(--surface3); }

@media (max-width: 760px) {
  .ks-sheet { width: 100%; }
}
</style>
