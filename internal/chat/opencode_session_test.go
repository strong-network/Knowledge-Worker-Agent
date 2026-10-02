// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

// fakeOpencode writes a script that records its arguments and exits with the
// given status, printing stderrText. Returns the binary path and the path the
// arguments land in.
func fakeOpencode(t *testing.T, exitCode int, stderrText string) (bin, argsPath string) {
	t.Helper()
	tmp := t.TempDir()
	bin = filepath.Join(tmp, "opencode")
	argsPath = filepath.Join(tmp, "args.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsPath + "\n"
	if stderrText != "" {
		script += "echo '" + stderrText + "' >&2\n"
	}
	script += "exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsPath
}

// captureLog redirects the standard logger and returns a restore func, so a
// test can assert on what was (and was not) reported.
func captureLog(into *strings.Builder) func() {
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(into)
	log.SetFlags(0)
	return func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}
}

// waitForFile polls until the path exists, so the test can observe work done
// on the background goroutine without sleeping for a fixed duration.
func waitForFile(t *testing.T, path string, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// The whole point of the change: an opencode session id must reach
// `opencode session delete <id>`. opencode has no retention of its own, so a
// delete that never runs means the transcript stays on disk forever.
func TestDeleteOpencodeSessionInvokesTheCLI(t *testing.T) {
	bin, argsPath := fakeOpencode(t, 0, "")

	deleteOpencodeSession(bin, "ses_abc123")

	raw, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("opencode was never invoked: %v", err)
	}
	got := strings.Fields(string(raw))
	want := []string{"session", "delete", "ses_abc123"}
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %v, want %v", got, want)
		}
	}
}

// The exported wrapper has to actually reach the binary, via config.OpencodeBin
// and its background goroutine.
func TestDeleteOpencodeSessionRunsInBackground(t *testing.T) {
	bin, argsPath := fakeOpencode(t, 0, "")
	prev := config.OpencodeBin
	config.OpencodeBin = bin
	t.Cleanup(func() { config.OpencodeBin = prev })

	DeleteOpencodeSession("ses_background")

	if !waitForFile(t, argsPath, 5*time.Second) {
		t.Fatal("expected the background delete to invoke opencode")
	}
	if got := string(mustRead(t, argsPath)); !strings.Contains(got, "ses_background") {
		t.Fatalf("wrong session deleted: %q", got)
	}
}

// Negative control 1: a chat with no opencode session — a copilot-backend chat,
// or one that never ran a turn — must not spawn anything. Deleting the empty
// string would ask opencode to delete a session named "".
func TestDeleteOpencodeSessionIgnoresEmptyID(t *testing.T) {
	bin, argsPath := fakeOpencode(t, 0, "")
	prev := config.OpencodeBin
	config.OpencodeBin = bin
	t.Cleanup(func() { config.OpencodeBin = prev })

	DeleteOpencodeSession("")
	DeleteOpencodeSession("   ")

	// Give a wrongly-spawned goroutine time to prove itself.
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(argsPath); err == nil {
		t.Fatal("opencode was invoked for an empty session id")
	}
}

// Negative control 2: with no binary configured there is nothing to run, and
// attempting it would fail noisily on every single delete.
func TestDeleteOpencodeSessionWithoutBinaryIsANoop(t *testing.T) {
	var logged strings.Builder
	restore := captureLog(&logged)
	deleteOpencodeSession("", "ses_abc123")
	restore()

	if logged.Len() != 0 {
		t.Fatalf("expected no attempt without a binary, got %q", logged.String())
	}
}

// A session opencode has already forgotten is the expected outcome of a
// retried delete. It must be swallowed, or the log trains its reader to ignore
// the line that also reports real failures.
func TestDeleteOpencodeSessionSwallowsSessionNotFound(t *testing.T) {
	bin, argsPath := fakeOpencode(t, 1, "Error: Session not found: ses_gone")

	var logged strings.Builder
	restore := captureLog(&logged)
	deleteOpencodeSession(bin, "ses_gone")
	restore()

	if !waitForFile(t, argsPath, time.Second) {
		t.Fatal("expected opencode to be invoked")
	}
	if logged.Len() != 0 {
		t.Fatalf("expected no log for a missing session, got %q", logged.String())
	}
}

// ...but a genuine failure must still be reported, or the deletion fails
// silently and the disk fills with no trace of why.
func TestDeleteOpencodeSessionLogsRealFailures(t *testing.T) {
	bin, _ := fakeOpencode(t, 1, "Error: database is locked")

	var logged strings.Builder
	restore := captureLog(&logged)
	deleteOpencodeSession(bin, "ses_locked")
	restore()

	got := logged.String()
	if !strings.Contains(got, "ses_locked") || !strings.Contains(got, "database is locked") {
		t.Fatalf("expected the failure to be reported, got %q", got)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
