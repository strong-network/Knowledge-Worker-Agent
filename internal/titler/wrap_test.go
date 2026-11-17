// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package titler

import (
	"context"
	"strings"
	"testing"
)

// The message is set apart as the thing to name.
func TestGenerate_WrapsTheMessage(t *testing.T) {
	rec := stubRun(t, `{"type":"text","sessionID":"","part":{"text":"Directory listing"}}`, nil)
	title, err := Generate(context.Background(), "opencode", "github-copilot/claude-haiku-4.5", "Run `ls -la` please")
	if err != nil || title != "Directory listing" {
		t.Fatalf("Generate = %q, %v", title, err)
	}
	if !strings.HasPrefix(rec.Prompt, "Name this conversation.") || !strings.Contains(rec.Prompt, "\n---\nRun `ls -la` please\n---") {
		t.Fatalf("prompt = %q", rec.Prompt)
	}
}
