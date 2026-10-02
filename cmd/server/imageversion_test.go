// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The README tells readers which version to pull and shows the versioned pull
// commands. Those are published instructions, and nothing at build time reads
// them, so a release that forgot to update them would quietly point people at
// an older image. The VERSION file is the source of truth; this keeps the two
// in step.
func TestReadmeDocumentsTheCurrentImageVersion(t *testing.T) {
	root := filepath.Join("..", "..")

	raw, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		t.Fatalf("read VERSION: %v", err)
	}
	version := strings.TrimSpace(string(raw))
	if version == "" {
		t.Fatal("VERSION is empty")
	}

	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	text := string(readme)

	for _, want := range []string{
		"The latest version is **" + version + "**",
		"docker pull ghcr.io/strong-network/knowledge-worker-agent:" + version,
		"docker pull strongnetwork/knowledge-worker-agent:" + version,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("README.md does not contain %q; update it for version %s", want, version)
		}
	}
}
