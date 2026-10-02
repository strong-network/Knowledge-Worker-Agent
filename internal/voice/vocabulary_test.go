// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package voice

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeVocab(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(flags) })
	return &buf
}

const sampleVocab = `# Citrix vocabulary

Free text is ignored, and so are tables without a Term column:

| Written | Likely mis-transcription |
|---|---|
| Ignored | "ignored form" |

## Products

| Notes | Heard as | Term |
|:---|---|---:|
| one word | "net scaler", "net skyler" | NetScaler |
| | "secure spaces", "SecurSpace's" | **SecurSpaces** |
| "SIT-riks" | | Citrix |
| | | Kubernetes |
`

func TestVocabularyReadsTermColumnsWhereverTheyAre(t *testing.T) {
	dir := t.TempDir()
	writeVocab(t, dir, "001-citrix.md", sampleVocab)
	v := LoadVocabulary(dir)
	var got []string
	for _, term := range v.terms {
		got = append(got, fmt.Sprintf("%s%v", term.text, term.forms))
	}
	want := []string{"NetScaler[net scaler net skyler]", "SecurSpaces[secure spaces SecurSpace's]", "Citrix[]", "Kubernetes[]"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("terms = %v, want %v", got, want)
	}
}

func TestVocabularySkipsBadRowsAndKeepsTheRest(t *testing.T) {
	logs := captureLog(t)
	dir := t.TempDir()
	long := strings.Repeat("x", 65)
	eleven := strings.Repeat(`"abcd", `, 11)
	writeVocab(t, dir, "001-team.md", "| Term | Heard as |\n|---|---|\n"+
		"| "+long+" | |\n"+ // line 3
		"| Okta | octa |\n"+ // line 4: unquoted
		"| HDX | \"HTX\" |\n"+ // line 5: too short
		"| SASE | "+eleven+" |\n"+ // line 6: too many
		"| CISO | \"-seesaw\" |\n"+ // line 7: starts with a dash
		"| --- |  |\n"+ // line 8: no letters
		"| Jira | \"gyra\" |\n") // line 9: fine
	v := LoadVocabulary(dir)
	if v.Len() != 1 || v.terms[0].text != "Jira" {
		t.Fatalf("terms = %+v, want only Jira", v.terms)
	}
	for _, want := range []string{
		"001-team.md:3: skipped, term longer than 64",
		"001-team.md:4: skipped, \"Heard as\" needs its forms in quotes",
		"001-team.md:5: skipped, a Heard as form is shorter than 4",
		"001-team.md:6: skipped, more than 10",
		"001-team.md:7: skipped, a Heard as form must start and end",
		"001-team.md:8: skipped, term has no letters",
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logs)
		}
	}
}

func TestVocabularyLimits(t *testing.T) {
	logs := captureLog(t)
	dir := t.TempDir()
	writeVocab(t, dir, "001-huge.md", "| Term |\n|---|\n| Huge |\n"+strings.Repeat("x", maxVocabularyFile))
	var rows strings.Builder
	rows.WriteString("| Term |\n|---|\n")
	for i := 0; i < maxVocabularyTerms+5; i++ {
		fmt.Fprintf(&rows, "| Term%d |\n", i)
	}
	writeVocab(t, dir, "002-many.md", rows.String())
	v := LoadVocabulary(dir)
	if v.Len() != maxVocabularyTerms {
		t.Fatalf("terms = %d, want the %d cap", v.Len(), maxVocabularyTerms)
	}
	if v.terms[0].text != "Term0" {
		t.Fatalf("the oversized file was read: first term %q", v.terms[0].text)
	}
	if !strings.Contains(logs.String(), "001-huge.md: skipped, larger than 256 KiB") ||
		strings.Count(logs.String(), "more than 5000 terms") != 1 {
		t.Fatalf("limits not logged once each:\n%s", logs)
	}
}

func TestVocabularyMergesFilesInNameOrderAndDuplicates(t *testing.T) {
	dir := t.TempDir()
	writeVocab(t, dir, "002-team.md", "| Term | Heard as |\n|---|---|\n| Jira | \"jeera\" |\n| Team Tool | |\n")
	writeVocab(t, dir, "001-company.md", "| Term | Heard as |\n|---|---|\n| jira | \"gyra\" |\n")
	writeVocab(t, dir, "notes.txt", "| Term |\n|---|\n| Ignored |\n")
	v := LoadVocabulary(dir)
	if v.Len() != 2 || v.terms[0].text != "jira" || v.terms[1].text != "Team Tool" {
		t.Fatalf("terms = %+v", v.terms)
	}
	if got := v.Correct("move it to gyra and jeera"); got != "move it to jira and jira" {
		t.Fatalf("merged forms not both corrected: %q", got)
	}
}

