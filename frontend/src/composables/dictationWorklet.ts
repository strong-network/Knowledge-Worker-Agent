// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// The dictation recorder's audio worklet. It runs on the audio thread,
// mixes the microphone down to mono, resamples it to 16 kHz and posts 16-bit
// samples to the page about every 50 ms.
//
// The resampler is a windowed-sinc low-pass evaluated at each output position,
// so sound above 8 kHz (mostly sibilants) is removed rather than folded back
// into the band the recogniser listens to. It is loaded with `?worker&url`, so
// it must not import anything.

declare const sampleRate: number
declare function registerProcessor(name: string, ctor: unknown): void
declare class AudioWorkletProcessor {
  readonly port: MessagePort
}

const OUT_RATE = 16000
// Filter half-width, in output samples. In input samples it scales with the
// rate, which keeps the transition band at about 1 kHz: flat to 7 kHz, and
// what would fold back into the speech band is at least 40 dB down.
const HALF_OUT = 16
// Kernel table resolution: entries per input sample, linearly interpolated.
const RES = 64
// Output samples per message: 50 ms.
const POST_EVERY = 800

class DictationProcessor extends AudioWorkletProcessor {
  // Input samples per output sample.
  private readonly step = sampleRate / OUT_RATE
  // Half the filter's length, in input samples.
  private readonly half = Math.ceil(HALF_OUT * Math.max(1, this.step))
  private readonly kernel: Float32Array
  // Input not yet consumed. It starts with `half` samples of silence so the
  // first output sample sits at the first input sample.
  private buf = new Float32Array(4096)
  private len = this.half
  // Position of the next output sample in `buf`, in input samples.
  private pos = this.half
  private out = new Int16Array(POST_EVERY)
  private outLen = 0

  constructor() {
    super()
    const half = this.half
    // Cycles per input sample, a little under the output's Nyquist frequency.
    const cutoff = Math.min(0.5, 0.5 / this.step) * 0.94
    this.kernel = new Float32Array(half * RES + 2)
    for (let i = 0; i < this.kernel.length; i++) {
      const x = i / RES
      const u = 2 * cutoff * x
      const sinc = u === 0 ? 1 : Math.sin(Math.PI * u) / (Math.PI * u)
      const hann = x >= half ? 0 : 0.5 + 0.5 * Math.cos((Math.PI * x) / half)
      this.kernel[i] = sinc * hann
    }
    this.port.onmessage = (e: MessageEvent) => {
      if (e.data !== 'flush') return
      // Enough silence to carry the filter past the last real sample.
      this.append(new Float32Array(half + Math.ceil(this.step)), 1)
      this.post()
      this.port.postMessage('flushed')
    }
  }

  process(inputs: Float32Array[][]): boolean {
    const channels = inputs[0]
    if (channels && channels.length > 0) this.append(channels[0], channels.length, channels)
    return true
  }

  private append(first: Float32Array, count: number, channels?: Float32Array[]) {
    const n = first.length
    if (this.len + n > this.buf.length) {
      const grown = new Float32Array(Math.max(this.buf.length * 2, this.len + n))
      grown.set(this.buf.subarray(0, this.len))
      this.buf = grown
    }
    for (let i = 0; i < n; i++) {
      let s = first[i]
      if (channels) for (let c = 1; c < count; c++) s += channels[c][i]
      this.buf[this.len + i] = s / count
    }
    this.len += n
    this.resample()
  }

  private resample() {
    const { buf, kernel, half } = this
    while (this.pos + half < this.len) {
      const t = this.pos
      const lo = Math.ceil(t - half)
      const hi = Math.floor(t + half)
      let acc = 0
      let norm = 0
      for (let i = lo; i <= hi; i++) {
        const x = Math.abs(t - i) * RES
        const j = x | 0
        const k = kernel[j] + (kernel[j + 1] - kernel[j]) * (x - j)
        acc += buf[i] * k
        norm += k
      }
      const s = Math.max(-1, Math.min(1, norm > 0 ? acc / norm : 0))
      this.out[this.outLen++] = Math.round(s < 0 ? s * 32768 : s * 32767)
      if (this.outLen === POST_EVERY) this.post()
      this.pos += this.step
    }
    // Drop what no later output sample can reach.
    const drop = Math.floor(this.pos) - half
    if (drop > 0) {
      buf.copyWithin(0, drop, this.len)
      this.len -= drop
      this.pos -= drop
    }
  }

  private post() {
    if (this.outLen === 0) return
    const chunk = this.out.slice(0, this.outLen)
    this.port.postMessage(chunk, [chunk.buffer])
    this.outLen = 0
  }
}

registerProcessor('kwa-dictation', DictationProcessor)
