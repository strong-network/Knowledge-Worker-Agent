// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"
)

// FileVersion is one version of a file, as the file_versions table holds it.
type FileVersion struct {
	Path    string
	V       int
	SHA     string
	Size    int64
	ModTime int64 // the file's, in unix nanoseconds, when the version was taken
	Author  string
	Turn    string
	At      int64 // when the version was taken or last updated, in unix milliseconds
}

const fileVersionCols = `path, v, sha256, size, mod_time, author, turn, created_ms`

func scanFileVersion(row interface{ Scan(...any) error }) (FileVersion, error) {
	var fv FileVersion
	err := row.Scan(&fv.Path, &fv.V, &fv.SHA, &fv.Size, &fv.ModTime, &fv.Author, &fv.Turn, &fv.At)
	return fv, err
}

// LatestFileVersion returns the newest version of path, and false when it has none.
func LatestFileVersion(path string) (FileVersion, bool, error) {
	fv, err := scanFileVersion(DB.QueryRow(`SELECT `+fileVersionCols+` FROM file_versions WHERE path = ? ORDER BY v DESC LIMIT 1`, path))
	if errors.Is(err, sql.ErrNoRows) {
		return FileVersion{}, false, nil
	}
	return fv, err == nil, err
}

// FileVersionAt returns version v of path, and false when there's no such version.
func FileVersionAt(path string, v int) (FileVersion, bool, error) {
	fv, err := scanFileVersion(DB.QueryRow(`SELECT `+fileVersionCols+` FROM file_versions WHERE path = ? AND v = ?`, path, v))
	if errors.Is(err, sql.ErrNoRows) {
		return FileVersion{}, false, nil
	}
	return fv, err == nil, err
}

// FileVersions returns every version of path, oldest first.
func FileVersions(path string) ([]FileVersion, error) {
	rows, err := DB.Query(`SELECT `+fileVersionCols+` FROM file_versions WHERE path = ? ORDER BY v`, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FileVersion{}
	for rows.Next() {
		fv, err := scanFileVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, fv)
	}
	return out, rows.Err()
}

// AddFileVersion stores fv as a new version.
func AddFileVersion(fv FileVersion) error {
	_, err := DB.Exec(`INSERT INTO file_versions (`+fileVersionCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		fv.Path, fv.V, fv.SHA, fv.Size, fv.ModTime, fv.Author, fv.Turn, fv.At)
	return err
}

// UpdateFileVersion replaces the content and times of an existing version.
func UpdateFileVersion(fv FileVersion) error {
	_, err := DB.Exec(`UPDATE file_versions SET sha256 = ?, size = ?, mod_time = ?, created_ms = ? WHERE path = ? AND v = ?`,
		fv.SHA, fv.Size, fv.ModTime, fv.At, fv.Path, fv.V)
	return err
}

// TouchFileVersion records that the file still has the content of version v at a new modification time.
func TouchFileVersion(path string, v int, modTime int64) error {
	_, err := DB.Exec(`UPDATE file_versions SET mod_time = ? WHERE path = ? AND v = ?`, modTime, path, v)
	return err
}

// PruneFileVersions deletes all but the newest keep versions of path, and
// returns the content hashes the deleted versions had.
func PruneFileVersions(path string, keep int) ([]string, error) {
	return deleteFileVersions(`path = ? AND v <= (SELECT MAX(v) FROM file_versions WHERE path = ?) - ?`, path, path, keep)
}

// ForgetFileVersions deletes the versions of path and of everything under it,
// and returns the content hashes they had.
func ForgetFileVersions(path string) ([]string, error) {
	return deleteFileVersions(`path = ? OR path LIKE ? ESCAPE '\'`, path, likePrefix(path))
}

func deleteFileVersions(where string, args ...any) ([]string, error) {
	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT DISTINCT sha256 FROM file_versions WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	var shas []string
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			rows.Close()
			return nil, err
		}
		shas = append(shas, sha)
	}
	rows.Close()
	if _, err := tx.Exec(`DELETE FROM file_versions WHERE `+where, args...); err != nil {
		return nil, err
	}
	return shas, tx.Commit()
}

// MoveFileVersions moves the versions of src, and of everything under it, to dest.
func MoveFileVersions(src, dest string) error {
	_, err := DB.Exec(`UPDATE file_versions SET path = ? || substr(path, ?) WHERE path = ? OR path LIKE ? ESCAPE '\'`,
		dest, utf8.RuneCountInString(src)+1, src, likePrefix(src))
	return err
}

// FileVersionPaths returns every path that has versions.
func FileVersionPaths() ([]string, error) {
	return strings1(`SELECT DISTINCT path FROM file_versions`)
}

// FileVersionSHAs returns every content hash a version refers to.
func FileVersionSHAs() (map[string]bool, error) {
	list, err := strings1(`SELECT DISTINCT sha256 FROM file_versions`)
	out := make(map[string]bool, len(list))
	for _, s := range list {
		out[s] = true
	}
	return out, err
}

// FileVersionSHAInUse reports whether any version refers to sha.
func FileVersionSHAInUse(sha string) bool {
	var one int
	return DB.QueryRow(`SELECT 1 FROM file_versions WHERE sha256 = ? LIMIT 1`, sha).Scan(&one) == nil
}

func strings1(query string) ([]string, error) {
	rows, err := DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// likePrefix matches what's under dir in a LIKE with ESCAPE '\'.
func likePrefix(dir string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(strings.TrimSuffix(dir, "/")) + "/%"
}
