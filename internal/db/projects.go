// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"database/sql"
	"encoding/json"
	"time"
)

// Project is a durable container that groups a shared workspace
// (files), project-level context (instructions + scoped data sources), and a
// collection of chats. workspace_path is the single persistent working
// directory shared by all of the project's chats. repo_url is set when the
// workspace is backed by a cloned Git repository (empty for a plain folder).
type Project struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	WorkspacePath string   `json:"workspace_path"`
	RepoURL       string   `json:"repo_url"`
	Instructions  string   `json:"instructions"`
	DataSources   []string `json:"data_sources"`
	// McpSelection is the project-level MCP connector selection:
	// server name → whether it is on for every chat in this project. An empty
	// map means "not customized" and callers seed from the workspace default.
	McpSelection map[string]bool `json:"mcp_selection"`
	ChatCount    int             `json:"chat_count"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
}

// decodeMcpSelection parses the JSON-encoded mcp_selection column into a
// non-nil map (so it always marshals as {} rather than null).
func decodeMcpSelection(raw string) map[string]bool {
	out := map[string]bool{}
	if raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	if out == nil {
		out = map[string]bool{}
	}
	return out
}

// decodeDataSources parses the JSON-encoded data_sources column into a
// non-nil slice (so it always marshals as [] rather than null).
func decodeDataSources(raw string) []string {
	out := []string{}
	if raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	if out == nil {
		out = []string{}
	}
	return out
}

// CreateProject inserts a new project. The caller is responsible for having
// provisioned workspacePath on disk (mkdir or repo clone) before/after.
func CreateProject(id, name, description, workspacePath, repoURL string) error {
	_, err := DB.Exec(
		`INSERT INTO projects (id, name, description, workspace_path, repo_url) VALUES (?, ?, ?, ?, ?)`,
		id, name, description, workspacePath, repoURL,
	)
	return err
}

// GetProject returns one project (with its live chat count) or nil.
func GetProject(id string) *Project {
	row := DB.QueryRow(`
		SELECT p.id, p.name, p.description, p.workspace_path, p.repo_url,
			p.instructions, p.data_sources, p.mcp_selection, p.created_at, p.updated_at,
			(SELECT COUNT(*) FROM sessions s WHERE s.project_id = p.id) AS chat_count
		FROM projects p WHERE p.id = ?
	`, id)
	p, err := scanProject(row)
	if err != nil {
		return nil
	}
	return p
}

// ProjectExists reports whether a project with the given id exists.
func ProjectExists(id string) bool {
	var n int
	err := DB.QueryRow(`SELECT COUNT(*) FROM projects WHERE id = ?`, id).Scan(&n)
	return err == nil && n > 0
}

// ListProjects returns all projects, most-recently-updated first, each with a
// live chat count. Never returns nil.
func ListProjects() []Project {
	rows, err := DB.Query(`
		SELECT p.id, p.name, p.description, p.workspace_path, p.repo_url,
			p.instructions, p.data_sources, p.mcp_selection, p.created_at, p.updated_at,
			(SELECT COUNT(*) FROM sessions s WHERE s.project_id = p.id) AS chat_count
		FROM projects p
		ORDER BY p.updated_at DESC, p.name ASC
	`)
	if err != nil {
		return []Project{}
	}
	defer rows.Close()

	out := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			continue
		}
		out = append(out, *p)
	}
	return out
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanProject(row rowScanner) (*Project, error) {
	var p Project
	var dataSources, mcpSelection string
	if err := row.Scan(
		&p.ID, &p.Name, &p.Description, &p.WorkspacePath, &p.RepoURL,
		&p.Instructions, &dataSources, &mcpSelection, &p.CreatedAt, &p.UpdatedAt, &p.ChatCount,
	); err != nil {
		return nil, err
	}
	p.DataSources = decodeDataSources(dataSources)
	p.McpSelection = decodeMcpSelection(mcpSelection)
	return &p, nil
}

// RenameProject updates a project's name.
func RenameProject(id, name string) error {
	_, err := DB.Exec(
		`UPDATE projects SET name = ?, updated_at = ? WHERE id = ?`,
		name, time.Now().UTC().Format(time.RFC3339), id,
	)
	return err
}

// UpdateProjectMeta updates the editable project-settings fields (description,
// instructions, data sources) in one call.
func UpdateProjectMeta(id, description, instructions string, dataSources []string) error {
	if dataSources == nil {
		dataSources = []string{}
	}
	ds, _ := json.Marshal(dataSources)
	_, err := DB.Exec(
		`UPDATE projects SET description = ?, instructions = ?, data_sources = ?, updated_at = ? WHERE id = ?`,
		description, instructions, string(ds), time.Now().UTC().Format(time.RFC3339), id,
	)
	return err
}

// UpdateProjectMcpSelection persists the project-level MCP connector selection,
// shared by all of the project's chats.
func UpdateProjectMcpSelection(id string, selection map[string]bool) error {
	if selection == nil {
		selection = map[string]bool{}
	}
	sel, _ := json.Marshal(selection)
	_, err := DB.Exec(
		`UPDATE projects SET mcp_selection = ?, updated_at = ? WHERE id = ?`,
		string(sel), time.Now().UTC().Format(time.RFC3339), id,
	)
	return err
}

// DeleteProject removes a project. Sessions that belong to it are unlinked
// (project_id cleared) rather than deleted here — the handler decides whether
// to also delete the project's chats. (The shared workspace directory is a
// filesystem concern handled by the caller.)
func DeleteProject(id string) error {
	if _, err := DB.Exec(`UPDATE sessions SET project_id = '' WHERE project_id = ?`, id); err != nil {
		return err
	}
	_, err := DB.Exec(`DELETE FROM projects WHERE id = ?`, id)
	return err
}

// SetSessionProject links (or unlinks, with projectID == "") a session to a
// project. Also bumps the project's updated_at so it sorts to the top.
func SetSessionProject(sessionID, projectID string) error {
	if _, err := DB.Exec(`UPDATE sessions SET project_id = ? WHERE id = ?`, projectID, sessionID); err != nil {
		return err
	}
	if projectID != "" {
		_, _ = DB.Exec(`UPDATE projects SET updated_at = ? WHERE id = ?`,
			time.Now().UTC().Format(time.RFC3339), projectID)
	}
	return nil
}

// GetSessionProject returns the project id a session belongs to ("" if none).
func GetSessionProject(sessionID string) string {
	var pid sql.NullString
	_ = DB.QueryRow(`SELECT project_id FROM sessions WHERE id = ?`, sessionID).Scan(&pid)
	return pid.String
}

// SetProjectRepoURL sets the durable-store remote (repo_url) for a project.
// Used when enabling backup binds a project workspace to a Git remote for the
// first time.
func SetProjectRepoURL(id, repoURL string) error {
	_, err := DB.Exec(`UPDATE projects SET repo_url = ? WHERE id = ?`, repoURL, id)
	return err
}

// SetProjectStarred sets the project-scoped star flag on a chat. This is
// independent of the global favorite flag stored in the session config.
func SetProjectStarred(sessionID string, starred bool) error {
	v := 0
	if starred {
		v = 1
	}
	_, err := DB.Exec(`UPDATE sessions SET project_starred = ? WHERE id = ?`, v, sessionID)
	return err
}

// ListProjectSessions returns the sessions belonging to a project, with
// project-starred chats first, then most-recently-updated. Never returns nil.
func ListProjectSessions(projectID string) []SessionListItem {
	rows, err := DB.Query(`
		SELECT s.id, s.config, s.draft, s.project_starred, s.created_at, s.updated_at,
			(SELECT COUNT(*) FROM messages m WHERE m.session_id = s.id) AS msg_count
		FROM sessions s
		WHERE s.project_id = ?
		ORDER BY s.project_starred DESC, s.updated_at DESC
	`, projectID)
	if err != nil {
		return []SessionListItem{}
	}
	defer rows.Close()
	return scanProjectSessions(rows)
}
