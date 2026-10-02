// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// Startup snapshots of the chat database.
//
// A workspace can be powered down at any moment by the idle timeout, so there
// is no shutdown hook worth trusting: the snapshot is taken at startup instead,
// and specifically *before* migrate() runs, so the copy predates any schema
// change the new build is about to apply.
//
// VACUUM INTO, never a file copy. Under WAL a committed row can still live in
// the -wal sidecar, and copying only the .db file after an abrupt power-down
// loses everything since the last checkpoint -- measured on the real corpus,
// the copy had lost the table outright, not merely recent rows. VACUUM INTO
// goes through the open handle, which has already replayed the WAL, so the
// result is consistent. It is also cheap: 46ms for an 8.2 MB database.
//
// Two behaviours of VACUUM INTO shape the code below, both verified:
//   - it refuses to write an existing file ("output file already exists"),
//     hence the write-to-temp-then-rename;
//   - it refuses to read a corrupt source ("database disk image is malformed"),
//     which is what stops a damaged database from overwriting a good snapshot.
//     No separate integrity check is needed to get that guarantee.
//
// What this does NOT protect against: loss of the workspace volume. Snapshots
// sit beside the database on the same disk. This is protection against logical
// damage (a bad migration, an over-eager sweep, an accidental bulk delete) and
// corruption -- not disaster recovery.

const (
	// SnapshotKeep is how many snapshots are retained.
	SnapshotKeep = 5

	// SnapshotMinInterval is how old the newest snapshot must be before another
	// is taken.
	//
	// This is the difference between five useful snapshots and five useless
	// ones. An idle-timeout workspace can restart several times a day, so
	// snapshotting on every boot would roll the whole history out within hours.
	// The damage most worth recovering from is logical, not physical -- a bad
	// migration or an over-eager delete -- and VACUUM INTO copies a logically
	// damaged database perfectly faithfully. Spacing the slots is what buys the
	// time to notice.
	SnapshotMinInterval = 24 * time.Hour

	snapshotPrefix = "snapshot-"
	snapshotSuffix = ".db"

	// Timestamped names sort lexically in chronological order, so ordering
	// never depends on mtime surviving a copy or a restore.
	snapshotStamp = "20060102T150405Z"

	// Fixed name so a snapshot interrupted by a power-down leaves exactly one
	// stale file, which the next run overwrites rather than accumulating.
	snapshotTmp = ".tmp" + snapshotSuffix
)

// SnapshotDir returns the directory holding snapshots of the database at path.
func SnapshotDir(path string) string { return layout.Backups(path) }

// SnapshotsEnabled reports whether startup snapshots are on. Set
// KWA_DB_BACKUP to 0/false/no/off to skip them.
func SnapshotsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(env.Get("KWA_DB_BACKUP"))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// Snapshot writes a consistent copy of the open database into SnapshotDir and
// prunes all but the newest SnapshotKeep.
//
// It returns the path written, or "" when no snapshot was taken -- which is a
// normal outcome, not a failure: snapshots disabled, the newest is younger than
// SnapshotMinInterval, or the database is new and has nothing worth copying.
func Snapshot(path string) (string, error) { return snapshotAt(path, time.Now()) }

// snapshotAt is Snapshot with an injectable clock, so retention can be tested
// without waiting a day.
func snapshotAt(path string, now time.Time) (string, error) {
	if !SnapshotsEnabled() {
		return "", nil
	}
	if DB == nil {
		return "", fmt.Errorf("snapshot: database not open")
	}

	// Nothing to preserve on a brand-new workspace. Size is not the test: by the
	// time this runs the file exists and has a page in it, because opening the
	// database and setting a pragma creates one. What actually distinguishes a
	// fresh database is that it has no tables yet -- snapshots run before
	// migrate(), so on a first boot the schema does not exist. This is also what
	// keeps snapshots out of the way of tests, which all start from a temp file.
	if _, err := os.Stat(path); err != nil {
		return "", nil
	}
	var tables int
	if err := DB.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`,
	).Scan(&tables); err != nil {
		return "", fmt.Errorf("snapshot: inspect schema: %w", err)
	}
	if tables == 0 {
		return "", nil
	}

	dir := SnapshotDir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("snapshot: create %s: %w", dir, err)
	}

	if existing := snapshotList(dir); len(existing) > 0 {
		if fi, err := os.Stat(existing[0]); err == nil {
			if now.Sub(fi.ModTime()) < SnapshotMinInterval {
				return "", nil
			}
		}
	}

	tmp := filepath.Join(dir, snapshotTmp)
	// VACUUM INTO refuses an existing target, so clear any file left behind by
	// a run that was killed midway.
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("snapshot: clear temp: %w", err)
	}
	if _, err := DB.Exec("VACUUM INTO ?", tmp); err != nil {
		return "", fmt.Errorf("snapshot: vacuum into %s: %w", tmp, err)
	}

	// Rename is atomic, so a snapshot slot is never observed half-written.
	final := filepath.Join(dir, snapshotPrefix+now.UTC().Format(snapshotStamp)+snapshotSuffix)
	if err := os.Rename(tmp, final); err != nil {
		return "", fmt.Errorf("snapshot: rename: %w", err)
	}

	snapshotPrune(dir)
	return final, nil
}

// snapshotList returns existing snapshot paths, newest first.
func snapshotList(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, snapshotPrefix) && strings.HasSuffix(n, snapshotSuffix) {
			out = append(out, filepath.Join(dir, n))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// snapshotPrune removes all but the newest SnapshotKeep snapshots. Pruning is
// best-effort: failing to delete an old copy is not a reason to fail a startup,
// and the next run tries again.
func snapshotPrune(dir string) {
	all := snapshotList(dir)
	if len(all) <= SnapshotKeep {
		return
	}
	for _, p := range all[SnapshotKeep:] {
		if err := os.Remove(p); err != nil {
			log.Printf("[db] snapshot: cannot prune %s: %v", p, err)
		}
	}
}
