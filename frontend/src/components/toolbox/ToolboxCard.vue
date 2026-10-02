<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
// One Toolbox item — a skill or a connector — as a card.
//
// Deliberately thin (G3): name, description, the control, a sign-in prompt when
// one is needed, and a warning when a connector is actually broken. No sign
// out, no remove, no edit, no "Provided by IT" tag — that is management and
// lives in the connectors modal.
//
// The two kinds differ in more than an icon. A connector's switch means "stays
// on for this chat"; a skill applies to one message and then clears, so it gets
// a radio-style control labelled with its own lifetime. Rendering both as
// switches would be a lie about the skill half.
import { computed } from 'vue'
import type { ToolboxItem } from '../../stores/toolbox'

const props = defineProps<{ item: ToolboxItem; busy?: boolean }>()
const emit = defineEmits<{
  (e: 'toggle', item: ToolboxItem): void
  (e: 'signin', item: ToolboxItem): void
  (e: 'setup', item: ToolboxItem): void
  (e: 'manage'): void
}>()

const isSkill = computed(() => props.item.kind === 'skill')

// Which band to render, in the G3c priority order — first match wins. These are
// *not* mutually exclusive conditions: `status` is auth state and `liveStatus`
// is opencode's live connection, independent fields from different endpoints. A
// connector that was never signed in is one that will also fail, so
// needs-signin + error is the likely case rather than an exotic one. The amber
// band wins because it names the cause and carries the action; the red line
// describes a consequence and can only hand off.
const band = computed<'signin' | 'key' | 'setup' | 'broken' | null>(() => {
  if (isSkill.value) return null
  if (props.item.status === 'needs-signin') return 'signin'
  if (props.item.status === 'needs-key') return 'key'
  if (props.item.status === 'needs-setup') return 'setup'
  if (props.item.status === 'on' && props.item.selected && !liveHealthy.value) return 'broken'
  return null
})

// Mirrors the backend's `connectors.IsConnected` — a denylist, not an allowlist.
// The frontend's older `connectorDotClass` allowlisted error/failed and sent
// anything unrecognised to "off", so a status opencode adds tomorrow would show
// as a silent grey nothing on a connector the user switched on. An unknown
// status is a failure we have not seen before, not a healthy one.
const liveHealthy = computed(() => {
  const s = (props.item.liveStatus || '').trim()
  if (!s) return true   // no turn has run in this chat yet; nothing has failed
  return s === 'connected' || s === 'connecting' || s === 'pending'
})

// Clicking the card is the toggle, but a card whose problem is a missing
// credential has no meaningful toggle — so it starts the remedy instead of
// leaving a dead click.
function onCardClick() {
  if (band.value === 'signin') return emit('signin', props.item)
  if (band.value === 'setup' || band.value === 'key') return emit('setup', props.item)
  if (props.item.readOnly) return
  emit('toggle', props.item)
}

const controlLabel = computed(() => {
  if (isSkill.value) {
    return props.item.selected
      ? `Clear ${props.item.name} from your next message`
      : `Use ${props.item.name} for your next message`
  }
  return props.item.selected
    ? `Turn off ${props.item.name} for this chat`
    : `Turn on ${props.item.name} for this chat`
})
</script>

