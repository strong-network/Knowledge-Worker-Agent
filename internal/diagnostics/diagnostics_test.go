// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package diagnostics

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func mkfile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHiddenItemsAreTheUsersOwnNamesakes(t *testing.T) {
	platform, user := t.TempDir(), t.TempDir()
	t.Setenv("OPENCODE_CONFIG_DIR", platform)
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", filepath.Join(user, "opencode.json"))
	mkfile(t, filepath.Join(platform, "agent", "writer.md"))
	mkfile(t, filepath.Join(platform, "agent", "reviewer.md"))
	mkfile(t, filepath.Join(user, "agent", "writer.md"))
	mkfile(t, filepath.Join(user, "agent", "mine.md"))
	mkfile(t, filepath.Join(platform, "skills", "slides", "SKILL.md"))
	mkfile(t, filepath.Join(platform, "skills", "pdf", "SKILL.md"))
	mkfile(t, filepath.Join(user, "skill", "slides", "SKILL.md"))
	// The bundled pdf skill in the user's dir is Knowledge Worker Agent's own copy.
	mkfile(t, filepath.Join(user, "skills", "pdf", "SKILL.md"))

	got := hiddenItems(platform, user)
	want := []Item{{"agent", "writer"}, {"skill", "slides"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hiddenItems = %+v, want %+v", got, want)
	}
	if got := hiddenItems("", user); len(got) != 0 {
		t.Fatalf("no assigned config: %+v", got)
	}
}

func TestSettingsShowValuesSafely(t *testing.T) {
	t.Setenv("KWA_PROJECT_ID", "proj_team")
	t.Setenv("KWA_CONFIG_REPO_URL", "")
	t.Setenv("SDS_CONFIG_REPO_URL", "https://alex:tok123@example.com/config.git")
	t.Setenv("KWA_EXAMPLE_TOKEN", "abc")
	t.Setenv("KWA_NOTE", "Bearer abcdefghijklmnop")

	got := map[string]Setting{}
	for _, s := range collectSettings() {
		got[s.Name] = s
	}
	for name, want := range map[string]Setting{
		"KWA_PROJECT_ID":      {Name: "KWA_PROJECT_ID", Value: "proj_team"},
		"KWA_CONFIG_REPO_URL": {Name: "KWA_CONFIG_REPO_URL", Value: "https://example.com/config.git", From: "SDS_CONFIG_REPO_URL"},
		"KWA_EXAMPLE_TOKEN":   {Name: "KWA_EXAMPLE_TOKEN", Value: "(set)"},
		"KWA_NOTE":            {Name: "KWA_NOTE", Value: "Bearer " + redacted},
	} {
		if got[name] != want {
			t.Errorf("%s = %+v, want %+v", name, got[name], want)
		}
	}
}

func TestReportEncodesListsAsArrays(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", filepath.Join(t.TempDir(), "opencode.json"))
	Configure(Sources{Version: "1.2.3-abc", Status: func() string { return "ready" }})
	rec := httptest.NewRecorder()
	HandleReport(rec, httptest.NewRequest("GET", "/api/diagnostics", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d, headers %v", rec.Code, rec.Header())
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if v := raw["app"].(map[string]any)["version"]; v != "1.2.3-abc" {
		t.Fatalf("app.version = %v", v)
	}
	// The dialog iterates these without guarding against null.
	for _, path := range []string{"settings", "old_settings", "paths", "config.hidden", "config.kept_defaults", "providers.items", "connectors.not_connected"} {
		var v any = raw
		for _, key := range strings.Split(path, ".") {
			v = v.(map[string]any)[key]
		}
		if _, ok := v.([]any); !ok {
			t.Errorf("%s = %#v, want an array", path, v)
		}
	}
}

func TestLogHandler(t *testing.T) {
	log := filepath.Join(t.TempDir(), "kwa.log")
	writeLines(t, log, 1, 3)
	Configure(Sources{LogFile: log})

	rec := httptest.NewRecorder()
	HandleLog(rec, httptest.NewRequest("GET", "/api/diagnostics/log?source=nope", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad source: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	HandleLog(rec, httptest.NewRequest("GET", "/api/diagnostics/log?lines=2", nil))
	var tail LogTail
	if err := json.Unmarshal(rec.Body.Bytes(), &tail); err != nil || !reflect.DeepEqual(tail.Lines, lines(2, 3)) {
		t.Fatalf("view: %v %+v", err, tail)
	}

	rec = httptest.NewRecorder()
	HandleLog(rec, httptest.NewRequest("GET", "/api/diagnostics/log?source=kwa&download=1", nil))
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="kwa-log-`) {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	if rec.Body.String() != "line 1\nline 2\nline 3\n" {
		t.Fatalf("download body = %q", rec.Body.String())
	}
}

func TestPathSizeCountsTheSubfolderInOneWalk(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".system", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".system", "logs", "b"), make([]byte, 30), 0o644); err != nil {
		t.Fatal(err)
	}
	total, sub, partial := pathSize(root, filepath.Join(root, ".system"), time.Now().Add(time.Minute))
	if total != 130 || sub != 30 || partial {
		t.Fatalf("pathSize = %d, %d, %v; want 130, 30, false", total, sub, partial)
	}
	if _, _, partial := pathSize(root, "", time.Now().Add(-time.Second)); !partial {
		t.Fatal("an expired deadline should report a partial walk")
	}
}
