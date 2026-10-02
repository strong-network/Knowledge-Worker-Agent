// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package envtest clears settings in tests under every name they're read by,
// so a value set in the developer's environment under an old name doesn't leak in.
package envtest

import (
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
)

// Clear sets each setting, and its old names, to "" for the test.
func Clear(t testing.TB, names ...string) {
	t.Helper()
	for _, name := range names {
		for _, n := range env.Names(name) {
			t.Setenv(n, "")
		}
	}
}