<template>
  <div
    class="tb-card"
    :class="[`k-${item.kind}`, { on: item.selected, 'has-band': !!band, ro: item.readOnly }]"
    @click="onCardClick"
  >
    <div class="tb-card-head">
      <span class="tb-icon" :class="`k-${item.kind}`" aria-hidden="true">
        <!-- Rounded square vs circle is the colour-blind-safe type cue,
             and it now also predicts which control the card carries. -->
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

      <div class="tb-card-text">
        <div class="tb-card-title">
          <span class="tb-name">{{ item.name }}</span>
          <span class="tb-badge" :class="`k-${item.kind}`">{{ isSkill ? 'Skill' : 'Connector' }}</span>
        </div>
        <!-- Empty today: `meta` is an array so a tool count is a push (seam 3).
             Rendered only when it has something, or the card ships a blank row. -->
        <div v-if="item.meta.length" class="tb-meta">{{ item.meta.join(' · ') }}</div>
        <!-- An endpoint is an address, not something to read: it sits on the
             name's own line, truncated, so a connector is one compact row. Prose
             stays below the head and unclamped. -->
        <p v-if="item.descriptionKind === 'endpoint'" class="tb-desc endpoint">{{ item.description }}</p>
      </div>

      <!-- Two control variants. A switch says "stays on", which is true of
           a connector and false of a skill. -->
      <label v-if="!isSkill" class="tb-switch" @click.stop>
        <input
          type="checkbox"
          role="switch"
          :checked="item.selected"
          :aria-checked="item.selected"
          :aria-disabled="item.readOnly || busy"
          :aria-label="controlLabel"
          @change="!item.readOnly && !busy && emit('toggle', item)"
        />
        <span class="tb-track" aria-hidden="true"></span>
      </label>

      <button
        v-else
        type="button"
        class="tb-arm"
        :class="{ on: item.selected }"
        :aria-pressed="item.selected"
        :aria-label="controlLabel"
        @click.stop="emit('toggle', item)"
      >
        <span class="tb-radio" aria-hidden="true"></span>
        <!-- Two labels, one per breakpoint. Both are decorative: `aria-label`
             above is the accessible name, so neither is read out twice. The
             narrow one is shortened rather than hidden — dropping it entirely
             leaves a bare radio that says nothing about the one-message
             lifetime, which is the whole reason this is not a switch. -->
        <span class="tb-arm-text" aria-hidden="true">Use for next message</span>
        <span class="tb-arm-text short" aria-hidden="true">Next message</span>
      </button>
    </div>

    <!-- Never clamped: prose sizes to its content. An endpoint is
         rendered inside the head instead — see above. -->
    <p v-if="item.descriptionKind !== 'endpoint'" class="tb-desc prose">{{ item.description }}</p>

    <div v-if="band === 'signin'" class="tb-band warn" @click.stop>
      <span class="tb-band-text">
        Sign in with your {{ item.name }} account so the assistant can use it.
      </span>
      <button type="button" class="tb-btn" @click="emit('signin', item)">Sign in</button>
    </div>

    <!-- No Sign in button here: `opencode mcp auth` always fails for a setup
         server, so offering one would be a dead end (G3a). -->
    <div v-else-if="band === 'setup'" class="tb-band warn" @click.stop>
      <span class="tb-band-text">
        {{ item.name }} needs a token or an address before it can connect.
      </span>
      <button type="button" class="tb-btn-ghost" @click="emit('setup', item)">
        Set up in Manage connectors
      </button>
    </div>

    <div v-else-if="band === 'key'" class="tb-band warn" @click.stop>
      <span class="tb-band-text">
        {{ item.name }} needs your personal API key before it can connect.
      </span>
      <button type="button" class="tb-btn-ghost" @click="emit('setup', item)">
        Add key in Manage connectors
      </button>
    </div>

    <div v-else-if="band === 'broken'" class="tb-band err" @click.stop>
      <i class="tb-dot" aria-hidden="true"></i>
      <span class="tb-band-text">Not responding right now.</span>
      <button type="button" class="tb-btn-ghost" @click="emit('manage')">Manage connectors</button>
    </div>
  </div>
</template>

<style scoped>
.tb-card {
  border: 1px solid var(--border);
  border-left: 3px solid transparent;
  border-radius: 10px;
  background: var(--surface2);
  padding: 10px 12px;
  cursor: pointer;
  display: flex; flex-direction: column; gap: 8px;
  transition: border-color .12s, background .12s;
}
.tb-card:hover { border-color: var(--text3); }
.tb-card.ro { cursor: default; }

/* The left edge carries the kind, the way the icon does — muted while the item
   is off and full once it is on. Tying it to state alone left every skill flat,
   since only one can ever be armed. */
.tb-card.k-skill { border-left-color: color-mix(in srgb, var(--accent) 32%, transparent); }
.tb-card.k-connector { border-left-color: color-mix(in srgb, var(--accent2) 32%, transparent); }
.tb-card.k-skill.on { border-left-color: var(--accent); }
.tb-card.k-connector.on { border-left-color: var(--accent2); }

.tb-card-head { display: flex; align-items: center; gap: 12px; }

.tb-icon {
  width: 34px; height: 34px; flex-shrink: 0;
  display: flex; align-items: center; justify-content: center;
}
.tb-icon svg { width: 18px; height: 18px; }
/* Rounded square = skill, circle = connector. The shape carries the type for
   anyone who cannot use the colour. */
.tb-icon.k-skill {
  border-radius: 10px;
  color: var(--accent);
  background: color-mix(in srgb, var(--accent) 14%, transparent);
}
.tb-icon.k-connector {
  border-radius: 50%;
  color: var(--accent2);
  background: color-mix(in srgb, var(--accent2) 14%, transparent);
}

