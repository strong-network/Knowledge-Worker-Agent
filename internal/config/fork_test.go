// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"slices"
	"testing"
)

// WithFork adds --fork among the flags, before the prompt, which must stay
// last.
func TestWithFork(t *testing.T) {
	args := BuildOpencodeArgs("-a prompt", "ses_1", SessionConfig{})
	forked := WithFork(args)
	n := len(forked)
	if n != len(args)+1 || forked[n-3] != "--fork" || forked[n-2] != "--" || forked[n-1] != "-a prompt" {
		t.Fatalf("WithFork = %q", forked)
	}
	if !slices.Equal(forked[:n-3], args[:len(args)-2]) {
		t.Errorf("WithFork changed the other flags: %q, from %q", forked, args)
	}
	if slices.Contains(args, "--fork") {
		t.Errorf("WithFork changed its input: %q", args)
	}
}
