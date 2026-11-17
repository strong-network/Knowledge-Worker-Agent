// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseModels_RealOutput(t *testing.T) {
	// Captured verbatim from `opencode models github-copilot` (opencode 1.17.10).
	out := `github-copilot/claude-haiku-4.5
github-copilot/claude-opus-4.5
github-copilot/claude-opus-4.6
github-copilot/claude-opus-4.6-fast
github-copilot/claude-opus-4.7
github-copilot/claude-opus-4.7-fast
github-copilot/claude-opus-4.8
github-copilot/claude-opus-4.8-fast
github-copilot/claude-sonnet-4.5
github-copilot/claude-sonnet-4.6
github-copilot/gemini-2.5-pro
github-copilot/gemini-3.5-flash
github-copilot/gpt-5-mini
github-copilot/gpt-5.3-codex
github-copilot/gpt-5.4
github-copilot/gpt-5.4-mini
github-copilot/gpt-5.5
`
	models := parseModels(out)
	if len(models) != 17 {
		t.Fatalf("expected 17 models, got %d: %v", len(models), models)
	}
	want := "github-copilot/claude-opus-4.8"
	found := false
	for _, m := range models {
		if m == want {
			found = true
		}
		if !contains(m, "/") {
			t.Errorf("model %q is not a provider/model identifier", m)
		}
	}
	if !found {
		t.Errorf("expected %q in models, got %v", want, models)
	}
	// Output must be sorted.
	for i := 1; i < len(models); i++ {
		if models[i-1] > models[i] {
			t.Errorf("models not sorted at %d: %q > %q", i, models[i-1], models[i])
		}
	}
}

func TestParseModels_FiltersNoise(t *testing.T) {
	out := "\x1b[0m\n" +
		"github-copilot/gpt-5.4\n" +
		"\n" +
		"some log line without a slash\n" +
		"github-copilot/gpt-5.4\n" + // duplicate
		"a line with / a space\n" +
		"trailing/\n" + // trailing slash, invalid
		"/leading\n" + // leading slash, invalid
		"github-copilot/claude-opus-4.8\n"
	models := parseModels(out)
	if len(models) != 2 {
		t.Fatalf("expected 2 models after filtering, got %d: %v", len(models), models)
	}
	if models[0] != "github-copilot/claude-opus-4.8" || models[1] != "github-copilot/gpt-5.4" {
		t.Errorf("unexpected models: %v", models)
	}
}

func TestParseModels_Empty(t *testing.T) {
	if got := parseModels(""); got == nil || len(got) != 0 {
		t.Fatalf("expected empty non-nil slice, got %#v", got)
	}
}

func TestSnapshotLoginDefaults(t *testing.T) {
	login.mu.Lock()
	resetLoginLocked()
	login.mu.Unlock()
	info := snapshotLogin()
	if info.Running || info.Done || info.Success {
		t.Errorf("fresh login state should be all-false, got %+v", info)
	}
	if info.StartedAt != "" {
		t.Errorf("expected empty StartedAt, got %q", info.StartedAt)
	}
}

// CheckStatus asks opencode's credential store, without running opencode.
func TestCheckStatusAndHandler(t *testing.T) {
	s := stubCredentials(t)
	if CheckStatus().Authenticated {
		t.Fatal("authenticated with no credential")
	}
	if err := storeCopilotToken("gho_x"); err != nil {
		t.Fatal(err)
	}
	if v := s.values[Provider]; v["type"] != "oauth" || v["access"] != "gho_x" || v["methodID"] != "device" {
		t.Errorf("stored %v", v)
	}
	st := CheckStatus()
	if !st.Authenticated || st.Provider != Provider {
		t.Fatalf("CheckStatus = %+v", st)
	}
	rec := httptest.NewRecorder()
	HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/api/opencode/auth/status", nil))
	var got Status
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || !got.Authenticated {
		t.Fatalf("handler: %d %s", rec.Code, rec.Body.String())
	}
	if err := SignOut(); err != nil || CheckStatus().Authenticated {
		t.Fatalf("still signed in after SignOut: %v", err)
	}
}

// TestFetchModelsFor_TimeoutNamesDeadline verifies a provider that exceeds
// modelsTimeout reports the deadline rather than the bare "signal: killed" that
// exec returns when it kills the child. The unhelpful message was what made a
// ~60s cold-start stall impossible to attribute from a boot log.
//
// The stub uses `exec sleep` so the killed pid is the sleep itself. Without it
// the shell forks a grandchild that inherits the stdout/stderr pipe, and
// CombinedOutput blocks until *that* exits — which is not a test artefact but
// the real behaviour: modelsTimeout bounds when the child is signalled, not how
// long this call can take. Measured: a 200ms deadline against a script forking
// `sleep 5` returns after 5s. That is why the caller times the call rather than
// assuming the deadline caps it.
func TestFetchModelsFor_TimeoutNamesDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub not supported on windows")
	}
	bin := writeFakeOpencode(t, `#!/bin/sh
echo "fetching models.dev catalogue"
exec sleep 30
`)
	oldBin, oldTimeout := OpencodeBin, modelsTimeout
	OpencodeBin, modelsTimeout = bin, 150*time.Millisecond
	defer func() { OpencodeBin, modelsTimeout = oldBin, oldTimeout }()

	got, err := FetchModelsFor("github-copilot")
	if err == nil {
		t.Fatal("expected an error when the child exceeds modelsTimeout")
	}
	if got == nil {
		t.Error("expected a non-nil empty slice so callers keep their cached list")
	}
	if !contains(err.Error(), "timed out") {
		t.Errorf("error should name the timeout, got %q", err)
	}
	// The child's own output is the only clue to what it was stuck on.
	if !contains(err.Error(), "models.dev") {
		t.Errorf("error should carry the subprocess output, got %q", err)
	}
}

// TestFetchModelsFor_FailureCarriesOutput verifies a non-timeout failure keeps
// the exit error and appends what the process printed.
func TestFetchModelsFor_FailureCarriesOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub not supported on windows")
	}
	bin := writeFakeOpencode(t, `#!/bin/sh
echo "provider not configured" >&2
exit 3
`)
	old := OpencodeBin
	OpencodeBin = bin
	defer func() { OpencodeBin = old }()

	_, err := FetchModelsFor("github-copilot")
	if err == nil {
		t.Fatal("expected an error on non-zero exit")
	}
	if !contains(err.Error(), "provider not configured") {
		t.Errorf("error should carry the subprocess output, got %q", err)
	}
	if contains(err.Error(), "timed out") {
		t.Errorf("a plain non-zero exit must not be reported as a timeout, got %q", err)
	}
}

// TestLastMeaningfulLine covers the detail extraction: ANSI stripped, trailing
// blank lines skipped, and long output truncated so a runaway child cannot dump
// an unbounded blob into the boot log.
func TestLastMeaningfulLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"whitespace only", "\n\n   \n", ""},
		{"skips trailing blanks", "first\nlast line\n\n\n", "last line"},
		{"strips ansi", "\x1b[31mred error\x1b[0m\n", "red error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lastMeaningfulLine(tc.in); got != tc.want {
				t.Errorf("lastMeaningfulLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	long := strings.Repeat("x", maxDetailLen+50)
	got := lastMeaningfulLine(long)
	if len([]rune(got)) != maxDetailLen+1 { // +1 for the ellipsis
		t.Errorf("expected truncation to %d runes plus an ellipsis, got %d", maxDetailLen, len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated detail should end with an ellipsis, got %q", got)
	}
}

func writeFakeOpencode(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "opencode")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	return bin
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
