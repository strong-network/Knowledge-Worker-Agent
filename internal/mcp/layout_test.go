// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"path/filepath"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout/layouttest"
)

func TestKeyAndPlatformDirsBeforeAndAfterTheMigration(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "opencode", "opencode.json")
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", cfg)
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	t.Setenv(layout.EnvHome, t.TempDir())

	if got, want := defaultKeyDir(), filepath.Join(filepath.Dir(filepath.Dir(cfg)), "sds-mcp-keys"); got != want {
		t.Errorf("before: key dir = %q, want %q", got, want)
	}

	root := layouttest.Migrated(t)
	if got, want := defaultKeyDir(), filepath.Join(root, ".system", "mcp-keys"); got != want {
		t.Errorf("after: key dir = %q, want %q", got, want)
	}
	if got, want := platformConfigDir(), filepath.Join(root, ".system", "config"); got != want {
		t.Errorf("after: platform dir = %q, want %q", got, want)
	}
}
