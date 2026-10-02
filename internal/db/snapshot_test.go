// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// seedSnapshotDB opens a database at path with some real content in it.
func seedSnapshotDB(t *testing.T, path string) {
	t.Helper()
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`CREATE TABLE IF NOT EXISTS snap (v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if _, err := DB.Exec(`INSERT INTO snap (v) VALUES ('row')`); err != nil {
			t.Fatal(err)
		}
	}
}

func snapRows(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM snap`).Scan(&n); err != nil {
		t.Fatalf("reading snapshot %s: %v", path, err)
	}
	return n
}

func TestSnapshotWritesReadableCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat.db")
	seedSnapshotDB(t, path)
	t.Cleanup(Close)

	got, err := Snapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if got == "" {
		t.Fatal("no snapshot written")
	}
	if n := snapRows(t, got); n != 100 {
		t.Errorf("snapshot has %d rows, want 100", n)
	}
	if filepath.Dir(got) != SnapshotDir(path) {
		t.Errorf("snapshot in %s, want %s", filepath.Dir(got), SnapshotDir(path))
	}
}

// The reason this feature uses VACUUM INTO rather than copying the file. A
// workspace is powered down without warning, so rows committed since the last
// checkpoint are still in the -wal sidecar. A snapshot must capture them.
func TestSnapshotCapturesUncheckpointedWAL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat.db")
	seedSnapshotDB(t, path)
	t.Cleanup(Close)

	// Precondition: the rows really are still in the WAL, not the main file.
	// Without this the test would pass for the wrong reason.
	if fi, err := os.Stat(path + "-wal"); err != nil || fi.Size() == 0 {
		t.Skipf("no uncheckpointed WAL to test against (size err=%v)", err)
	}
	plain, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	naive := filepath.Join(dir, "naive.db")
	if err := os.WriteFile(naive, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	ndb, err := sql.Open("sqlite", "file:"+naive+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	naiveErr := ndb.QueryRow(`SELECT COUNT(*) FROM snap`).Scan(&n)
	ndb.Close()
	if naiveErr == nil && n == 100 {
		t.Skip("main db file was already complete; nothing for VACUUM INTO to recover here")
	}

	// The actual assertion: the snapshot has everything the naive copy lost.
	got, err := Snapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if rows := snapRows(t, got); rows != 100 {
		t.Errorf("snapshot has %d rows, want 100 (naive copy had %d, err=%v)", rows, n, naiveErr)
	}
}

func TestSnapshotSkipsWhenRecent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat.db")
	seedSnapshotDB(t, path)
	t.Cleanup(Close)

	now := time.Now()
	first, err := snapshotAt(path, now)
	if err != nil || first == "" {
		t.Fatalf("first snapshot: %q err=%v", first, err)
	}

	second, err := snapshotAt(path, now.Add(SnapshotMinInterval-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if second != "" {
		t.Errorf("snapshot taken %v after the last one, want skip", SnapshotMinInterval-time.Minute)
	}

	// Nudge the newest snapshot's mtime back past the interval, which is what
	// the real clock does between boots.
	old := now.Add(-2 * SnapshotMinInterval)
	if err := os.Chtimes(first, old, old); err != nil {
		t.Fatal(err)
	}
	third, err := snapshotAt(path, now)
	if err != nil {
		t.Fatal(err)
	}
	if third == "" {
		t.Error("snapshot skipped even though the newest is older than the interval")
	}
}

func TestSnapshotKeepsOnlyTheNewestFive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat.db")
	seedSnapshotDB(t, path)
	t.Cleanup(Close)

	start := time.Now().Add(-30 * 24 * time.Hour)
	var written []string
	for i := 0; i < SnapshotKeep+3; i++ {
		at := start.Add(time.Duration(i) * 2 * SnapshotMinInterval)
		got, err := snapshotAt(path, at)
		if err != nil {
			t.Fatal(err)
		}
		if got == "" {
			t.Fatalf("snapshot %d skipped unexpectedly", i)
		}
		written = append(written, got)
		// Age it so the next call is past the interval.
		if err := os.Chtimes(got, at, at); err != nil {
			t.Fatal(err)
		}
	}

	kept := snapshotList(SnapshotDir(path))
	if len(kept) != SnapshotKeep {
		t.Fatalf("kept %d snapshots, want %d", len(kept), SnapshotKeep)
	}
	// The oldest must be gone and the newest must remain.
	if _, err := os.Stat(written[0]); !os.IsNotExist(err) {
		t.Errorf("oldest snapshot %s still present", written[0])
	}
	newest := written[len(written)-1]
	if _, err := os.Stat(newest); err != nil {
		t.Errorf("newest snapshot %s was pruned: %v", newest, err)
	}
	// Every survivor must still be a usable database, not a truncated file.
	for _, p := range kept {
		if n := snapRows(t, p); n != 100 {
			t.Errorf("%s has %d rows, want 100", p, n)
		}
	}
}

// A fresh workspace has nothing worth copying, and must not accrue an empty
// snapshot directory. Note the file is not empty by the time this is checked --
// opening the database creates a page -- so the guard has to be the absence of
// tables, which is why this test asserts on the directory rather than trusting
// a size check. This is also what keeps snapshots out of the way of the other
// tests in this package, which all start from an empty temp file.
func TestSnapshotSkipsFreshDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fresh.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Open without migrating, so the database genuinely has no schema yet.
	var err error
	DB, err = sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	DB.SetMaxOpenConns(1)
	if _, err := DB.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Close)

	if fi, err := os.Stat(path); err != nil || fi.Size() == 0 {
		t.Fatalf("precondition: opening the db should have created pages (size err=%v)", err)
	}

	got, err := Snapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("snapshot %q taken for a database with no schema", got)
	}
	if _, err := os.Stat(SnapshotDir(path)); !os.IsNotExist(err) {
		t.Error("snapshot directory created for a fresh database")
	}
}

func TestSnapshotDisabledByEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat.db")
	seedSnapshotDB(t, path)
	t.Cleanup(Close)

	t.Setenv("KWA_DB_BACKUP", "off")
	got, err := Snapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("snapshot %q taken while disabled", got)
	}
	if _, err := os.Stat(SnapshotDir(path)); !os.IsNotExist(err) {
		t.Error("snapshot directory created while disabled")
	}
}

// VACUUM INTO refuses to write a file that already exists, so an interrupted
// run must not wedge every later attempt.
func TestSnapshotRecoversFromStaleTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat.db")
	seedSnapshotDB(t, path)
	t.Cleanup(Close)

	sdir := SnapshotDir(path)
	if err := os.MkdirAll(sdir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(sdir, snapshotTmp)
	if err := os.WriteFile(stale, []byte("half-written garbage"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Snapshot(path)
	if err != nil {
		t.Fatalf("stale temp file blocked the snapshot: %v", err)
	}
	if got == "" {
		t.Fatal("no snapshot written")
	}
	if n := snapRows(t, got); n != 100 {
		t.Errorf("snapshot has %d rows, want 100", n)
	}
}

// Init must take the snapshot before applying migrations, so the copy predates
// any schema change the new build makes.
//
// The discriminator is migrate()'s own tables: a snapshot taken before it runs
// cannot contain "sessions". Asserting only that pre-existing data survives
// would pass either way, since migrate adds tables rather than removing them.
func TestInitSnapshotsBeforeMigrating(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat.db")

	// A database from before this build: real content, but none of the schema
	// migrate() is about to create. Built without Init, which would migrate it.
	raw, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	if _, err := raw.Exec(`CREATE TABLE premigration (v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO premigration (v) VALUES ('before')`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	// The upgrade path: open, snapshot, then migrate.
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Close)

	// migrate() really did run, so the comparison below is meaningful.
	var live int
	if err := DB.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'sessions'`,
	).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatal("precondition: migrate() should have created the sessions table")
	}

	snaps := snapshotList(SnapshotDir(path))
	if len(snaps) == 0 {
		t.Fatal("Init did not write a snapshot")
	}
	db, err := sql.Open("sqlite", "file:"+snaps[0]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var v string
	if err := db.QueryRow(`SELECT v FROM premigration`).Scan(&v); err != nil {
		t.Fatalf("pre-migration data missing from the snapshot: %v", err)
	}
	if v != "before" {
		t.Errorf("premigration row = %q, want \"before\"", v)
	}

	var migrated int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'sessions'`,
	).Scan(&migrated); err != nil {
		t.Fatal(err)
	}
	if migrated != 0 {
		t.Error("snapshot contains migrate()'s schema, so it was taken after migrating, not before")
	}
}
