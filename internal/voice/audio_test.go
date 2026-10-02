// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package voice

import (
	"encoding/binary"
	"math"
	"math/rand"
	"strings"
	"testing"
)

// synth builds audio from (seconds, speaking) pieces: a tone near -15 dB for
// speech, faint noise near -70 dB for silence, like the recordings the thresholds were measured on.
func synth(pieces ...float64) []int16 {
	rng := rand.New(rand.NewSource(25))
	var pcm []int16
	speaking := true
	for _, secs := range pieces {
		n := int(secs * sampleRate)
		for i := 0; i < n; i++ {
			if speaking {
				pcm = append(pcm, int16(8000*math.Sin(2*math.Pi*220*float64(len(pcm))/sampleRate)))
			} else {
				pcm = append(pcm, int16(rng.NormFloat64()*10))
			}
		}
		speaking = !speaking
	}
	return pcm
}

func levelsOf(pcm []int16) []float64 { return appendLevels(nil, pcm) }

func TestLevelsAreIncremental(t *testing.T) {
	pcm := synth(1, 0.5, 0.33)
	whole := levelsOf(pcm)
	var parts []float64
	for n := 0; n <= len(pcm); n += 997 {
		parts = appendLevels(parts, pcm[:n])
	}
	parts = appendLevels(parts, pcm)
	if len(whole) != len(pcm)/frameSamples || len(parts) != len(whole) {
		t.Fatalf("frames: whole %d, parts %d, want %d", len(whole), len(parts), len(pcm)/frameSamples)
	}
	for i := range whole {
		if whole[i] != parts[i] {
			t.Fatalf("frame %d differs", i)
		}
	}
	if whole[0] < -20 || whole[0] > -10 || whole[len(whole)-40] > -60 {
		t.Fatalf("levels off: speech %.1f dB, silence %.1f dB", whole[0], whole[len(whole)-40])
	}
}

func TestSilenceThresholdFollowsTheNoiseFloor(t *testing.T) {
	if got := silenceThreshold(levelsOf(synth(0.2))); got != earlyThreshold {
		t.Fatalf("with 0.2 s the threshold is %.1f, want %.1f", got, earlyThreshold)
	}
	thr := silenceThreshold(levelsOf(synth(2, 1, 2)))
	if thr < -70 || thr > -50 {
		t.Fatalf("threshold %.1f dB, want about 10 dB above a -70 dB floor", thr)
	}
	loud := synth(2, 1, 2)
	rng := rand.New(rand.NewSource(7))
	for i := range loud {
		loud[i] = int16(max(-32768, min(32767, int(loud[i])+int(rng.NormFloat64()*300))))
	}
	if noisy := silenceThreshold(levelsOf(loud)); noisy < thr+20 {
		t.Fatalf("a noisier room should raise the threshold: %.1f vs %.1f", noisy, thr)
	}
}

// Someone who starts talking at once has no quiet tenth yet; a silent start
// has no loud end. Neither may flip speech and silence.
func TestSilenceThresholdWithoutQuietOrLoudParts(t *testing.T) {
	speech := levelsOf(synth(5))
	if n := speechFrames(speech, silenceThreshold(speech)); n != len(speech) {
		t.Fatalf("continuous speech: %d of %d frames count as speech", n, len(speech))
	}
	quiet := levelsOf(synth(0, 5))
	if n := speechFrames(quiet, silenceThreshold(quiet)); n != 0 {
		t.Fatalf("silence: %d frames count as speech", n)
	}
}

func TestFindPause(t *testing.T) {
	cases := []struct {
		name   string
		pcm    []int16
		wantAt int
	}{
		{"speech, 0.7 s pause, speech", synth(1, 0.7, 1), 100 + cutIntoPause},
		{"the latest of two pauses", synth(1, 0.7, 1, 0.8, 0.5), 270 + cutIntoPause},
		{"a 0.5 s pause is not one", synth(1, 0.5, 1), -1},
		{"too little speech before the pause", synth(0.1, 1, 0.1), -1},
		{"leading silence is not a pause", synth(0, 1, 1), -1},
	}
	for _, c := range cases {
		lv := levelsOf(c.pcm)
		got := findPause(lv, silenceThreshold(lv))
		if got < c.wantAt-2 || got > c.wantAt+2 || (c.wantAt == -1 && got != -1) {
			t.Errorf("%s: cut at %d, want %d", c.name, got, c.wantAt)
		}
	}
}

