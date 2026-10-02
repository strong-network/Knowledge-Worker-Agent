// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessions

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/durable"
)

// Durable-storage backup HTTP handlers for chats.

// backupStateResponse is the payload for the backup/sync status affordance.
// Status is one of db.BackupStatus* (plain-language, never raw git).
type backupStateResponse struct {
	SessionID    string `json:"session_id"`
	Temporary    bool   `json:"temporary"`
	Remote       string `json:"remote"`
	Status       string `json:"status"`
	LastSyncedAt string `json:"last_synced_at"`
}

// HandleBackupState — GET /api/sessions/{session_id}/backup
// Returns the durable-storage state for a chat. Temporary chats report a
// "not backed up" status (they are never provisioned a durable store).
func HandleBackupState(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	if !db.SessionExists(sid) {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, backupStateValue(sid))
}

type enableBackupRequest struct {
	RemoteURL string `json:"remote_url"`
}

// HandleEnableBackup — POST /api/sessions/{session_id}/backup/enable
// Provisions durable backup for a chat against a customer-controlled Git remote
// (scaffold + secret exclusions + attach remote + first sync). Rejected for
// Temporary chats. Body: {"remote_url": "..."}.
func HandleEnableBackup(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	if !db.SessionExists(sid) {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

	var body enableBackupRequest
	_ = json.NewDecoder(r.Body).Decode(&body)
	r.Body.Close()
	remote := strings.TrimSpace(body.RemoteURL)
	if remote == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "remote_url is required"})
		return
	}

	cfg, _ := db.GetSessionConfig(sid)
	projectID := db.GetSessionProject(sid)
	transcript, err := durable.EnableBackupForChat(sid, sid, projectID, cfg, remote)
	if err != nil {
		code := http.StatusBadGateway
		if err == durable.ErrTemporaryChat {
			code = http.StatusConflict
		}
		log.Printf("[sessions] enable backup failed for %s: %v", sid, err)
		writeJSON(w, code, map[string]any{"error": err.Error(), "output": transcript})
		return
	}
	writeJSON(w, http.StatusOK, backupStateValue(sid))
}

// backupStateValue builds the backup-state payload for a chat (shared by the
// GET handler and the enable handler's success response).
func backupStateValue(sid string) backupStateResponse {
	cfg, _ := db.GetSessionConfig(sid)
	st := db.GetSessionBackupState(sid)
	status := st.Status
	if cfg.Temporary {
		status = db.BackupStatusSkipped
	}
	return backupStateResponse{
		SessionID:    sid,
		Temporary:    cfg.Temporary,
		Remote:       st.Remote,
		Status:       status,
		LastSyncedAt: st.LastSyncedAt,
	}
}
