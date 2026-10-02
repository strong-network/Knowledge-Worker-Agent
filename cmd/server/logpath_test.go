// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"path/filepath"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env/envtest"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout/layouttest"
)

func TestLogFilePath(t *testing.T) {
	envtest.Clear(t, "KWA_LOG_FILE")
	t.Setenv(layout.EnvHome, t.TempDir())
	if got := logFilePath(); got != "/tmp/chat-logs/chat.log" {
		t.Errorf("before the migration: %q", got)
	}

	root := layouttest.Migrated(t)
	if got, want := logFilePath(), filepath.Join(root, ".system", "logs", "knowledge-worker-agent.log"); got != want {
		t.Errorf("after the migration: %q, want %q", got, want)
	}

	t.Setenv("KWA_LOG_FILE", "/srv/kwa.log")
	if got := logFilePath(); got != "/srv/kwa.log" {
		t.Errorf("KWA_LOG_FILE: %q", got)
	}
}
