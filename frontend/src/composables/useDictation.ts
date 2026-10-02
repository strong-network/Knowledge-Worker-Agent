// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import { ref, computed, getCurrentInstance, onBeforeUnmount } from 'vue'
import workletUrl from './dictationWorklet.ts?worker&url'
import {
  DictationError,
  startDictation,
  sendDictationAudio,
  stopDictation,
  discardDictation,
  type DictationProgress,
} from '../api'

// Dictation: records the microphone, streams it to the server and
// reports the words as they come back. The composer decides where they go.
//
// Audio is uploaded every 250 ms with at most one upload in flight; whatever is
// captured meanwhile goes into the next one. The microphone is asked for before
// the server's dictation is created, because the browser's permission prompt
// can outlast the ten seconds the server waits for audio.

export type DictationState = 'idle' | 'starting' | 'recording' | 'finishing'

export type DictationProblem = 'blocked' | 'no_mic' | 'mic_busy' | 'in_use' | 'no_speech' | 'failed'

// What went wrong, why, and how to fix it.
export const dictationMessages: Record<DictationProblem, { title: string; body: string }> = {
  blocked: {
    title: 'Microphone blocked.',
    body: 'Your browser isn\u2019t letting Knowledge Worker Agent use the microphone. Allow microphone access for this site in your browser settings, then try again. If it stays blocked, contact your organization\u2019s IT administrator.',
  },
  no_mic: { title: 'No microphone found.', body: 'Connect a microphone, then try again.' },
  mic_busy: {
    title: 'Microphone unavailable.',
    body: 'Another app may be using it. Close that app, then try again.',
  },
  in_use: {
    title: 'Dictation in use.',
    body: 'You\u2019re already dictating in another Knowledge Worker Agent tab. Stop it there, then try again.',
  },
  no_speech: { title: 'No speech detected.', body: 'Try again, speaking a little closer to the microphone.' },
  failed: {
    title: 'Transcription stopped.',
    body: 'Something went wrong while turning your speech into text. Try again to continue.',
  },
}

export interface DictationHandlers {
  // The words so far, called whenever they change while recording.
  onText: (text: string) => void
  // Called once when a dictation is over, with the words to keep: all of them
  // when it ended well, the final ones when transcription failed part-way, and
  // '' when nothing is kept (discarded, no speech, never started).
  onEnd: (text: string, ok: boolean) => void
}

const UPLOAD_EVERY_MS = 250
const MAX_SECONDS = 120
// The server takes at most 256 KiB per upload.
const MAX_UPLOAD_SAMPLES = 128 * 1024 - 1024
const FLUSH_WAIT_MS = 300

// Whether this browser can dictate at all. The server's `voice_available`
// says whether this workspace can.
export function dictationSupported(): boolean {
  return typeof window !== 'undefined' &&
    window.isSecureContext &&
    typeof navigator.mediaDevices?.getUserMedia === 'function' &&
    typeof AudioWorkletNode !== 'undefined'
}

function join(a: string, b: string): string {
  a = a.trim()
  b = b.trim()
  return a && b ? `${a} ${b}` : a || b
}

function micProblem(e: unknown): DictationProblem {
  switch ((e as { name?: string })?.name) {
    case 'NotAllowedError':
    case 'SecurityError':
      return 'blocked'
    case 'NotFoundError':
    case 'OverconstrainedError':
      return 'no_mic'
    default:
      return 'mic_busy'
  }
}