func TestQuietestFindsTheDip(t *testing.T) {
	pcm := synth(10.2, 0.3, 2.5)
	lv := levelsOf(pcm)
	got := quietest(lv, capFrames-capWindow, capFrames)
	if got < 1020 || got > 1050 {
		t.Fatalf("quietest at frame %d, want inside the dip at 1020-1050", got)
	}
	if got := quietest(lv[:5], 0, 100); got != 0 {
		t.Fatalf("too short: %d", got)
	}
}

func TestWAV(t *testing.T) {
	pcm := []int16{0, 1, -1, 32767, -32768}
	b := wav(pcm)
	if len(b) != 44+10 || string(b[0:4]) != "RIFF" || string(b[8:16]) != "WAVEfmt " || string(b[36:40]) != "data" {
		t.Fatalf("bad header: % x", b[:44])
	}
	if binary.LittleEndian.Uint32(b[24:]) != sampleRate || binary.LittleEndian.Uint16(b[22:]) != 1 || binary.LittleEndian.Uint16(b[34:]) != 16 {
		t.Fatal("not 16 kHz mono 16-bit")
	}
	if int16(binary.LittleEndian.Uint16(b[44+8:])) != -32768 || binary.LittleEndian.Uint32(b[40:]) != 10 {
		t.Fatal("samples not written")
	}
}

func TestFittedContext(t *testing.T) {
	for secs, want := range map[float64]int{2: 164, 10: 564, 29: 1514 - 14, 40: 1500} {
		if got := fittedContext(int(secs * sampleRate)); got != want {
			t.Errorf("%.0f s: %d, want %d", secs, got, want)
		}
	}
}

func TestSuspicious(t *testing.T) {
	for _, c := range []struct {
		text   string
		speech float64
		want   string
	}{
		{"", 2, "empty"},
		{"  ...  ", 2, "empty"},
		{"Draft a battlecard for NetScaler versus F5.", 3.5, ""},
		{"it was the beauty of it it was the beauty of it it was the beauty of it", 2, "rate"},
		{"it was the beauty of it, it was the beauty of it, it was the beauty of it", 6, "repeat"},
		{"l m m m m m m m m", 4, "repeat"},
		{"in actual fact there are", 9, "short"},
		{"yes", 0.4, ""},
		{strings.Repeat("going toward the benches ", 12), 30, "repeat"},
	} {
		if got := suspicious(c.text, c.speech); got != c.want {
			t.Errorf("suspicious(%q, %.1f) = %q, want %q", c.text, c.speech, got, c.want)
		}
	}
}

func TestCleanText(t *testing.T) {
	for in, want := range map[string]string{
		" Flag anything you can't substantiate. [BLANK_AUDIO]": "Flag anything you can't substantiate.",
		"(music) Hello  there\n(Laughs)":                       "Hello there",
		"keep (the brackets I said) please":                    "keep (the brackets I said) please",
		// Sounds Whisper names, standing as sentences of their own.
		"grouped into executive themes. (buzzer)": "grouped into executive themes.",
		"(sniffing)":                                       "",
		" (people chattering)":                             "",
		"(Sniffing) Draft the brief.":                      "Draft the brief.",
		"Send it. (sniffs). Then check.":                   "Send it. Then check.",
		"Done! (keyboard clicking) (sniffing)":             "Done!",
		"Is it ready? (door closes) Yes.":                  "Is it ready? Yes.",
		"Done. (sniffing) 5 items are left.":               "Done. 5 items are left.",
		"Draft the Battlecard for NetScaler vs F5. *sigh*": "Draft the Battlecard for NetScaler vs F5.",
		"Done. *sigh* (sniffing)":                          "Done.",
		"Done. (sniffing) *sigh*":                          "Done.",
		"what changed this week. (click, click)":           "what changed this week.",
		// Parentheses the user may have dictated.
		"Use NetScaler (ADC).":                      "Use NetScaler (ADC).",
		"It is a *very* good start.":                "It is a *very* good start.",
		"Check this. (See the appendix, page 4.)":   "Check this. (See the appendix, page 4.)",
		"Done. (see the notes) for details":         "Done. (see the notes) for details",
		"Done. (one two three four five six seven)": "Done. (one two three four five six seven)",
	} {
		if got := cleanText(in); got != want {
			t.Errorf("cleanText(%q) = %q, want %q", in, got, want)
		}
	}
}
