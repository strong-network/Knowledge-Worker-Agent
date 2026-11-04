// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package versions

import (
	"os"
	"testing"
)

// The tests work in t.TempDir(), which Knowledge Worker Agent opens only in
// the home folder: the temporary folder stands in for it.
func TestMain(m *testing.M) {
	os.Setenv("HOME", os.TempDir())
	os.Exit(m.Run())
}
