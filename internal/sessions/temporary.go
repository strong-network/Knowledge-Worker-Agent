// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessions

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// Temporary chats aren't kept in the chat history: the sweeper deletes one
// db.RetentionTemporary after the last message from the user or the agent.

// expiryMu keeps the sweeper from deleting a chat while a message to it is on
// its way: the message either counts as activity first or finds the chat gone.
var expiryMu sync.Mutex

const expiredMessage = "This temporary chat was deleted after 24 hours of inactivity."

// temporaryView is what a client needs to show a chat's temporary state.
type temporaryView struct {
	Temporary bool  `json:"temporary"`
	ExpiresIn int64 `json:"expires_in"`
	Favorite  bool  `json:"favorite"`
	Pinned    bool  `json:"pinned"`
}

// HandleSetTemporary serves PUT /api/sessions/{session_id}/temporary with
// {"temporary": true} to make a chat temporary, or false to keep it.
func HandleSetTemporary(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	var body struct {
		Temporary *bool `json:"temporary"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil || body.Temporary == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `Send {"temporary": true} or false.`})
		return
	}
	expiryMu.Lock()
	defer expiryMu.Unlock()
	if !db.SessionExists(sid) {
		msg := "This chat no longer exists."
		if db.ChatExpired(sid) {
			msg = expiredMessage
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": msg})
		return
	}
	if *body.Temporary {
		if reason := cannotBeTemporary(sid); reason != "" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": reason})
			return
		}
	}
	if err := db.SetTemporary(sid, *body.Temporary); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	cfg, _ := db.GetSessionConfig(sid)
	left, _ := db.TemporaryExpiresIn(sid, time.Now())
	writeJSON(w, http.StatusOK, temporaryView{Temporary: cfg.Temporary, ExpiresIn: left, Favorite: cfg.Favorite, Pinned: cfg.Pinned})
}

// cannotBeTemporary says why a chat can't be made temporary, or "" if it can.
// Deleting it would take away what coworkers were given.
func cannotBeTemporary(sid string) string {
	if db.IsSessionDeprecated(sid) {
		return "Read-only chats can't be made temporary."
	}
	if db.SessionShared(sid) {
		return "This chat is shared, so it can't be made temporary. Stop sharing first."
	}
	for _, a := range db.SharedArtifacts() {
		if a.SessionID == sid {
			return "Files from this chat are shared, so it can't be made temporary. Stop sharing them first."
		}
	}
	return ""
}

// admitMessage is called before a message starts a turn. It reports false
// when the chat was a temporary chat that has been deleted. Otherwise the
// message counts as activity, so the sweeper leaves the chat alone.
func admitMessage(sid string) bool {
	expiryMu.Lock()
	defer expiryMu.Unlock()
	if db.SessionExists(sid) {
		db.TouchActivity(sid)
		return true
	}
	return !db.ChatExpired(sid)
}

// StartTemporarySweeper deletes temporary chats whose time has run out, once
// a minute.
func StartTemporarySweeper() {
	log.Printf("[sessions] temporary chats are deleted %s after their last message", db.RetentionTemporary)
	go func() {
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			SweepTemporary(time.Now())
			<-tick.C
		}
	}()
}

// SweepTemporary deletes the temporary chats whose time ran out by now. A chat
// with a turn running is left: the turn's reply starts its time again.
func SweepTemporary(now time.Time) int {
	deleted := 0
	for _, sid := range db.TemporaryChatsDue(now) {
		if expireChat(sid, now) {
			deleted++
		}
	}
	if err := db.PruneExpiredChats(now.Add(-30 * 24 * time.Hour)); err != nil {
		log.Printf("[sessions] could not prune expired chats: %v", err)
	}
	return deleted
}

func expireChat(sid string, now time.Time) bool {
	expiryMu.Lock()
	defer expiryMu.Unlock()
	if chat.IsStreaming(sid) {
		return false
	}
	if left, ok := db.TemporaryExpiresIn(sid, now); !ok || left > 0 {
		return false
	}
	// Its day notes go too: a temporary chat is gone completely.
	if _, err := deleteChat(sid, true); err != nil {
		log.Printf("[sessions] could not delete temporary chat %s: %v", sid, err)
		return false
	}
	if err := db.RecordExpiredChat(sid); err != nil {
		log.Printf("[sessions] could not record expired chat %s: %v", sid, err)
	}
	log.Printf("[sessions] deleted temporary chat %s after %s without a message", sid, db.RetentionTemporary)
	return true
}
