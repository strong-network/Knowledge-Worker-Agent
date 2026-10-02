// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
)

// TestMain isolates the package from the developer's own environment.
//
// Two things bleed in otherwise, and both look like flakes rather than the
// environment problems they are:
//
//   - OPENCODE_CONFIG_DIR defaults to a path under the real user's home, and
//     listServers merges the platform-owned config dir into its result. A
//     workspace with a materialized ~/.config/opencode-platform/opencode.json
//     would see it in every test in this package.
//   - KWA_CENTRAL_CONFIG turns EnsureDefaults into a catalogue *remover*. A
//     centrally managed workspace (the normal state for anyone working on
//     central connectors, so this
//     is the normal state for anyone working on it) would fail every test
//     that expects the built-in catalogue to install.
//
// Tests that exercise either mode opt in explicitly with t.Setenv.
func TestMain(m *testing.M) {
	empty, err := os.MkdirTemp("", "mcp-no-platform-config-")
	if err != nil {
		panic(err)
	}
	os.Setenv("OPENCODE_CONFIG_DIR", empty)
	for _, n := range env.Names(config.EnvCentralConfig) {
		os.Unsetenv(n)
	}
	code := m.Run()
	os.RemoveAll(empty)
	os.Exit(code)
}

// withPlatformConfig writes a platform-owned opencode.json (and optionally the
// mcp-meta.json sidecar) into a temp dir and points OPENCODE_CONFIG_DIR at it,
// standing in for what the materializer produces.
func withPlatformConfig(t *testing.T, opencodeJSON, mcpMetaJSON string) string {
	t.Helper()
	dir := t.TempDir()
	if opencodeJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(opencodeJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if mcpMetaJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, mcpMetaFileName), []byte(mcpMetaJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("OPENCODE_CONFIG_DIR", dir)
	return dir
}

// withGlobalConfig points the user's Global opencode.json at a temp file,
// optionally seeded with content.
func withGlobalConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.json")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", path)
	return path
}

// readGlobal returns the "mcp" block of the user's Global config.
func readGlobal(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := readConfig(path)
	if err != nil {
		t.Fatalf("read global: %v", err)
	}
	block, _ := raw["mcp"].(map[string]any)
	if block == nil {
		block = map[string]any{}
	}
	return block
}

func findServer(t *testing.T, name string) serverView {
	t.Helper()
	views, err := listServers()
	if err != nil {
		t.Fatalf("listServers: %v", err)
	}
	for _, v := range views {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("server %q not listed; got %v", name, names(views))
	return serverView{}
}

func names(views []serverView) []string {
	out := make([]string, 0, len(views))
	for _, v := range views {
		out = append(out, v.Name)
	}
	return out
}
