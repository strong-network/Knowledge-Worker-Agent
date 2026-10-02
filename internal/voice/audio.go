// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package voice

import (
	"encoding/binary"
	"math"
	"sort"
)

// Audio is 16 kHz mono 16-bit PCM, analysed in 10 ms frames. The numbers are
// the ones measured on the product team's recordings.
const (
	sampleRate   = 16000
	frameSamples = sampleRate / 100

	pauseFrames    = 60   // 0.6 s of silence after speech makes text final
	cutIntoPause   = 30   // cut 0.3 s into the pause
	minSpeech      = 20   // 0.2 s of speech before anything is transcribed
	capFrames      = 1200 // a stretch longer than 12 s is cut anyway,
	capWindow      = 300  // at the quietest point of its last 3 s
	quietFrames    = 20
	noiseMargin    = 10.0 // silence: up to 10 dB above the noise floor,
	speechMargin   = 15.0 // but at least 15 dB below the loud end,
	lowestCeiling  = -55.0
	earlyThreshold = -50.0
)

// appendLevels adds the level in dB of every complete frame of pcm that levels
// doesn't cover yet.
func appendLevels(levels []float64, pcm []int16) []float64 {
	for f := len(levels); (f+1)*frameSamples <= len(pcm); f++ {
		var sum float64
		for _, s := range pcm[f*frameSamples : (f+1)*frameSamples] {
			sum += float64(s) * float64(s)
		}
		rms := math.Sqrt(sum/frameSamples) / 32768
		levels = append(levels, 20*math.Log10(rms+1e-9))
	}
	return levels
}

// silenceThreshold follows the recording's own noise floor, its quietest tenth,
// so it adapts to each microphone and room. It never comes within 15 dB of the
// recording's loud end, or speech with no pause yet would count as silence;
// and that ceiling never drops below -55 dB, or a silent start would count as
// speech.
func silenceThreshold(levels []float64) float64 {
	if len(levels) < 30 {
		return earlyThreshold
	}
	sorted := append([]float64(nil), levels...)
	sort.Float64s(sorted)
	floor := sorted[(len(sorted)-1)/10]
	loud := sorted[(len(sorted)-1)*9/10]
	return min(floor+noiseMargin, max(loud-speechMargin, lowestCeiling))
}

func speechFrames(levels []float64, thr float64) int {
	n := 0
	for _, lv := range levels {
		if lv > thr {
			n++
		}
	}
	return n
}

// findPause returns the frame to cut at inside the latest complete pause that
// follows enough speech, or -1.
func findPause(levels []float64, thr float64) int {
	speech, run, cut := 0, 0, -1
	for i, lv := range levels {
		if lv > thr {
			speech++
			run = 0
			continue
		}
		run++
		if run == pauseFrames && speech >= minSpeech {
			cut = i - run + 1 + cutIntoPause
		}
	}
	return cut
}

// quietest returns the centre of the quietest quietFrames-long window whose
// start lies in [lo, hi).
func quietest(levels []float64, lo, hi int) int {
	lo = max(lo, 0)
	hi = min(hi, len(levels)-quietFrames+1)
	if hi <= lo {
		return min(len(levels), max(lo, 0))
	}
	best, bestSum := lo, math.Inf(1)
	for s := lo; s < hi; s++ {
		var sum float64
		for _, lv := range levels[s : s+quietFrames] {
			sum += lv
		}
		if sum < bestSum {
			best, bestSum = s, sum
		}
	}
	return best + quietFrames/2
}

// wav wraps pcm in a WAV header, the only format the engine reads.
func wav(pcm []int16) []byte {
	data := len(pcm) * 2
	b := make([]byte, 44+data)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+data))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1) // PCM
	binary.LittleEndian.PutUint16(b[22:], 1) // mono
	binary.LittleEndian.PutUint32(b[24:], sampleRate)
	binary.LittleEndian.PutUint32(b[28:], sampleRate*2)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(data))
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(s))
	}
	return b
}

// fittedContext sizes the engine's audio window to n samples: 50 positions per
// second plus a margin, capped at the model's 1500.
func fittedContext(n int) int {
	return min(1500, int(math.Ceil(float64(n)/sampleRate*50))+64)
}
