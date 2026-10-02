// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"path/filepath"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout/layouttest"
)

func TestSnapshotDirAfterTheMigration(t *testing.T) {
	root := layouttest.Migrated(t)
	moved := filepath.Join(root, ".system", "knowledge-worker-agent.db")
	if got, want := SnapshotDir(moved), filepath.Join(root, ".system", "backups"); got != want {
		t.Errorf("SnapshotDir(moved database) = %q, want %q", got, want)
	}
	custom := filepath.Join(t.TempDir(), "custom.db")
	if got := SnapshotDir(custom); got != custom+".backups" {
		t.Errorf("SnapshotDir(custom database) = %q, want it next to the database", got)
	}
}
