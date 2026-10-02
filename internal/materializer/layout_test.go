// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package materializer

import (
	"path/filepath"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout/layouttest"
)

func TestFromEnvAfterTheMigration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	root := layouttest.Migrated(t)
	userGlobal := filepath.Join(home, ".config", "opencode")
	sys := filepath.Join(root, ".system")

	t.Setenv(EnvConfigDir, "")
	t.Setenv(EnvCacheDir, "")
	o := Options{}.FromEnv(userGlobal)
	if o.ConfigDir != filepath.Join(sys, "config") || o.CacheDir != filepath.Join(sys, "config-repo") {
		t.Errorf("unset: ConfigDir=%q CacheDir=%q", o.ConfigDir, o.CacheDir)
	}

	// The old default, set explicitly, still moves.
	t.Setenv(EnvConfigDir, filepath.Join(home, ".config", "opencode-platform"))
	if o := (Options{}).FromEnv(userGlobal); o.ConfigDir != filepath.Join(sys, "config") {
		t.Errorf("old default: ConfigDir = %q", o.ConfigDir)
	}

	// A custom place stays, and so does one the caller passes.
	t.Setenv(EnvConfigDir, "/srv/platform")
	t.Setenv(EnvCacheDir, "/srv/cache")
	if o := (Options{}).FromEnv(userGlobal); o.ConfigDir != "/srv/platform" || o.CacheDir != "/srv/cache" {
		t.Errorf("custom: ConfigDir=%q CacheDir=%q", o.ConfigDir, o.CacheDir)
	}
	if o := (Options{ConfigDir: "/given"}).FromEnv(userGlobal); o.ConfigDir != "/given" {
		t.Errorf("given: ConfigDir = %q", o.ConfigDir)
	}
}
