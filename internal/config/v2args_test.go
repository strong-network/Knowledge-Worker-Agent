// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"slices"
	"strings"
	"testing"
)

func TestBuildOpencodeArgsV2(t *testing.T) {
	cfg := SessionConfig{
		Model:           "github-copilot/claude-sonnet-5.5",
		ReasoningEffort: "high",
		Agent:           "reviewer",
		Workdir:         "/home/developer/chat",
		Yolo:            true,
	}
	got := strings.Join(BuildOpencodeArgsV2("-a prompt", "ses_1", cfg), " ")
	want := "run --format json --auto --model github-copilot/claude-sonnet-5.5#high --agent reviewer --session ses_1 -- -a prompt"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestBuildOpencodeArgsV2_WithoutYoloOrSession(t *testing.T) {
	args := BuildOpencodeArgsV2("hi", "", SessionConfig{Model: "github-copilot/claude-haiku-4.5", Mode: "plan"})
	for _, absent := range []string{"--auto", "--session", "--dir", "--variant", "--dangerously-skip-permissions"} {
		if slices.Contains(args, absent) {
			t.Errorf("args contain %s: %q", absent, args)
		}
	}
	if got := strings.Join(args, " "); got != "run --format json --model github-copilot/claude-haiku-4.5 --agent plan -- hi" {
		t.Errorf("got %q", got)
	}
}

func TestWithForkOnV2Args(t *testing.T) {
	got := strings.Join(WithFork(BuildOpencodeArgsV2("hi", "ses_1", SessionConfig{Model: "github-copilot/claude-haiku-4.5"})), " ")
	if got != "run --format json --model github-copilot/claude-haiku-4.5 --session ses_1 --fork -- hi" {
		t.Fatalf("got %q", got)
	}
}
