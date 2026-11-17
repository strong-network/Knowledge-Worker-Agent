// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"path/filepath"
	"testing"
)

// opencode stays in the v2 folder earlier releases kept it in beside 1.x.
func TestOpencodeInstallDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KWA_OPENCODE_INSTALL_DIR", dir)
	if got := OpencodeInstallDir(); got != filepath.Join(dir, "v2") {
		t.Errorf("OpencodeInstallDir = %s", got)
	}
}
