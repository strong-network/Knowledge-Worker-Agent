// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package guest

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// Answering a permission request authorises a tool in the owner's workspace,
// so guests may do it only when the owner has allowed it for this share.
// Only the opencode server backend asks; the other backends
// have no request a guest could answer, so there is no guest /answer route.

// frameFor is Frame for one chat: with answers allowed, a permission request
// reaches guests in full, since approving blind is worse than not being asked.
func frameFor(sid string) func(map[string]any) (map[string]any, bool) {
	return func(ev map[string]any) (map[string]any, bool) {
		if ev["type"] == "permission" && db.SessionShareOptions(sid).AllowPermissions {
			return pick(ev, "type", "permission_id", "permission", "patterns"), true
		}
		return Frame(ev)
	}
}

func (s *server) mayAnswer(w http.ResponseWriter, sid string) bool {
	if !db.SessionShareOptions(sid).AllowPermissions {
		owner := config.OwnerFullName
		if owner == "" {
			owner = "The owner"
		}
		writeJSON(w, http.StatusForbidden, map[string]string{"error": owner + " answers the assistant's requests in this chat."})
		return false
	}
	return true
}

// pendingPermission serves GET .../permission: the request waiting now, for a
// page that learns answers are allowed after the request was shown to it.
func (s *server) pendingPermission(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("id")
	if !shared(w, sid) {
		return
	}
	if _, ok := mustIdentify(w, r); !ok {
		return
	}
	if !s.mayAnswer(w, sid) {
		return
	}
	p, ok := chat.PendingPermission(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Nothing is waiting for an answer."})
		return
	}
	writeJSON(w, http.StatusOK, pick(p, "type", "permission_id", "permission", "patterns"))
}

// answerPermission allows once or denies. Never "always": that is a standing
// rule in the owner's workspace, which outlasts the share it was made in.
func (s *server) answerPermission(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(w, r) {
		return
	}
	sid := r.PathValue("id")
	if !shared(w, sid) {
		return
	}
	id, ok := mustIdentify(w, r)
	if !ok {
		return
	}
	if !s.mayAnswer(w, sid) {
		return
	}
	var body struct {
		PermissionID string `json:"permission_id"`
		Response     string `json:"response"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	resp := strings.TrimSpace(body.Response)
	if resp != "once" && resp != "reject" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `response must be "once" or "reject"`})
		return
	}
	ok, err := chat.AnswerPermission(r.Context(), sid, strings.TrimSpace(body.PermissionID), resp, chat.Author{ID: id.ID, Name: id.Name})
	if err != nil {
		log.Printf("[ERROR] guest: session=%s answer permission: %v", sid, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "The answer could not be delivered."})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "That request has already been answered."})
		return
	}
	log.Printf("[guest] session=%s permission %s by guest %s", sid, resp, id.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
