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

// withKeyStore stands in for Knowledge Worker Agent's key store: empty files in a temp dir.
func withKeyStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := mcpKeyFile
	SetMCPKeyFile(func(name string) (string, error) {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err != nil {
			return p, os.WriteFile(p, nil, 0o600)
		}
		return p, nil
	})
	t.Cleanup(func() { mcpKeyFile = prev })
	return dir
}

func apiKeyFragment(auth string, headers string) map[string]any {
	frag := mustJSON(`{"mcp":{"kfp":{"type":"remote","url":"https://mcp.acme.example.com/mcp"` + headers + `}},"sds":{"auth":` + auth + `}}`)
	return frag
}

func kfpHeaders(t *testing.T, frag map[string]any) map[string]any {
	t.Helper()
	entry := frag["mcp"].(map[string]any)["kfp"].(map[string]any)
	h, _ := entry["headers"].(map[string]any)
	return h
}

// Connector API keys: the artifact defines the server; the header reads each user's own key
// file, which exists (empty) before the config referencing it does.
func TestAPIKeyServerReadsTheUsersKeyFile(t *testing.T) {
	dir := withKeyStore(t)
	frag := apiKeyFragment(`{"type":"api-key","label":"KFP","help":"Paste your key."}`, "")
	meta, warnings := takeMCPMeta("mcp.kfp.v1", frag)
	if len(warnings) != 0 {
		t.Errorf("warnings: %v", warnings)
	}
	path := filepath.Join(dir, "kfp")
	if got := kfpHeaders(t, frag)["Authorization"]; got != "Bearer {file:"+path+"}" {
		t.Errorf("Authorization = %v", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the key file must exist before opencode reads the config: %v", err)
	}
	if len(meta) != 1 || meta[0].Auth.Type != "api-key" || meta[0].Auth.Label != "KFP" {
		t.Errorf("meta = %+v", meta)
	}
}

func TestAPIKeyHeaderAndSchemeCanBeDeclared(t *testing.T) {
	dir := withKeyStore(t)
	frag := apiKeyFragment(`{"type":"api-key","header":"X-API-Key","scheme":""}`, "")
	takeMCPMeta("mcp.kfp.v1", frag)
	h := kfpHeaders(t, frag)
	if h["X-API-Key"] != "{file:"+filepath.Join(dir, "kfp")+"}" || h["Authorization"] != nil {
		t.Errorf("headers = %v", h)
	}
}

// A key in the repo would be one key for everyone; the header is Knowledge Worker Agent's.
func TestAPIKeyArtifactCannotSupplyTheKeyHeaderItself(t *testing.T) {
	withKeyStore(t)
	frag := apiKeyFragment(`{"type":"api-key"}`, `,"headers":{"authorization":"Bearer shared-secret","X-Team":"sds"}`)
	_, warnings := takeMCPMeta("mcp.kfp.v1", frag)
	h := kfpHeaders(t, frag)
	if strings.Contains(stringify(h), "shared-secret") || h["X-Team"] != "sds" || !strings.HasPrefix(h["Authorization"].(string), "Bearer {file:") {
		t.Errorf("headers = %v", h)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "Authorization") {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestAPIKeyNeedsARemoteServerAndAKeyStore(t *testing.T) {
	withKeyStore(t)
	local := mustJSON(`{"mcp":{"kfp":{"type":"local","command":["kfp"]}},"sds":{"auth":{"type":"api-key"}}}`)
	if _, warnings := takeMCPMeta("mcp.kfp.v1", local); len(warnings) != 1 || !strings.Contains(warnings[0], "not a remote server") {
		t.Errorf("local server: %v", warnings)
	}
	SetMCPKeyFile(nil)
	frag := apiKeyFragment(`{"type":"api-key"}`, "")
	if _, warnings := takeMCPMeta("mcp.kfp.v1", frag); len(warnings) != 1 || kfpHeaders(t, frag) != nil {
		t.Errorf("no key store: warnings=%v headers=%v", warnings, kfpHeaders(t, frag))
	}
}

func mustJSON(s string) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		panic(err)
	}
	return m
}

func stringify(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// The documented example artifact produces the header the docs promise: its
// own header name, with no scheme before the key.
func TestExampleAPIKeyArtifact(t *testing.T) {
	dir := withKeyStore(t)
	frag, err := loadFragment(filepath.Join(exampleRepoDir(t), "artifacts/mcp/acme-docs.json"))
	if err != nil {
		t.Fatal(err)
	}
	meta, warnings := takeMCPMeta("mcp.acme-docs.v1", frag)
	entry := frag["mcp"].(map[string]any)["acme-docs"].(map[string]any)
	h, _ := entry["headers"].(map[string]any)
	if len(warnings) != 0 || h["X-API-Key"] != "{file:"+filepath.Join(dir, "acme-docs")+"}" || h["Authorization"] != nil {
		t.Errorf("warnings=%v headers=%v", warnings, h)
	}
	if len(meta) != 1 || meta[0].Auth.Type != "api-key" || meta[0].Auth.Label == "" || meta[0].Auth.Help == "" {
		t.Errorf("meta = %+v", meta)
	}
}
