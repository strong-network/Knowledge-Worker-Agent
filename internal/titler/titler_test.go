// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package titler

import (
	"context"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env/envtest"
)

// stubRun replaces the real opencode invocation for the duration of a test.
func stubRun(t *testing.T, out string, err error) *struct {
	Bin, Model, Prompt string
	Calls              int
} {
	t.Helper()
	rec := &struct {
		Bin, Model, Prompt string
		Calls              int
	}{}
	prev := runTitle
	runTitle = func(_ context.Context, bin, model, prompt string) ([]byte, error) {
		rec.Bin, rec.Model, rec.Prompt = bin, model, prompt
		rec.Calls++
		return []byte(out), err
	}
	t.Cleanup(func() { runTitle = prev })
	return rec
}

// A normal run: opencode emits a text part carrying the title.
func TestGenerate_ReturnsTitleFromTextEvent(t *testing.T) {
	stubRun(t, `{"type":"step_start","sessionID":"ses_1","part":{}}
{"type":"text","sessionID":"ses_1","part":{"text":"JWT 401 after signing key rotation"}}
{"type":"step_finish","sessionID":"ses_1","part":{"cost":0.0008}}
`, nil)

	got, err := Generate(context.Background(), "opencode", "m", "why do we 401 after rotating keys")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "JWT 401 after signing key rotation" {
		t.Fatalf("got %q", got)
	}
}

// The whole point of --agent title is that it is cheap and tool-less. When the
// agent does not resolve, opencode warns and silently runs the DEFAULT agent
// instead, so its output must never be accepted as a title.
func TestGenerate_RejectsAgentFallback(t *testing.T) {
	stubRun(t, `!  agent "title" not found. Falling back to default agent
{"type":"text","sessionID":"ses_1","part":{"text":"I have read your files and here is what I found"}}
`, nil)

	if _, err := Generate(context.Background(), "opencode", "m", "hello"); err == nil {
		t.Fatal("expected the fallback to be rejected")
	}
}

// A failed run (e.g. an unavailable model) exits 0 and reports only an error
// event, so the exit code cannot be used to detect it.
func TestGenerate_RejectsErrorEvent(t *testing.T) {
	stubRun(t, `{"type":"error","sessionID":"ses_1","error":{"name":"UnknownError","data":{"message":"Unexpected server error."}}}
`, nil)

	_, err := Generate(context.Background(), "opencode", "m", "hello")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "Unexpected server error") {
		t.Fatalf("error should carry the reported cause, got %v", err)
	}
}

func TestGenerate_RejectsEmptyOutput(t *testing.T) {
	stubRun(t, "", nil)
	if _, err := Generate(context.Background(), "opencode", "m", "hello"); err == nil {
		t.Fatal("expected an error when no title was produced")
	}
}

// Titling cannot run without a resolved model: opencode has no built-in
// default provider, so an empty model would leave the run with no provider.
func TestGenerate_RequiresModelAndPrompt(t *testing.T) {
	rec := stubRun(t, `{"type":"text","part":{"text":"x"}}`, nil)

	if _, err := Generate(context.Background(), "opencode", "", "hello"); err == nil {
		t.Fatal("expected an error with no model")
	}
	if _, err := Generate(context.Background(), "opencode", "m", "   "); err == nil {
		t.Fatal("expected an error with a blank prompt")
	}
	if rec.Calls != 0 {
		t.Fatalf("opencode should not be invoked, got %d calls", rec.Calls)
	}
}

// A large first message must not turn into a large bill.
func TestGenerate_CapsPromptLength(t *testing.T) {
	rec := stubRun(t, `{"type":"text","part":{"text":"Big paste"}}`, nil)

	if _, err := Generate(context.Background(), "opencode", "m", strings.Repeat("x", 50000)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.Prompt) != maxPromptChars {
		t.Fatalf("prompt should be capped to %d, got %d", maxPromptChars, len(rec.Prompt))
	}
}

func TestCleanTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Simple title", "Simple title"},
		{"  padded  ", "padded"},
		{"\"Quoted title\"", "Quoted title"},
		{"- Bulleted title", "Bulleted title"},
		{"<think>hmm, what to call this</think>\nActual title", "Actual title"},
		{"\n\nFirst line\nSecond line", "First line"},
		{"", ""},
		{"   \n  ", ""},
		// Non-ASCII must not be cut mid-rune.
		{"Résoudre erreur 502 proxy", "Résoudre erreur 502 proxy"},
	}
	for _, c := range cases {
		if got := cleanTitle(c.in); got != c.want {
			t.Errorf("cleanTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCleanTitle_TruncatesRunesSafely(t *testing.T) {
	got := cleanTitle(strings.Repeat("é", 200))
	if n := len([]rune(got)); n != maxTitleChars {
		t.Fatalf("expected %d runes, got %d", maxTitleChars, n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected an ellipsis, got %q", got)
	}
}

func TestEnabled_DefaultsOnAndRespectsOptOut(t *testing.T) {
	envtest.Clear(t, "KWA_AUTO_TITLE")
	if !Enabled() {
		t.Fatal("auto-titling should be on by default")
	}
	for _, off := range []string{"0", "false", "no", "off", "OFF"} {
		t.Setenv("KWA_AUTO_TITLE", off)
		if Enabled() {
			t.Fatalf("KWA_AUTO_TITLE=%q should disable titling", off)
		}
	}
	t.Setenv("KWA_AUTO_TITLE", "1")
	if !Enabled() {
		t.Fatal("KWA_AUTO_TITLE=1 should enable titling")
	}
}

// The session id is needed so the throwaway opencode session can be cleaned up.
func TestParseTitleOutput_ExtractsSessionID(t *testing.T) {
	_, sid, _ := parseTitleOutput([]byte(`{"type":"text","sessionID":"ses_abc","part":{"text":"t"}}`))
	if sid != "ses_abc" {
		t.Fatalf("got %q", sid)
	}
}

// opencode interleaves human-readable lines with JSONL; they must not break
// parsing of an otherwise good run.
func TestParseTitleOutput_IgnoresNonJSONLines(t *testing.T) {
	title, _, runErr := parseTitleOutput([]byte(`some banner text
{"type":"text","part":{"text":"Real title"}}
trailing noise`))
	if title != "Real title" || runErr != "" {
		t.Fatalf("title=%q runErr=%q", title, runErr)
	}
}
