// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// The journal records each change before it's made, synced to disk, so a
// failure or a crash can always be undone from it.
const (
	opBegin      = "begin"            // Path: the root; Detail: the release; Mode: on or undo
	opMkdir      = "mkdir"            // Path: a folder the migration created
	opCreate     = "create"           // Path: a file the run is about to write
	opRename     = "rename"           // From renamed to To; Detail "database" for our database
	opCopy       = "copy"             // From copied to To
	opCopyDB     = "copy-opencode-db" // Path: the copy of opencode's database, To: the database
	opRewrite    = "rewrite"          // in our database at Path: Table.Field of row ID, Old to New
	opRepoint    = "repoint"          // opencode's database is about to change
	opRelink     = "relink"           // the link at Path, from target Old to New
	opComplete   = "complete"
	opRolledBack = "rolled-back"
)

const journalName = "journal.jsonl"

// Entry is one line of the journal.
type Entry struct {
	Op     string `json:"op"`
	Path   string `json:"path,omitempty"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Table  string `json:"table,omitempty"`
	Field  string `json:"field,omitempty"`
	ID     string `json:"id,omitempty"`
	Old    string `json:"old,omitempty"`
	New    string `json:"new,omitempty"`
	Detail string `json:"detail,omitempty"`
	Mode   Mode   `json:"mode,omitempty"`
}

// modeOf returns whether a run migrated or undid, from its journal. Runs
// before undo existed only migrated.
func modeOf(entries []Entry) Mode {
	if len(entries) > 0 && entries[0].Op == opBegin && entries[0].Mode == Undo {
		return Undo
	}
	return On
}

// journal is a run's journal, locked for as long as the run, or its rollback,
// has it open: a run whose journal is locked is still alive.
type journal struct{ f *os.File }

func createJournal(dir string) (*journal, error) {
	f, err := os.OpenFile(filepath.Join(dir, journalName), os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	if err := syncDir(dir); err != nil {
		f.Close()
		return nil, err
	}
	return &journal{f}, nil
}

// appendJournal opens a run's journal to roll it back. It fails when another
// process still runs it.
func appendJournal(dir string) (*journal, error) {
	f, err := os.OpenFile(filepath.Join(dir, journalName), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another process is running the migration in %s", dir)
	}
	return &journal{f}, nil
}

// add writes e and syncs it, before the change it describes is made.
func (j *journal) add(e Entry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := j.f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	if err := j.f.Sync(); err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	return nil
}

func (j *journal) close() error { return j.f.Close() }

// readJournal reads a run's journal. A last line cut short by a crash is
// ignored: its change was never made.
func readJournal(dir string) ([]Entry, error) {
	data, err := os.ReadFile(filepath.Join(dir, journalName))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	var out []Entry
	for i, line := range lines {
		if line == "" {
			continue
		}
		var e Entry
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
func finished(entries []Entry) bool {
	for _, e := range entries {
		if e.Op == opComplete || e.Op == opRolledBack {
			return true
		}
	}
	return false
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
