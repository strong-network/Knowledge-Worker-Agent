// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodev2

import (
	"os"
	"path/filepath"
	"testing"
)

// What earlier releases kept for going back to opencode 1.x goes; the rest of
// the system folder stays.
func TestRemoveLeftovers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KWA_HOME", home)
	sys := filepath.Join(home, ".system")
	if err := os.MkdirAll(sys, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(sys, "knowledge-worker-agent.db")
	for _, p := range append(leftovers(), keep) {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	RemoveLeftovers()

	for _, p := range leftovers() {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", p, err)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("removed the database: %v", err)
	}
	// Nothing left to remove is not an error.
	RemoveLeftovers()
}
