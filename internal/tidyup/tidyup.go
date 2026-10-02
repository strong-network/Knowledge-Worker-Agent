// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package tidyup implements the workspace-hygiene surface: listing the
// chats worth reviewing, listing the orphaned folders left behind by an older
// bug, and acting on what the user selects.
//
// Nothing here removes anything containing user work without being asked. The
// list is safe to show, ignoring it is a valid outcome, and an item that is
// never acted on is offered again rather than deleted.
package tidyup

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/workdirs"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// HandleList — GET /api/tidyup
// The review list: chats idle past the review window, plus any orphaned
// folders. "total" is how many chats qualified before the display limit, so the
// user is told the full extent even when the list is capped.
func HandleList(w http.ResponseWriter, r *http.Request) {
	if !config.HygieneEnabled {
		writeJSON(w, http.StatusOK, map[string]any{
			"items": []db.TidyItem{}, "orphans": []Orphan{},
			"count": 0, "total": 0, "enabled": false,
		})
		return
	}

	now := time.Now()
	items, total := db.ReviewCandidates(now, config.HygieneReviewAfter)
	// Measuring a folder means walking it, so it happens here — once, for the
	// items actually being shown — rather than inside the database query.
	for i := range items {
		if workdirs.IsAutoChat(items[i].Workdir) {
			items[i].SizeBytes = DirSize(items[i].Workdir)
		} else {
			// Not ours to delete, so not ours to measure: reporting a size for
			// a folder we will not touch implies that we might.
			items[i].Workdir = ""
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":        items,
		"orphans":      FindOrphans(),
		"count":        len(items),
		"total":        total,
		"limit":        db.ReviewListLimit,
		"review_after": config.HygieneReviewAfter.String(),
		"enabled":      true,
	})
}

// HandleCount — GET /api/tidyup/count
// Just how many chats are waiting, for the sidebar badge.
//
// Deliberately separate from HandleList: the badge is fetched on every app
// load, while the list measures directories on disk. Making the badge pay for
// that walk would tax startup for a number.
func HandleCount(w http.ResponseWriter, r *http.Request) {
	if !config.HygieneEnabled {
		writeJSON(w, http.StatusOK, map[string]any{"count": 0, "enabled": false})
		return
	}
	_, total := db.ReviewCandidates(time.Now(), config.HygieneReviewAfter)
	writeJSON(w, http.StatusOK, map[string]any{"count": total, "enabled": true})
}

type cleanupRequest struct {
	Items []cleanupTarget `json:"items"`
}

type cleanupTarget struct {
	// Kind is "chat" or "orphan".
	Kind string `json:"kind"`
	// ID is the session id, for a chat.
	ID string `json:"id"`
	// Path is the directory, for an orphan.
	Path string `json:"path"`
}

type cleanupResult struct {
	Kind    string `json:"kind"`
	ID      string `json:"id,omitempty"`
	Path    string `json:"path,omitempty"`
	Removed bool   `json:"removed"`
	Error   string `json:"error,omitempty"`
}

// HandleCleanup — POST /api/tidyup/cleanup
// Deletes the selected chats and orphaned folders. Every item was shown to the
// user and explicitly chosen; nothing is inferred. The durable remote is left
// intact — removing a user's git remote is a separate, explicit decision.
// Body: {"items":[{"kind":"chat","id":"…"},{"kind":"orphan","path":"…"}]}.
func HandleCleanup(w http.ResponseWriter, r *http.Request) {
	var req cleanupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	r.Body.Close()

	results := make([]cleanupResult, 0, len(req.Items))
	removed := 0
	for _, t := range req.Items {
		res := cleanupResult{Kind: t.Kind, ID: t.ID, Path: t.Path}
		var err error
		switch t.Kind {
		case "chat":
			err = cleanupChat(t.ID)
		case "orphan":
			err = cleanupOrphan(t.Path)
		default:
			res.Error = "unknown kind"
			results = append(results, res)
			continue
		}
		if err != nil {
			res.Error = err.Error()
		} else {
			res.Removed = true
			removed++
		}
		results = append(results, res)
	}
	if removed > 0 {
		log.Printf("[hygiene] user cleanup: removed %d of %d selected items", removed, len(req.Items))
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "removed": removed})
}

type snoozeRequest struct {
	// IDs are the sessions to defer. A single "id" may be sent instead.
	IDs []string `json:"ids"`
	ID  string   `json:"id"`
}

// HandleKeep — POST /api/tidyup/keep
// "Keep for now": defers the selected chats until the snooze window elapses,
// after which they are offered again.
//
// This is deliberately not the permanent Keep flag. Answering "not now" once
// must not exempt a chat from every future review — that would build a second
// invisible pile, which is the problem this feature exists to solve.
func HandleKeep(w http.ResponseWriter, r *http.Request) {
	var req snoozeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	r.Body.Close()

	ids := req.IDs
	if req.ID != "" {
		ids = append(ids, req.ID)
	}
	now := time.Now()
	snoozed := 0
	for _, id := range ids {
		if !db.SessionExists(id) {
			continue
		}
		if err := db.SnoozeSession(id, now, config.HygieneSnooze); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		snoozed++
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"snoozed": snoozed,
		"until":   now.Add(config.HygieneSnooze).UTC().Format(time.RFC3339),
	})
}

// cleanupChat removes a chat the user selected: stops any activity, deletes the
// record, and removes its auto-created workspace folder. A user-chosen
// directory is left alone by the rails in internal/workdirs.
func cleanupChat(sid string) error {
	// Someone else is using a shared chat, so it is not clutter, and a bulk
	// sweep is the wrong place to discover it was shared.
	if db.IsSessionShared(sid) {
		return tidyError("chat is shared; stop sharing it first")
	}
	cfg, _ := db.GetSessionConfig(sid)
	// Read before the row goes away; it is the only record of opencode's copy.
	ocSession := db.GetOpencodeSession(sid)
	chat.StopSession(sid)
	chat.EndSession(sid)
	if err := db.DeleteSession(sid); err != nil {
		return err
	}
	workdirs.RemoveAutoChat(cfg.Workdir)
	chat.DeleteOpencodeSession(ocSession)
	return nil
}

// cleanupOrphan removes an orphaned chat folder.
//
// The path arrives from the browser, so none of it is trusted. It is re-checked
// against the deletion rails and re-confirmed to be unreferenced at the moment
// of deletion: the list the user acted on may be minutes old, and a directory
// since claimed by a session must not be removed out from under it.
func cleanupOrphan(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errUnknownOrphan
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return errUnknownOrphan
	}
	if !workdirs.IsAutoChat(abs) {
		return errUnknownOrphan
	}
	known, ok := db.SessionWorkdirs()
	if !ok {
		return errOrphanScanUnavailable
	}
	if len(known) == 0 || known[abs] {
		// Either the session set is unusable — in which case everything looks
		// orphaned — or this directory belongs to a live chat after all.
		return errUnknownOrphan
	}
	if err := os.RemoveAll(abs); err != nil {
		return err
	}
	log.Printf("[hygiene] removed orphaned chat workspace %s", abs)
	return nil
}

type tidyError string

func (e tidyError) Error() string { return string(e) }

const (
	errUnknownOrphan         = tidyError("not an orphaned chat workspace")
	errOrphanScanUnavailable = tidyError("session list unavailable")
)
