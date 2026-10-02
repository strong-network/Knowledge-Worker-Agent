// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func updateRequest(t *testing.T, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/mcp/servers/"+name, strings.NewReader(body))
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	HandleUpdate(rec, req)
	return rec
}

func countConfigChanges(t *testing.T) *int {
	t.Helper()
	n := 0
	SetConfigChangeHook(func() { n++ })
	t.Cleanup(func() { SetConfigChangeHook(nil) })
	return &n
}

// An edit rewrites what the form shows and nothing else: the on/off choice
// and keys the form never displays are kept.
func TestUpdateRewritesTheDefinitionAndKeepsTheRest(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		on := "false"
		if enabled {
			on = "true"
		}
		path := withGlobalConfig(t, `{"theme":"dark","mcp":{"mine":{"type":"remote","url":"https://old.example.com/mcp","enabled":`+on+`,"oauth":{"clientId":"abc"}}}}`)
		changes := countConfigChanges(t)

		rec := updateRequest(t, "mine", `{"name":"ignored","transport":"http","url":" https://new.example.com/mcp ","headers":{"X-Team":"blue"},"timeout":5000}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("enabled=%v: status = %d: %s", enabled, rec.Code, rec.Body.String())
		}
		entry, _ := readGlobal(t, path)["mine"].(map[string]any)
		if entry["url"] != "https://new.example.com/mcp" || entry["timeout"] != float64(5000) {
			t.Errorf("enabled=%v: definition not rewritten: %v", enabled, entry)
		}
		if h, _ := entry["headers"].(map[string]any); h["X-Team"] != "blue" {
			t.Errorf("enabled=%v: headers not written: %v", enabled, entry)
		}
		if entry["enabled"] != enabled {
			t.Errorf("enabled=%v: on/off changed to %v", enabled, entry["enabled"])
		}
		if o, _ := entry["oauth"].(map[string]any); o["clientId"] != "abc" {
			t.Errorf("enabled=%v: unmanaged key dropped: %v", enabled, entry)
		}
		if raw, _ := readConfig(path); raw["theme"] != "dark" {
			t.Errorf("enabled=%v: rest of opencode.json disturbed: %v", enabled, raw)
		}
		if len(readGlobal(t, path)) != 1 {
			t.Errorf("enabled=%v: body name created a second entry: %v", enabled, readGlobal(t, path))
		}
		if *changes != 1 {
			t.Errorf("enabled=%v: config-change hook ran %d times", enabled, *changes)
		}
	}
}

// The other transport's fields must not survive a switch, or opencode gets an
// entry that is half local, half remote.
func TestUpdateSwitchingTransportDropsTheOldFields(t *testing.T) {
	path := withGlobalConfig(t, `{"mcp":{"mine":{"type":"remote","url":"https://x.example.com/mcp","headers":{"A":"b"},"enabled":true}}}`)

	rec := updateRequest(t, "mine", `{"transport":"stdio","command":"npx","args":["-y","pkg name"],"env":{"K":"v"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	entry, _ := readGlobal(t, path)["mine"].(map[string]any)
	if _, ok := entry["url"]; ok {
		t.Errorf("url survived the switch: %v", entry)
	}
	if _, ok := entry["headers"]; ok {
		t.Errorf("headers survived the switch: %v", entry)
	}
	if entry["type"] != "local" || !reflect.DeepEqual(toStringSlice(entry["command"]), []string{"npx", "-y", "pkg name"}) {
		t.Errorf("stdio definition wrong: %v", entry)
	}
}

func TestUpdateRefusals(t *testing.T) {
	const seed = `{"mcp":{"mine":{"type":"remote","url":"https://keep.example.com/mcp","enabled":true}}}`

	t.Run("unknown name is not created", func(t *testing.T) {
		path := withGlobalConfig(t, seed)
		changes := countConfigChanges(t)
		if rec := updateRequest(t, "nosuch", `{"transport":"http","url":"https://x.example.com/mcp"}`); rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
		if _, ok := readGlobal(t, path)["nosuch"]; ok || *changes != 0 {
			t.Errorf("a refused edit wrote config (hook runs %d)", *changes)
		}
	})

	t.Run("invalid definition leaves the entry alone", func(t *testing.T) {
		path := withGlobalConfig(t, seed)
		changes := countConfigChanges(t)
		if rec := updateRequest(t, "mine", `{"transport":"http","url":"  "}`); rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		if entry, _ := readGlobal(t, path)["mine"].(map[string]any); entry["url"] != "https://keep.example.com/mcp" || *changes != 0 {
			t.Errorf("entry changed by a refused edit: %v (hook runs %d)", entry, *changes)
		}
	})

	t.Run("provisioned", func(t *testing.T) {
		withGlobalConfig(t, "")
		withPlatformConfig(t, atlassianArtifact, atlassianMeta)
		rec := updateRequest(t, "atlassian", `{"transport":"http","url":"https://evil.example.com/mcp"}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409", rec.Code)
		}
		if sv := findServer(t, "atlassian"); sv.URL != "https://mcp.atlassian.com/v1/mcp/authv2" {
			t.Errorf("definition was overwritten: %q", sv.URL)
		}
	})
}

// managedEntryKeys decides what a rewrite may delete. A field added to
// mcpEntry but not listed there would be kept from the old entry, so an edit
// could never clear it.
func TestManagedEntryKeysCoverMcpEntry(t *testing.T) {
	typ := reflect.TypeOf(mcpEntry{})
	for i := 0; i < typ.NumField(); i++ {
		key := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if !managedEntryKeys[key] {
			t.Errorf("mcpEntry field %q is missing from managedEntryKeys", key)
		}
	}
	if len(managedEntryKeys) != typ.NumField() {
		t.Errorf("managedEntryKeys has %d keys, mcpEntry %d fields", len(managedEntryKeys), typ.NumField())
	}
}

// A running `opencode serve` reads its config only at startup: without the
// hook it can't connect a new server and keeps one the user removed.
func TestAddAndRemoveRunTheConfigChangeHook(t *testing.T) {
	path := withGlobalConfig(t, `{"mcp":{"keep":{"type":"remote","url":"https://keep.example.com/mcp"}}}`)
	withPlatformConfig(t, atlassianArtifact, atlassianMeta)
	changes := countConfigChanges(t)

	add := func(body string) int {
		rec := httptest.NewRecorder()
		HandleAdd(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/servers", strings.NewReader(body)))
		return rec.Code
	}
	remove := func(name string) int {
		req := httptest.NewRequest(http.MethodDelete, "/api/mcp/servers/"+name, nil)
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		HandleRemove(rec, req)
		return rec.Code
	}

	for _, refused := range []string{`{"name":"atlassian","url":"https://x.example.com/mcp"}`, `{"name":"bad","transport":"stdio"}`} {
		if code := add(refused); code < 400 || *changes != 0 {
			t.Fatalf("refused add %s = %d, hook runs %d", refused, code, *changes)
		}
	}
	if code := remove("atlassian"); code != http.StatusConflict || *changes != 0 {
		t.Fatalf("refused remove = %d, hook runs %d", code, *changes)
	}

	if code := add(`{"name":"new","url":"https://new.example.com/mcp"}`); code != http.StatusOK || *changes != 1 {
		t.Fatalf("add = %d, hook runs %d, want 200 and 1", code, *changes)
	}
	if code := remove("keep"); code != http.StatusOK || *changes != 2 {
		t.Fatalf("remove = %d, hook runs %d, want 200 and 2", code, *changes)
	}
	if block := readGlobal(t, path); block["keep"] != nil || block["new"] == nil {
		t.Errorf("config after add and remove: %v", block)
	}
}
