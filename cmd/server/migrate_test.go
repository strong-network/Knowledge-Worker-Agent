// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/migration"
)

func TestMigrationAtStartup(t *testing.T) {
	for v, want := range map[string]migration.Mode{
		"":        migration.On,
		"dry-run": migration.DryRun,
		"on":      migration.On,
		"undo":    migration.Undo,
		"maybe":   migration.Off,
	} {
		t.Setenv(migration.EnvMode, v)
		if got := migrationAtStartup(); got != want {
			t.Errorf("%s=%q: got %q, want %q", migration.EnvMode, v, got, want)
		}
	}
}

func TestRunMigrateUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"maybe"}, {"dry-run", "extra"}} {
		if got := runMigrate(args); got != 2 {
			t.Errorf("runMigrate(%q) = %d, want 2", args, got)
		}
	}
}

func TestTheStatusServerAnswersEverything(t *testing.T) {
	s := &statusPage{}
	s.set(migration.Progress{Phase: "Updating chat history", Done: 120, Total: 311, Left: 9*time.Minute + time.Second})
	h := statusHandler(s)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	if rec := get("/healthz"); rec.Code != 200 || rec.Body.String() != "ok" {
		t.Errorf("/healthz: %d %q", rec.Code, rec.Body.String())
	}

	rec := get("/api/status")
	var st struct {
		Status    string        `json:"status"`
		Ready     bool          `json:"opencode_ready"`
		Migration migrationView `json:"migration"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || st.Status != "migrating" || st.Ready {
		t.Fatalf("/api/status: %v %s", err, rec.Body.String())
	}
	if m := st.Migration; m.State != "running" || m.Done != 120 || m.Total != 311 || m.Minutes != 10 || m.Phase != "Updating chat history" {
		t.Errorf("/api/status migration: %+v", m)
	}

	rec = get("/api/sessions")
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Errorf("/api/sessions: %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}

	for _, path := range []string{"/", "/some/page"} {
		rec = get(path)
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" ||
			!strings.Contains(rec.Body.String(), "Don't stop or restart the workspace until this finishes.") {
			t.Errorf("%s: %d, Cache-Control %q", path, rec.Code, rec.Header().Get("Cache-Control"))
		}
	}
}

func TestViewOf(t *testing.T) {
	v := viewOf(migration.Report{Duration: 14 * time.Minute, NeedBytes: 3_500_000_000, FreeBytes: 15_200_000_000})
	if v.State != "planned" || v.Minutes != 14 || v.Need != "3.5 GB" || v.Free != "15.2 GB" || v.Short != "" {
		t.Errorf("planned: %+v", v)
	}
	v = viewOf(migration.Report{
		NeedBytes: 3_500_000_000, FreeBytes: 1_100_000_000,
		Problems: []migration.Problem{{Check: "disk space", Detail: "needs 3.5 GB"}},
	})
	if v.State != "blocked" || v.Short != "2.4 GB" || len(v.Reasons) != 1 || v.Undo {
		t.Errorf("blocked: %+v", v)
	}
	if v := viewOf(migration.Report{Undo: true}); !v.Undo {
		t.Errorf("undo: %+v", v)
	}
}

func TestTheStatusPageSaysWhenItsAnUndo(t *testing.T) {
	rec := httptest.NewRecorder()
	statusHandler(&statusPage{undo: true}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if !strings.Contains(rec.Body.String(), `"undo":true`) {
		t.Errorf("/api/status: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	statusHandler(&statusPage{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if strings.Contains(rec.Body.String(), `"undo"`) {
		t.Errorf("/api/status of a migration: %s", rec.Body.String())
	}
}

func TestTheMigrationRunsWithThePinnedOpencode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KWA_WORKSPACE_HEARTBEAT", "false")
	host, port, bin := config.Host, config.Port, config.OpencodeBin
	t.Cleanup(func() {
		config.Host, config.Port, config.OpencodeBin = host, port, bin
		prepareOpencode, runMigration = ensureOpencode, migration.Run
		migrationState.Store(nil)
	})
	config.Host, config.Port = "127.0.0.1", 0

	var calls []string
	runMigration = func(o migration.Options) error {
		calls = append(calls, "run with "+o.OpencodeBin)
		return &migration.Refused{Reasons: []string{"stopped by the test"}}
	}
	for _, c := range []struct {
		name    string
		mode    migration.Mode
		install error
		want    []string
	}{
		{"installed first", migration.On, nil, []string{"install", "run with /pinned/opencode"}},
		{"install failed", migration.On, errors.New("offline"), []string{"install", "run with /other/opencode"}},
		{"nothing to undo", migration.Undo, nil, nil},
	} {
		calls, config.OpencodeBin = nil, "/other/opencode"
		prepareOpencode = func() error {
			calls = append(calls, "install")
			if c.install == nil {
				config.OpencodeBin = "/pinned/opencode"
			}
			return c.install
		}
		runMigrationAtStartup(c.mode)
		if !slices.Equal(calls, c.want) {
			t.Errorf("%s: %q, want %q", c.name, calls, c.want)
		}
	}
}
