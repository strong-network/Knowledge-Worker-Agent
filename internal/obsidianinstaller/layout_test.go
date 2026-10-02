// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package obsidianinstaller

import (
	"path/filepath"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env/envtest"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout/layouttest"
)

func TestDefaultsAfterTheMigration(t *testing.T) {
	t.Setenv("COPILOT_OBSIDIAN_INSTALL_DIR", "")
	envtest.Clear(t, "KWA_OBSIDIAN_VAULT")
	root := layouttest.Migrated(t)
	if got, want := DefaultInstallDir(), filepath.Join(root, ".system", "obsidian"); got != want {
		t.Errorf("install dir = %q, want %q", got, want)
	}
	if got, want := DefaultVaultDir(), filepath.Join(root, "ObsidianVault"); got != want {
		t.Errorf("vault = %q, want %q", got, want)
	}
}
