// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

// ArtifactFix is a background turn making a file self-contained: State is
// running, done or failed. Times are RFC3339 UTC.
type ArtifactFix struct {
	ID, Folder, RelPath string
	FixSessionID        string
	// SourceSHA256 is the original as the fix started from it.
	SourceSHA256 string
	State        string
	// Problem says why a failed fix failed; Findings is the check's JSON for a
	// result that still isn't self-contained.
	Problem, Findings     string
	StartedAt, FinishedAt string
}

const fixColumns = `id, folder, rel_path, fix_session_id, source_sha256, state, problem, findings,
	strftime('%Y-%m-%dT%H:%M:%SZ', started_at), COALESCE(strftime('%Y-%m-%dT%H:%M:%SZ', finished_at), '')`

func scanFix(row interface{ Scan(...any) error }) (ArtifactFix, error) {
	var f ArtifactFix
	err := row.Scan(&f.ID, &f.Folder, &f.RelPath, &f.FixSessionID, &f.SourceSHA256, &f.State, &f.Problem, &f.Findings, &f.StartedAt, &f.FinishedAt)
	return f, err
}

// CreateArtifactFix records a fix as running.
func CreateArtifactFix(f ArtifactFix) error {
	_, err := DB.Exec(`INSERT INTO artifact_fixes (id, folder, rel_path, fix_session_id, source_sha256, state) VALUES (?, ?, ?, ?, ?, 'running')`,
		f.ID, f.Folder, f.RelPath, f.FixSessionID, f.SourceSHA256)
	return err
}

// GetArtifactFix returns the fix with that id.
func GetArtifactFix(id string) (ArtifactFix, bool) {
	f, err := scanFix(DB.QueryRow(`SELECT `+fixColumns+` FROM artifact_fixes WHERE id = ?`, id))
	return f, err == nil
}

// RunningFixAt returns the fix running for the file at folder and relPath.
func RunningFixAt(folder, relPath string) (ArtifactFix, bool) {
	f, err := scanFix(DB.QueryRow(`SELECT `+fixColumns+` FROM artifact_fixes WHERE folder = ? AND rel_path = ? AND state = 'running'`, folder, relPath))
	return f, err == nil
}

// RunningFixes returns every fix still running, oldest first. Never nil.
func RunningFixes() []ArtifactFix {
	out := []ArtifactFix{}
	rows, err := DB.Query(`SELECT ` + fixColumns + ` FROM artifact_fixes WHERE state = 'running' ORDER BY started_at, id`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		if f, err := scanFix(rows); err == nil {
			out = append(out, f)
		}
	}
	return out
}

// FinishArtifactFix records how a fix ended.
func FinishArtifactFix(id, state, problem, findings string) error {
	_, err := DB.Exec(`UPDATE artifact_fixes SET state = ?, problem = ?, findings = ?, finished_at = CURRENT_TIMESTAMP WHERE id = ?`,
		state, problem, findings, id)
	return err
}

// FailRunningFixes ends every fix still marked running, with problem as why.
func FailRunningFixes(problem string) (int64, error) {
	res, err := DB.Exec(`UPDATE artifact_fixes SET state = 'failed', problem = ?, finished_at = CURRENT_TIMESTAMP WHERE state = 'running'`, problem)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
