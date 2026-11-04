// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodev2

import (
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"

	_ "modernc.org/sqlite"
)

// The first time 2.x starts, it upgrades opencode's database: it copies the
// 1.x sessions into its own tables and clears 1.x's event log. 1.x still reads
// its sessions afterwards, but a turn that goes wrong there can't be undone,
// so KWA keeps a copy of the database as it was before KWA first started 2.x.

// beforeV2 is that copy.
func beforeV2() string { return filepath.Join(layout.System(), "opencode-db-before-2.x.db") }

// dataDir is opencode's data folder, which holds its database.
func dataDir() string {
	base := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "opencode")
}

// keepDatabaseBeforeV2 writes the copy, once. A failure is logged and doesn't
// stop the server: the copy is a safety net, not a condition. A test without
// its own data folder must not read the developer's real database.
func keepDatabaseBeforeV2() {
	if testing.Testing() && os.Getenv("XDG_DATA_HOME") == "" {
		return
	}
	dst := beforeV2()
	if _, err := os.Stat(dst); err == nil {
		return
	}
	src := filepath.Join(dataDir(), "opencode.db")
	info, err := os.Stat(src)
	if err != nil || openedBy2(src) {
		return
	}
	size := info.Size()
	if wal, err := os.Stat(src + "-wal"); err == nil {
		size += wal.Size()
	}
	if free, ok := freeSpace(filepath.Dir(dst)); ok && free < uint64(size)+minFreeAfterCopy {
		log.Printf("[opencode-v2] not enough disk space to keep a copy of %s before 2.x first opens it: it takes up to %s, and %s is free", src, gigabytes(uint64(size)), gigabytes(free))
		return
	}
	if err := copyDatabase(src, dst); err != nil {
		log.Printf("[opencode-v2] could not keep a copy of %s before 2.x first opens it: %v", src, err)
		return
	}
	log.Printf("[opencode-v2] kept a copy of opencode's database from before 2.x: %s", dst)
}

// minFreeAfterCopy is the disk space the copy must leave, for 2.x and the workspace.
const minFreeAfterCopy = 1 << 30

// openedBy2 reports whether 2.x has opened the database at path: then a copy
// would no longer be of the database before 2.x.
func openedBy2(path string) bool {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return false
	}
	defer db.Close()
	var n int
	err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'session_v2'`).Scan(&n)
	return err == nil && n > 0
}

// freeSpace is the space free to an unprivileged user in dir, made if needed.
// A var so tests can run short of it.
var freeSpace = func(dir string) (uint64, bool) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, false
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return st.Bavail * uint64(st.Bsize), true
}

func gigabytes(n uint64) string { return strconv.FormatFloat(float64(n)/1e9, 'f', 1, 64) + " GB" }

// copyDatabase writes a consistent copy of the SQLite database src to dst.
// VACUUM INTO, not a file copy: under WAL, committed rows can still be in the
// -wal file.
func copyDatabase(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+src+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	tmp := dst + ".tmp"
	_ = os.Remove(tmp)
	if _, err := db.Exec("VACUUM INTO ?", tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
