<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// Add (or repair) a connector.
//
// The shape of this sheet is set by the probe: "Add connector" stays disabled
// until a check succeeds, so the user is never allowed to store an address that
// was going to fail and then left to work out why from a row that just reads
// "off". Everything else is progressive disclosure — one field by default,
// because a server address is usually the only thing the user was given.
//
// It doubles as the editor for an existing connector, saved through
// PUT /api/mcp/servers/{name}, which keeps its on/off state. The name field is
// locked in that mode: sign-ins and each chat's connector choices are keyed by
// it, so a rename would really be a second connector.
import { computed, ref, watch } from 'vue'
import { useConnectorsStore } from '../../stores/connectors'
import { useModalDialog } from '../../composables/useModalDialog'
import { probeMcpServer, type McpProbeResult, type McpServer } from '../../api'
import { displayName } from '../../utils/connectorStatus'

// `repair` means the edit came from a `setup` connector's "Set up" button.
const props = defineProps<{ editing?: McpServer | null; repair?: boolean }>()
const emit = defineEmits<{ (e: 'close'): void }>()

// Layered over the connectors modal, so it takes the keyboard while it is up:
// Escape closes the sheet and leaves the modal behind it open, and Tab cannot
// reach the connector list the sheet is covering.
const sheet = ref<HTMLElement | null>(null)
useModalDialog(() => sheet.value, { onClose: () => emit('close') })

const store = useConnectorsStore()

const isEdit = computed(() => !!props.editing)
const isRepair = computed(() => isEdit.value && props.repair === true)

type Transport = 'http' | 'stdio'

const name = ref('')
const transport = ref<Transport>('http')
const url = ref('')
const commandLine = ref('')
const headersText = ref('')
const envText = ref('')
const timeoutSecs = ref<number | null>(null)
const advanced = ref(false)

const checking = ref(false)
const result = ref<McpProbeResult | null>(null)
const saving = ref(false)
const saveError = ref('')

// Seed from the connector being edited. Its help text is the only guidance
// some servers have, so it stays visible while the user edits.
watch(() => props.editing, s => {
  if (!s) return
  name.value = s.name
  const t = (s.transport || s.type || '').toLowerCase()
  transport.value = t === 'stdio' || (!s.url && !!s.command) ? 'stdio' : 'http'
  url.value = s.url || ''
  commandLine.value = [s.command || '', ...(s.args || [])].filter(Boolean).map(quoteArg).join(' ')
  headersText.value = Object.entries(s.headers || {}).map(([k, v]) => `${k}: ${v}`).join('\n')
  envText.value = Object.entries(s.env || {}).map(([k, v]) => `${k}=${v}`).join('\n')
  timeoutSecs.value = s.timeout ? Math.round(s.timeout / 1000) : null
  // Headers, environment and timeout are much of what there is to edit.
  advanced.value = true
}, { immediate: true })

// Any edit invalidates the check. Without this the user could probe one
// address, paste another, and add the second on the first one's evidence.
watch([transport, url, commandLine, headersText, envText, timeoutSecs], () => {
  result.value = null
  saveError.value = ''
})

const target = computed(() => transport.value === 'stdio' ? commandLine.value.trim() : url.value.trim())
const canCheck = computed(() => !!target.value && !checking.value)
const checkLabel = computed(() => transport.value === 'stdio' ? 'Test command' : 'Check address')

// A server that wants credentials answers `initialize` with a 401, which the
// probe rightly reports as ok + requires_auth — but it means the server never
// got as far as sending its serverInfo, so there is no name to borrow. That is
// the normal outcome for every OAuth-protected connector, not an edge case, and
// without a fallback it left the user at a disabled button with the name field
// hidden behind a disclosure. Suggest one from the address instead.
function nameFromUrl(raw: string): string {
  let host: string
  try { host = new URL(raw.trim()).hostname } catch { return '' }
  const labels = host.split('.').filter(Boolean)
  // mcp.otter.ai → otter. These prefixes name the endpoint, not the product.
  while (labels.length > 1 && ['www', 'mcp', 'api', 'server'].includes(labels[0]!.toLowerCase())) {
    labels.shift()
  }
  const first = (labels[0] || '').toLowerCase()
  // An address given as a bare IP has no name in it to find.
  if (!/^[a-z0-9-]+$/.test(first) || /^\d+$/.test(first)) return ''
  return first
}

