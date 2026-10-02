// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// The DSN previously used mattn/go-sqlite3 parameter names, which
// modernc.org/sqlite ignores silently: the database ran in "delete" journal
// mode with no busy timeout while the connection string appeared to ask for
// WAL and 5s. These tests assert the settings actually took effect, which is
// the only way that class of bug is visible -- opening the database succeeds
// either way.

func TestInitEnablesWAL(t *testing.T) {
	setupTestDB(t)

	var mode string
	if err := DB.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want \"wal\" (DSN pragma syntax is probably wrong for modernc.org/sqlite)", mode)
	}
}

func TestInitSetsBusyTimeout(t *testing.T) {
	setupTestDB(t)

	var ms int
	if err := DB.QueryRow("PRAGMA busy_timeout").Scan(&ms); err != nil {
		t.Fatal(err)
	}
	if ms != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", ms)
	}
}

// A committed row must survive copying only the .db file. Under WAL it can
// live entirely in the -wal sidecar; SQLite checkpoints and removes the sidecar
// when the last connection closes, so a cleanly shut-down database stays
// self-contained. This guards that property -- it would fail if a future change
// left a second connection open past Close, or disabled the checkpoint on close.
func TestCloseCheckpointsWAL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ckpt.db")

	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`CREATE TABLE t (v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		if _, err := DB.Exec(`INSERT INTO t (v) VALUES ('committed')`); err != nil {
			t.Fatal(err)
		}
	}

	Close()

	// Copy only the main database file, as a snapshot or a support engineer would.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(dir, "copy.db")
	if err := os.WriteFile(cp, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", "file:"+cp+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
		t.Fatalf("copy of the .db file is unusable, so the WAL was not checkpointed on close: %v", err)
	}
	if n != 200 {
		t.Errorf("copied database has %d rows, want 200", n)
	}
}

// Init must be usable on a database a previous build left in rollback-journal
// mode: every existing workspace is in that state, so upgrading has to convert
// it rather than fail.
func TestInitUpgradesExistingDeleteModeDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")

	// Build a database the way the previous release did: the rollback journal,
	// with no WAL. The journal mode is set explicitly rather than by leaving a
	// DSN parameter to be ignored, because newer driver releases honour the
	// old parameter names and would quietly make this a WAL database.
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	old.SetMaxOpenConns(1)
	if _, err := old.Exec(`PRAGMA journal_mode=delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE legacy (v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO legacy (v) VALUES ('pre-existing')`); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := old.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "delete" {
		t.Fatalf("precondition: legacy db should be in delete mode, got %q", mode)
	}
	old.Close()

	if err := Init(path); err != nil {
		t.Fatalf("opening a pre-existing delete-mode database: %v", err)
	}
	t.Cleanup(Close)

	if err := DB.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode after upgrade = %q, want \"wal\"", mode)
	}

	var v string
	if err := DB.QueryRow(`SELECT v FROM legacy`).Scan(&v); err != nil {
		t.Fatalf("pre-existing data unreadable after upgrade: %v", err)
	}
	if v != "pre-existing" {
		t.Errorf("legacy row = %q, want \"pre-existing\"", v)
	}
}
