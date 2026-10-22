<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useUiStore } from '../../stores/ui'
import { useArtifactsStore } from '../../stores/artifacts'
import { useSessionsStore } from '../../stores/sessions'
import {
  checkArtifact, shareArtifact, setArtifactAudience, setArtifactDownload, repairArtifact, stopArtifact,
  artifactMembers, startArtifactFix, NotSelfContainedError,
  type Artifact, type ArtifactFinding, type ArtifactFix, type ShareAudience, type ShareMember,
} from '../../api'
import { audienceLabel, findingText, STATUS_LABEL } from '../../utils/artifacts'
import { PASTE_LINK_HINT } from '../../utils/shareLinks'
import MemberPicker from './MemberPicker.vue'

// Sharing one file from a chat, in steps: check that it's self-contained (and
// have it fixed if it isn't), choose who can open it, then its link and
// settings. A shared file opens on the last step.
const ui = useUiStore()
const artifacts = useArtifactsStore()
const sessionsStore = useSessionsStore()
const target = artifacts.target

type Step = 'checking' | 'failed' | 'blocked' | 'fixing' | 'choose' | 'shared'
const step = ref<Step>('checking')
const findings = ref<ArtifactFinding[]>([])
const art = ref<Artifact | null>(null)
const members = ref<ShareMember[] | null>(null)
const membersError = ref('')
const choice = ref<'project' | 'members'>('members')
const picked = ref<number[]>([])
const allowDownload = ref(false)
const busy = ref(false)
const toggling = ref(false)
const error = ref('')
const saveError = ref('')
const announcement = ref('')
const copied = ref(false)
const pasteHint = ref(false)
const linkInput = ref<HTMLInputElement | null>(null)
// The fix this dialog follows: the one running for the file, or its last.
const fixId = ref('')
const fix = computed<ArtifactFix | null>(() => artifacts.fixes.find(f => f.id === fixId.value) ?? null)
const fixRunning = computed(() => fix.value?.state === 'running')
const fixLeft = computed(() => (fix.value?.findings || []).filter(f => f.blocking))
// The file was fixed while this dialog was open.
const fixed = ref(false)

const message = (e: unknown) => (e instanceof Error ? e.message : String(e))
const blocking = computed(() => findings.value.filter(f => f.blocking))
const warnings = computed(() => findings.value.filter(f => !f.blocking))
const attention = computed(() => (art.value?.findings || []).filter(f => f.blocking))
const downloadOn = computed(() => (step.value === 'shared' ? !!art.value?.allow_download : allowDownload.value))
// Who can open it is editable once the platform has said who that is.
const canChoose = computed(() => step.value === 'choose' || !!art.value?.audience)

function announce(text: string) {
  // Clearing first makes a repeated message be read again.
  announcement.value = ''
  setTimeout(() => { announcement.value = text }, 50)
}

const audience = (): ShareAudience => ({ user_ids: [...picked.value], project: choice.value === 'project' })

function adopt(a: Artifact) {
  art.value = a
  artifacts.put(a)
  if (a.audience) {
    choice.value = a.audience.project ? 'project' : 'members'
    picked.value = [...a.audience.user_ids]
  }
}

async function loadMembers() {
  try {
    members.value = await artifactMembers()
    membersError.value = ''
  } catch (e) {
    membersError.value = message(e)
  }
}

async function check() {
  if (!target) return
  step.value = 'checking'
  error.value = ''
  try {
    const c = await checkArtifact(target.sessionId, target.path)
    findings.value = c.findings
    step.value = c.self_contained ? 'choose' : 'blocked'
    if (c.self_contained && !members.value) void loadMembers()
  } catch (e) {
    error.value = message(e)
    step.value = 'failed'
  }
}

