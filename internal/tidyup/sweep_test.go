// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package tidyup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// enableHygiene turns the sweep on with the given empty-chat window and restores
// the previous settings afterwards, so one test cannot leak policy into another.
func enableHygiene(t *testing.T, emptyAfter time.Duration) {
	t.Helper()
	prevEnabled, prevWindow := config.HygieneEnabled, config.HygieneEmptyAfter
	t.Cleanup(func() {
		config.HygieneEnabled, config.HygieneEmptyAfter = prevEnabled, prevWindow
	})
	config.HygieneEnabled = true
	config.HygieneEmptyAfter = emptyAfter
}

// chatWithWorkdir creates an empty chat with a real auto-created folder on disk,
// idle by the given amount.
func chatWithWorkdir(t *testing.T, base, id string, idleFor time.Duration) string {
	t.Helper()
	dir := filepath.Join(base, "chat-"+id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(id, config.SessionConfig{Label: "Scratch", Workdir: dir}); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(-idleFor).UTC().Format(time.RFC3339)
	if _, err := db.DB.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`, ts, id); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSweepDeletesIdleEmptyChatsAndTheirFolders(t *testing.T) {
	setupDB(t)
	base := t.TempDir()
	config.Workspace = base
	enableHygiene(t, 24*time.Hour)

	stale := chatWithWorkdir(t, base, "aaaaaaaaaaaa", 48*time.Hour)
	fresh := chatWithWorkdir(t, base, "bbbbbbbbbbbb", 2*time.Hour)

	res := SweepOnce(time.Now())

	if res.Deleted != 1 {
		t.Errorf("deleted = %d, want 1", res.Deleted)
	}
	if res.WorkdirsRemoved != 1 {
		t.Errorf("workdirs removed = %d, want 1", res.WorkdirsRemoved)
	}
	if db.SessionExists("aaaaaaaaaaaa") {
		t.Error("the idle empty chat should be gone")
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("its folder should be gone, stat err = %v", err)
	}
	if !db.SessionExists("bbbbbbbbbbbb") {
		t.Error("a chat idle for only two hours must survive")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("its folder must survive: %v", err)
	}
}

// A chat with a message is not the sweeper's business, however old it is.
// This is the line the whole design rests on: automation only touches items
// with nothing in them.
func TestSweepNeverTouchesAChatContainingAMessage(t *testing.T) {
	setupDB(t)
	base := t.TempDir()
	config.Workspace = base
	enableHygiene(t, 24*time.Hour)

	dir := chatWithWorkdir(t, base, "cccccccccccc", 365*24*time.Hour)
	if err := db.AddMessage("cccccccccccc", "user", "hello"); err != nil {
		t.Fatal(err)
	}
	// AddMessage bumps updated_at; force it back so only the message saves it.
	ts := time.Now().Add(-365 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := db.DB.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`, ts, "cccccccccccc"); err != nil {
		t.Fatal(err)
	}

	res := SweepOnce(time.Now())

	if res.Deleted != 0 {
		t.Errorf("deleted = %d, want 0", res.Deleted)
	}
	if !db.SessionExists("cccccccccccc") {
		t.Fatal("a year-old chat with one message was deleted")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("its folder must survive: %v", err)
	}
}

// A chat can look idle in the database while a turn is running against it —
// the process registry is the only place that knows. It must be skipped, and
// its folder left alone.
func TestSweepSkipsAChatWithWorkInFlight(t *testing.T) {
	setupDB(t)
	base := t.TempDir()
	config.Workspace = base
	enableHygiene(t, 24*time.Hour)

	dir := chatWithWorkdir(t, base, "dddddddddddd", 48*time.Hour)
	chat.Procs.Store("dddddddddddd", &chat.ActiveProcess{})
	t.Cleanup(func() { chat.Procs.Delete("dddddddddddd") })

	res := SweepOnce(time.Now())

	if res.Skipped != 1 || res.Deleted != 0 {
		t.Errorf("skipped = %d, deleted = %d; want 1 and 0", res.Skipped, res.Deleted)
	}
	if !db.SessionExists("dddddddddddd") {
		t.Fatal("a chat with a running process was deleted")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("its folder must survive: %v", err)
	}
}

// A chat whose workdir the user chose is deleted from the database like any
// other empty chat, but the directory is not ours to remove.
func TestSweepLeavesAUserChosenFolderAlone(t *testing.T) {
	setupDB(t)
	base := t.TempDir()
	config.Workspace = base
	enableHygiene(t, 24*time.Hour)

	chosen := filepath.Join(base, "my-existing-project")
	if err := os.MkdirAll(chosen, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession("eeeeeeeeeeee", config.SessionConfig{Label: "Work", Workdir: chosen}); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := db.DB.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`, ts, "eeeeeeeeeeee"); err != nil {
		t.Fatal(err)
	}

	res := SweepOnce(time.Now())

	if res.Deleted != 1 {
		t.Errorf("deleted = %d, want 1", res.Deleted)
	}
	if res.WorkdirsRemoved != 0 {
		t.Errorf("workdirs removed = %d, want 0", res.WorkdirsRemoved)
	}
	if _, err := os.Stat(chosen); err != nil {
		t.Fatalf("a user-chosen folder was destroyed: %v", err)
	}
}

// The disable switch has to be absolute: with hygiene off, nothing is deleted
// however stale it looks.
func TestSweepDoesNothingWhenDisabled(t *testing.T) {
	setupDB(t)
	base := t.TempDir()
	config.Workspace = base

	cases := []struct {
		name       string
		enabled    bool
		emptyAfter time.Duration
	}{
		{"hygiene disabled", false, 24 * time.Hour},
		{"empty-chat window set to zero", true, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "ffffffffff" + tc.name[:2]
			dir := chatWithWorkdir(t, base, id, 400*24*time.Hour)
			enableHygiene(t, tc.emptyAfter)
			config.HygieneEnabled = tc.enabled

			res := SweepOnce(time.Now())

			if res.Considered != 0 || res.Deleted != 0 {
				t.Errorf("considered = %d, deleted = %d; want 0 and 0", res.Considered, res.Deleted)
			}
			if !db.SessionExists(id) {
				t.Error("the chat should survive")
			}
			if _, err := os.Stat(dir); err != nil {
				t.Errorf("its folder should survive: %v", err)
			}
		})
	}
}
