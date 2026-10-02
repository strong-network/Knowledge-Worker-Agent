// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// pathChange is one stored path the migration rewrites.
type pathChange struct {
	Old, New string
	Existed  bool // the old path existed when the run started
}

// rewrite is one value in our database that changes.
type rewrite struct {
	Table, Field, ID string
	Old, New         string
	Paths            []pathChange
}

// rewritable lists the only columns the migration rewrites, so a journal can't
// name any other.
var rewritable = map[string]bool{"sessions.config": true, "projects.workspace_path": true, "scheduled_tasks.workdir": true}

// planRewrites works out every value in our database at path that changes.
func planRewrites(path string, p Plan) ([]rewrite, error) {
	db, err := openReadOnly(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer db.Close()
	var out []rewrite
	rows, err := db.Query(`SELECT id, COALESCE(config, '') FROM sessions ORDER BY id`)
	if err != nil && !noSuchTable(err) {
		return nil, err
	}
	if err == nil {
		for rows.Next() {
			var id, raw string
			if err := rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return nil, err
			}
			if newRaw, changes := rewriteConfig(raw, p); len(changes) > 0 {
				out = append(out, rewrite{Table: "sessions", Field: "config", ID: id, Old: raw, New: newRaw, Paths: changes})
			}
		}
		rows.Close()
	}
	for _, c := range []struct{ table, field string }{{"projects", "workspace_path"}, {"scheduled_tasks", "workdir"}} {
		rows, err := db.Query(`SELECT id, "` + c.field + `" FROM "` + c.table + `" ORDER BY id`)
		if err != nil {
			if noSuchTable(err) {
				continue
			}
			return nil, err
		}
		for rows.Next() {
			var id, old string
			if err := rows.Scan(&id, &old); err != nil {
				rows.Close()
				return nil, err
			}
			if pc, ok := change(old, p); ok {
				out = append(out, rewrite{Table: c.table, Field: c.field, ID: id, Old: old, New: pc.New, Paths: []pathChange{pc}})
			}
		}
		rows.Close()
	}
	return out, nil
}

func change(old string, p Plan) (pathChange, bool) {
	if old == "" {
		return pathChange{}, false
	}
	n, moved := p.Remap(old)
	if !moved {
		return pathChange{}, false
	}
	_, err := os.Lstat(old)
	return pathChange{Old: old, New: n, Existed: err == nil}, true
}

// rewriteConfig rewrites the paths in a session's settings: its folder, and
// its added and plugin folders. Only those values change; every other byte,
// the order of the keys included, stays as it was, so undoing gives back the
// same text.
func rewriteConfig(raw string, p Plan) (string, []pathChange) {
	dec := json.NewDecoder(strings.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return raw, nil
	}
	type splice struct {
		start, end int
		with       []byte
	}
	var splices []splice
	var changes []pathChange
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return raw, nil
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return raw, nil
		}
		end := int(dec.InputOffset())
		var with []byte
		switch t {
		case "workdir":
			var s string
			if json.Unmarshal(v, &s) == nil {
				if pc, ok := change(s, p); ok {
					changes = append(changes, pc)
					with, _ = json.Marshal(pc.New)
				}
			}
		case "add_dirs", "plugin_dir":
			var list []string
			if json.Unmarshal(v, &list) != nil {
				continue
			}
			changed := false
			for i, s := range list {
				if pc, ok := change(s, p); ok {
					changes = append(changes, pc)
					list[i], changed = pc.New, true
				}
			}
			if changed {
				with, _ = json.Marshal(list)
			}
		}
		if with != nil {
			splices = append(splices, splice{end - len(v), end, with})
		}
	}
	if len(changes) == 0 {
		return raw, nil
	}
	var b strings.Builder
	last := 0
	for _, sp := range splices {
		b.WriteString(raw[last:sp.start])
		b.Write(sp.with)
		last = sp.end
	}
	b.WriteString(raw[last:])
	if !json.Valid([]byte(b.String())) {
		return raw, nil
	}
	return b.String(), changes
}

func countPaths(rws []rewrite) int {
	n := 0
	for _, rw := range rws {
		n += len(rw.Paths)
	}
	return n
}

// openWritable opens our database for writing, keeping its journal mode.
func openWritable(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
}

// applyRewrites journals every change, then makes them all in one transaction.
func applyRewrites(path string, rws []rewrite, j *journal) error {
	for _, rw := range rws {
		if err := j.add(Entry{Op: opRewrite, Path: path, Table: rw.Table, Field: rw.Field, ID: rw.ID, Old: rw.Old, New: rw.New}); err != nil {
			return err
		}
	}
	db, err := openWritable(path)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for _, rw := range rws {
		if !rewritable[rw.Table+"."+rw.Field] {
			tx.Rollback()
			return fmt.Errorf("%s.%s isn't a column the migration rewrites", rw.Table, rw.Field)
		}
		res, err := tx.Exec(`UPDATE "`+rw.Table+`" SET "`+rw.Field+`" = ? WHERE id = ? AND "`+rw.Field+`" = ?`, rw.New, rw.ID, rw.Old)
		if err != nil {
			tx.Rollback()
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			tx.Rollback()
			return fmt.Errorf("%s %s changed while the migration ran", rw.Table, rw.ID)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return db.Close()
}

// revertRewrites sets every journaled value back where it still has its new
// value; one never applied is left alone.
func revertRewrites(entries []Entry) error {
	byDB := map[string][]Entry{}
	for _, e := range entries {
		if e.Op == opRewrite {
			byDB[e.Path] = append(byDB[e.Path], e)
		}
	}
	for path, es := range byDB {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("can't set stored paths back: %w", err)
		}
		db, err := openWritable(path)
		if err != nil {
			return err
		}
		tx, err := db.Begin()
		if err != nil {
			db.Close()
			return err
		}
		for _, e := range es {
			if !rewritable[e.Table+"."+e.Field] {
				tx.Rollback()
				db.Close()
				return fmt.Errorf("the journal names %s.%s, which the migration never rewrites", e.Table, e.Field)
			}
			if _, err := tx.Exec(`UPDATE "`+e.Table+`" SET "`+e.Field+`" = ? WHERE id = ? AND "`+e.Field+`" = ?`, e.Old, e.ID, e.New); err != nil {
				tx.Rollback()
				db.Close()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			db.Close()
			return err
		}
		if err := db.Close(); err != nil {
			return err
		}
	}
	return nil
}