async function share() {
  if (!target) return
  busy.value = true
  error.value = ''
  try {
    adopt(await shareArtifact(target.sessionId, target.path, audience(), allowDownload.value))
    step.value = 'shared'
    announce('Shared. Copy the link to send it.')
  } catch (e) {
    if (e instanceof NotSelfContainedError) {
      findings.value = e.findings
      step.value = 'blocked'
    } else {
      error.value = message(e)
    }
  } finally {
    busy.value = false
  }
}

async function startFix() {
  if (!target) return
  busy.value = true
  error.value = ''
  try {
    const f = await startArtifactFix(target.sessionId, target.path)
    fixId.value = f.id
    fixed.value = false
    artifacts.putFix(f)
    if (f.state !== 'running') {
      await settle(f)
      return
    }
    if (step.value !== 'shared') step.value = 'fixing'
    announce('Making it self-contained. You can close this.')
    // A shared file's status becomes Fixing.
    if (artifacts.byPath.has(target.path)) void artifacts.load()
  } catch (e) {
    error.value = message(e)
  } finally {
    busy.value = false
  }
}

// A fix that ended: a fixed file moves on to choosing people; otherwise the
// file is checked again, and the dialog says why the fix didn't work.
async function settle(f: ArtifactFix) {
  if (f.state === 'done') {
    fixed.value = true
    announce(`${f.name} is self-contained now.`)
  } else {
    announce(`The fix didn't work. ${f.problem || ''}`)
  }
  if (step.value === 'shared') await reload()
  else await check()
}

watch(() => fix.value?.state, (now, before) => {
  if (before === 'running' && now && now !== 'running' && fix.value) void settle(fix.value)
})

async function openFixChat() {
  const id = fix.value?.chat_id
  if (!id) return
  ui.closeModal()
  try {
    await sessionsStore.loadSessions()
    await sessionsStore.switchSession(id)
    ui.showChat()
  } catch {
    ;(window as any).showToast?.("Couldn't open the fix chat. It may have been deleted.")
  }
}

// Re-read the shared file, so a failed change shows what holds.
async function reload() {
  if (!target) return
  await artifacts.load()
  const a = artifacts.byPath.get(target.path)
  if (a) adopt(a)
}

// Once shared, changes save as they're made. One save at a time, and one more
// afterwards if anything changed meanwhile, so quick picks land in order.
let saving: Promise<void> | null = null
let again = false

function persist() {
  if (step.value !== 'shared' || !art.value) return
  if (saving) {
    again = true
    return
  }
  const id = art.value.id
  saving = (async () => {
    try {
      do {
        again = false
        adopt(await setArtifactAudience(id, audience()))
      } while (again)
      saveError.value = ''
      announce('Saved.')
    } catch (e) {
      const why = message(e)
      await reload()
      saveError.value = why
    } finally {
      saving = null
    }
  })()
}

function choose(c: 'project' | 'members') {
  choice.value = c
  persist()
}

function pick(ids: number[]) {
  picked.value = ids
  persist()
}

async function setDownload(on: boolean) {
  if (step.value !== 'shared' || !art.value) {
    allowDownload.value = on
    return
  }
  toggling.value = true
  saveError.value = ''
  try {
    adopt(await setArtifactDownload(art.value.id, on))
    announce(on ? 'People can now download the file.' : 'People can no longer download the file.')
  } catch (e) {
    const why = message(e)
    await reload()
    saveError.value = why
  } finally {
    toggling.value = false
  }
}

async function repair() {
  if (!art.value) return
  busy.value = true
  error.value = ''
  try {
    adopt(await repairArtifact(art.value.id))
    announce('Repaired.')
  } catch (e) {
    error.value = message(e)
  } finally {
    busy.value = false
  }
}

async function stop() {
  const a = art.value
  if (!a || !confirm(`Stop sharing ${a.name}?\n\nEveryone you shared it with loses access immediately.`)) return
  busy.value = true
  error.value = ''
  try {
    await stopArtifact(a.id)
    artifacts.drop(a.id)
    ;(window as any).showToast?.(`Stopped sharing ${a.name}`)
    ui.closeModal()
  } catch (e) {
    error.value = message(e)
  } finally {
    busy.value = false
  }
}