.tb-card-text { flex: 1; min-width: 0; }
.tb-card-title { display: flex; align-items: center; gap: 7px; flex-wrap: wrap; }
.tb-name { font-size: 13px; font-weight: 600; color: var(--text); word-break: break-word; }

.tb-badge {
  font-size: 10.5px; font-weight: 700; letter-spacing: .03em;
  padding: 2px 7px; border-radius: 999px; flex-shrink: 0;
}
.tb-badge.k-skill { color: var(--accent); background: color-mix(in srgb, var(--accent) 14%, transparent); }
.tb-badge.k-connector { color: var(--accent2); background: color-mix(in srgb, var(--accent2) 14%, transparent); }

.tb-meta { font-size: 11.5px; font-weight: 600; color: var(--text3); margin-top: 3px; }

.tb-desc { margin: 0; font-size: 12.5px; line-height: 1.5; color: var(--text2); text-wrap: pretty; }
/* An address, not prose: one line, truncated, so it cannot push the row tall. */
.tb-desc.endpoint {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11px; color: var(--text2); margin-top: 2px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

/* Switch, ported from .conn-switch so the two surfaces agree on what one looks
   like. Accent rather than green: here it means "selected for this chat", not
   "healthy". */
.tb-switch { position: relative; display: inline-flex; align-items: center; cursor: pointer; flex-shrink: 0; }
.tb-switch input { position: absolute; opacity: 0; width: 0; height: 0; }
.tb-track {
  position: relative; display: block; width: 34px; height: 20px; border-radius: 999px;
  background: var(--surface3); border: 1px solid var(--border);
  transition: background .12s, border-color .12s;
}
.tb-track::after {
  content: ''; position: absolute; top: 2px; left: 2px; width: 14px; height: 14px;
  border-radius: 50%; background: var(--text3); transition: transform .12s, background .12s;
}
.tb-switch input:checked + .tb-track { background: var(--accent); border-color: var(--accent); }
.tb-switch input:checked + .tb-track::after { transform: translateX(14px); background: #fff; }
.tb-switch input[aria-disabled="true"] + .tb-track { opacity: .55; }
.tb-switch input:focus-visible + .tb-track { outline: 2px solid var(--accent); outline-offset: 2px; }

/* The skill control. A radio mark and a label that states the lifetime, so the
   card says what it does rather than leaving the switch to imply the opposite. */
.tb-arm {
  display: inline-flex; align-items: center; gap: 6px; flex-shrink: 0;
  background: none; border: 1px solid var(--border); border-radius: 999px;
  padding: 4px 11px 4px 8px; cursor: pointer;
  font-family: inherit; font-size: 11.5px; font-weight: 600; color: var(--text2);
}
.tb-arm:hover { background: var(--hover); color: var(--text); }
.tb-arm.on { border-color: var(--accent); color: var(--accent); }
.tb-arm:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.tb-radio {
  width: 12px; height: 12px; border-radius: 50%; flex-shrink: 0;
  border: 1.5px solid var(--text3); position: relative;
}
.tb-arm.on .tb-radio { border-color: var(--accent); }
.tb-arm.on .tb-radio::after {
  content: ''; position: absolute; inset: 2px;
  border-radius: 50%; background: var(--accent);
}

.tb-band {
  display: flex; align-items: center; gap: 9px; flex-wrap: wrap;
  padding: 9px 11px; border-radius: 11px;
  font-size: 12px; line-height: 1.45; color: var(--text);
  cursor: default;
}
.tb-band.warn { background: color-mix(in srgb, var(--orange) 12%, transparent); }
.tb-band.err { background: color-mix(in srgb, var(--red) 12%, transparent); }
.tb-band-text { flex: 1; min-width: 140px; }
.tb-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--red); flex-shrink: 0; }

.tb-btn {
  background: var(--accent); color: #fff; border: 1px solid transparent;
  padding: 5px 14px; border-radius: 7px; cursor: pointer;
  font-family: inherit; font-size: 12px; font-weight: 600;
}
.tb-btn:hover { background: var(--accent-h); }
.tb-btn-ghost {
  background: var(--surface); border: 1px solid var(--border); color: var(--text2);
  padding: 5px 12px; border-radius: 7px; cursor: pointer;
  font-family: inherit; font-size: 12px;
}
.tb-btn-ghost:hover { color: var(--text); background: var(--surface3); }

.tb-arm-text.short { display: none; }

@media (max-width: 560px) {
  .tb-arm-text { display: none; }
  .tb-arm-text.short { display: inline; }
  .tb-arm { padding: 4px 9px 4px 7px; }
}
</style>
