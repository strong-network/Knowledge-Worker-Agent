// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package migration holds what's left of the one-folder migration, which moved
// Knowledge Worker Agent's files into the root up to release 2.1. Later
// releases don't move files: they prune the copies earlier runs kept, notice a
// database an older release left at the old place, and stop on a workspace
// whose files were never moved.
package migration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// Unmoved reports why the workspace can't start: its files are still where
// releases before the one-folder layout kept them, or a move into the root
// didn't finish or was undone. A new workspace has nothing to move.
func Unmoved() (string, bool) {
	if layout.Migrated() {
		return "", false
	}
	if runs, _ := os.ReadDir(layout.MigrationDir()); len(runs) > 0 {
		return fmt.Sprintf("A move of its files into %s didn't finish, or was undone.", layout.Root()), true
	}
	for _, db := range oldDatabases() {
		if exists(db) {
			return fmt.Sprintf("Its chats are still in %s.", db), true
		}
	}
	return "", false
}

// oldDatabases is a var so tests can keep to their own folders: the old
// defaults include absolute paths.
var oldDatabases = layout.OldDatabases

// MarkNew records that a new workspace keeps its files in the root, as a move
// with nothing to move did.
func MarkNew() error {
	if err := os.MkdirAll(layout.MigrationDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(layout.CompletionFile(), []byte("new\n"), 0o600)
}

// StrayDatabase returns a database that has appeared at our database's old
// place since the migration, which an older release, run since, would create.
func StrayDatabase() (string, bool) {
	if !layout.Migrated() {
		return "", false
	}
	for _, r := range layout.Moved() {
		if filepath.Clean(r.To) == filepath.Clean(layout.Database()) && exists(r.From) {
			return r.From, true
		}
	}
	return "", false
}

const journalName = "journal.jsonl"

// entry is one line of a run's journal; only its operation matters now.
type entry struct {
	Op string `json:"op"`
}

// readJournal reads a run's journal. A last line cut short by a crash is
// ignored: its change was never made.
func readJournal(dir string) ([]entry, error) {
	data, err := os.ReadFile(filepath.Join(dir, journalName))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	var out []entry
	for i, line := range lines {
		if line == "" {
			continue
		}
		var e entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			if i == len(lines)-1 && !strings.HasSuffix(string(data), "\n") {
				break
			}
			return nil, fmt.Errorf("journal %s, line %d: %w", dir, i+1, err)
		}
		out = append(out, e)
	}
	return out, nil
}

// finished reports whether a run completed or was rolled back.
func finished(entries []entry) bool {
	for _, e := range entries {
		if e.Op == "complete" || e.Op == "rolled-back" {
			return true
		}
	}
	return false
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