// POST /api/mcp/servers upserts, so a name that already exists rewrites that
// connector. Fine when the user typed it; not fine when we suggested it.
function unusedName(base: string): string {
  if (!base) return ''
  const taken = new Set(store.servers.map(s => s.name.trim().toLowerCase()))
  if (!taken.has(base)) return base
  for (let i = 2; i <= 20; i++) {
    if (!taken.has(`${base}-${i}`)) return `${base}-${i}`
  }
  return ''
}

const suggestedName = computed(() => unusedName(
  (result.value?.name || (transport.value === 'http' ? nameFromUrl(url.value) : '')).trim().toLowerCase()))

// The server usually names itself, so the name field is optional until the
// check comes back without one.
const effectiveName = computed(() => (name.value.trim() || suggestedName.value).trim())
const canAdd = computed(() => !!result.value?.ok && !!effectiveName.value && !saving.value)

const nameSource = computed<'' | 'server' | 'address'>(() => {
  if (name.value.trim() || !effectiveName.value) return ''
  return result.value?.name ? 'server' : 'address'
})

// A disabled button with no reason is a dead end, and this one is reachable:
// an address with no name in it (an IP, say) still probes fine.
const blockedReason = computed(() => {
  if (!result.value?.ok || effectiveName.value || saving.value) return ''
  return "This server didn't tell us its name, and the address doesn't suggest one. "
    + 'Give it a name to continue.'
})

function parseKV(text: string, sep = '='): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of text.split('\n')) {
    const t = line.trim()
    if (!t) continue
    const idx = t.indexOf(sep)
    if (idx <= 0) continue
    const k = t.slice(0, idx).trim()
    const v = t.slice(idx + 1).trim()
    if (k) out[k] = v
  }
  return out
}

// Shell-like tokenizer supporting "..." and '...' quoting. Splitting happens
// here, once, and the already-split command + args go to both the probe and the
// add — so the two can never disagree about how a command line divides.
function tokenize(line: string): string[] {
  const tokens: string[] = []
  let cur = ''
  let quote: '"' | "'" | null = null
  for (let i = 0; i < line.length; i++) {
    const c = line[i]
    if (quote) {
      if (c === quote) { quote = null; continue }
      cur += c
      continue
    }
    if (c === '"' || c === "'") { quote = c as '"' | "'"; continue }
    if (c === ' ' || c === '\t') {
      if (cur) { tokens.push(cur); cur = '' }
      continue
    }
    cur += c
  }
  if (cur) tokens.push(cur)
  return tokens
}

