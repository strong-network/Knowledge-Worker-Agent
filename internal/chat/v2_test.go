// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// A turn on opencode 2.x runs in the chat's folder with 2.x's flags. This fake
// can't serve, so the run falls back to a private server.
func TestRunStreamOpencodeV2(t *testing.T) {
	tmp := t.TempDir()
	if err := db.Init(filepath.Join(tmp, "test.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	folder := filepath.Join(tmp, "chat")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	argsPath, cwdPath, stdinPath := filepath.Join(tmp, "args.txt"), filepath.Join(tmp, "cwd.txt"), filepath.Join(tmp, "stdin.txt")
	bin := filepath.Join(tmp, "opencode")
	script := "#!/bin/sh\n" +
		"case \"$1\" in --version) echo 'opencode v2.2.5'; exit 0;; serve) echo 'cannot serve' >&2; exit 1;; esac\n" +
		"printf '%s\\n' \"$@\" > " + argsPath + "\npwd > " + cwdPath + "\ncat > " + stdinPath + "\n" +
		"printf '%s\\n' '{\"type\":\"text\",\"timestamp\":1,\"sessionID\":\"ses_v2\",\"part\":{\"type\":\"text\",\"text\":\"Hello from V2\",\"time\":{\"end\":1}}}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KWA_OPENCODE_AUTO_UPDATE", "false")
	t.Setenv("KWA_HOME", filepath.Join(tmp, "home"))
	t.Setenv("KWA_OPENCODE_INSTALL_DIR", filepath.Join(tmp, "install"))
	prevEnsured, prevBin, prevWS := opencodeEnsured, config.OpencodeBin, config.Workspace
	opencodeEnsured = false
	t.Cleanup(func() { opencodeEnsured, config.OpencodeBin, config.Workspace = prevEnsured, prevBin, prevWS })
	config.Workspace, config.OpencodeBin = tmp, bin

	cfg := config.SessionConfig{
		Backend:         config.BackendOpencode,
		Workdir:         folder,
		Model:           "github-copilot/claude-sonnet-5.5",
		ReasoningEffort: "high",
		Yolo:            true,
	}
	if err := db.CreateSession("v2-test", cfg); err != nil {
		t.Fatal(err)
	}
	events, proc := RunStream("v2-test", "hello", cfg)
	if proc == nil {
		t.Fatal("expected an active process")
	}
	var sawChunk, sawResult bool
	timeout := time.After(30 * time.Second)
	for done := false; !done; {
		select {
		case ev, ok := <-events:
			if !ok {
				done = true
				break
			}
			sawChunk = sawChunk || (ev.Kind == "chunk" && ev.Text == "Hello from V2")
			sawResult = sawResult || (ev.Kind == "result" && ev.SessionID == "ses_v2")
		case <-timeout:
			t.Fatal("timed out waiting for the fake opencode stream")
		}
	}
	if !sawChunk || !sawResult {
		t.Fatalf("sawChunk=%v sawResult=%v", sawChunk, sawResult)
	}
	raw, _ := os.ReadFile(argsPath)
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")
	got := strings.Join(args, " ")
	if got != "run --format json --auto --model github-copilot/claude-sonnet-5.5#high --standalone" {
		t.Fatalf("args = %q", got)
	}
	// The prompt, preamble and all, is on stdin as it is.
	if stdin, _ := os.ReadFile(stdinPath); !strings.HasSuffix(string(stdin), "hello") {
		t.Errorf("stdin = %q, want the prompt", stdin)
	}
	for _, a := range args {
		if a == "--dir" || a == "--variant" || a == "--dangerously-skip-permissions" {
			t.Errorf("args contain the 1.x flag %s: %q", a, got)
		}
	}
	if cwd, _ := os.ReadFile(cwdPath); strings.TrimSpace(string(cwd)) != folder {
		t.Errorf("ran in %q, want the chat's folder %q", strings.TrimSpace(string(cwd)), folder)
	}
}
