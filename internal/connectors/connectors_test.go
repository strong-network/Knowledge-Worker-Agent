// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package connectors

import (
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/mcp"
)

func TestEffectiveSelection(t *testing.T) {
	servers := []mcp.GlobalServer{
		{Name: "github", Enabled: true},
		{Name: "atlassian", Enabled: true},
		{Name: "obsidian", Enabled: false},
	}

	// Nil override → every globally-enabled server is on. A globally-disabled
	// server is not selectable at all: opencode never connects it, so it must
	// not appear in a chat's selection.
	got := EffectiveSelection(nil, servers)
	if !got["github"] || !got["atlassian"] {
		t.Fatalf("seed: expected enabled servers on, got %v", got)
	}
	if _, ok := got["obsidian"]; ok {
		t.Fatalf("seed: globally-disabled server must be omitted, got %v", got)
	}

	// Per-context override wins for enabled servers, but cannot resurrect a
	// globally-disabled one.
	override := map[string]bool{"github": false, "obsidian": true}
	got = EffectiveSelection(override, servers)
	if got["github"] {
		t.Errorf("override: github should be false")
	}
	if _, ok := got["obsidian"]; ok {
		t.Errorf("override: cannot select a globally-disabled server")
	}
	if !got["atlassian"] {
		t.Errorf("override: atlassian should remain true (global default)")
	}
}

func TestEnabledOnly(t *testing.T) {
	servers := []mcp.GlobalServer{
		{Name: "github", Enabled: true},
		{Name: "figma", Enabled: false},
		{Name: "context7", Enabled: true},
	}
	got := enabledOnly(servers)
	if len(got) != 2 || got[0].Name != "github" || got[1].Name != "context7" {
		t.Fatalf("expected only the enabled servers, got %v", got)
	}
}

func TestIsConnected(t *testing.T) {
	cases := map[string]bool{
		"connected":    true,
		"connecting":   true,
		"pending":      true,
		"disconnected": false,
		"disabled":     false,
		"error":        false,
		"failed":       false,
		"":             false,
	}
	for status, want := range cases {
		if got := IsConnected(status); got != want {
			t.Errorf("IsConnected(%q) = %v, want %v", status, got, want)
		}
	}
}
