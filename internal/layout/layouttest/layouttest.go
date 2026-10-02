// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package layouttest sets up a completed one-folder migration for tests.
package layouttest

import (
	"os"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// Migrated points KWA_HOME at a new temporary root, records the migration as
// completed there, and returns the root.
func Migrated(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv(layout.EnvHome, root)
	if err := os.MkdirAll(layout.MigrationDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.CompletionFile(), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}
