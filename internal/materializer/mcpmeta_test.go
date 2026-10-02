// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package materializer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTakeMCPMetaStripsSDSBlock(t *testing.T) {
	frag := map[string]any{
		"mcp": map[string]any{
			"atlassian": map[string]any{
				"type": "remote",
				"url":  "https://mcp.atlassian.com/v1/mcp/authv2",
			},
		},
		"sds": map[string]any{
			"auth": map[string]any{
				"type":    "oauth",
				"label":   "Atlassian",
				"help":    "Sign in to Jira and Confluence.",
				"helpUrl": "https://support.atlassian.com/rovo",
			},
		},
	}

	got, warnings := takeMCPMeta("mcp.atlassian.v1", frag)

	// "sds" is not part of opencode's schema and must never reach opencode.json.
	if _, ok := frag["sds"]; ok {
		t.Error("sds block left in the fragment")
	}
	if _, ok := frag["mcp"]; !ok {
		t.Error("mcp block removed")
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(got), got)
	}
	m := got[0]
	if m.Name != "atlassian" {
		t.Errorf("name = %q, want atlassian", m.Name)
	}
	if m.Auth.Type != "oauth" {
		t.Errorf("auth type = %q, want oauth", m.Auth.Type)
	}
	if m.Auth.Label != "Atlassian" || m.Auth.Help == "" || m.Auth.HelpURL == "" {
		t.Errorf("label/help/helpUrl lost: %+v", m.Auth)
	}
}

func TestTakeMCPMetaWithoutSDSBlock(t *testing.T) {
	// An artifact carrying only the native mcp block is still provisioned; it
	// just falls back to the built-in catalogue for classification.
	frag := map[string]any{"mcp": map[string]any{"acme": map[string]any{"type": "remote"}}}

	got, warnings := takeMCPMeta("mcp.acme.v1", frag)
	if len(got) != 1 || got[0].Name != "acme" {
		t.Fatalf("got %+v, want one acme entry", got)
	}
	if got[0].Auth.Type != "" {
		t.Errorf("invented an auth type: %+v", got[0].Auth)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
}

func TestTakeMCPMetaToleratesMalformedSDS(t *testing.T) {
	// A metadata typo must not fail the whole materialization — the server
	// degrades to the catalogue's classification, which is strictly better than
	// taking down every other artifact the project is assigned.
	cases := []map[string]any{
		{"mcp": map[string]any{"acme": map[string]any{}}, "sds": "not an object"},
		{"mcp": map[string]any{"acme": map[string]any{}}, "sds": map[string]any{"auth": "nope"}},
		{"mcp": map[string]any{"acme": map[string]any{}}, "sds": map[string]any{"auth": map[string]any{"type": 7}}},
	}
	for i, frag := range cases {
		got, _ := takeMCPMeta("mcp.acme.v1", frag)
		if len(got) != 1 || got[0].Name != "acme" {
			t.Fatalf("case %d: got %+v, want one acme entry", i, got)
		}
		if got[0].Auth.Type != "" {
			t.Errorf("case %d: invented an auth type: %+v", i, got[0].Auth)
		}
		if _, ok := frag["sds"]; ok {
			t.Errorf("case %d: malformed sds block left in the fragment", i)
		}
	}
}

// An artifact declaring "enabled" — in either direction — must not reach the
// platform config. opencode merges per key and the platform file wins, so the
// key would take the on/off switch away from the user while the modal's toggle
// still appeared to work.
func TestTakeMCPMetaStripsEnabledInEitherDirection(t *testing.T) {
	for _, declared := range []bool{true, false} {
		frag := map[string]any{
			"mcp": map[string]any{
				"atlassian": map[string]any{
					"type":    "remote",
					"url":     "https://example.com/mcp",
					"enabled": declared,
				},
			},
		}
		_, warnings := takeMCPMeta("mcp.atlassian.v1", frag)

		block := frag["mcp"].(map[string]any)
		entry := block["atlassian"].(map[string]any)
		if _, still := entry["enabled"]; still {
			t.Fatalf("enabled=%v: key survived into the fragment", declared)
		}
		// The rest of the definition is untouched — only the one key goes.
		if entry["url"] != "https://example.com/mcp" || entry["type"] != "remote" {
			t.Errorf("enabled=%v: stripping damaged the entry: %v", declared, entry)
		}
		if len(warnings) != 1 {
			t.Fatalf("enabled=%v: got %d warnings, want 1: %v", declared, len(warnings), warnings)
		}
		// The warning has to name the artifact and the server, or an
		// administrator cannot act on it.
		if !strings.Contains(warnings[0], "mcp.atlassian.v1") || !strings.Contains(warnings[0], "atlassian") {
			t.Errorf("enabled=%v: warning does not name the artifact and server: %q", declared, warnings[0])
		}
	}
}

func TestTakeMCPMetaAppliesSDSBlockToEveryServerInTheFragment(t *testing.T) {
	// Degenerate but must be deterministic: the sds block is per-artifact while
	// the mcp block is a map, so a multi-server fragment applies it to each, in
	// sorted name order.
	frag := map[string]any{
		"mcp": map[string]any{
			"zeta":  map[string]any{"type": "remote"},
			"alpha": map[string]any{"type": "remote"},
		},
		"sds": map[string]any{"auth": map[string]any{"type": "oauth"}},
	}
	got, _ := takeMCPMeta("mcp.multi.v1", frag)
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Errorf("not in sorted order: %v, %v", got[0].Name, got[1].Name)
	}
	for _, m := range got {
		if m.Auth.Type != "oauth" {
			t.Errorf("%s did not inherit the sds block: %+v", m.Name, m.Auth)
		}
	}
}

