// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// Retention is how long a finished run keeps its copies. Undo doesn't need
// them: it makes fresh ones.
const Retention = 30 * 24 * time.Hour

// runCopies are the parts of a run's folder kept only for a while: the
// databases' copies, the exports, the hard-link backup, and the files copied
// or set aside. Its journal, plan, manifest and report stay.
var runCopies = []string{"backup", "database.db", "opencode", "opencode.db", "opencode-config", "files", "removed"}

// Prune deletes the copies of every run that finished more than Retention
// before now, and returns how many runs it pruned. A run that hasn't finished
// is left alone, since recovery needs it.
func Prune(now time.Time) (int, error) {
	runs, err := os.ReadDir(layout.MigrationDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	pruned := 0
	for _, r := range runs {
		dir := filepath.Join(layout.MigrationDir(), r.Name())
		if !r.IsDir() {
			continue
		}
		entries, err := readJournal(dir)
		if err != nil || !finished(entries) {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, journalName))
		if err != nil || now.Sub(info.ModTime()) < Retention {
			continue
		}
		removed := false
		for _, name := range runCopies {
			if !exists(filepath.Join(dir, name)) {
				continue
			}
			if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
				return pruned, err
			}
			removed = true
		}
		if removed {
			pruned++
		}
	}
	return pruned, nil
}
