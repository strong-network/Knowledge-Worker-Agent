// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessions

import (
	"encoding/json"
	"net/http"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/connectors"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// HandleGetSessionMcp returns the per-chat connector selection and live status
// for a standalone chat.
// GET /api/sessions/{session_id}/mcp
func HandleGetSessionMcp(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	if !db.SessionExists(sid) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	cfg, err := db.GetSessionConfig(sid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	conns, err := connectors.Build(cfg.Workdir, cfg.McpSelection)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connectors": conns})
}

// HandleSetSessionMcp updates the per-chat connector selection, persists it, and
// applies the change live to the running opencode server (if one is serving the
// chat) by connecting/disconnecting the affected servers.
// PUT /api/sessions/{session_id}/mcp   body: {"selection": {"github": true, ...}}
func HandleSetSessionMcp(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	if !db.SessionExists(sid) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	var body struct {
		Selection map[string]bool `json:"selection"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body: " + err.Error()})
		return
	}
	cfg, err := db.GetSessionConfig(sid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	sel, err := connectors.Merge(cfg.McpSelection, body.Selection)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	cfg.McpSelection = sel
	if err := db.UpdateSessionConfig(sid, cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	connectors.Apply(cfg.Workdir, sel)

	conns, err := connectors.Build(cfg.Workdir, sel)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connectors": conns})
}
