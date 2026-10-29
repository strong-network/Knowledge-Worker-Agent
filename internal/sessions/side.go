// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessions

import (
	"net/http"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// Side chats: a temporary chat for a side question, in its parent's folder,
// whose agent starts from the parent's conversation and adds nothing to it.

// HandleSideChat serves POST /api/sessions/{session_id}/side: makes a side
// chat of the chat, for the client to open.
func HandleSideChat(w http.ResponseWriter, r *http.Request) {
	parent := r.PathValue("session_id")
	cfg, err := db.GetSessionConfig(parent)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "This chat no longer exists."})
		return
	}
	if db.IsSessionDeprecated(parent) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Read-only chats can't have side chats."})
		return
	}
	// Everything about how the agent works is the parent's; what it's called,
	// and that it's kept, aren't.
	cfg.ParentID, cfg.Fork, cfg.Temporary = parent, true, true
	cfg.Favorite, cfg.Pinned, cfg.Keep = false, false, false
	cfg.Label, cfg.LabelManual, cfg.SessionName = "Side chat", false, ""
	sid := chat.NewUUID()
	if err := db.CreateSession(sid, cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sid, "config": cfg})
}
