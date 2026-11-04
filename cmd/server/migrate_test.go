// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("KWA_WORKSPACE_HEARTBEAT", "false")
	bin1 := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(bin1, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KWA_OPENCODE_BIN", bin1)
	host, port, bin, major, set := config.Host, config.Port, config.OpencodeBin, config.OpencodeMajor, config.OpencodeMajorSet
	t.Cleanup(func() {
		config.Host, config.Port, config.OpencodeBin = host, port, bin
		config.OpencodeMajor, config.OpencodeMajorSet = major, set
		prepareOpencode, runMigration, reloadConfig = ensureOpencode, migration.Run, config.Init
		migrationState.Store(nil)
	})
	config.Host, config.Port = "127.0.0.1", 0

	var calls []string
	runMigration = func(o migration.Options) error {
		calls = append(calls, fmt.Sprintf("run %d.x %s", config.OpencodeMajor, o.OpencodeBin))
		return &migration.Refused{Reasons: []string{"stopped by the test"}}
	}
	reloadConfig = func() { calls = append(calls, "reload") }
	db := migration.OpencodeDatabase()
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		mode    migration.Mode
		major   int
		set     bool // by KWA_OPENCODE_MAJOR
		noDB    bool
		install error
		want    []string
	}{
		{"installed first", migration.On, 1, false, false, nil, []string{"install", "run 1.x /pinned/opencode", "reload"}},
		{"install failed", migration.On, 1, false, false, errors.New("offline"), []string{"install", "run 1.x /other/opencode", "reload"}},
		{"no chats to re-point", migration.On, 1, false, true, nil, []string{"run 1.x /other/opencode", "reload"}},
		{"on 2.x by default", migration.On, 2, false, true, nil, []string{"run 1.x " + bin1, "reload"}},
		{"KWA_OPENCODE_MAJOR=2", migration.On, 2, true, false, nil, nil},
		{"nothing to undo", migration.Undo, 1, false, false, nil, nil},
	} {
		calls, config.OpencodeBin = nil, "/other/opencode"
		config.OpencodeMajor, config.OpencodeMajorSet = c.major, c.set
		if c.noDB {
			_ = os.Remove(db)
		} else if err := os.WriteFile(db, nil, 0o600); err != nil {
			t.Fatal(err)
		}
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
