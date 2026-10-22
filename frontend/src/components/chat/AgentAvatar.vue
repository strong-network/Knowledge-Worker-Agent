<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script lang="ts">
import { isTodoTool } from '../../utils/todos'
import FRAMES from './pixel-frames.json'

// thinking: no step, tool or text yet. working: from the first of those until the turn ends.
// finished: one happy face after a turn ends well. idle: waiting for the user. static: an older reply.
export type AvatarState = 'thinking' | 'working' | 'finished' | 'idle' | 'static'
// What the display shows while the agent works: Typing for text, otherwise the kind of tool it used.
export type WorkFace = 'spinner' | 'scan' | 'search' | 'fetch' | 'type'

export function workFaceFor(tool: string, mcpServer?: string): WorkFace {
  const name = tool.toLowerCase()
  if (isTodoTool(name)) return 'scan'
  if (name.includes('search') || name === 'grep' || name === 'glob') return 'search'
  if (mcpServer || name === 'webfetch') return 'fetch'
  return 'spinner'
}

type Frames = { animations: Record<string, { role: string; still: number; episodeMs?: number; frames: { ms: number; rows: string[] }[] }> }

const BG = '#03a9f1'
const INK = '#fff'
// Unlit pixels stay faintly visible, so the face reads as a screen rather than floating dots.
const LEVELS: Record<string, number> = { '#': 1, '+': 0.5, '.': 0.14 }

// A 9 by 7 display without its corners, so it fits the rounded face.
const CELLS: [number, number][] = []
for (let r = 0; r < 7; r++) {
  for (let c = 0; c < 9; c++) if (!((r === 0 || r === 6) && (c === 0 || c === 8))) CELLS.push([c, r])
}

const ANIMS = Object.fromEntries(Object.entries((FRAMES as Frames).animations).map(([name, a]) => [name, {
  ...a,
  levels: a.frames.map(f => CELLS.map(([c, r]) => LEVELS[f.rows[r][c]] ?? LEVELS['.'])),
}]))
const IDLE_MOVES = Object.keys(ANIMS).filter(name => ANIMS[name].role === 'idle-move')

// The app icon's robot, 1.24 times larger: without a ring the disc edge is the frame.
function markup(levels: number[]): string {
  const pixels = CELLS.map(([c, r], i) =>
    `<rect class="px" x="${69.5 + c * 10}" y="${84 + r * 10}" width="8" height="8" rx="1.6" opacity="${levels[i]}"/>`).join('')
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 227 227" aria-hidden="true" focusable="false">` +
    `<circle cx="113.5" cy="113.5" r="113.5" fill="${BG}"/>` +
    `<g transform="translate(113.5 113.5) scale(1.24) translate(-113.5 -101)">` +
    `<g fill="${BG}" stroke="${INK}" stroke-width="8" stroke-linecap="round" stroke-linejoin="round">` +
    `<path d="M113.5 48.5V73.5"/><rect x="45" y="104.5" width="20" height="29" rx="7"/>` +
    `<rect x="162" y="104.5" width="20" height="29" rx="7"/><rect x="59" y="73.5" width="109" height="89" rx="30"/></g>` +
    `<g fill="${INK}"><circle cx="113.5" cy="48.5" r="9.5"/>${pixels}</g></g></svg>`
}

const LIVE = markup(ANIMS.idle.levels[0])
const STILL = `data:image/svg+xml,${encodeURIComponent(LIVE)}`
</script>

<script setup lang="ts">
import { ref, watch, onMounted, onBeforeUnmount } from 'vue'

const props = defineProps<{
  state: AvatarState
  work?: WorkFace
  // The user is writing a prompt: idle animations wait until the composer is empty again.
  typing?: boolean
}>()

const FINISHED_MS = 2400
const REST_MS = 30_000

// Mounting already finished means a remount, not a turn that just ended.
const shown = ref<AvatarState>(props.state === 'finished' ? 'idle' : props.state)
const face = ref('')
let pixels: SVGRectElement[] = []
let frame = 0
let frameTimer: ReturnType<typeof setTimeout> | undefined
let timer: ReturnType<typeof setTimeout> | undefined
let last = ''

const reduced = () => matchMedia('(prefers-reduced-motion: reduce)').matches

function bind(el: unknown) {
  pixels = el instanceof Element ? [...el.querySelectorAll<SVGRectElement>('.px')] : []
  if (face.value) draw()
}

// Only pixel opacities change, once per frame, so nothing else re-renders.
function draw() {
  const levels = ANIMS[face.value].levels[frame]
  pixels.forEach((px, i) => { px.style.opacity = String(levels[i]) })
}

function play(name: string) {
  clearTimeout(frameTimer)
  face.value = name
  frame = reduced() ? ANIMS[name].still : 0
  draw()
  if (!reduced()) frameTimer = setTimeout(advance, ANIMS[name].frames[frame].ms)
}

function advance() {
  const a = ANIMS[face.value]
  frame = (frame + 1) % a.frames.length
  draw()
  frameTimer = setTimeout(advance, a.frames[frame].ms)
}

function show(state: AvatarState) {
  clearTimeout(timer)
  shown.value = state
  if (state === 'static') {
    clearTimeout(frameTimer)
    face.value = ''
  } else if (state === 'thinking') play('think')
  else if (state === 'working') play(props.work || 'spinner')
  else if (state === 'finished') {
    play('happy')
    timer = setTimeout(() => show('idle'), FINISHED_MS)
  } else {
    play('idle')
    rest()
  }
}

function rest() {
  clearTimeout(timer)
  if (props.typing || reduced()) return
  timer = setTimeout(playIdleMove, REST_MS)
}

// One idle animation at random, never the same twice in a row, then back to the blink.
function playIdleMove() {
  if (document.hidden) return rest()
  const pool = IDLE_MOVES.filter(name => name !== last)
  last = pool[Math.floor(Math.random() * pool.length)]
  play(last)
  timer = setTimeout(() => {
    play('idle')
    rest()
  }, ANIMS[last].episodeMs)
}

watch(() => props.state, show)

watch(() => props.work, work => {
  if (shown.value === 'working' && work && work !== face.value) play(work)
})

watch(() => props.typing, typing => {
  if (shown.value !== 'idle') return
  if (!typing) return rest()
  clearTimeout(timer)
  if (face.value !== 'idle') play('idle')
})

onMounted(() => show(shown.value))

onBeforeUnmount(() => {
  clearTimeout(timer)
  clearTimeout(frameTimer)
})
</script>

<template>
  <!-- Older replies never move, and a long chat has hundreds: one element each, with a picture of the resting face. -->
  <img v-if="shown === 'static'" class="companion" :src="STILL" alt="" data-state="static" />
  <span v-else :ref="bind" class="companion" :data-state="shown" :data-face="face" v-html="LIVE" />
</template>

<style scoped>
.companion { display: block; width: 100%; height: 100%; }
.companion :deep(svg) { display: block; width: 100%; height: 100%; }
</style>
