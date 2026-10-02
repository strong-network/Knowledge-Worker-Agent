// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// copyDatabase writes a consistent copy of the SQLite database at src to dst,
// and returns its row counts. It only reads src.
func copyDatabase(src, dst string) (map[string]int64, error) {
	db, err := openReadOnly(src)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`VACUUM INTO ?`, dst)
	db.Close()
	if err != nil {
		return nil, fmt.Errorf("copy %s: %w", src, err)
	}
	if problems := checkIntegrityFull("the copy of "+src, dst); len(problems) > 0 {
		return nil, fmt.Errorf("%s", problems[0])
	}
	return rowCounts(dst)
}

// rowCounts counts the rows in every ordinary table of the database at path.
func rowCounts(path string) (map[string]int64, error) {
	db, err := openReadOnly(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND sql NOT LIKE 'CREATE VIRTUAL TABLE%'`)
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, t)
	}
	rows.Close()
	counts := map[string]int64{}
	for _, t := range tables {
		var n int64
		if err := db.QueryRow(`SELECT COUNT(*) FROM "` + t + `"`).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", t, err)
		}
		counts[t] = n
	}
	return counts, nil
}

// sameCounts checks every table in b has as many rows as in a, or for a table
// in grow, up to that many more.
func sameCounts(a, b map[string]int64, grow map[string]int64) error {
	for t, n := range a {
		if b[t] < n || b[t] > n+grow[t] {
			return fmt.Errorf("table %s has %d rows, had %d", t, b[t], n)
		}
	}
	for t := range b {
		if _, ok := a[t]; !ok {
			return fmt.Errorf("table %s is new", t)
		}
	}
	return nil
}

// checkIntegrityFull runs SQLite's full integrity check.
func checkIntegrityFull(name, path string) []Problem {
	db, err := openReadOnly(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return []Problem{{"database check", fmt.Sprintf("%s: %v", name, err)}}
	}
	defer db.Close()
	var res string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&res); err != nil {
		return []Problem{{"database check", fmt.Sprintf("%s: %v", name, err)}}
	}
	if res != "ok" {
		return []Problem{{"database check", fmt.Sprintf("%s: %s", name, res)}}
	}
	return nil
}

// hardLinkBackup gives every file under every renamed folder a second name in
// dir, which takes no space for its contents, so no bug can delete one.
// Links are recreated. Our database isn't included: it's copied instead.
func hardLinkBackup(p Plan, dir string) error {
	for i, m := range p.Moves {
		if m.Kind != Rename || m.Name == layout.NameDatabase {
			continue
		}
		base := filepath.Join(dir, fmt.Sprintf("%02d-%s", i, filepath.Base(m.From)))
		err := filepath.WalkDir(m.From, func(cur string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if cur != m.From && p.isSource(cur) {
				return skipEntry(d)
			}
			rel, _ := filepath.Rel(m.From, cur)
			dst := filepath.Join(base, rel)
			switch {
			case d.IsDir():
				return os.MkdirAll(dst, 0o700)
			case d.Type().IsRegular():
				if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
					return err
				}
				return os.Link(cur, dst)
			case d.Type()&fs.ModeSymlink != 0:
				target, err := os.Readlink(cur)
				if err != nil {
					return err
				}
				if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
					return err
				}
				return os.Symlink(target, dst)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("backup of %s: %w", m.From, err)
		}
	}
	return nil
}

// opencodeUserConfig returns opencode's own configuration file, as it finds it.
func opencodeUserConfig() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode", "opencode.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "opencode", "opencode.json")
}

// copySmallFiles keeps a copy of configuration the move affects, which KWA
// rewrites at its next start.
func copySmallFiles(dir string) error {
	src := opencodeUserConfig()
	if _, err := os.Stat(src); err != nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return copyFile(src, filepath.Join(dir, "opencode.json"), 0o600)
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// countRows returns how many rows of table have column = value in the
// database at path.
func countRows(db *sql.DB, table, column, value string) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM "`+table+`" WHERE "`+column+`" = ?`, value).Scan(&n)
	return n, err
}
