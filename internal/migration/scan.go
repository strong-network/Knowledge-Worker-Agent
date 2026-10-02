// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// openReadOnly opens a SQLite database for reading only.
func openReadOnly(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	return sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
}

// StoredPaths counts the absolute paths in our database by what the migration
// does with them.
type StoredPaths struct {
	Rewritten map[string]int // by table and field, paths under a folder that moves
	Kept      int            // paths elsewhere, such as chats in the home folder itself
}

// ScanStoredPaths reads the paths stored in our database at dbPath.
func ScanStoredPaths(dbPath string, p Plan) (StoredPaths, error) {
	res := StoredPaths{Rewritten: map[string]int{}}
	db, err := openReadOnly(dbPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return res, nil
		}
		return res, err
	}
	defer db.Close()
	count := func(field, path string) {
		if strings.TrimSpace(path) == "" {
			return
		}
		if _, moved := p.Remap(path); moved {
			res.Rewritten[field]++
		} else {
			res.Kept++
		}
	}
	rows, err := db.Query(`SELECT config FROM sessions`)
	if err != nil && !noSuchTable(err) {
		return res, err
	}
	if err == nil {
		for rows.Next() {
			var raw sql.NullString
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return res, err
			}
			var cfg struct {
				Workdir   string   `json:"workdir"`
				AddDirs   []string `json:"add_dirs"`
				PluginDir []string `json:"plugin_dir"`
			}
			if json.Unmarshal([]byte(raw.String), &cfg) != nil {
				continue
			}
			count("sessions.workdir", cfg.Workdir)
			for _, d := range cfg.AddDirs {
				count("sessions.add_dirs", d)
			}
			for _, d := range cfg.PluginDir {
				count("sessions.plugin_dir", d)
			}
		}
		rows.Close()
	}
	for _, q := range []struct{ field, query string }{
		{"projects.workspace_path", `SELECT workspace_path FROM projects`},
		{"scheduled_tasks.workdir", `SELECT workdir FROM scheduled_tasks`},
	} {
		rows, err := db.Query(q.query)
		if err != nil {
			if noSuchTable(err) {
				continue
			}
			return res, err
		}
		for rows.Next() {
			var v sql.NullString
			if err := rows.Scan(&v); err == nil {
				count(q.field, v.String)
			}
		}
		rows.Close()
	}
	return res, nil
}

// OpencodeSessions describes opencode's sessions whose folder moves. Each must
// be re-pointed with opencode's export and import.
type OpencodeSessions struct {
	Repoint  int   // sessions to re-point
	Children int   // of which subagent sessions
	Missing  int   // whose folder was already gone, left as they are
	Data     int64 // bytes of their messages and parts, which their exports hold
}

// OpencodeDatabase returns opencode's database, as opencode finds it.
func OpencodeDatabase() string {
	base := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "opencode", "opencode.db")
}

// ScanOpencodeSessions reads opencode's database at path.
func ScanOpencodeSessions(path string, p Plan) (OpencodeSessions, error) {
	var res OpencodeSessions
	db, err := openReadOnly(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return res, nil
		}
		return res, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, directory, COALESCE(parent_id, '') FROM session`)
	if err != nil {
		if noSuchTable(err) {
			return res, nil
		}
		return res, err
	}
	var ids []string
	for rows.Next() {
		var id, dir, parent string
		if err := rows.Scan(&id, &dir, &parent); err != nil {
			rows.Close()
			return res, err
		}
		if _, moved := p.Remap(dir); !moved {
			continue
		}
		if _, err := os.Stat(dir); err != nil {
			res.Missing++
			continue
		}
		res.Repoint++
		if parent != "" {
			res.Children++
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		for _, q := range []string{
			`SELECT COALESCE(SUM(LENGTH(data)), 0) FROM message WHERE session_id = ?`,
			`SELECT COALESCE(SUM(LENGTH(data)), 0) FROM part WHERE session_id = ?`,
		} {
			var n int64
			if err := db.QueryRow(q, id).Scan(&n); err != nil && !noSuchTable(err) {
				return res, err
			}
			res.Data += n
		}
	}
	return res, nil
}

// Links describes the symbolic links inside the folders that move.
type Links struct {
	Rewrite []string // absolute links into a moved place, rewritten to its new place
	Broken  []string // relative links that would point elsewhere after the move
}

// skipDir reports folders the scans don't look into: version control, package
// installs, and KWA's own per-folder data.
func skipDir(name string) bool {
	return name == ".git" || name == "node_modules" || name == ".system"
}

// ScanLinks finds the links the migration must rewrite, or can't keep working.
func ScanLinks(p Plan) Links {
	var res Links
	for _, m := range p.Renames() {
		_ = filepath.WalkDir(m.From, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && path != m.From && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			if path != m.From && p.isSource(path) {
				return skipEntry(d)
			}
			if d.Type()&fs.ModeSymlink == 0 {
				return nil
			}
			target, err := os.Readlink(path)
			if err != nil {
				return nil
			}
			if filepath.IsAbs(target) {
				if _, moved := p.Remap(target); moved {
					res.Rewrite = append(res.Rewrite, path)
				}
				return nil
			}
			// A relative link keeps working if it resolves to the same thing from
			// its new place: true within one moved folder, not always across two.
			before := filepath.Join(filepath.Dir(path), target)
			newLink, _ := p.Remap(path)
			after := filepath.Join(filepath.Dir(newLink), target)
			if want, _ := p.Remap(before); filepath.Clean(want) != filepath.Clean(after) {
				res.Broken = append(res.Broken, path)
			}
			return nil
		})
	}
	return res
}

// Mentions lists the files in moved folders whose contents name an old path.
// The migration doesn't edit them; the report lists them.
type Mentions struct {
	Files []string
	Envs  []string // Python virtual environments among them, by folder
}

// maxMentionFile is the largest file the scan reads; larger ones are rarely
// text that names a path.
const maxMentionFile = 1 << 20

// ScanMentions reads the text files in the folders that move.
func ScanMentions(p Plan) Mentions {
	var res Mentions
	var needles [][]byte
	for _, m := range p.Renames() {
		needles = append(needles, []byte(filepath.Clean(m.From)+string(filepath.Separator)))
	}
	envs := map[string]bool{}
	for _, m := range p.Renames() {
		_ = filepath.WalkDir(m.From, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if path != m.From && p.isSource(path) {
				return skipEntry(d)
			}
			if d.IsDir() {
				if path != m.From && skipDir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			if info, err := d.Info(); err != nil || info.Size() > maxMentionFile {
				return nil
			}
			if !mentions(path, needles) {
				return nil
			}
			if env := venvOf(path, m.From); env != "" {
				if !envs[env] {
					envs[env] = true
					res.Envs = append(res.Envs, env)
				}
				return nil
			}
			res.Files = append(res.Files, path)
			return nil
		})
	}
	return res
}

func mentions(path string, needles [][]byte) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxMentionFile))
	if err != nil || bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return false // binary
	}
	for _, n := range needles {
		if bytes.Contains(data, n) {
			return true
		}
	}
	return false
}

// venvOf returns the Python virtual environment path is in, if any: the
// nearest folder up to root that holds a pyvenv.cfg.
func venvOf(path, root string) string {
	for dir := filepath.Dir(path); under(dir, root); dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "pyvenv.cfg")); err == nil {
			return dir
		}
		if dir == filepath.Clean(root) {
			break
		}
	}
	return ""
}

func noSuchTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}
