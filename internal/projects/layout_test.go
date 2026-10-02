// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package projects

import (
	"path/filepath"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout/layouttest"
)

func TestProvisionWorkspaceAfterTheMigration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := layouttest.Migrated(t)

	res, err := ProvisionWorkspace("Roadmap", "")
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	if want := filepath.Join(root, "Projects", "roadmap"); res.WorkspacePath != want {
		t.Errorf("workspace = %q, want %q", res.WorkspacePath, want)
	}
}
