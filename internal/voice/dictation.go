// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package voice

import (
	"context"
	"log"
	"strings"
	"sync"
)

const (
	maxDictationSamples = 2 * 60 * sampleRate
	finalAttempts       = 2
)

// progress is what the composer shows: text that will not change, and the
// words after it that still may.
type progress struct {
	Final string `json:"final"`
	Live  string `json:"live"`
	Limit bool   `json:"limit"`
}

// dictation holds one recording in memory and turns it into text: a quick,
// checked pass for live text whenever new audio has arrived, and an accurate
// pass that makes text final at each pause.
type dictation struct {
	id    string
	vocab *Vocabulary
	eng   transcriber

	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	done   chan struct{}

	mu       sync.Mutex
	pcm      []int16
	samples  int
	finals   []string
	live     string
	stopping bool
	failed   bool
	stats    struct{ live, skipped, final int }
}

func newDictation(id string, vocab *Vocabulary, eng transcriber) *dictation {
	ctx, cancel := context.WithCancel(context.Background())
	return &dictation{id: id, vocab: vocab, eng: eng, ctx: ctx, cancel: cancel,
		wake: make(chan struct{}, 1), done: make(chan struct{})}
}

func (d *dictation) poke() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// add appends samples up to the two-minute limit.
func (d *dictation) add(samples []int16) {
	d.mu.Lock()
	if room := maxDictationSamples - len(d.pcm); len(samples) > room {
		samples = samples[:max(room, 0)]
	}
	d.pcm = append(d.pcm, samples...)
	d.samples = len(d.pcm)
	d.mu.Unlock()
	d.poke()
}

func (d *dictation) progress() (progress, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return progress{Final: strings.Join(d.finals, " "), Live: d.live, Limit: d.samples >= maxDictationSamples}, d.failed
}

func (d *dictation) isStopping() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stopping
}

// stop asks for the rest to be made final; done closes when it is.
func (d *dictation) stop() {
	d.mu.Lock()
	d.stopping = true
	d.mu.Unlock()
	d.poke()
}

// discard abandons the dictation and drops its audio.
func (d *dictation) discard() {
	d.cancel()
	d.mu.Lock()
	d.pcm, d.live = nil, ""
	d.mu.Unlock()
}

func (d *dictation) run() {
	defer close(d.done)
	var levels []float64
	boundary, seen := 0, -1
	for d.ctx.Err() == nil {
		d.mu.Lock()
		n, stopping := len(d.pcm), d.stopping
		pcm := d.pcm[:n:n]
		d.mu.Unlock()
		if n == seen && !stopping {
			select {
			case <-d.wake:
			case <-d.ctx.Done():
			}
			continue
		}
		seen = n
		levels = appendLevels(levels, pcm)
		thr := silenceThreshold(levels)
		seg := levels[boundary/frameSamples:]

		cut := findPause(seg, thr)
		if cut < 0 && len(seg) > capFrames {
			cut = quietest(seg, capFrames-capWindow, capFrames)
		}
		if cut >= 0 {
			if !d.finalPass(pcm[boundary:boundary+cut*frameSamples], speechFrames(seg[:cut], thr)) {
				return
			}
			boundary, seen = boundary+cut*frameSamples, -1
			continue
		}
		speech := speechFrames(seg, thr)
		if stopping {
			d.finalPass(pcm[boundary:], speech)
			return
		}
		if speech >= minSpeech {
			d.livePass(pcm[boundary:], float64(speech)/100)
		}
	}
}

// finalPass transcribes a stretch with the accurate full window and appends it
// to the final text. It reports false when the dictation can't go on.
func (d *dictation) finalPass(pcm []int16, speech int) bool {
	if speech < minSpeech {
		d.mu.Lock()
		d.live = ""
		d.mu.Unlock()
		return true
	}
	d.mu.Lock()
	r := request{pcm: pcm, prompt: d.vocab.Hint(d.live, strings.Join(d.finals, " "))}
	d.mu.Unlock()
	var text string
	var err error
	for attempt := 0; attempt < finalAttempts; attempt++ {
		if text, err = d.eng.Transcribe(d.ctx, r); err == nil || d.ctx.Err() != nil {
			break
		}
	}
	if d.ctx.Err() != nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.live = ""
	if err != nil {
		log.Printf("[voice] final pass failed: %v", err)
		d.failed = true
		return false
	}
	if text = d.vocab.Correct(cleanText(text)); text != "" {
		d.finals = append(d.finals, text)
	}
	d.stats.final++
	return true
}

// livePass is the quick pass: fitted window, no retries, and a result that
// looks broken is dropped so the previous live text stays. Its hint is only the
// end of the final text: vocabulary terms slow every request down, the
// corrections still apply, and the final pass that replaces this text carries
// the full hint.
func (d *dictation) livePass(pcm []int16, speechSeconds float64) {
	d.mu.Lock()
	r := request{pcm: pcm, prompt: tailWords(strings.Join(d.finals, " "), hintTailWords, hintTailBytes),
		audioCtx: fittedContext(len(pcm)), noRetry: true}
	d.mu.Unlock()
	text, err := d.eng.Transcribe(d.ctx, r)
	d.mu.Lock()
	defer d.mu.Unlock()
	if text = cleanText(text); err != nil || suspicious(text, speechSeconds) != "" {
		d.stats.skipped++
		return
	}
	d.live = d.vocab.Correct(text)
	d.stats.live++
}
