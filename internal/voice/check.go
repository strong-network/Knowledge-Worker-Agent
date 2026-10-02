// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package voice

import (
	"bytes"
	"compress/zlib"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Whisper marks non-speech as [BLANK_AUDIO], [Music] or (laughs).
var nonSpeech = regexp.MustCompile(`\[[^\]]{0,40}\]|\((?i)[^)]{0,40}(?:music|laugh|applause|silence|inaudible|blank)[^)]{0,40}\)`)

// Whisper also names any other sound it hears, "(sniffing)", "(people
// chattering)" or "*sigh*", as a few plain words.
var soundLabel = regexp.MustCompile(`(?:\(([\p{L}' ,-]{1,60})\)|\*([\p{L}' ,-]{1,60})\*)[.!?]?`)

const maxLabelWords = 6

// cleanText drops non-speech markers and collapses whitespace.
func cleanText(s string) string {
	s = dropSoundLabels(nonSpeech.ReplaceAllString(s, " "))
	return strings.Join(strings.Fields(s), " ")
}

// dropSoundLabels drops a label that stands as a sentence of its own, which is
// where Whisper puts a sound. One inside a sentence is kept: the user may have
// dictated it.
func dropSoundLabels(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range soundLabel.FindAllStringSubmatchIndex(s, -1) {
		before := strings.TrimRight(s[last:m[0]], " \t\n")
		after := strings.TrimLeft(s[m[1]:], " \t\n")
		words := ""
		for g := 2; g+1 < len(m); g += 2 {
			if m[g] >= 0 {
				words = s[m[g]:m[g+1]]
			}
		}
		if !sentenceEnds(before) || !sentenceStarts(after) || len(strings.Fields(words)) > maxLabelWords {
			continue
		}
		b.WriteString(s[last:m[0]])
		b.WriteByte(' ')
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

func sentenceEnds(before string) bool {
	return before == "" || strings.ContainsAny(before[len(before)-1:], ".!?")
}

func sentenceStarts(after string) bool {
	r, _ := utf8.DecodeRuneInString(after)
	return after == "" || unicode.IsUpper(r) || unicode.IsDigit(r) || r == '(' || r == '*'
}

// Speaking rates outside these bounds mean the quick pass went wrong: it
// stopped early or ran away (measured before dictation shipped).
const (
	minWordsPerSecond = 1.6
	maxWordsPerSecond = 4.5
	minRateSeconds    = 3.0
)

// suspicious reports why a quick-pass result looks broken, or "" when it looks
// fine. speech is the seconds of speech in the audio it came from.
func suspicious(text string, speech float64) string {
	words := wordPattern.FindAllString(strings.ToLower(text), -1)
	if len(words) == 0 {
		return "empty"
	}
	rate := float64(len(words)) / max(speech, 0.5)
	if rate > maxWordsPerSecond {
		return "rate"
	}
	if speech >= minRateSeconds && rate < minWordsPerSecond {
		return "short"
	}
	grams := map[string]int{}
	for i := 0; i+3 <= len(words); i++ {
		g := strings.Join(words[i:i+3], " ")
		if grams[g]++; grams[g] >= 3 {
			return "repeat"
		}
	}
	for i, run := 1, 1; i < len(words); i++ {
		if words[i] == words[i-1] {
			if run++; run >= 4 {
				return "repeat"
			}
		} else {
			run = 1
		}
	}
	if len(text) > 40 {
		var buf bytes.Buffer
		zw := zlib.NewWriter(&buf)
		_, _ = zw.Write([]byte(text))
		_ = zw.Close()
		if float64(len(text))/float64(buf.Len()) > 2.4 {
			return "repeat"
		}
	}
	return ""
}
