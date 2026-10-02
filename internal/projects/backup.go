// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package projects

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/durable"
)

// Durable-storage backup HTTP handlers for projects.

type projectBackupResponse struct {
	ProjectID    string `json:"project_id"`
	RepoURL      string `json:"repo_url"`
	Keep         bool   `json:"keep"`
	Status       string `json:"status"`
	LastSyncedAt string `json:"last_synced_at"`
}

// HandleBackupState — GET /api/projects/{id}/backup
func HandleBackupState(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := db.GetProject(id)
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "project not found"})
		return
	}
	writeJSON(w, http.StatusOK, projectBackupValue(id))
}

type enableProjectBackupRequest struct {
	RemoteURL string `json:"remote_url"`
}

// HandleEnableBackup — POST /api/projects/{id}/backup/enable
// Provisions durable backup for a project's shared workspace against a
// customer-controlled Git remote. If the project has no repo_url yet, the
// supplied remote is recorded as its repo_url.
func HandleEnableBackup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := db.GetProject(id)
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "project not found"})
		return
	}

	var body enableProjectBackupRequest
	_ = json.NewDecoder(r.Body).Decode(&body)
	r.Body.Close()

	remote := strings.TrimSpace(body.RemoteURL)
	if remote == "" {
		remote = strings.TrimSpace(p.RepoURL) // fall back to an existing repo URL
	}
	if remote == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "remote_url is required"})
		return
	}

	transcript, err := durable.EnableBackupForProject(id, p.WorkspacePath, remote)
	if err != nil {
		log.Printf("[projects] enable backup failed for %s: %v", id, err)
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "output": transcript})
		return
	}
	// Record the remote as the project's repo_url if it didn't have one.
	if strings.TrimSpace(p.RepoURL) == "" {
		_ = db.SetProjectRepoURL(id, remote)
	}
	writeJSON(w, http.StatusOK, projectBackupValue(id))
}

type keepRequest struct {
	Keep bool `json:"keep"`
}

// HandleSetKeep — POST /api/projects/{id}/keep
// Marks (or unmarks) a project as exempt from inactivity cleanup ("Keep").
func HandleSetKeep(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !db.ProjectExists(id) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "project not found"})
		return
	}
	var req keepRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	r.Body.Close()
	if err := db.SetProjectKeep(id, req.Keep); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project_id": id, "keep": req.Keep})
}

func projectBackupValue(id string) projectBackupResponse {
	p := db.GetProject(id)
	st := db.GetProjectBackupState(id)
	resp := projectBackupResponse{
		ProjectID:    id,
		Keep:         db.GetProjectKeep(id),
		Status:       st.Status,
		LastSyncedAt: st.LastSyncedAt,
	}
	if p != nil {
		resp.RepoURL = p.RepoURL
	}
	return resp
}