func TestVocabularyMissingDirIsEmpty(t *testing.T) {
	for _, dir := range []string{"", filepath.Join(t.TempDir(), "absent")} {
		v := LoadVocabulary(dir)
		if v.Len() != 0 || v.Correct("secure spaces") != "secure spaces" {
			t.Fatalf("dir %q: not empty", dir)
		}
		if got := v.Hint("", "the end of the final text"); got != "the end of the final text" {
			t.Fatalf("hint without vocabulary = %q", got)
		}
	}
}

func TestCorrectWholeWordsAnyCaseAnySpacing(t *testing.T) {
	dir := t.TempDir()
	writeVocab(t, dir, "001.md", sampleVocab)
	v := LoadVocabulary(dir)
	for in, want := range map[string]string{
		"Check every Secure Spaces claim":   "Check every SecurSpaces claim",
		"the SecurSpace's roadmap":          "the SecurSpaces roadmap",
		"secure   spaces and NET SCALER":    "SecurSpaces and NetScaler",
		"a secure spacesuit, a net scalers": "a secure spacesuit, a net scalers",
		"unsecure spaces stay":              "unsecure spaces stay",
		"nothing to fix here":               "nothing to fix here",
	} {
		if got := v.Correct(in); got != want {
			t.Errorf("Correct(%q) = %q, want %q", in, got, want)
		}
	}
}

// A form that is also a term would rewrite words said on purpose.
func TestCorrectNeverRewritesAnotherTerm(t *testing.T) {
	logs := captureLog(t)
	dir := t.TempDir()
	writeVocab(t, dir, "001.md", "| Term | Heard as |\n|---|---|\n| StoreFront | \"Store Front\" |\n| Store Front | |\n")
	v := LoadVocabulary(dir)
	if got := v.Correct("the Store Front team"); got != "the Store Front team" {
		t.Fatalf("rewrote a term: %q", got)
	}
	if !strings.Contains(logs.String(), `Heard as "Store Front" is the term`) {
		t.Fatalf("not logged:\n%s", logs)
	}
}

func TestHintOrderAndSuggestions(t *testing.T) {
	dir := t.TempDir()
	writeVocab(t, dir, "001.md", sampleVocab)
	v := LoadVocabulary(dir)
	got := v.Hint("we run kubernetes and net scaler, not KUBE", "Draft a battlecard for the team.")
	want := "Kubernetes, NetScaler, SecurSpaces. Draft a battlecard for the team."
	if got != want {
		t.Fatalf("Hint = %q\nwant   %q", got, want)
	}
	// Written form, spacing and case ignored; a term only suggested once.
	if got := v.Hint("Kuber Netes then kubernetes", ""); got != "Kubernetes, NetScaler, SecurSpaces." {
		t.Fatalf("Hint = %q", got)
	}
}

// Hint: always keeps a term whose only mishearings are real words ("medic")
// in every hint, without correcting anything.
func TestHintAlwaysColumn(t *testing.T) {
	logs := captureLog(t)
	dir := t.TempDir()
	writeVocab(t, dir, "001.md", "| Term | Heard as | Hint |\n|---|---|---|\n"+
		"| MEDDIC | | always |\n| NetScaler | \"net scaler\" | |\n| Okta | | sometimes |\n| Kubernetes | | |\n")
	v := LoadVocabulary(dir)
	if got := v.Hint("", ""); got != "MEDDIC, NetScaler." {
		t.Fatalf("Hint = %q, want the pinned and the misheard term", got)
	}
	if got := v.Correct("a medic and a net scaler"); got != "a medic and a NetScaler" {
		t.Fatalf("Correct = %q", got)
	}
	if v.Len() != 3 || !strings.Contains(logs.String(), `001.md:5: skipped, Hint is either empty or "always"`) {
		t.Fatalf("terms %d; log:\n%s", v.Len(), logs)
	}
}

func TestHintStaysInBudgetDroppingTheFrontFirst(t *testing.T) {
	dir := t.TempDir()
	var rows strings.Builder
	rows.WriteString("| Term | Heard as |\n|---|---|\n")
	var context []string
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&rows, "| Context%02d |  |\n", i)
		context = append(context, fmt.Sprintf("context%02d", i))
	}
	rows.WriteString("| SecurSpaces | \"secure spaces\" |\n| NetScaler | \"net scaler\" |\n")
	writeVocab(t, dir, "001.md", rows.String())
	v := LoadVocabulary(dir)
	final := strings.Repeat("word ", 100)
	hint := v.Hint(strings.Join(context, " "), final)
	if len(hint) > hintBytes {
		t.Fatalf("hint is %d bytes, over %d", len(hint), hintBytes)
	}
	if !strings.Contains(hint, "SecurSpaces, NetScaler.") || strings.Contains(hint, "Context00") || !strings.Contains(hint, "Context79") {
		t.Fatalf("front not dropped first: %q", hint)
	}
	if !strings.HasSuffix(hint, strings.TrimSpace(strings.Repeat("word ", hintTailWords))) {
		t.Fatalf("final tail missing or not last: %q", hint)
	}
}
