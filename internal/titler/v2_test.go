// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package titler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodev2"
)

// On opencode 2.x the message is set apart as the thing to name.
func TestGenerate_V2WrapsTheMessage(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'opencode v2.2.5'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	opencodev2.Detect(bin)
	rec := stubRun(t, `{"type":"text","sessionID":"","part":{"text":"Directory listing"}}`, nil)
	title, err := Generate(context.Background(), bin, "github-copilot/claude-haiku-4.5", "Run `ls -la` please")
	if err != nil || title != "Directory listing" {
		t.Fatalf("Generate = %q, %v", title, err)
	}
	if !strings.HasPrefix(rec.Prompt, "Name this conversation.") || !strings.Contains(rec.Prompt, "\n---\nRun `ls -la` please\n---") {
		t.Fatalf("prompt = %q", rec.Prompt)
	}
}
