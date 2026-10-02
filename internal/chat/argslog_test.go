// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

func TestArgsForLogLeavesThePromptOut(t *testing.T) {
	const prompt = "-- summarise the board minutes for Alex Example"
	args := config.BuildOpencodeArgs(prompt, "ses_1", config.SessionConfig{Model: "github-copilot/claude-haiku-4.5"})
	got := argsForLog(args)
	if strings.Contains(got, "board minutes") {
		t.Fatalf("the prompt reached the log: %s", got)
	}
	if want := "-- <prompt: 47 bytes>"; !strings.HasSuffix(got, want) {
		t.Errorf("got %q, want it to end with %q", got, want)
	}
	if !strings.Contains(got, "--session ses_1") {
		t.Errorf("the other arguments are missing: %s", got)
	}
	if args[len(args)-1] != prompt {
		t.Error("argsForLog changed the arguments it was given")
	}
	if got := argsForLog([]string{"run", "--format", "json"}); got != "run --format json" {
		t.Errorf("no prompt: %q", got)
	}
}
