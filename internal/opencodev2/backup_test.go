// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodev2

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func countRows(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM session").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Before 2.x first opens opencode's database, KWA keeps a copy, once.
func TestKeepDatabaseBeforeV2(t *testing.T) {
	t.Setenv("KWA_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	keepDatabaseBeforeV2() // no database yet: nothing to keep
	if _, err := os.Stat(beforeV2()); !os.IsNotExist(err) {
		t.Fatalf("kept a copy of nothing: %v", err)
	}

	src := filepath.Join(dataDir(), "opencode.db")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+src)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"PRAGMA journal_mode=WAL", "CREATE TABLE session (id TEXT)", "INSERT INTO session VALUES ('ses_1'), ('ses_2')"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	keepDatabaseBeforeV2() // with the writer still open, as WAL leaves rows in the -wal file
	if n := countRows(t, beforeV2()); n != 2 {
		t.Fatalf("the copy has %d sessions, want 2", n)
	}
	if info, err := os.Stat(beforeV2()); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("copy: %v, mode %v", err, info.Mode())
	}

	// Later starts leave the copy as it was.
	if _, err := db.Exec("INSERT INTO session VALUES ('ses_3')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	keepDatabaseBeforeV2()
	if n := countRows(t, beforeV2()); n != 2 {
		t.Fatalf("the copy changed: %d sessions", n)
	}
}

// newDatabase writes an opencode database with a session table, and any tables
// named in extra.
func newDatabase(t *testing.T, extra ...string) string {
	t.Helper()
	src := filepath.Join(dataDir(), "opencode.db")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+src)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range append([]string{"CREATE TABLE session (id TEXT)", "INSERT INTO session VALUES ('ses_1')"}, extra...) {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return src
}

// A copy made after 2.x has opened the database wouldn't be of it before 2.x.
func TestKeepDatabaseBeforeV2SkipsADatabase2xHasOpened(t *testing.T) {
	t.Setenv("KWA_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	newDatabase(t, "CREATE TABLE session_v2 (id TEXT)")
	keepDatabaseBeforeV2()
	if _, err := os.Stat(beforeV2()); !os.IsNotExist(err) {
		t.Fatalf("kept a copy after 2.x: %v", err)
	}
}

func TestKeepDatabaseBeforeV2LeavesDiskSpace(t *testing.T) {
	t.Setenv("KWA_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	src := newDatabase(t)
	info, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	old := freeSpace
	t.Cleanup(func() { freeSpace = old })
	var free uint64
	freeSpace = func(string) (uint64, bool) { return free, true }

	free = uint64(info.Size()) + minFreeAfterCopy - 1
	keepDatabaseBeforeV2()
	if _, err := os.Stat(beforeV2()); !os.IsNotExist(err) {
		t.Fatalf("kept a copy that leaves less than %d bytes free: %v", minFreeAfterCopy, err)
	}
	free = uint64(info.Size()) + minFreeAfterCopy
	keepDatabaseBeforeV2()
	if n := countRows(t, beforeV2()); n != 1 {
		t.Fatalf("the copy has %d sessions, want 1", n)
	}
}