// The inverse of tokenize, so an argument with a space in it (a vault path,
// say) is still one argument after a trip through the form.
function quoteArg(arg: string): string {
  if (!/[\s"']/.test(arg)) return arg
  return arg.includes('"') ? `'${arg}'` : `"${arg}"`
}

function payload() {
  const base: any = {
    transport: transport.value,
    env: parseKV(envText.value, '='),
  }
  if (transport.value === 'stdio') {
    const tokens = tokenize(commandLine.value.trim())
    base.command = tokens[0] || ''
    base.args = tokens.slice(1)
  } else {
    base.url = url.value.trim()
    base.headers = parseKV(headersText.value, ':')
  }
  return base
}

async function check() {
  checking.value = true
  saveError.value = ''
  try {
    result.value = await probeMcpServer(payload())
    // Nothing to borrow and nothing to derive — open the disclosure rather than
    // pointing at a field the user would have to go hunting for.
    if (result.value.ok && !effectiveName.value) advanced.value = true
  } finally {
    checking.value = false
  }
}

async function add() {
  saving.value = true
  saveError.value = ''
  const req = { ...payload(), timeout: timeoutSecs.value ? timeoutSecs.value * 1000 : undefined }
  try {
    if (isEdit.value) await store.update(props.editing!.name, req)
    else await store.add({ ...req, name: effectiveName.value })
    ;(window as any).showToast?.(
      isEdit.value ? `Updated “${effectiveName.value}”` : `Added “${effectiveName.value}”`)
    emit('close')
  } catch (e: any) {
    saveError.value = e.message || String(e)
  } finally {
    saving.value = false
  }
}

const monogram = computed(() => {
  const n = effectiveName.value
  return n ? n[0]!.toUpperCase() : '?'
})

// Tool names are snake_case identifiers; the design shows them as a readable
// list. Cap it so a server with 47 tools doesn't push the buttons off screen.
const toolSummary = computed(() => {
  const tools = result.value?.tools || []
  if (!tools.length) return ''
  const shown = tools.slice(0, 6).map(t => t.replace(/[_-]+/g, ' '))
  const rest = tools.length - shown.length
  return shown.join(', ') + (rest > 0 ? `, and ${rest} more` : '')
})

// The verdict lands below the button that asked for it, so nothing moves focus
// and nothing is announced. The failure panel is role="alert" and speaks for
// itself; success was the silent half, which is backwards — it is the verdict
// that unlocks the Add button.
const liveVerdict = computed(() => {
  if (checking.value) return 'Checking…'
  if (!result.value?.ok) return ''
  return `Reached ${effectiveName.value || 'the server'}.`
    + (toolSummary.value ? ` It can: ${toolSummary.value}.` : '')
    + (result.value.requires_auth ? ` ${signInNote.value}` : '')
    + (isEdit.value ? ' You can save it now.' : ' You can add it now.')
})

// The probe never carries the user's sign-in, so a signed-in connector reads
// as "requires auth" too. What an edit changes is whether that sign-in holds.
const signInNote = computed(() => isEdit.value
  ? "If you changed the address, you'll need to sign in again."
  : 'Will ask you to sign in once you turn it on.')

const sheetTitle = computed(() => {
  if (!isEdit.value) return 'Add a custom server'
  const n = displayName(props.editing!)
  return isRepair.value ? `Set up ${n}` : `Edit ${n}`
})
</script>

<template>
  <div ref="sheet" class="cs-sheet" role="dialog" aria-modal="true" :aria-label="sheetTitle">
    <header class="cs-head">
      <h3>{{ sheetTitle }}</h3>
      <p v-if="isRepair">Fill in the details your IT team gave you, then check it works.</p>
      <p v-else-if="isEdit">Change what you need, then check it still works.</p>
      <p v-else-if="transport === 'stdio'">Running on this computer — give the command to run.</p>
      <p v-else>Paste the address your IT team or the tool's provider gave you.</p>
    </header>

    <div class="cs-body">
      <!-- An admin wrote this text precisely because the connector can't be
           set up without it. It belongs next to the fields, not behind them. -->
      <p v-if="isEdit && (props.editing!.help || props.editing!.helpUrl)" class="cs-guidance">
        <span v-if="props.editing!.help">{{ props.editing!.help }}</span>
        <a
          v-if="props.editing!.helpUrl"
          :href="props.editing!.helpUrl"
          target="_blank"
          rel="noopener noreferrer"
        >How to set this up ↗</a>
      </p>

      <div v-if="transport === 'http'" class="cs-field">
        <label for="cs-url">Server address</label>
        <div class="cs-input-row">
          <input
            id="cs-url"
            v-model="url"
            placeholder="https://mcp.internal.company.com/sap"
            spellcheck="false"
            @keydown.enter="canCheck && check()"
          />
          <span v-if="result?.ok" class="cs-inline-ok">✓ Reachable</span>
        </div>
        <small>Starts with https:// — nothing else to configure.</small>
      </div>

      <div v-else class="cs-field">
        <label for="cs-cmd">Command to run</label>
        <div class="cs-input-row">
          <input
            id="cs-cmd"
            v-model="commandLine"
            placeholder="npx -y @upstash/context7-mcp"
            spellcheck="false"
            @keydown.enter="canCheck && check()"
          />
          <span v-if="result?.ok" class="cs-inline-ok">✓ Works</span>
        </div>
        <small>Paste the full command line. Quotes are respected.</small>
      </div>

      <button class="cs-disclosure" :aria-expanded="advanced" @click="advanced = !advanced">
        {{ advanced ? '▾' : '▸' }} Advanced settings
      </button>

      <div v-if="advanced" class="cs-advanced">
        <div class="cs-field">
          <label for="cs-name">Display name</label>
          <input id="cs-name" v-model="name" :disabled="isEdit" placeholder="e.g. SAP Reporting" />
          <small v-if="isEdit">The name can't change here — that would add a second connector instead of fixing this one.</small>
          <small v-else-if="blockedReason">This server didn't tell us its name. Give it one here.</small>
          <small v-else>Leave blank to use the name the server reports, or one from the address.</small>
        </div>

        <fieldset class="cs-field cs-choice" :disabled="isEdit">
          <legend>How it connects</legend>
          <label :class="{ sel: transport === 'http' }">
            <input type="radio" value="http" v-model="transport" />
            <span class="cs-choice-title">Over the web</span>
            <span class="cs-choice-sub">http — a server address</span>
          </label>
          <label :class="{ sel: transport === 'stdio' }">
            <input type="radio" value="stdio" v-model="transport" />
            <span class="cs-choice-title">On this computer</span>
            <span class="cs-choice-sub">stdio — a local command</span>
          </label>
        </fieldset>

        <div v-if="transport === 'http'" class="cs-field">
          <label for="cs-headers">Headers</label>
          <textarea id="cs-headers" v-model="headersText" rows="2" placeholder="Authorization: Bearer xxx"></textarea>
          <small>One per line. Only needed if you were given an access token.</small>
        </div>

        <div class="cs-field">
          <label for="cs-env">Environment variables</label>
          <textarea id="cs-env" v-model="envText" rows="2" placeholder="KEY=value (one per line)"></textarea>
          <small>{{ transport === 'stdio' ? 'For API keys the command needs.' : 'Rarely needed for web connectors.' }}</small>
        </div>

        <div class="cs-field cs-narrow">
          <label for="cs-timeout">Give up after</label>
          <div class="cs-suffix">
            <input id="cs-timeout" v-model.number="timeoutSecs" type="number" min="1" placeholder="30" />
            <span>seconds</span>
          </div>
        </div>

        <p v-if="transport === 'stdio'" class="cs-warn">
          Local commands run on your machine with your permissions. Only add commands from a
          source you trust.
        </p>
      </div>

      <!-- Probe verdict. Deliberately below the fields: it is the answer to the
           question the fields ask, and it is also what unlocks the Add button. -->
      <div v-if="result && result.ok" class="cs-found">
        <div class="cs-found-head">
          <span class="cs-mono" aria-hidden="true">{{ monogram }}</span>
          <div>
            <div class="cs-found-name">{{ effectiveName || 'Unnamed connector' }}</div>
            <div v-if="nameSource" class="cs-found-sub">
              Name suggested {{ nameSource === 'server' ? 'by the server' : 'from the address' }} —
              you can rename it under Advanced settings.
            </div>
          </div>
        </div>
        <p v-if="toolSummary" class="cs-found-tools">It can: {{ toolSummary }}</p>
        <p v-if="result.requires_auth" class="cs-found-auth">{{ signInNote }}</p>
      </div>

      <div v-else-if="result && !result.ok" class="cs-failed" role="alert">
        <strong>{{ transport === 'stdio' ? "That command didn't work" : "We couldn't reach that address" }}</strong>
        <span>{{ result.error }} {{ isEdit ? 'Nothing has been changed.' : 'Nothing has been added.' }}</span>
      </div>

      <div v-if="saveError" class="cs-failed" role="alert">
        <strong>Couldn't save the connector</strong>
        <span>{{ saveError }}</span>
      </div>

      <p class="sr-only" role="status" aria-live="polite">{{ liveVerdict }}</p>
    </div>

    <footer class="cs-foot">
      <span class="cs-foot-note" :class="{ blocked: !!blockedReason }">
        {{ blockedReason || (isEdit ? '' : 'Only you will see this connector.') }}
      </span>
      <button class="cs-btn-ghost" @click="emit('close')">Cancel</button>
      <button class="cs-btn-ghost" :disabled="!canCheck" @click="check">
        {{ checking ? 'Checking…' : (result && !result.ok ? 'Try again' : checkLabel) }}
      </button>
      <button
        class="cs-btn"
        :disabled="!canAdd"
        :title="blockedReason || (result?.ok ? '' : `Check it works first — that's what tells us the details are right.`)"
        @click="add"
      >
        {{ saving ? 'Saving…' : (isEdit ? 'Save changes' : 'Add and turn on') }}
      </button>
    </footer>
  </div>
</template>

<style scoped>
.cs-sheet {
  width: 560px; max-width: 100%; max-height: 100%;
  display: flex; flex-direction: column;
  background: var(--surface); border: 1px solid var(--border); border-radius: 12px;
  box-shadow: 0 18px 48px rgba(0, 0, 0, .45);
}

.cs-head { padding: 18px 20px 12px; border-bottom: 1px solid var(--border); }
.cs-head h3 { font-size: 15px; font-weight: 700; margin: 0 0 4px; }
.cs-head p { font-size: 12px; color: var(--text2); margin: 0; }

.cs-body { padding: 16px 20px; overflow-y: auto; flex: 1; min-height: 0; }

.cs-guidance {
  font-size: 12px; line-height: 1.5; color: var(--text);
  background: color-mix(in srgb, var(--orange) 10%, transparent);
  border-left: 2px solid var(--orange);
  padding: 9px 12px; margin: 0 0 14px; border-radius: 0 6px 6px 0;
}
.cs-guidance a { color: var(--accent); text-decoration: none; margin-left: 6px; white-space: nowrap; }
.cs-guidance a:hover { text-decoration: underline; }

.cs-field { display: flex; flex-direction: column; gap: 4px; margin-bottom: 12px; border: none; padding: 0; }
.cs-field > label, .cs-field > legend {
  font-size: 11px; font-weight: 600; color: var(--text2);
  text-transform: uppercase; letter-spacing: .04em; padding: 0;
}
.cs-field small { font-size: 11px; color: var(--text3); line-height: 1.45; }
.cs-field input[type="text"], .cs-field input:not([type]), .cs-field input[type="number"], .cs-field textarea {
  width: 100%; box-sizing: border-box;
  background: var(--surface2); border: 1px solid var(--border); color: var(--text);
  border-radius: 7px; padding: 8px 10px; font-size: 13px; font-family: var(--mono); outline: none;
}
.cs-field textarea { resize: vertical; font-size: 12px; }
.cs-field input:focus, .cs-field textarea:focus { border-color: var(--accent); }
.cs-field input:disabled { opacity: .6; cursor: not-allowed; }

.cs-input-row { display: flex; align-items: center; gap: 10px; }
.cs-input-row input { flex: 1; min-width: 0; }
.cs-inline-ok { font-size: 12px; font-weight: 600; color: var(--green); white-space: nowrap; }

.cs-narrow .cs-suffix { display: flex; align-items: center; gap: 8px; }
.cs-narrow .cs-suffix input[type="number"] { width: 90px; flex: none; }
.cs-narrow .cs-suffix span { font-size: 12px; color: var(--text2); }

.cs-disclosure {
  background: none; border: none; padding: 2px 0; margin: 2px 0 12px; cursor: pointer;
  font-size: 12px; font-weight: 600; color: var(--accent); font-family: inherit;
}
.cs-disclosure:hover { text-decoration: underline; }

.cs-advanced {
  border: 1px solid var(--border); border-radius: 9px;
  padding: 14px; margin-bottom: 14px; background: var(--surface2);
}
.cs-advanced .cs-field:last-child { margin-bottom: 0; }

.cs-choice { display: flex; flex-direction: column; gap: 6px; }
/* The cards are <label>s, so they would otherwise inherit the small uppercase
   treatment that marks a field's name. They are options, not names. */
.cs-choice label {
  display: flex; flex-direction: column; gap: 1px;
  border: 1px solid var(--border); border-radius: 8px; padding: 9px 12px; cursor: pointer;
  text-transform: none; letter-spacing: normal; font-size: 13px; font-weight: 400;
}
.cs-choice label.sel { border-color: var(--accent); background: color-mix(in srgb, var(--accent) 10%, transparent); }
.cs-choice input { position: absolute; opacity: 0; width: 0; height: 0; }
.cs-choice input:focus-visible + .cs-choice-title { outline: 2px solid var(--accent); outline-offset: 2px; }
.cs-choice-title { font-size: 13px; font-weight: 600; color: var(--text); }
.cs-choice-sub { font-size: 11px; color: var(--text2); font-family: var(--mono); }
.cs-choice:disabled label { opacity: .6; cursor: not-allowed; }

.cs-warn {
  font-size: 11px; line-height: 1.5; color: var(--text); margin: 10px 0 0;
  padding: 8px 10px; border-radius: 6px;
  background: color-mix(in srgb, var(--orange) 10%, transparent);
  border: 1px solid color-mix(in srgb, var(--orange) 30%, transparent);
}

.cs-found {
  border: 1px solid color-mix(in srgb, var(--green) 40%, transparent);
  background: color-mix(in srgb, var(--green) 9%, transparent);
  border-radius: 9px; padding: 12px 14px; margin-top: 4px;
}
.cs-found-head { display: flex; align-items: center; gap: 10px; }
.cs-mono {
  width: 32px; height: 32px; border-radius: 8px; flex-shrink: 0;
  display: flex; align-items: center; justify-content: center;
  font-weight: 700; font-size: 14px; color: var(--accent);
  background: color-mix(in srgb, var(--accent) 14%, transparent);
}
.cs-found-name { font-size: 13px; font-weight: 600; color: var(--text); }
.cs-found-sub { font-size: 11px; color: var(--text2); margin-top: 1px; }
.cs-found-tools { font-size: 12px; color: var(--text); margin: 10px 0 0; line-height: 1.5; }
.cs-found-auth { font-size: 12px; color: var(--orange); margin: 6px 0 0; font-weight: 600; }

.cs-failed {
  display: flex; flex-direction: column; gap: 3px;
  border: 1px solid color-mix(in srgb, var(--red) 40%, transparent);
  background: color-mix(in srgb, var(--red) 9%, transparent);
  border-radius: 9px; padding: 11px 14px; margin-top: 4px;
}
.cs-failed strong { font-size: 13px; color: var(--text); }
.cs-failed span { font-size: 12px; color: var(--text2); line-height: 1.5; }

.cs-foot {
  display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
  padding: 12px 20px; border-top: 1px solid var(--border);
  background: var(--surface2); border-radius: 0 0 12px 12px;
}
.cs-foot-note { flex: 1; font-size: 11px; color: var(--text3); }
/* When it explains a disabled button it is an instruction, not a footnote — and
   it will not fit in the sliver of room three buttons leave, so it takes its
   own line above them. */
.cs-foot-note.blocked {
  flex: 0 0 100%; order: -1; padding-bottom: 8px;
  font-size: 12px; color: var(--orange);
}

.cs-btn {
  background: var(--accent); color: #fff; border: 1px solid transparent;
  padding: 7px 16px; border-radius: 7px; cursor: pointer; font-size: 13px; font-weight: 600;
}
.cs-btn:hover:not(:disabled) { background: var(--accent-h); }
.cs-btn:disabled { opacity: .5; cursor: not-allowed; }
.cs-btn-ghost {
  background: var(--surface); border: 1px solid var(--border); color: var(--text2);
  padding: 7px 14px; border-radius: 7px; cursor: pointer; font-size: 13px;
}
.cs-btn-ghost:hover:not(:disabled) { color: var(--text); background: var(--surface3); }
.cs-btn-ghost:disabled { opacity: .5; cursor: not-allowed; }

@media (max-width: 760px) {
  .cs-sheet { width: 100%; }
  /* The reassurance is droppable at this width; an instruction that explains a
     disabled button is not. */
  .cs-foot-note:not(.blocked) { display: none; }
}
</style>
