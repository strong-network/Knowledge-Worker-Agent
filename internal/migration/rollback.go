// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// Recover sets back any run that didn't finish, such as one a crash or a
// stopped workspace interrupted. It runs first at every start, before anything
// else opens a file the migration may have moved.
func Recover(out io.Writer) error {
	runs, err := os.ReadDir(layout.MigrationDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, r := range runs {
		dir := filepath.Join(layout.MigrationDir(), r.Name())
		if !r.IsDir() {
			continue
		}
		entries, err := readJournal(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if finished(entries) {
			continue
		}
		what := "One-folder migration"
		if modeOf(entries) == Undo {
			what = "Undoing the one-folder migration"
		}
		fmt.Fprintf(out, "  ⚠ %s %s didn't finish; setting everything back\n", what, r.Name())
		j, err := appendJournal(dir)
		if err != nil {
			return err
		}
		err = rollback(dir, j, "interrupted")
		j.close()
		if err != nil {
			return fmt.Errorf("setting back the migration in %s: %w", dir, err)
		}
		fmt.Fprintf(out, "  ✓ %s %s set back; nothing has changed\n", what, r.Name())
	}
	return nil
}

// rollback undoes a run from its journal, newest change first. Each undo checks
// what's on disk first, so a rollback that stops partway can run again. Only
// when every file is verified back in its place are the run's copies deleted.
// reason, "failed: …" or "interrupted", is journaled with the rollback.
func rollback(dir string, j *journal, reason string) error {
	entries, err := readJournal(dir)
	if err != nil {
		return err
	}
	if len(entries) > 0 && entries[0].Op == opBegin && entries[0].Mode == "" {
		// A journal from before completion was journaled, and before undo: removing
		// the records is how its rollback undoes a completion. Newer journals undo
		// their completion step, which keeps an undo's record of what moved.
		for _, f := range []string{layout.CompletionFile(), layout.MovedFile()} {
			if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	var copyDB *Entry
	for i := range entries {
		if entries[i].Op == opCopyDB {
			copyDB = &entries[i]
		}
	}
	reverted := false
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		var err error
		switch e.Op {
		case opRelink:
			err = undoRelink(e)
		case opRepoint:
			if copyDB != nil {
				err = restoreDatabase(copyDB.Path, copyDB.To)
			}
		case opRewrite:
			if !reverted {
				reverted = true
				err = revertRewrites(entries)
			}
		case opCopy:
			err = undoCopy(e)
		case opRename:
			err = undoRename(e)
		case opMkdir:
			_ = os.Remove(e.Path) // only a folder left empty
		case opCreate:
			if rmErr := os.Remove(e.Path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				err = rmErr
			}
		}
		if err != nil {
			return err
		}
	}

	var moves []Move
	if b, err := os.ReadFile(filepath.Join(dir, "plan.json")); err == nil {
		if err := json.Unmarshal(b, &moves); err != nil {
			return err
		}
	}
	if manifest, err := readManifest(filepath.Join(dir, "manifest.jsonl")); err == nil {
		grown := map[string]bool{}
		for _, m := range moves {
			if m.Name == layout.NameDatabase {
				grown[m.From] = true
			}
		}
		if problems := verifyManifest(Plan{Moves: moves}, manifest, false, nil, grown); len(problems) > 0 {
			return fmt.Errorf("after setting back, %s; the backups in %s are kept", problems[0], dir)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, name := range runCopies {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return j.add(Entry{Op: opRolledBack, Detail: reason})
}

// LastFailure reports whether the newest run failed with this release and in
// this mode, and why. A failure tends to repeat, and every attempt costs the
// user the wait, so such a run isn't tried again until the release changes. A
// run a stop interrupted is.
func LastFailure(release string, mode Mode) (string, bool) {
	runs, err := os.ReadDir(layout.MigrationDir())
	if err != nil {
		return "", false
	}
	for i := len(runs) - 1; i >= 0; i-- {
		if !runs[i].IsDir() {
			continue
		}
		entries, err := readJournal(filepath.Join(layout.MigrationDir(), runs[i].Name()))
		if err != nil || len(entries) == 0 {
			continue
		}
		last := entries[len(entries)-1]
		if entries[0].Op != opBegin || entries[0].Detail != release || modeOf(entries) != mode || last.Op != opRolledBack {
			return "", false
		}
		reason, failed := strings.CutPrefix(last.Detail, "failed: ")
		return reason, failed
	}
	return "", false
}

// StrayDatabase returns a database that has appeared at our database's old
// place since the migration, which an older release, run since, would create.
func StrayDatabase() (string, bool) {
	if !layout.Migrated() {
		return "", false
	}
	for _, r := range layout.Moved() {
		if samePath(r.To, layout.Database()) && exists(r.From) {
			return r.From, true
		}
	}
	return "", false
}

func undoRename(e Entry) error {
	toThere, fromThere := exists(e.To), exists(e.From)
	switch {
	case toThere && !fromThere:
		if e.Detail == "database" {
			if err := checkpoint(e.To); err != nil {
				return err
			}
		}
		return os.Rename(e.To, e.From)
	case !toThere && fromThere:
		return nil // never renamed
	case toThere && fromThere:
		return fmt.Errorf("can't move %s back: %s exists", e.To, e.From)
	default:
		return fmt.Errorf("can't move %s back: neither it nor %s exists", e.To, e.From)
	}
}

// undoCopy removes a copy, but never the only one.
func undoCopy(e Entry) error {
	if !exists(e.From) {
		return fmt.Errorf("can't remove the copy %s: its original %s is missing", e.To, e.From)
	}
	return os.RemoveAll(e.To)
}

func undoRelink(e Entry) error {
	cur, err := os.Readlink(e.Path)
	switch {
	case err == nil && cur == e.Old:
		return nil
	case err == nil && cur != e.New:
		return fmt.Errorf("%s points to %s, neither its old nor its new target", e.Path, cur)
	case err == nil:
		if err := os.Remove(e.Path); err != nil {
			return err
		}
	case exists(e.Path):
		return fmt.Errorf("%s isn't a link any more", e.Path)
	}
	return os.Symlink(e.Old, e.Path)
}

// restoreDatabase puts opencode's database back as it was before any import, by
// renaming its copy into place, which needs no free space.
func restoreDatabase(copyPath, db string) error {
	if !exists(copyPath) {
		return nil // already restored
	}
	for _, s := range []string{"-wal", "-shm"} {
		if err := os.Remove(db + s); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Rename(copyPath, db)
}
