// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const hiddenSeed = `{"mcp":{
	"web":{"type":"remote","url":"https://web.example.com/mcp","headers":{"Authorization":"Bearer s3cret"},"enabled":true},
	"cli":{"type":"local","command":["npx","-y","pkg"],"environment":{"API_KEY":"k3y"},"enabled":true}}}`

func addRequest(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	HandleAdd(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/servers", strings.NewReader(body)))
	return rec
}

func storedEntry(t *testing.T, path, name string) map[string]any {
	t.Helper()
	entry, _ := readGlobal(t, path)[name].(map[string]any)
	return entry
}

func stringField(entry map[string]any, field, key string) string {
	m, _ := entry[field].(map[string]any)
	s, _ := m[key].(string)
	return s
}

// Header and environment values are usually credentials, so no response
// carries them: the GitHub connector's bearer token was readable here.
func TestResponsesHideHeaderAndEnvironmentValues(t *testing.T) {
	withGlobalConfig(t, hiddenSeed)
	list := httptest.NewRecorder()
	HandleList(list, httptest.NewRequest(http.MethodGet, "/api/mcp/servers", nil))
	get := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/mcp/servers/web", nil)
	req.SetPathValue("name", "web")
	HandleGet(get, req)
	edit := updateRequest(t, "web", `{"transport":"http","url":"https://web.example.com/mcp","headers":{"Authorization":"`+hiddenValue+`"}}`)
	add := addRequest(t, `{"name":"new","transport":"http","url":"https://new.example.com/mcp","headers":{"Authorization":"Bearer fresh"}}`)

	for name, rec := range map[string]*httptest.ResponseRecorder{"list": list, "get": get, "update": edit, "add": add} {
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, hiddenValue) {
			t.Errorf("%s: %d %s", name, rec.Code, body)
		}
		for _, secret := range []string{"s3cret", "k3y", "fresh"} {
			if strings.Contains(body, secret) {
				t.Errorf("%s returns a credential: %s", name, body)
			}
		}
	}
	var listed struct{ Servers []serverView }
	json.Unmarshal(list.Body.Bytes(), &listed)
	for _, sv := range listed.Servers {
		if sv.Name == "web" && sv.Headers["Authorization"] != hiddenValue || sv.Name == "cli" && sv.Environment["API_KEY"] != hiddenValue {
			t.Errorf("%s: names should stay, values hidden: %+v", sv.Name, sv)
		}
	}
}

// An edit that leaves a hidden value alone keeps the saved one; one that
// changes it saves the new one.
func TestAnEditKeepsTheValuesItLeavesHidden(t *testing.T) {
	path := withGlobalConfig(t, hiddenSeed)
	if rec := updateRequest(t, "web", `{"transport":"http","url":"https://web.example.com/mcp","headers":{"Authorization":"`+hiddenValue+`","X-Team":"blue"}}`); rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if e := storedEntry(t, path, "web"); stringField(e, "headers", "Authorization") != "Bearer s3cret" || stringField(e, "headers", "X-Team") != "blue" {
		t.Errorf("headers after the edit: %v", e)
	}
	if rec := updateRequest(t, "cli", `{"transport":"stdio","command":"npx","args":["-y","pkg"],"env":{"API_KEY":"`+hiddenValue+`"}}`); rec.Code != http.StatusOK {
		t.Fatalf("update cli: %d %s", rec.Code, rec.Body.String())
	}
	if e := storedEntry(t, path, "cli"); stringField(e, "environment", "API_KEY") != "k3y" {
		t.Errorf("environment after the edit: %v", e)
	}
	if rec := updateRequest(t, "web", `{"transport":"http","url":"https://web.example.com/mcp","headers":{"Authorization":"Bearer new"}}`); rec.Code != http.StatusOK {
		t.Fatalf("update with a new value: %d", rec.Code)
	}
	if e := storedEntry(t, path, "web"); stringField(e, "headers", "Authorization") != "Bearer new" {
		t.Errorf("a new value wasn't saved: %v", e)
	}
}

// A saved credential never goes to an address or command it wasn't saved
// for: otherwise an edit could send the GitHub token to any server.
func TestAHiddenValueIsRefusedForAnotherTarget(t *testing.T) {
	for name, c := range map[string]struct {
		server string
		do     func(t *testing.T) *httptest.ResponseRecorder
	}{
		"another address": {"web", func(t *testing.T) *httptest.ResponseRecorder {
			return updateRequest(t, "web", `{"transport":"http","url":"https://evil.example.com/mcp","headers":{"Authorization":"`+hiddenValue+`"}}`)
		}},
		"another command": {"cli", func(t *testing.T) *httptest.ResponseRecorder {
			return updateRequest(t, "cli", `{"transport":"stdio","command":"npx","args":["-y","evil"],"env":{"API_KEY":"`+hiddenValue+`"}}`)
		}},
		"an add replacing it elsewhere": {"web", func(t *testing.T) *httptest.ResponseRecorder {
			return addRequest(t, `{"name":"web","transport":"http","url":"https://evil.example.com/mcp","headers":{"Authorization":"`+hiddenValue+`"}}`)
		}},
		"a new server": {"", func(t *testing.T) *httptest.ResponseRecorder {
			return addRequest(t, `{"name":"other","transport":"http","url":"https://web.example.com/mcp","headers":{"Authorization":"`+hiddenValue+`"}}`)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			path := withGlobalConfig(t, hiddenSeed)
			before := storedEntry(t, path, c.server)
			rec := c.do(t)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "again") {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			after, _ := json.Marshal(storedEntry(t, path, c.server))
			if was, _ := json.Marshal(before); string(after) != string(was) {
				t.Errorf("a refused request changed the server: %s", after)
			}
			if _, ok := readGlobal(t, path)["other"]; ok {
				t.Error("a refused add was saved")
			}
		})
	}
}

// Checking the server being edited may use its saved values, at its saved
// address only.
func TestACheckUsesHiddenValuesOnlyAtTheSavedAddress(t *testing.T) {
	seen := make(chan string, 8)
	answer := func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"serverInfo": map[string]any{"name": "x"}}})
	}
	saved := httptest.NewServer(http.HandlerFunc(answer))
	defer saved.Close()
	other := httptest.NewServer(http.HandlerFunc(answer))
	defer other.Close()
	withGlobalConfig(t, `{"mcp":{"web":{"type":"remote","url":"`+saved.URL+`","headers":{"Authorization":"Bearer s3cret"},"enabled":true}}}`)

	if res := probe(t, map[string]any{"name": "web", "transport": "http", "url": saved.URL, "headers": map[string]string{"Authorization": hiddenValue}}); !res.OK {
		t.Fatalf("check at the saved address: %q", res.Error)
	}
	if got := <-seen; got != "Bearer s3cret" {
		t.Errorf("sent %q, want the saved value", got)
	}
	// The rest of that check's requests.
	for len(seen) > 0 {
		<-seen
	}
	for _, body := range []map[string]any{
		{"name": "web", "transport": "http", "url": other.URL, "headers": map[string]string{"Authorization": hiddenValue}},
		{"transport": "http", "url": saved.URL, "headers": map[string]string{"Authorization": hiddenValue}},
	} {
		if res := probe(t, body); res.OK || !strings.Contains(res.Error, "Authorization") {
			t.Errorf("%v: ok=%v error=%q", body, res.OK, res.Error)
		}
	}
	select {
	case got := <-seen:
		t.Errorf("a refused check still reached a server, with %q", got)
	default:
	}
}
