// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package materializer

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTakeProviderMetaStripsSDSBlock(t *testing.T) {
	frag := map[string]any{
		"provider": map[string]any{
			"mistral": map[string]any{"npm": "@ai-sdk/mistral"},
		},
		"sds": map[string]any{
			"auth": map[string]any{
				"type":      "managed",
				"secretRef": "/etc/secrets/mistral.key",
				"label":     "Mistral (on-prem)",
				"help":      "Provisioned by your administrator",
			},
			"presets": map[string]any{
				"default":  "mistral/small",
				"thinking": "mistral/large",
			},
		},
	}

	got := takeProviderMeta(frag)

	// "sds" is not part of opencode's schema and must never reach opencode.json.
	if _, ok := frag["sds"]; ok {
		t.Error("sds block left in the fragment")
	}
	if _, ok := frag["provider"]; !ok {
		t.Error("provider block removed")
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(got), got)
	}
	m := got[0]
	if m.ID != "mistral" {
		t.Errorf("id = %q, want mistral", m.ID)
	}
	if m.Auth.Type != "managed" || m.Auth.SecretRef != "/etc/secrets/mistral.key" {
		t.Errorf("auth = %+v", m.Auth)
	}
	if m.Auth.Label != "Mistral (on-prem)" || m.Auth.Help == "" {
		t.Errorf("label/help lost: %+v", m.Auth)
	}
	if m.Presets["default"] != "mistral/small" || m.Presets["thinking"] != "mistral/large" {
		t.Errorf("presets = %v", m.Presets)
	}
}

func TestTakeProviderMetaWithoutSDSBlock(t *testing.T) {
	// An artifact that carries only the native provider block is still offered,
	// just without a label or preset nominations.
	frag := map[string]any{"provider": map[string]any{"acme": map[string]any{}}}

	got := takeProviderMeta(frag)
	if len(got) != 1 || got[0].ID != "acme" {
		t.Fatalf("got %+v, want one acme entry", got)
	}
	if got[0].Auth.Type != "" || got[0].Presets != nil {
		t.Errorf("invented metadata: %+v", got[0])
	}
}

func TestTakeProviderMetaToleratesMalformedSDS(t *testing.T) {
	// A metadata typo must not fail the whole materialization — the provider
	// degrades to the default auth type and the built-in preset heuristic.
	cases := []map[string]any{
		{"provider": map[string]any{"acme": map[string]any{}}, "sds": "not an object"},
		{"provider": map[string]any{"acme": map[string]any{}}, "sds": map[string]any{"auth": "nope", "presets": 7}},
		{"provider": map[string]any{"acme": map[string]any{}}, "sds": map[string]any{
			"presets": map[string]any{"default": 42, "thinking": ""},
		}},
	}
	for i, frag := range cases {
		got := takeProviderMeta(frag)
		if _, ok := frag["sds"]; ok {
			t.Errorf("case %d: malformed sds block not stripped", i)
		}
		if len(got) != 1 || got[0].ID != "acme" {
			t.Fatalf("case %d: got %+v", i, got)
		}
		if len(got[0].Presets) != 0 {
			t.Errorf("case %d: non-string preset kept: %v", i, got[0].Presets)
		}
	}
}

func TestTakeProviderMetaWithoutProviderBlock(t *testing.T) {
	// Nothing to describe: the sds block is still stripped, but no metadata is
	// emitted for a fragment that declares no provider.
	frag := map[string]any{"sds": map[string]any{"auth": map[string]any{"type": "user-key"}}}
	if got := takeProviderMeta(frag); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
	if _, ok := frag["sds"]; ok {
		t.Error("sds block left behind")
	}
}

func TestTakeProviderMetaMultipleProvidersAreSorted(t *testing.T) {
	// A degenerate shape (the documented one is a provider per artifact), but
	// it must be deterministic: the sidecar's order decides preset conflicts.
	frag := map[string]any{
		"provider": map[string]any{"zeta": map[string]any{}, "alpha": map[string]any{}, "mid": map[string]any{}},
		"sds":      map[string]any{"auth": map[string]any{"type": "user-key"}},
	}
	got := takeProviderMeta(frag)
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3", len(got))
	}
	for i, want := range []string{"alpha", "mid", "zeta"} {
		if got[i].ID != want {
			t.Fatalf("index %d = %q, want %q (full: %+v)", i, got[i].ID, want, got)
		}
	}
}

// readSidecar loads the provider metadata sidecar from a materialized dir.
func readSidecar(t *testing.T, dir string) providerMetaFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ProviderMetaFile))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var doc providerMetaFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse sidecar: %v", err)
	}
	return doc
}

