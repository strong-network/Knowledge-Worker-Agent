// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// home gives a test its own home folder and root, with no old settings.
func home(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv(layout.EnvHome, filepath.Join(h, layout.DefaultDirName))
	t.Setenv("COPILOT_DB_PATH", "")
	t.Cleanup(func() { oldDatabases = layout.OldDatabases })
	oldDatabases = func() []string {
		var mine []string
		for _, p := range layout.OldDatabases() {
			if strings.HasPrefix(p, h+string(filepath.Separator)) {
				mine = append(mine, p)
			}
		}
		return mine
	}
	return h
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestANewWorkspaceIsMarkedAndStarts(t *testing.T) {
	home(t)
	if why, ok := Unmoved(); ok {
		t.Fatalf("a new workspace was stopped: %s", why)
	}
	if err := MarkNew(); err != nil {
		t.Fatal(err)
	}
	if !layout.Migrated() {
		t.Fatal("not marked")
	}
	if got, want := layout.Database(), filepath.Join(layout.System(), "knowledge-worker-agent.db"); got != want {
		t.Errorf("database at %s, want the root's %s", got, want)
	}
	if _, ok := Unmoved(); ok {
		t.Error("stopped once marked")
	}
}

func TestAWorkspaceOnTheOldLayoutIsStopped(t *testing.T) {
	for name, old := range map[string]string{
		"default":    ".copilot-web.db",
		"image":      "copilot-web.db",
		"by setting": "elsewhere/chats.db",
	} {
		t.Run(name, func(t *testing.T) {
			h := home(t)
			if name == "by setting" {
				t.Setenv("COPILOT_DB_PATH", filepath.Join(h, old))
			}
			write(t, filepath.Join(h, old), "db")
			why, ok := Unmoved()
			if !ok || !strings.Contains(why, filepath.Join(h, old)) {
				t.Fatalf("Unmoved = %q, %v; want it stopped, naming the database", why, ok)
			}
		})
	}
}

// A move that didn't finish, or was undone, leaves its run in the migration
// folder and no completion record.
func TestAnUnfinishedOrUndoneMoveIsStopped(t *testing.T) {
	home(t)
	write(t, filepath.Join(layout.MigrationDir(), "20261001T144341Z", journalName), `{"op":"begin"}`+"\n")
	if why, ok := Unmoved(); !ok || !strings.Contains(why, "didn't finish") {
		t.Fatalf("Unmoved = %q, %v", why, ok)
	}
}

func TestStrayDatabase(t *testing.T) {
	h := home(t)
	old := filepath.Join(h, ".copilot-web.db")
	moved, _ := json.Marshal([]layout.Rename{{From: old, To: filepath.Join(layout.System(), "knowledge-worker-agent.db")}})
	write(t, layout.MovedFile(), string(moved))
	write(t, layout.CompletionFile(), "run\n")
	if _, ok := StrayDatabase(); ok {
		t.Fatal("stray without a database at the old place")
	}
	write(t, old, "db")
	if got, ok := StrayDatabase(); !ok || got != old {
		t.Fatalf("StrayDatabase = %s, %v", got, ok)
	}
}

func TestPruneRemovesOnlyOldCopiesOfFinishedRuns(t *testing.T) {
	home(t)
	run := func(name string, ops ...string) string {
		dir := filepath.Join(layout.MigrationDir(), name)
		var lines string
		for _, op := range ops {
			lines += `{"op":"` + op + `"}` + "\n"
		}
		write(t, filepath.Join(dir, journalName), lines)
		write(t, filepath.Join(dir, "database.db"), "copy")
		write(t, filepath.Join(dir, "report.txt"), "report")
		return dir
	}
	done := run("done", "begin", "complete")
	going := run("going", "begin")

	if n, err := Prune(time.Now()); err != nil || n != 0 {
		t.Fatalf("Prune now = %d, %v", n, err)
	}
	if n, err := Prune(time.Now().Add(Retention + time.Hour)); err != nil || n != 1 {
		t.Fatalf("Prune after 30 days = %d, %v", n, err)
	}
	if exists(filepath.Join(done, "database.db")) || !exists(filepath.Join(done, "report.txt")) {
		t.Error("the finished run kept its copy, or lost its report")
	}
	if !exists(filepath.Join(going, "database.db")) {
		t.Error("pruned a run that hasn't finished")
	}
}