async function copyLink() {
  const url = art.value?.url
  if (!url) return
  try {
    await navigator.clipboard.writeText(url)
    copied.value = true
    pasteHint.value = true
    announce(`Link copied. ${PASTE_LINK_HINT}`)
    setTimeout(() => { copied.value = false }, 2000)
  } catch {
    // No clipboard access (an insecure context): leave the link selected.
    linkInput.value?.select()
    announce('Select the link and copy it.')
  }
}

const noneChosen = computed(() =>
  step.value === 'shared'
    ? 'No one is chosen yet, so only you can open it.'
    : 'No one is chosen yet, so only you will be able to open it.',
)

onMounted(() => {
  if (!target) {
    ui.closeModal()
    return
  }
  const last = artifacts.lastFixAt(target.path)
  if (last) fixId.value = last.id
  const known = artifacts.byPath.get(target.path)
  if (!known) {
    if (last?.state === 'running') step.value = 'fixing'
    else void check()
    return
  }
  adopt(known)
  step.value = 'shared'
  void loadMembers()
  void reload()
})
</script>

<template>
  <div
    class="modal art-modal"
    role="dialog"
    aria-modal="true"
    aria-labelledby="art-modal-title"
    @click.stop
  >
    <header class="art-head">
      <h2 id="art-modal-title">Share {{ target?.name }}</h2>
    </header>

    <div class="art-body">
      <p v-if="step === 'checking'" class="art-muted" role="status">Checking that the file is self-contained…</p>

      <p v-else-if="step === 'failed'" class="art-alert" role="alert">{{ error }}</p>

      <template v-else>
        <section v-if="fixRunning" class="art-section" aria-labelledby="art-fixing-h">
          <h3 id="art-fixing-h">Making it self-contained…</h3>
          <div class="art-progress" aria-hidden="true"><span></span></div>
          <p class="art-hint">
            The assistant is rewriting a copy in a chat of its own. When it's done, the copy replaces your file,
            and your file is kept as a backup.
          </p>
          <p class="art-hint">You can close this. The fix keeps running.</p>
          <button class="art-link-btn" @click="openFixChat">Follow it in its chat</button>
        </section>

        <div v-if="fix?.state === 'failed' && step !== 'choose'" class="art-alert art-problem">
          <p><strong>The fix didn't work.</strong> {{ fix.problem }}</p>
          <template v-if="fixLeft.length">
            <p class="art-left">In the fixed file:</p>
            <ul class="art-findings">
              <li v-for="(f, i) in fixLeft" :key="i">{{ findingText(f) }}</li>
            </ul>
          </template>
          <button class="art-link-btn" @click="openFixChat">Open the fix chat</button>
        </div>

        <p v-if="fixed && step !== 'blocked'" class="art-note">
          It's self-contained now. Your original file is kept in the chat's folder, in
          <code>.system/artifact-backups</code>.
        </p>

        <section v-if="step === 'blocked'" class="art-section" aria-labelledby="art-blocked-h">
          <h3 id="art-blocked-h">People you share this with can't see parts of it</h3>
          <ul class="art-findings">
            <li v-for="(f, i) in blocking" :key="i">{{ findingText(f) }}</li>
          </ul>
          <p class="art-hint">
            Shared files can't load anything from the internet or from other files, so browsers block these parts.
          </p>
        </section>

        <p v-if="error && (step === 'blocked' || step === 'fixing')" class="art-alert" role="alert">{{ error }}</p>

        <template v-if="step === 'choose' || step === 'shared'">
          <section v-if="step === 'shared' && art" class="art-section" aria-labelledby="art-link-h">
            <h3 id="art-link-h">Link</h3>
            <div class="art-link">
              <input
                ref="linkInput"
                class="form-input"
                readonly
                :value="art.url"
                aria-label="Link to this file"
                @focus="($event.target as HTMLInputElement).select()"
                @copy="pasteHint = true"
              />
              <button class="art-btn-ghost" @click="copyLink">{{ copied ? 'Copied' : 'Copy' }}</button>
              <a class="art-btn-ghost" :href="art.url" target="_blank" rel="noopener noreferrer">Open</a>
            </div>
            <p v-if="pasteHint" class="art-paste-hint">{{ PASTE_LINK_HINT }}</p>
            <div v-if="art.status !== 'shared' && !fixRunning" class="art-alert art-problem">
              <p><strong>{{ STATUS_LABEL[art.status] }}.</strong> {{ art.problem }}</p>
              <ul v-if="attention.length" class="art-findings">
                <li v-for="(f, i) in attention" :key="i">{{ findingText(f) }}</li>
              </ul>
              <template v-if="art.status === 'attention'">
                <button class="art-btn" :disabled="busy" @click="startFix">{{ busy ? 'Starting…' : 'Make self-contained' }}</button>
                <p class="art-hint">Uses your model, like a chat message.</p>
              </template>
              <button
                v-if="art.status === 'removed' || art.status === 'unavailable'"
                class="art-btn"
                :disabled="busy"
                @click="repair"
              >
                {{ busy ? 'Repairing…' : 'Repair' }}
              </button>
            </div>
          </section>

          <section v-if="step === 'choose' && warnings.length" class="art-section" aria-labelledby="art-warn-h">
            <h3 id="art-warn-h">Parts that may not work for the people you share it with</h3>
            <ul class="art-findings">
              <li v-for="(f, i) in warnings" :key="i">{{ findingText(f) }}</li>
            </ul>
          </section>

          <section class="art-section" aria-labelledby="art-who-h">
            <h3 id="art-who-h">Who can open it</h3>
            <p v-if="!canChoose" class="art-muted">
              Open Knowledge Worker Agent from your workspace to see and change who can open it.
            </p>
            <div v-else class="art-audience">
              <div class="art-choice" role="radiogroup" aria-labelledby="art-who-h">
                <label class="art-radio">
                  <input type="radio" name="art-audience" value="project" :checked="choice === 'project'" @change="choose('project')" />
                  Everyone in the project
                </label>
                <label class="art-radio">
                  <input type="radio" name="art-audience" value="members" :checked="choice === 'members'" @change="choose('members')" />
                  Select project members
                </label>
              </div>
              <template v-if="choice === 'members'">
                <p v-if="membersError" class="art-alert">{{ membersError }}</p>
                <p v-else-if="!members" class="art-muted">Loading…</p>
                <p v-else-if="!members.length" class="art-muted">No one else is a member of this project.</p>
                <template v-else>
                  <MemberPicker :members="members" :selected="picked" label="Choose project members" @change="pick" />
                  <p v-if="!picked.length" class="art-hint">{{ noneChosen }}</p>
                </template>
              </template>
            </div>
            <p v-if="saveError" class="art-alert" role="alert">{{ saveError }}</p>
          </section>

          <section class="art-section">
            <div class="art-row" :class="{ on: downloadOn }">
              <span class="art-row-main">
                <span class="art-option-name">Let people download the file</span>
                <span class="art-hint">Copies they download stay with them after you stop sharing.</span>
              </span>
              <label class="art-switch">
                <input
                  type="checkbox"
                  role="switch"
                  :checked="downloadOn"
                  :aria-checked="downloadOn"
                  :disabled="toggling"
                  aria-label="Let people download the file"
                  @change="setDownload(($event.target as HTMLInputElement).checked)"
                />
                <span class="art-track" aria-hidden="true"></span>
              </label>
            </div>
          </section>

          <p v-if="error" class="art-alert" role="alert">{{ error }}</p>

          <div v-if="step === 'choose'" class="art-notice">
            They'll see everything in this file, including data from your connectors.
            It opens while your workspace is running.
          </div>
        </template>
      </template>
    </div>

    <p class="sr-only" role="status" aria-live="polite">{{ announcement }}</p>

    <footer class="art-foot">
      <span class="art-summary">
        {{ step === 'shared' && art ? audienceLabel(art.audience) : step === 'blocked' ? 'Uses your model, like a chat message.' : '' }}
      </span>
      <div class="art-foot-actions">
        <button class="art-btn-ghost" @click="ui.closeModal()">{{ step === 'shared' || step === 'fixing' ? 'Close' : 'Cancel' }}</button>
        <button v-if="step === 'failed'" class="art-btn" @click="check">Check again</button>
        <button v-if="step === 'blocked'" class="art-btn" :disabled="busy" @click="startFix">
          {{ busy ? 'Starting…' : 'Make self-contained' }}
        </button>
        <button v-if="step === 'choose'" class="art-btn" :disabled="busy" @click="share">
          {{ busy ? 'Sharing…' : 'Share' }}
        </button>
        <button v-if="step === 'shared'" class="art-btn-danger" :disabled="busy" @click="stop">
          {{ busy ? 'Stopping…' : 'Stop sharing' }}
        </button>
      </div>
    </footer>
  </div>
