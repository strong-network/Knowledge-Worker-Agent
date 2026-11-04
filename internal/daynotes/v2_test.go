// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package daynotes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodev2"
)

// On opencode 2.x a note runs as KWA's own agent: 2.x's title agent only
// writes titles.
func TestExecNote_V2UsesKWAsAgent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KWA_HOME", filepath.Join(dir, "home"))
	argsPath, stdinPath := filepath.Join(dir, "args"), filepath.Join(dir, "stdin")
	bin := filepath.Join(dir, "opencode")
	script := "#!/bin/sh\ncase \"$1\" in --version) echo 'opencode v2.2.5'; exit 0;; serve) exit 1;; esac\nprintf '%s\\n' \"$@\" > " + argsPath + "\ncat > " + stdinPath + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := config.OpencodeBin
	config.OpencodeBin = bin
	t.Cleanup(func() { config.OpencodeBin = prev })
	opencodev2.Detect(bin)

	if _, err := execNote(context.Background(), bin, "github-copilot/claude-haiku-4.5", "-Summarise this"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(argsPath)
	got := strings.Join(strings.Fields(string(raw)), " ")
	want := "run --agent " + opencodev2.NoteAgent + " --model github-copilot/claude-haiku-4.5 --format json --standalone"
	if got != want {
		t.Fatalf("args = %q\nwant   %q", got, want)
	}
	if stdin, _ := os.ReadFile(stdinPath); string(stdin) != "-Summarise this" {
		t.Errorf("stdin = %q, want the prompt", stdin)
	}
}
