// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessions

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// A chat works only in the home folder: a new chat or a settings change can't
// point it at a hidden folder directly in the home folder, or outside it.
func TestAChatsFolderStaysInTheHomeFolder(t *testing.T) {
	setupTestDB(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	notes := filepath.Join(home, "Notes")
	if err := os.Mkdir(notes, 0o755); err != nil {
		t.Fatal(err)
	}

	newChat := func(workdir string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/sessions/new", strings.NewReader(`{"label":"Work","workdir":"`+workdir+`"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		HandleNewSession(w, req)
		return w
	}
	for _, dir := range []string{filepath.Join(home, ".ssh"), "/usr/share"} {
		if w := newChat(dir); w.Code != http.StatusForbidden {
			t.Errorf("new chat in %s: %d %s, want 403", dir, w.Code, w.Body.String())
		}
	}
	if w := newChat(notes); w.Code != http.StatusOK {
		t.Errorf("new chat in %s: %d %s, want 200", notes, w.Code, w.Body.String())
	}

	patch := func(sid, body string) int {
		req := httptest.NewRequest("PATCH", "/api/sessions/"+sid+"/config", strings.NewReader(body))
		req.SetPathValue("session_id", sid)
		w := httptest.NewRecorder()
		HandleUpdateSessionConfig(w, req)
		return w.Code
	}
	// A chat from before the limit keeps its folder through other changes.
	if err := db.CreateSession("legacy", config.SessionConfig{Label: "Old", Workdir: "/usr/share"}); err != nil {
		t.Fatal(err)
	}
	if cfg := patchConfig(t, "legacy", `{"model":"m1"}`); cfg.Workdir != "/usr/share" || cfg.Model != "m1" {
		t.Errorf("after a model change: %+v", cfg)
	}
	for _, dir := range []string{filepath.Join(home, ".config"), "/etc"} {
		if code := patch("legacy", `{"workdir":"`+dir+`"}`); code != http.StatusForbidden {
			t.Errorf("moving the chat to %s: %d, want 403", dir, code)
		}
	}
	if cfg, _ := db.GetSessionConfig("legacy"); cfg.Workdir != "/usr/share" {
		t.Errorf("a refused move changed the folder to %q", cfg.Workdir)
	}
	if cfg := patchConfig(t, "legacy", `{"workdir":"`+notes+`"}`); cfg.Workdir != notes {
		t.Errorf("moving the chat to %s: folder is %q", notes, cfg.Workdir)
	}
}
