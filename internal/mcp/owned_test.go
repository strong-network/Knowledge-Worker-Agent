// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
)

func withOwnedReset(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		ownedMu.Lock()
		owned = map[string]bool{}
		ownedMu.Unlock()
	})
}

func recallAt(bin string, port string) DefaultServer {
	return DefaultServer{
		Name: "recall", Transport: "stdio", Command: bin,
		Args: []string{"mcp-recall", "--url", "http://127.0.0.1:" + port}, Auth: AuthLocal, Enabled: true,
	}
}

// A moved binary left recall pointing at a file that no longer exists, and
// add-if-absent never corrected it. Every start must bring the definition up to
// date, and keep the user's on/off choice and anything else in the entry.
func TestEnsureOwnedServerRewritesAStaleDefinition(t *testing.T) {
	withOwnedReset(t)
	path := withGlobalConfig(t, `{"mcp":{"recall":{"type":"local",
		"command":["/old/build/copilot-web-ui","mcp-recall","--url","http://127.0.0.1:1"],
		"enabled":false,"timeout":30,"x-note":"kept"}}}`)

	var log strings.Builder
	EnsureOwnedServer(&log, recallAt("/new/build/knowledge-worker-agent", "8765"))

	sv := findServer(t, "recall")
	if sv.Command != "/new/build/knowledge-worker-agent" || !slices.Equal(sv.Args, []string{"mcp-recall", "--url", "http://127.0.0.1:8765"}) {
		t.Errorf("definition = %s %v, want the new binary and port", sv.Command, sv.Args)
	}
	if sv.Enabled {
		t.Error("the user had switched recall off; the rewrite switched it on")
	}
	if sv.Timeout != 30 {
		t.Errorf("timeout = %d, want the user's 30 kept", sv.Timeout)
	}
	if entry, _ := readGlobal(t, path)["recall"].(map[string]any); entry["x-note"] != "kept" {
		t.Errorf("an unmanaged key was lost: %v", entry)
	}
	if !strings.Contains(log.String(), `updated "recall"`) || !strings.Contains(log.String(), "/old/build/copilot-web-ui") {
		t.Errorf("log = %q, want it to say what changed", log.String())
	}
}

func TestEnsureOwnedServerFirstWriteAndNoChurn(t *testing.T) {
	withOwnedReset(t)
	path := withGlobalConfig(t, "")

	EnsureOwnedServer(nil, recallAt("/bin/kwa", "8765"))
	if sv := findServer(t, "recall"); !sv.Enabled || sv.Command != "/bin/kwa" {
		t.Fatalf("first write = %+v, want it registered and on", sv)
	}

	before, _ := os.Stat(path)
	var log strings.Builder
	EnsureOwnedServer(&log, recallAt("/bin/kwa", "8765"))
	after, _ := os.Stat(path)
	if log.Len() != 0 || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("an unchanged definition was rewritten (log %q)", log.String())
	}
}

// An edit or a removal would undo itself at the next start, so both are
// refused; switching it on and off is the user's and survives.
func TestOwnedServerCanBeSwitchedButNotChanged(t *testing.T) {
	withOwnedReset(t)
	withGlobalConfig(t, "")
	EnsureOwnedServer(nil, recallAt("/bin/kwa", "8765"))

	for _, c := range []struct {
		method string
		handle func(http.ResponseWriter, *http.Request)
		body   string
	}{
		{http.MethodPut, HandleUpdate, `{"transport":"stdio","command":"/elsewhere"}`},
		{http.MethodDelete, HandleRemove, ""},
		{http.MethodPost, HandleAdd, `{"name":"recall","transport":"stdio","command":"/elsewhere"}`},
	} {
		req := httptest.NewRequest(c.method, "/api/mcp/servers/recall", strings.NewReader(c.body))
		req.SetPathValue("name", "recall")
		rec := httptest.NewRecorder()
		c.handle(rec, req)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "set up by Knowledge Worker Agent") {
			t.Errorf("%s = %d %s, want 409 with the reason", c.method, rec.Code, rec.Body.String())
		}
	}
	if sv := findServer(t, "recall"); sv.Command != "/bin/kwa" || !sv.Owned {
		t.Errorf("recall = %+v, want it unchanged and marked owned", sv)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/mcp/servers/recall/enabled", strings.NewReader(`{"enabled":false}`))
	req.SetPathValue("name", "recall")
	rec := httptest.NewRecorder()
	HandleSetEnabled(rec, req)
	if rec.Code != http.StatusOK || findServer(t, "recall").Enabled {
		t.Errorf("switching off = %d, enabled %v; want it off", rec.Code, findServer(t, "recall").Enabled)
	}
}

func TestOnlyOwnedServersAreMarked(t *testing.T) {
	withOwnedReset(t)
	withGlobalConfig(t, `{"mcp":{"mine":{"type":"local","command":["/bin/sh"],"enabled":false}}}`)
	EnsureOwnedServer(nil, recallAt("/bin/kwa", "8765"))
	if findServer(t, "mine").Owned {
		t.Error("a server the user added is marked owned")
	}
}

func TestWarnMissingCommands(t *testing.T) {
	withGlobalConfig(t, `{"mcp":{
		"gone":{"type":"local","command":["/nonexistent/kwa","mcp-recall"],"enabled":true},
		"gone-but-off":{"type":"local","command":["/nonexistent/other"],"enabled":false},
		"present":{"type":"local","command":["/bin/sh"],"enabled":true},
		"remote":{"type":"remote","url":"https://mcp.example.com/mcp","enabled":true}
	}}`)
	var log strings.Builder
	WarnMissingCommands(&log)
	if !strings.Contains(log.String(), `"gone" is on, but its program /nonexistent/kwa isn't found`) {
		t.Errorf("log = %q, want a warning for the missing program", log.String())
	}
	for _, quiet := range []string{"gone-but-off", "present", "remote"} {
		if strings.Contains(log.String(), `"`+quiet+`"`) {
			t.Errorf("warned about %q: %q", quiet, log.String())
		}
	}
}
