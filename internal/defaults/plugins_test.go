// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package defaults

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The opencode 1.x guards earlier builds installed go: opencode does their
// work itself and warns that it can't load them. Only our own, unchanged
// copies are removed.
func TestRemovePlugins(t *testing.T) {
	dir := t.TempDir()
	guard := filepath.Join(dir, "plugin", "pdf-guard.js")
	if _, err := copyTree(pluginFS, pluginRoot, dir); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RemovePlugins(&out, dir); err != nil {
		t.Fatal(err)
	}
	left, _ := os.ReadDir(filepath.Join(dir, "plugin"))
	if len(left) != 0 {
		t.Fatalf("left %d plugin files", len(left))
	}
	if !strings.Contains(out.String(), "Removed ") {
		t.Errorf("output = %q", out.String())
	}
	// Nothing to remove is not an error.
	if err := RemovePlugins(&bytes.Buffer{}, dir); err != nil {
		t.Fatal(err)
	}
	// An edited copy is someone's work.
	if err := os.WriteFile(guard, []byte("// mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemovePlugins(&bytes.Buffer{}, dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(guard); string(b) != "// mine\n" {
		t.Error("removed an edited copy")
	}
}
