// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package voice

import (
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/materializer"
)

// Vocabulary limits. A bad row is skipped, never the whole file.
const (
	maxVocabularyFile  = 256 << 10
	maxVocabularyTerms = 5000
	maxTermRunes       = 64
	maxFormsPerTerm    = 10
	minFormRunes       = 4

	// The engine keeps only the last 223 tokens of a prompt and silently drops
	// the start, so the hint is kept well inside that here.
	hintBytes     = 600
	hintTailWords = 40
	hintTailBytes = 240
)

// Vocabulary is the merged set of product terms dictation teaches the engine:
// a hint for the engine's prompt and corrections for known mishearings.
type Vocabulary struct {
	terms []vocabTerm
	// byKey maps a squashed term or Heard-as form to its term, for spotting the
	// terms a rough transcript suggests.
	byKey map[string]int
	fix   *regexp.Regexp
	fixTo map[string]string
}

type vocabTerm struct {
	text   string
	forms  []string
	pinned bool // Hint: always
}

// always reports whether the term is in every hint: it has known mishearings,
// or its only mishearings are real words that can't be corrected.
func (t vocabTerm) always() bool { return len(t.forms) > 0 || t.pinned }

// vocabularyDir is where the materializer puts assigned vocabularies. It
// is read on every call: the materializer exports the variable during bootstrap.
func vocabularyDir() string {
	cfg := strings.TrimSpace(os.Getenv(materializer.EnvConfigDir))
	if cfg == "" {
		return ""
	}
	return filepath.Join(cfg, materializer.VocabularyDir)
}

// LoadVocabulary reads every *.md in dir in name order, which the materializer
// makes assignment order. A missing dir is an empty vocabulary.
func LoadVocabulary(dir string) *Vocabulary {
	b := &vocabBuilder{index: map[string]int{}}
	if dir != "" {
		names, _ := filepath.Glob(filepath.Join(dir, "*.md"))
		sort.Strings(names)
		for _, name := range names {
			b.file(name)
		}
	}
	return b.build()
}

type vocabBuilder struct {
	terms []vocabTerm
	index map[string]int // lower-cased term -> position, to merge duplicates
	full  bool
}

var (
	quotedForm   = regexp.MustCompile(`"([^"]*)"|\x{201C}([^\x{201D}]*)\x{201D}`)
	tableDivider = regexp.MustCompile(`^:?-+:?$`)
)

func (b *vocabBuilder) file(path string) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	name := filepath.Base(path)
	if info.Size() > maxVocabularyFile {
		log.Printf("[voice] vocabulary %s: skipped, larger than %d KiB", name, maxVocabularyFile>>10)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[voice] vocabulary %s: %v", name, err)
		return
	}
	termCol, formCol, hintCol := -1, -1, -1
	for i, line := range strings.Split(string(data), "\n") {
		cells, ok := tableCells(line)
		if !ok {
			termCol, formCol, hintCol = -1, -1, -1
			continue
		}
		if col := columnOf(cells, "term"); col >= 0 {
			termCol, formCol, hintCol = col, columnOf(cells, "heard as"), columnOf(cells, "hint")
			continue
		}
		if termCol < 0 || isDivider(cells) || termCol >= len(cells) {
			continue
		}
		cell := func(col int) string {
			if col >= 0 && col < len(cells) {
				return cells[col]
			}
			return ""
		}
		if reason := b.row(cells[termCol], cell(formCol), cell(hintCol)); reason != "" {
			log.Printf("[voice] vocabulary %s:%d: skipped, %s", name, i+1, reason)
		}
	}
}

// tableCells splits a Markdown table row into trimmed cells.
func tableCells(line string) ([]string, bool) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "|") {
		return nil, false
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, "|"), "|")
	cells := strings.Split(s, "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells, true
}

func columnOf(cells []string, header string) int {
	for i, c := range cells {
		if strings.EqualFold(strings.Trim(c, "*_` "), header) {
			return i
		}
	}
	return -1
}

func isDivider(cells []string) bool {
	for _, c := range cells {
		if !tableDivider.MatchString(c) {
			return false
		}
	}
	return true
}

// row adds one term and its forms, or returns why the row was skipped.
func (b *vocabBuilder) row(rawTerm, rawForms, rawHint string) string {
	text := strings.Join(strings.Fields(strings.Trim(rawTerm, "*_` ")), " ")
	if text == "" {
		return ""
	}
	if utf8.RuneCountInString(text) > maxTermRunes {
		return "term longer than 64 characters"
	}
	if !strings.ContainsFunc(text, isAlnum) {
		return "term has no letters or digits"
	}
	pinned := false
	switch h := strings.ToLower(strings.Trim(rawHint, "*_` ")); h {
	case "":
	case "always":
		pinned = true
	default:
		return `Hint is either empty or "always"`
	}
	var forms []string
	if strings.TrimSpace(rawForms) != "" {
		matches := quotedForm.FindAllStringSubmatch(rawForms, -1)
		if len(matches) == 0 {
			return `"Heard as" needs its forms in quotes`
		}
		if len(matches) > maxFormsPerTerm {
			return "more than 10 Heard as forms"
		}
		for _, m := range matches {
			f := strings.Join(strings.Fields(m[1]+m[2]), " ")
			switch {
			case utf8.RuneCountInString(f) < minFormRunes:
				return "a Heard as form is shorter than 4 characters"
			case utf8.RuneCountInString(f) > maxTermRunes:
				return "a Heard as form is longer than 64 characters"
			case !startsAndEndsAlnum(f):
				return "a Heard as form must start and end with a letter or digit"
			}
			if !strings.EqualFold(f, text) {
				forms = append(forms, f)
			}
		}
	}
	key := strings.ToLower(text)
	if i, ok := b.index[key]; ok {
		b.terms[i].forms = append(b.terms[i].forms, forms...)
		b.terms[i].pinned = b.terms[i].pinned || pinned
		return ""
	}
	if len(b.terms) >= maxVocabularyTerms {
		if !b.full {
			b.full = true
			return "more than 5000 terms; the rest are ignored"
		}
		return ""
	}
	b.index[key] = len(b.terms)
	b.terms = append(b.terms, vocabTerm{text: text, forms: forms, pinned: pinned})
	return ""
}