// TestMaterializeWritesProviderSidecar drives the real example config repo, so
// the documented artifact shape and the code that reads it stay in step.
func TestMaterializeWritesProviderSidecar(t *testing.T) {
	repo := exampleRepoDir(t)
	a, err := ParseAssignment(readExampleAssignment(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Resolve("proj_onprem")
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "platform-opencode")
	if _, err := Materialize(cfg, repo, res, ""); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	// The sds block must not reach opencode.json — it is not part of the schema
	// and opencode rejects unknown top-level keys.
	oc := readJSON(t, filepath.Join(cfg, "opencode.json"))
	if oc["sds"] != nil {
		t.Errorf("sds block leaked into opencode.json: %v", oc["sds"])
	}
	prov, ok := oc["provider"].(map[string]any)
	if !ok || prov["onprem"] == nil {
		t.Fatalf("provider block not merged: %v", oc["provider"])
	}

	doc := readSidecar(t, cfg)
	if len(doc.Providers) != 1 {
		t.Fatalf("sidecar = %+v, want one provider", doc.Providers)
	}
	m := doc.Providers[0]
	if m.ID != "onprem" {
		t.Errorf("id = %q, want onprem", m.ID)
	}
	if m.Auth.Type != "managed" || m.Auth.SecretRef == "" {
		t.Errorf("auth = %+v", m.Auth)
	}
	// The id in the sidecar must be the same one used in opencode.json and in
	// the credential store, or the registry authenticates the wrong thing.
	if _, ok := prov[m.ID]; !ok {
		t.Errorf("sidecar id %q is not a key of the provider block", m.ID)
	}
	if m.Presets["default"] != "onprem/large" || m.Presets["small"] != "onprem/small" {
		t.Errorf("presets = %v", m.Presets)
	}
}

// TestMaterializeAlwaysWritesTheSidecar: an empty array distinguishes
// "materialized, nothing assigned" from "never materialized" — the registry
// needs that to decide between central mode and the fall-back.
func TestMaterializeAlwaysWritesTheSidecar(t *testing.T) {
	repo := exampleRepoDir(t)
	a, err := ParseAssignment(readExampleAssignment(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Resolve("proj_team") // no provider assigned
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "platform-opencode")
	if _, err := Materialize(cfg, repo, res, ""); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	if doc := readSidecar(t, cfg); len(doc.Providers) != 0 {
		t.Errorf("sidecar = %+v, want empty", doc.Providers)
	}
	// Written as [], not null, so a reader can range over it directly.
	raw, err := os.ReadFile(filepath.Join(cfg, ProviderMetaFile))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("null")) {
		t.Errorf("sidecar encodes null: %s", raw)
	}
}

// "kwa" is the block's name; "sds", its name before, still works, and neither
// reaches opencode.json.
func TestTakeMetaBlockPrefersKWA(t *testing.T) {
	kwa := map[string]any{"auth": map[string]any{"type": "managed"}}
	sds := map[string]any{"auth": map[string]any{"type": "user-key"}}
	for _, c := range []struct {
		name string
		frag map[string]any
		want any
	}{
		{"kwa", map[string]any{"kwa": kwa, "provider": map[string]any{}}, kwa},
		{"sds", map[string]any{"sds": sds, "provider": map[string]any{}}, sds},
		{"both", map[string]any{"kwa": kwa, "sds": sds, "provider": map[string]any{}}, kwa},
		{"neither", map[string]any{"provider": map[string]any{}}, nil},
	} {
		got := takeMetaBlock(c.frag)
		if stringify(got) != stringify(c.want) {
			t.Errorf("%s: got %s, want %s", c.name, stringify(got), stringify(c.want))
		}
		_, hasKWA := c.frag["kwa"]
		_, hasSDS := c.frag["sds"]
		if hasKWA || hasSDS || c.frag["provider"] == nil {
			t.Errorf("%s: fragment left as %v", c.name, c.frag)
		}
	}

	frag := map[string]any{
		"provider": map[string]any{"mistral": map[string]any{}},
		"kwa":      map[string]any{"auth": map[string]any{"type": "managed", "label": "Mistral"}, "presets": map[string]any{"default": "mistral/small"}},
	}
	got := takeProviderMeta(frag)
	if len(got) != 1 || got[0].Auth.Type != "managed" || got[0].Auth.Label != "Mistral" || got[0].Presets["default"] != "mistral/small" {
		t.Errorf("provider meta from a kwa block: %+v", got)
	}
	names, _ := takeMCPMeta("acme", map[string]any{
		"mcp": map[string]any{"acme": map[string]any{"type": "remote", "url": "https://mcp.example.com"}},
		"kwa": map[string]any{"auth": map[string]any{"type": "oauth", "label": "Acme"}},
	})
	if len(names) != 1 || names[0].Auth.Type != "oauth" || names[0].Auth.Label != "Acme" {
		t.Errorf("connector meta from a kwa block: %+v", names)
	}
}
