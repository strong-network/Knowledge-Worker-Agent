// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import "database/sql"

// SharedArtifact is one file shared on its own port. SharedAt is RFC3339 UTC.
type SharedArtifact struct {
	ID            string
	Folder        string
	RelPath       string
	SessionID     string // "" once the chat it was shared from is deleted
	Port          int
	AllowDownload bool
	SharedAt      string
	// CheckKey is the file's size and time the cached check (CheckJSON) is for.
	CheckKey, CheckJSON string
}

const artifactColumns = `id, folder, rel_path, COALESCE(session_id, ''), port, allow_download,
	strftime('%Y-%m-%dT%H:%M:%SZ', shared_at), check_key, check_json`

func scanArtifact(row interface{ Scan(...any) error }) (SharedArtifact, error) {
	var a SharedArtifact
	err := row.Scan(&a.ID, &a.Folder, &a.RelPath, &a.SessionID, &a.Port, &a.AllowDownload, &a.SharedAt, &a.CheckKey, &a.CheckJSON)
	return a, err
}

// CreateSharedArtifact records a shared file.
func CreateSharedArtifact(a SharedArtifact) error {
	_, err := DB.Exec(
		`INSERT INTO shared_artifacts (id, folder, rel_path, session_id, port, allow_download) VALUES (?, ?, ?, ?, ?, ?)`,
		a.ID, a.Folder, a.RelPath, nullable(a.SessionID), a.Port, a.AllowDownload,
	)
	return err
}

// GetSharedArtifact returns the shared file with that id.
func GetSharedArtifact(id string) (SharedArtifact, bool) {
	a, err := scanArtifact(DB.QueryRow(`SELECT `+artifactColumns+` FROM shared_artifacts WHERE id = ?`, id))
	return a, err == nil
}

// SharedArtifactAt returns the shared file at folder and relPath, if it is shared.
func SharedArtifactAt(folder, relPath string) (SharedArtifact, bool) {
	a, err := scanArtifact(DB.QueryRow(`SELECT `+artifactColumns+` FROM shared_artifacts WHERE folder = ? AND rel_path = ?`, folder, relPath))
	return a, err == nil
}

// SharedArtifacts returns every shared file, oldest share first. Never nil.
func SharedArtifacts() []SharedArtifact {
	out := []SharedArtifact{}
	rows, err := DB.Query(`SELECT ` + artifactColumns + ` FROM shared_artifacts ORDER BY shared_at, id`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		if a, err := scanArtifact(rows); err == nil {
			out = append(out, a)
		}
	}
	return out
}

// SharedArtifactFolders returns the folders that hold a shared file, for
// skipping them in a loop over other rows (the database has one connection,
// so a query inside that loop would wait forever).
func SharedArtifactFolders() map[string]bool {
	out := map[string]bool{}
	rows, err := DB.Query(`SELECT DISTINCT folder FROM shared_artifacts`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var f string
		if rows.Scan(&f) == nil {
			out[f] = true
		}
	}
	return out
}

// FolderHasSharedArtifact reports whether a file in folder is shared.
func FolderHasSharedArtifact(folder string) bool {
	var one int
	err := DB.QueryRow(`SELECT 1 FROM shared_artifacts WHERE folder = ? LIMIT 1`, folder).Scan(&one)
	return err == nil
}

// DeleteSharedArtifact forgets a shared file.
func DeleteSharedArtifact(id string) error {
	_, err := DB.Exec(`DELETE FROM shared_artifacts WHERE id = ?`, id)
	return err
}

// SetSharedArtifactPort moves a shared file to another port.
func SetSharedArtifactPort(id string, port int) error {
	return updateArtifact(`UPDATE shared_artifacts SET port = ? WHERE id = ?`, port, id)
}

// SetSharedArtifactPath records the file's new path inside its folder.
func SetSharedArtifactPath(id, relPath string) error {
	return updateArtifact(`UPDATE shared_artifacts SET rel_path = ? WHERE id = ?`, relPath, id)
}

// SetSharedArtifactDownload sets whether viewers can download the file.
func SetSharedArtifactDownload(id string, allow bool) error {
	return updateArtifact(`UPDATE shared_artifacts SET allow_download = ? WHERE id = ?`, allow, id)
}

// SetArtifactCheck stores the self-contained check's result for the file as it
// was at key.
func SetArtifactCheck(id, key, result string) error {
	return updateArtifact(`UPDATE shared_artifacts SET check_key = ?, check_json = ? WHERE id = ?`, key, result, id)
}

// ClearArtifactSession forgets the chat a file was shared from, once that chat
// is deleted and the file stays.
func ClearArtifactSession(id string) error {
	return updateArtifact(`UPDATE shared_artifacts SET session_id = NULL WHERE id = ?`, id)
}

func updateArtifact(q string, args ...any) error {
	res, err := DB.Exec(q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