func (b *vocabBuilder) build() *Vocabulary {
	v := &Vocabulary{terms: b.terms, byKey: map[string]int{}, fixTo: map[string]string{}}
	var patterns []string
	for i := range v.terms {
		t := &v.terms[i]
		// A form that is itself a term would rewrite something said on purpose.
		kept := t.forms[:0]
		for _, f := range t.forms {
			if j, ok := b.index[strings.ToLower(f)]; ok && j != i {
				log.Printf("[voice] vocabulary: Heard as %q is the term %q, not corrected", f, v.terms[j].text)
				continue
			}
			norm := strings.ToLower(f)
			if _, dup := v.fixTo[norm]; dup {
				continue
			}
			v.fixTo[norm] = t.text
			kept = append(kept, f)
			patterns = append(patterns, strings.ReplaceAll(regexp.QuoteMeta(f), " ", `\s+`))
		}
		t.forms = kept
		for _, s := range append([]string{t.text}, t.forms...) {
			if k := squash(s); len(k) >= 3 {
				if _, taken := v.byKey[k]; !taken {
					v.byKey[k] = i
				}
			}
		}
	}
	if len(patterns) > 0 {
		sort.SliceStable(patterns, func(i, j int) bool { return len(patterns[i]) > len(patterns[j]) })
		v.fix = regexp.MustCompile(`(?i)\b(?:` + strings.Join(patterns, "|") + `)\b`)
	}
	return v
}

// Len is the number of terms.
func (v *Vocabulary) Len() int { return len(v.terms) }

// Correct replaces every Heard-as form with its term: whole words, any case.
func (v *Vocabulary) Correct(text string) string {
	if v.fix == nil {
		return text
	}
	return v.fix.ReplaceAllStringFunc(text, func(m string) string {
		if t, ok := v.fixTo[strings.ToLower(strings.Join(strings.Fields(m), " "))]; ok {
			return t
		}
		return m
	})
}

// Hint builds the engine prompt: terms the context suggests, then the
// always-hinted terms, then the end of the final text. The engine drops a long
// prompt's start, so the least important part goes first and is what gets
// left out when the budget runs short.
func (v *Vocabulary) Hint(context, final string) string {
	tail := tailWords(final, hintTailWords, hintTailBytes)
	budget := hintBytes
	if tail != "" {
		budget -= len(tail) + 1
	}
	seen := map[int]bool{}
	var items []string
	for _, i := range v.suggested(context) {
		if !v.terms[i].always() && !seen[i] {
			seen[i] = true
			items = append(items, v.terms[i].text)
		}
	}
	for _, t := range v.terms {
		if t.always() {
			items = append(items, t.text)
		}
	}
	// Keep the longest run from the end that fits: "a, b, c." costs each item
	// plus ", " between items and the final ".".
	start, size := len(items), 1
	for start > 0 {
		add := len(items[start-1])
		if start < len(items) {
			add += 2
		}
		if size+add > budget {
			break
		}
		size += add
		start--
	}
	list := ""
	if start < len(items) {
		list = strings.Join(items[start:], ", ") + "."
	}
	return strings.TrimSpace(list + " " + tail)
}

var wordPattern = regexp.MustCompile(`[\p{L}\p{N}']+`)

// suggested returns the terms whose written form or Heard-as form appears in
// text, ignoring spacing and case, in order of appearance.
func (v *Vocabulary) suggested(text string) []int {
	if len(v.byKey) == 0 {
		return nil
	}
	words := wordPattern.FindAllString(text, -1)
	var out []int
	for i := range words {
		joined := ""
		for n := 0; n < 5 && i+n < len(words); n++ {
			joined += words[i+n]
			if t, ok := v.byKey[squash(joined)]; ok {
				out = append(out, t)
			}
		}
	}
	return out
}

// squash lower-cases s and keeps only letters and digits.
func squash(s string) string {
	var b strings.Builder
	for _, r := range s {
		if isAlnum(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func isAlnum(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func startsAndEndsAlnum(s string) bool {
	first, _ := utf8.DecodeRuneInString(s)
	last, _ := utf8.DecodeLastRuneInString(s)
	return isAlnum(first) && isAlnum(last)
}

// tailWords returns the last n words of s, shortened from the front to fit max bytes.
func tailWords(s string, n, max int) string {
	words := strings.Fields(s)
	if len(words) > n {
		words = words[len(words)-n:]
	}
	out := strings.Join(words, " ")
	for len(out) > max && len(words) > 1 {
		words = words[1:]
		out = strings.Join(words, " ")
	}
	if len(out) > max {
		return ""
	}
	return out
}
