// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package projects

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/connectors"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/durable"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/workdirs"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// HandleList — GET /api/projects
func HandleList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"projects": db.ListProjects()})
}

// HandleGet — GET /api/projects/{id}
func HandleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := db.GetProject(id)
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "project not found"})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

type createRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	RepoURL     string `json:"repo_url"`
}

// HandleCreate — POST /api/projects. Provisions the shared workspace (fresh
// folder, or a repo clone when repo_url is supplied), then records the project.
func HandleCreate(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	r.Body.Close()

	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "project name is required"})
		return
	}

	res, err := ProvisionWorkspace(name, req.RepoURL)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":  err.Error(),
			"output": res.CloneOutput,
		})
		return
	}

	id := strings.ReplaceAll(chat.NewUUID(), "-", "")
	if err := db.CreateProject(id, name, strings.TrimSpace(req.Description), res.WorkspacePath, res.RepoURL); err != nil {
		// Roll back a freshly-created folder (but never delete a directory the
		// clone reused/created if recording fails — safest to leave clones).
		if res.RepoURL == "" {
			_ = os.RemoveAll(res.WorkspacePath)
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	// Durable file storage: scaffold the durable-store folder convention and write the
	// platform-populated .system/metadata for the project's shared workspace.
	// Best-effort — a metadata failure does not undo a successfully created
	// project.
	if err := durable.InitProjectShare(res.WorkspacePath, id); err != nil {
		log.Printf("[projects] durable share init failed for %s: %v", id, err)
	}

	writeJSON(w, http.StatusOK, db.GetProject(id))
}

type updateRequest struct {
	Name         *string   `json:"name"`
	Description  *string   `json:"description"`
	Instructions *string   `json:"instructions"`
	DataSources  *[]string `json:"data_sources"`
}

// HandleUpdate — PATCH /api/projects/{id}. Renames and/or updates project
// settings (description, instructions, data sources). Fields omitted from the
// body are left unchanged.
func HandleUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing := db.GetProject(id)
	if existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "project not found"})
		return
	}

	var req updateRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	r.Body.Close()

	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "project name cannot be empty"})
			return
		}
		if err := db.RenameProject(id, name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
	}

	// Meta update (description / instructions / data_sources): only issue it
	// when at least one of those fields was provided; use existing values for
	// the rest so a partial PATCH doesn't wipe them.
	if req.Description != nil || req.Instructions != nil || req.DataSources != nil {
		desc := existing.Description
		if req.Description != nil {
			desc = strings.TrimSpace(*req.Description)
		}
		instr := existing.Instructions
		if req.Instructions != nil {
			instr = *req.Instructions
		}
		ds := existing.DataSources
		if req.DataSources != nil {
			ds = *req.DataSources
		}
		if err := db.UpdateProjectMeta(id, desc, instr, ds); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
	}

	writeJSON(w, http.StatusOK, db.GetProject(id))
}

// HandleDelete — DELETE /api/projects/{id}. Removes the project. By default the
// project's chats are unlinked (kept as loose chats); with ?delete_chats=true
// the chats are deleted too. The shared workspace directory is left on disk
// (the user's files are the durable source of truth); a repo-backed workspace
// is never auto-removed.
func HandleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := db.GetProject(id)
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "project not found"})
		return
	}

	if strings.EqualFold(r.URL.Query().Get("delete_chats"), "true") {
		for _, s := range db.ListProjectSessions(id) {
			chat.StopSession(s.SessionID)
			chat.EndSession(s.SessionID)
			cfg, _ := db.GetSessionConfig(s.SessionID)
			ocSession := db.GetOpencodeSession(s.SessionID)
			db.DeleteSession(s.SessionID)
			// Workspace hygiene: keep every session-delete path on the same rails. A project
			// chat runs in the shared project workspace, so this is a no-op
			// today; it stops the leak from returning if a loose chat can ever
			// be moved into a project.
			workdirs.RemoveAutoChat(cfg.Workdir)
			chat.DeleteOpencodeSession(ocSession)
		}
	}
	if err := db.DeleteProject(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// HandleListChats — GET /api/projects/{id}/chats. Project chats, starred first.
func HandleListChats(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !db.ProjectExists(id) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "project not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": db.ListProjectSessions(id)})
}

// HandleNewChat — POST /api/projects/{id}/chats. Creates a chat whose working
// directory is the project's shared workspace (the per-chat auto-folder is
// suppressed), inheriting the project's instructions + data sources, and links
// it to the project.
func HandleNewChat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := db.GetProject(id)
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "project not found"})
		return
	}

	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	r.Body.Close()

	cfg := config.DefaultSessionConfig()
	if body != nil {
		raw, _ := json.Marshal(body)
		_ = json.Unmarshal(raw, &cfg)
	}
	cfg.Backend = config.NormalizeBackend(cfg.Backend)
	// Fix the working directory to the shared project workspace (suppresses the
	// per-chat folder) and layer in the project's standing instructions.
	cfg.Workdir = p.WorkspacePath
	cfg.ProjectInstructions = p.Instructions
	// Seed the chat's connector selection from the project's shared selection
	// so a project chat inherits the project-level choice. The
	// project selection remains the source of truth; this copy keeps the
	// session config self-describing.
	if len(p.McpSelection) > 0 {
		cfg.McpSelection = p.McpSelection
	}

	sid := chat.NewUUID()
	if err := db.CreateSession(sid, cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := db.SetSessionProject(sid, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sid, "config": cfg, "project_id": id})
}

type starRequest struct {
	Starred bool `json:"starred"`
}

// HandleStarChat — POST /api/sessions/{session_id}/project-star. Toggles the
// project-scoped star on a chat (independent of the global favorite flag).
func HandleStarChat(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	var req starRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	r.Body.Close()
	if err := db.SetProjectStarred(sid, req.Starred); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "starred": req.Starred})
}

// HandleGetProjectMcp — GET /api/projects/{id}/mcp. Returns the project's shared
// connector selection and live status. Live status reflects the
// single opencode process bound to the project's shared workspace.
func HandleGetProjectMcp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := db.GetProject(id)
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "project not found"})
		return
	}
	conns, err := connectors.Build(p.WorkspacePath, p.McpSelection)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connectors": conns})
}

// HandleSetProjectMcp — PUT /api/projects/{id}/mcp. Updates the project's shared
// connector selection, persists it, and applies the change live to the project's
// running opencode process. The change affects every chat in the project (one
// shared workspace, one process); the UI surfaces that blast radius.
// body: {"selection": {"github": true, ...}}
func HandleSetProjectMcp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := db.GetProject(id)
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "project not found"})
		return
	}
	var body struct {
		Selection map[string]bool `json:"selection"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid body: " + err.Error()})
		return
	}
	r.Body.Close()

	sel, err := connectors.Merge(p.McpSelection, body.Selection)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := db.UpdateProjectMcpSelection(id, sel); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	connectors.Apply(p.WorkspacePath, sel)

	conns, err := connectors.Build(p.WorkspacePath, sel)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connectors": conns})
}
