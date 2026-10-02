// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readRaw(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return raw
}

func TestUpsertLocalServerWritesOpencodeSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", path)

	sv := serverView{
		Name:        "obsidian",
		Transport:   "stdio",
		Command:     "obsidian-mcp",
		Args:        []string{"/vault"},
		Environment: map[string]string{"FOO": "bar"},
		Enabled:     true,
	}
	stored, err := upsertServer(sv)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if stored.Type != "local" || stored.Transport != "stdio" {
		t.Errorf("expected local/stdio, got %s/%s", stored.Type, stored.Transport)
	}

	raw := readRaw(t, path)
	block := raw["mcp"].(map[string]any)
	entry := block["obsidian"].(map[string]any)
	if entry["type"] != "local" {
		t.Errorf("expected type=local, got %v", entry["type"])
	}
	cmd, _ := entry["command"].([]any)
	if len(cmd) != 2 || cmd[0] != "obsidian-mcp" || cmd[1] != "/vault" {
		t.Errorf("expected command [obsidian-mcp /vault], got %v", cmd)
	}
	if en, ok := entry["enabled"].(bool); !ok || !en {
		t.Errorf("expected enabled=true, got %v", entry["enabled"])
	}
}

func TestUpsertRemoteServerWritesOpencodeSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", path)

	sv := serverView{
		Name:      "atlassian",
		Transport: "http",
		URL:       "https://mcp.atlassian.com/v1/mcp",
	}
	stored, err := upsertServer(sv)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if stored.Type != "remote" || stored.Transport != "http" {
		t.Errorf("expected remote/http, got %s/%s", stored.Type, stored.Transport)
	}

	raw := readRaw(t, path)
	entry := raw["mcp"].(map[string]any)["atlassian"].(map[string]any)
	if entry["type"] != "remote" {
		t.Errorf("expected type=remote, got %v", entry["type"])
	}
	if entry["url"] != "https://mcp.atlassian.com/v1/mcp" {
		t.Errorf("unexpected url: %v", entry["url"])
	}
}

func TestUpsertPreservesUnrelatedConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", path)

	// Pre-existing opencode config with unrelated keys and another MCP server.
	initial := `{
	  "$schema": "https://opencode.ai/config.json",
	  "theme": "opencode",
	  "model": "github-copilot/claude-sonnet-4.6",
	  "mcp": { "existing": { "type": "remote", "url": "https://x" } }
	}`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := upsertServer(serverView{Name: "atlassian", Transport: "http", URL: "https://mcp.atlassian.com/v1/mcp"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	raw := readRaw(t, path)
	if raw["theme"] != "opencode" {
		t.Errorf("theme key was lost: %v", raw["theme"])
	}
	if raw["model"] != "github-copilot/claude-sonnet-4.6" {
		t.Errorf("model key was lost: %v", raw["model"])
	}
	block := raw["mcp"].(map[string]any)
	if _, ok := block["existing"]; !ok {
		t.Error("existing MCP server was lost")
	}
	if _, ok := block["atlassian"]; !ok {
		t.Error("new atlassian server was not added")
	}
}

func TestListGetRemoveServers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", path)

	_, _ = upsertServer(serverView{Name: "b", Transport: "http", URL: "https://b"})
	_, _ = upsertServer(serverView{Name: "a", Transport: "stdio", Command: "acmd"})

	list, err := listServers()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || list[0].Name != "a" || list[1].Name != "b" {
		t.Fatalf("expected sorted [a b], got %+v", list)
	}

	sv, ok, err := getServer("a")
	if err != nil || !ok {
		t.Fatalf("get a: ok=%v err=%v", ok, err)
	}
	if sv.Command != "acmd" {
		t.Errorf("unexpected command: %q", sv.Command)
	}

	if _, ok, _ := getServer("missing"); ok {
		t.Error("expected missing server to report ok=false")
	}

	if err := removeServer("a"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok, _ := getServer("a"); ok {
		t.Error("expected 'a' to be removed")
	}
	// Removing a missing server is a no-op.
	if err := removeServer("a"); err != nil {
		t.Errorf("expected no-op remove, got %v", err)
	}
}

func TestListServersMissingFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", filepath.Join(dir, "does-not-exist.json"))
	list, err := listServers()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list for missing file, got %+v", list)
	}
}