</template>

<style scoped>
/* Laid out like the chat Share dialog (ShareModal.vue): the body scrolls, the
   header and footer stay put. */
.art-modal {
  width: min(640px, 95vw);
  max-height: 90vh;
  padding: 0;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

.art-head { padding: 20px 20px 16px; border-bottom: 1px solid var(--border); }
.art-head h2 { font-size: 19px; font-weight: 700; margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.art-body { flex: 1; min-height: 0; overflow-y: auto; padding: 16px 20px; display: flex; flex-direction: column; gap: 24px; }

.art-section h3 { font-size: 13px; font-weight: 600; margin: 0 0 8px; }
.art-hint, .art-muted { font-size: 12px; color: var(--text2); margin: 0 0 8px; line-height: 1.45; }
.art-alert {
  font-size: 12px; margin: 8px 0 0; padding: 8px 12px; border-radius: 7px;
  border: 1px solid var(--border);
  background: color-mix(in srgb, var(--orange) 12%, transparent);
  color: var(--text);
}
.art-paste-hint {
  font-size: 12px; margin: 8px 0 0; padding: 8px 12px; border-radius: 7px; line-height: 1.45;
  border: 1px solid var(--border); border-left: 3px solid var(--accent); background: var(--surface2); color: var(--text);
}
.art-problem p { margin: 0; }
.art-problem .art-btn { margin-top: 8px; }
.art-problem .art-hint { margin: 6px 0 0; }
.art-problem .art-left { margin-top: 6px; }
.art-problem .art-link-btn { margin-top: 6px; }
.art-note {
  font-size: 12px; margin: 0; padding: 8px 12px; border-radius: 7px; line-height: 1.45;
  border: 1px solid var(--border);
  background: color-mix(in srgb, var(--green) 12%, transparent);
}
.art-link-btn {
  background: none; border: none; padding: 0; font: inherit; font-size: 12px;
  color: var(--accent); text-decoration: underline; cursor: pointer;
}

/* Indeterminate: a fix gives no measure of how far it has got. */
.art-progress { position: relative; height: 4px; margin: 4px 0 12px; border-radius: 999px; background: var(--surface3); overflow: hidden; }
.art-progress span {
  position: absolute; top: 0; bottom: 0; left: -35%; width: 35%; border-radius: inherit;
  background: var(--accent); animation: art-slide 1.4s ease-in-out infinite;
}
@keyframes art-slide { to { left: 100%; } }
@media (prefers-reduced-motion: reduce) {
  .art-progress span { animation: none; left: 0; width: 100%; opacity: .5; }
}

.art-findings { margin: 0 0 8px; padding-left: 18px; font-size: 12px; line-height: 1.5; }
.art-problem .art-findings { margin: 6px 0 0; }

.art-link { display: flex; gap: 8px; }
.art-link .form-input { flex: 1; min-width: 0; font-size: 12px; }
.art-link a { display: inline-flex; align-items: center; text-decoration: none; }

.art-audience { display: flex; flex-direction: column; gap: 12px; }
.art-choice { display: flex; flex-direction: column; gap: 8px; }
.art-radio { display: flex; align-items: center; gap: 8px; font-size: 13px; cursor: pointer; }
.art-radio input { margin: 0; accent-color: var(--accent); }
.art-audience .art-hint { margin: 0; }

.art-row {
  display: flex; align-items: flex-start; gap: 16px;
  padding: 8px 12px;
  border: 1px solid var(--border);
  border-left: 3px solid color-mix(in srgb, var(--accent) 32%, transparent);
  border-radius: 7px;
  background: var(--surface2);
  font-size: 13px;
}
.art-row.on { border-left-color: var(--accent); }
.art-row-main { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.art-row-main .art-hint { margin: 0; }
.art-option-name { font-weight: 600; }
/* The Connectors switch (ConnectorRow.vue), so one toggle reads the same everywhere. */
.art-switch { position: relative; display: inline-flex; align-items: center; cursor: pointer; flex-shrink: 0; margin-top: 2px; }
.art-switch input { position: absolute; opacity: 0; width: 0; height: 0; }
.art-track {
  position: relative; display: block; width: 34px; height: 20px; border-radius: 999px;
  background: var(--surface3); border: 1px solid var(--border);
  transition: background .12s, border-color .12s;
}
.art-track::after {
  content: ''; position: absolute; top: 2px; left: 2px; width: 14px; height: 14px;
  border-radius: 50%; background: var(--text3); transition: transform .12s, background .12s;
}
.art-switch input:checked + .art-track { background: var(--green); border-color: var(--green); }
.art-switch input:checked + .art-track::after { transform: translateX(14px); background: #fff; }
.art-switch input:disabled + .art-track { opacity: .55; cursor: default; }
.art-switch input:focus-visible + .art-track { outline: 2px solid var(--accent); outline-offset: 2px; }

.art-notice {
  font-size: 12px; line-height: 1.5; color: var(--text2);
  padding: 12px; border-radius: 7px;
  border: 1px solid var(--border);
  background: var(--surface2);
}

.art-foot {
  display: flex; align-items: center; justify-content: space-between; gap: 16px;
  padding: 12px 20px;
  border-top: 1px solid var(--border);
  background: var(--surface2);
}
.art-summary { font-size: 12px; color: var(--text2); }
.art-foot-actions { display: flex; gap: 8px; flex-shrink: 0; }

.art-btn {
  background: var(--accent); color: #fff; border: 1px solid transparent;
  padding: 7px 16px; border-radius: 7px; cursor: pointer; font-size: 13px; font-weight: 600;
}
.art-btn:hover { background: var(--accent-h); }
.art-btn-ghost {
  background: var(--surface2); border: 1px solid var(--border); color: var(--text2);
  padding: 7px 16px; border-radius: 7px; cursor: pointer; font-size: 13px; white-space: nowrap;
}
.art-btn-ghost:hover { color: var(--text); background: var(--surface3); }
.art-btn-danger {
  background: var(--surface2); border: 1px solid var(--red); color: var(--red);
  padding: 7px 16px; border-radius: 7px; cursor: pointer; font-size: 13px; font-weight: 600;
}
.art-btn-danger:hover { background: color-mix(in srgb, var(--red) 12%, transparent); }
.art-btn:disabled, .art-btn-ghost:disabled, .art-btn-danger:disabled { opacity: .6; cursor: default; }

@media (max-width: 760px) {
  .art-foot { flex-wrap: wrap; }
}
</style>