// The sidecar is always written, even with nothing assigned, so a reader can
// tell "materialized, no MCP servers" from "never materialized".
func TestMaterializeAlwaysWritesMCPMetaSidecar(t *testing.T) {
	repo := exampleRepoDir(t)
	a, err := ParseAssignment(readExampleAssignment(t))
	if err != nil {
		t.Fatal(err)
	}
	// proj_onprem is assigned a provider and no MCP servers.
	res, err := a.Resolve("proj_onprem")
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "platform-opencode")
	if _, err := Materialize(cfg, repo, res, ""); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(cfg, MCPMetaFile))
	if err != nil {
		t.Fatalf("sidecar not written: %v", err)
	}
	var doc mcpMetaFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse sidecar: %v", err)
	}
	if doc.Servers == nil {
		t.Error("servers is null; want [] so the sidecar reads as materialized-but-empty")
	}
	if len(doc.Servers) != 0 {
		t.Errorf("got %d servers for a project with no mcp assignment", len(doc.Servers))
	}
}

// The sidecar is metadata for Knowledge Worker Agent only: it must never leak into the
// config opencode reads.
func TestMaterializeKeepsMCPMetaOutOfOpencodeJSON(t *testing.T) {
	repo := exampleRepoDir(t)
	a, err := ParseAssignment(readExampleAssignment(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Resolve("proj_team")
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "platform-opencode")
	if _, err := Materialize(cfg, repo, res, ""); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	oc := readJSON(t, filepath.Join(cfg, "opencode.json"))
	if oc["sds"] != nil {
		t.Errorf("sds block reached opencode.json: %v", oc["sds"])
	}
	block, _ := oc["mcp"].(map[string]any)
	if len(block) == 0 {
		t.Fatalf("no mcp block: %v", oc)
	}
	for name, v := range block {
		entry, _ := v.(map[string]any)
		if _, ok := entry["enabled"]; ok {
			t.Errorf("%s carries an enabled key: %v", name, entry)
		}
	}
}

// End-to-end over the REAL artifacts shipped in the example repo: the sidecar
// has to carry enough for a user who has never heard of the server to connect
// it. This is the "standing alone" requirement — a provisioned server must not
// depend on Knowledge Worker Agent's built-in catalogue, which is being retired.
func TestMaterializeRealArtifactsCarryEnoughToConnect(t *testing.T) {
	withKeyStore(t)
	repo := exampleRepoDir(t)
	a, err := ParseAssignment(readExampleAssignment(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Resolve("proj_team")
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "platform-opencode")
	warnings, err := Materialize(cfg, repo, res, "")
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("the shipped example artifacts should be clean, got %v", warnings)
	}

	raw, err := os.ReadFile(filepath.Join(cfg, MCPMetaFile))
	if err != nil {
		t.Fatal(err)
	}
	var doc mcpMetaFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	byName := map[string]MCPMeta{}
	for _, s := range doc.Servers {
		byName[s.Name] = s
	}

	// One connector per way of connecting. The ones users must act on also
	// link to more help.
	for name, auth := range map[string]string{"microsoft-learn": "none", "atlassian": "oauth", "acme-docs": "api-key"} {
		m, ok := byName[name]
		if !ok {
			t.Errorf("%s missing from the sidecar", name)
			continue
		}
		if m.Auth.Type != auth {
			t.Errorf("%s: auth type = %q, want %s — it decides what the row offers", name, m.Auth.Type, auth)
		}
		if m.Auth.Label == "" || m.Auth.Help == "" {
			t.Errorf("%s: a provisioned server with no label or help is a row with no route forward: %+v", name, m.Auth)
		}
		if auth != "none" && !strings.HasPrefix(m.Auth.HelpURL, "https://") {
			t.Errorf("%s: helpUrl = %q, want an https link", name, m.Auth.HelpURL)
		}
	}

	// The definitions reached opencode.json, still with no "enabled" key.
	oc := readJSON(t, filepath.Join(cfg, "opencode.json"))
	block, _ := oc["mcp"].(map[string]any)
	atlassian, _ := block["atlassian"].(map[string]any)
	if atlassian["url"] != "https://mcp.atlassian.com/v1/mcp/authv2" {
		t.Errorf("atlassian url = %v", atlassian["url"])
	}
	if _, ok := atlassian["enabled"]; ok {
		t.Errorf("atlassian carries an enabled key: %v", atlassian)
	}
}