export function useDictation(handlers: DictationHandlers) {
  const state = ref<DictationState>('idle')
  const elapsed = ref(0)
  const problem = ref<DictationProblem | null>(null)
  const announcement = ref('')
  const active = computed(() => state.value !== 'idle')

  // Everything below belongs to one dictation. `gen` changes when it ends, so
  // an await that resumes after that knows to stop.
  let gen = 0
  let id = ''
  let stream: MediaStream | null = null
  let ctx: AudioContext | null = null
  let node: AudioWorkletNode | null = null
  let pending: Int16Array[] = []
  let pendingSamples = 0
  let final = ''
  let live = ''
  let startedAt = 0
  let clock: ReturnType<typeof setInterval> | null = null
  let wake: (() => void) | null = null
  let pump: Promise<void> | null = null

  function announce(text: string) {
    // A live region only speaks when its text changes.
    announcement.value = text === announcement.value ? `${text}\u00a0` : text
  }

  function releaseMic() {
    stream?.getTracks().forEach(t => t.stop())
    stream = null
  }

  function closeAudio() {
    if (node) {
      node.port.onmessage = null
      node.disconnect()
      node = null
    }
    if (ctx) {
      void ctx.close().catch(() => {})
      ctx = null
    }
  }

  function teardown() {
    gen++
    if (clock) clearInterval(clock)
    clock = null
    wake?.()
    releaseMic()
    closeAudio()
    id = ''
    pending = []
    pendingSamples = 0
    pump = null
  }

  // `said` is the last phrase, made final by stopping; it is read out ahead of
  // the confirmation so every phrase is announced once.
  function end(text: string, ok: boolean, why: DictationProblem | null, said = '') {
    // A dictation that failed while recording still holds the server's one
    // slot until its idle timeout; free it so Try again works straight away.
    const was = id
    teardown()
    if (was) discardDictation(was)
    state.value = 'idle'
    problem.value = why
    if (why) announce(`${dictationMessages[why].title} ${dictationMessages[why].body}`)
    else if (ok) announce(join(said, 'Text added. Review it, then send.'))
    handlers.onEnd(text, ok)
  }

  // Transcription failed part-way: keep the final words, drop the live ones.
  function fail(e: unknown) {
    const kept = e instanceof DictationError && e.text ? e.text : final
    end(kept, false, 'failed')
  }

  function take(): Int16Array<ArrayBuffer> {
    const n = Math.min(pendingSamples, MAX_UPLOAD_SAMPLES)
    const out = new Int16Array(n)
    let at = 0
    while (at < n) {
      const chunk = pending[0]
      const want = n - at
      if (chunk.length <= want) {
        out.set(chunk, at)
        at += chunk.length
        pending.shift()
      } else {
        out.set(chunk.subarray(0, want), at)
        pending[0] = chunk.subarray(want)
        at += want
      }
    }
    pendingSamples -= n
    return out
  }

  function apply(p: DictationProgress) {
    if (p.final !== final) {
      const added = p.final.startsWith(final) ? p.final.slice(final.length) : p.final
      if (added.trim()) announce(added.trim())
    }
    final = p.final
    live = p.live
    handlers.onText(join(final, live))
  }

  function nap(ms: number): Promise<void> {
    return new Promise(resolve => {
      const t = setTimeout(() => { wake = null; resolve() }, ms)
      wake = () => { clearTimeout(t); wake = null; resolve() }
    })
  }

  // Uploads while recording. Returns when recording stops or fails.
  async function run(my: number) {
    let last = performance.now()
    while (my === gen && state.value === 'recording') {
      const backlog = pendingSamples >= MAX_UPLOAD_SAMPLES
      await nap(backlog ? 0 : Math.max(0, last + UPLOAD_EVERY_MS - performance.now()))
      if (my !== gen || state.value !== 'recording') return
      last = performance.now()
      if (pendingSamples === 0) continue
      try {
        const p = await sendDictationAudio(id, take())
        if (my !== gen) return
        apply(p)
        if (p.limit) { void stop(); return }
      } catch (e) {
        if (my === gen) fail(e)
        return
      }
    }
  }

  async function start() {
    if (state.value !== 'idle') return
    const my = ++gen
    problem.value = null
    final = ''
    live = ''
    state.value = 'starting'

    // Created inside the click, where the browser lets audio start.
    try {
      ctx = new AudioContext()
    } catch {
      end('', false, 'failed')
      return
    }
    const loaded = ctx.audioWorklet.addModule(workletUrl)
    loaded.catch(() => {})

    let got: MediaStream
    try {
      got = await navigator.mediaDevices.getUserMedia({ audio: true })
    } catch (e) {
      if (my === gen) end('', false, micProblem(e))
      return
    }
    // Held locally until now: a stream granted to a dictation that has since
    // ended must not overwrite the next one's.
    if (my !== gen) { got.getTracks().forEach(t => t.stop()); return }
    stream = got

    try {
      await loaded
      if (my !== gen || !ctx) return
      if (ctx.state === 'suspended') void ctx.resume().catch(() => {})
      node = new AudioWorkletNode(ctx, 'kwa-dictation', { numberOfInputs: 1, numberOfOutputs: 1 })
      node.port.onmessage = (e: MessageEvent) => {
        if (e.data instanceof Int16Array) {
          pending.push(e.data)
          pendingSamples += e.data.length
        }
      }
      // The worklet writes nothing to its output; connecting it keeps it running.
      ctx.createMediaStreamSource(got).connect(node).connect(ctx.destination)
    } catch {
      if (my === gen) end('', false, 'failed')
      return
    }

    try {
      const got = await startDictation()
      if (my !== gen) { discardDictation(got); return }
      id = got
    } catch (e) {
      if (my !== gen) return
      end('', false, e instanceof DictationError && e.code === 'in_use' ? 'in_use' : 'failed')
      return
    }

    state.value = 'recording'
    startedAt = performance.now()
    elapsed.value = 0
    clock = setInterval(() => {
      elapsed.value = Math.floor((performance.now() - startedAt) / 1000)
      if (elapsed.value >= MAX_SECONDS) void stop()
    }, 250)
    announce('Recording')
    pump = run(my)
  }

  // Asks the worklet for the samples it is still holding.
  function flush(): Promise<void> {
    const n = node
    if (!n) return Promise.resolve()
    return new Promise(resolve => {
      const t = setTimeout(resolve, FLUSH_WAIT_MS)
      const forward = n.port.onmessage
      n.port.onmessage = (e: MessageEvent) => {
        if (e.data === 'flushed') { clearTimeout(t); resolve(); return }
        forward?.call(n.port, e)
      }
      n.port.postMessage('flush')
    })
  }

  async function stop() {
    if (state.value === 'starting') { discard(); return }
    if (state.value !== 'recording') return
    const my = gen
    state.value = 'finishing'
    announce('Finishing')
    if (clock) clearInterval(clock)
    clock = null
    // The microphone goes first, so the browser's indicator goes out now.
    releaseMic()
    await flush()
    closeAudio()
    wake?.()
    await pump
    if (my !== gen) return

    try {
      while (pendingSamples > 0) {
        apply(await sendDictationAudio(id, take()))
        if (my !== gen) return
      }
      const text = await stopDictation(id)
      if (my !== gen) return
      id = ''
      end(text, true, null, text.startsWith(final) ? text.slice(final.length) : '')
    } catch (e) {
      if (my !== gen) return
      if (e instanceof DictationError && e.code === 'no_speech') { id = ''; end('', false, 'no_speech'); return }
      fail(e)
    }
  }

  function toggle() {
    if (state.value === 'idle') void start()
    else if (state.value !== 'finishing') void stop()
  }

  // Throws the dictation away. The composer puts back what was there before.
  function discard() {
    if (state.value === 'idle') return
    const was = id
    teardown()
    if (was) discardDictation(was)
    state.value = 'idle'
    handlers.onEnd('', false)
  }

  function clearProblem() {
    problem.value = null
  }

  // A tab that closes mid-dictation shouldn't hold the one-at-a-time slot for
  // the ten seconds the server would otherwise wait.
  function onPageHide() {
    if (id) discardDictation(id)
  }
  window.addEventListener('pagehide', onPageHide)

  if (getCurrentInstance()) {
    onBeforeUnmount(() => {
      window.removeEventListener('pagehide', onPageHide)
      discard()
    })
  }

  return { state, active, elapsed, problem, announcement, start, stop, toggle, discard, clearProblem }
}
