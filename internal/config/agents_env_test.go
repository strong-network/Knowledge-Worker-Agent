// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/materializer"
)

// The platform config dir's env var name is declared in both packages: the
// materializer exports it, config reads it. Duplicated rather than imported so
// that config stays a leaf, so pin the two together — if they ever drift, the
// agent picker silently stops showing centrally assigned agents, which is
// exactly the bug this pairing exists to prevent.
func TestEnvOpencodeConfigDirMatchesMaterializer(t *testing.T) {
	if EnvOpencodeConfigDir != materializer.EnvConfigDir {
		t.Errorf("config.EnvOpencodeConfigDir = %q, materializer.EnvConfigDir = %q — these must name the same variable",
			EnvOpencodeConfigDir, materializer.EnvConfigDir)
	}
}
