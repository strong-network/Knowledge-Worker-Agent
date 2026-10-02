// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package recall

// Cross-session retrieval: the three retrieval verbs, as read-only HTTP endpoints.
//
// Why HTTP at all, when the consumer is an MCP server? Because opencode spawns
// the MCP server as a separate process, which cannot share the web server's
// *sql.DB. The alternatives were to open the SQLite file a second time from
// that process, or to reach the data over the network. HTTP won:
//
//   - One process owns the database. No second handle, no concurrency question.
//   - Read-only becomes structural: the bridge can only issue GETs, and every
//     handler here only ever runs SELECTs.
//   - The spec's durability contract is "the JSON shapes, not the tables".
//     An endpoint returning those shapes *is* that contract, with the mapping
//     in exactly one place.
//   - A future search UI in the frontend calls the same endpoints.
//
// These are localhost and unauthenticated like the rest of the API
// (AGENTS.md §5): the security model is a single-user, network-isolated
// workspace, and this adds no new exposure. It does add read access to chat
// history over HTTP -- but /api/sessions and /api/history already expose the
// same data to the same audience.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func intParam(r *http.Request, name string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get(name)))
	return n
}

// HandleList serves GET /api/recall/sessions.
//
// Params: from, to (YYYY-MM-DD or RFC3339), project, limit, offset,
// min_messages, notes (range|none).
func HandleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	notes := strings.ToLower(strings.TrimSpace(q.Get("notes")))
	if notes != "" && notes != db.NotesRange && notes != db.NotesNone {
		writeErr(w, http.StatusBadRequest, `notes must be "range" or "none"`)
		return
	}
	res, err := db.RecallList(db.RecallListOptions{
		From:    q.Get("from"),
		To:      q.Get("to"),
		Project: q.Get("project"),
		Limit:   intParam(r, "limit"),
		Offset:  intParam(r, "offset"),
		MinMsgs: intParam(r, "min_messages"),
		Notes:   notes,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// HandleSearch serves GET /api/recall/search.
//
// Params: q (required), role, from, to, project, limit, offset.
func HandleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := strings.TrimSpace(q.Get("q"))
	if query == "" {
		writeErr(w, http.StatusBadRequest, "q is required")
		return
	}
	role := strings.ToLower(strings.TrimSpace(q.Get("role")))
	if role != "" && role != "user" && role != "assistant" {
		writeErr(w, http.StatusBadRequest, `role must be "user" or "assistant"`)
		return
	}
	hits, err := db.RecallSearch(db.RecallSearchOptions{
		Query:   query,
		Role:    role,
		From:    q.Get("from"),
		To:      q.Get("to"),
		Project: q.Get("project"),
		Limit:   intParam(r, "limit"),
		Offset:  intParam(r, "offset"),
	})
	if err != nil {
		// The index being absent is a server-side condition the caller cannot
		// fix and must not mistake for "no results" -- report it as such. The
		// retrieval side never tries to build the index: under the reader/writer
		// split, the process asking may have no write access at all.
		if strings.Contains(err.Error(), "index unavailable") {
			writeErr(w, http.StatusServiceUnavailable, "search index unavailable")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, hits)
}

// HandleRead serves GET /api/recall/read.
//
// Params: session_id (required), limit, offset, order (oldest|newest),
// from, to.
func HandleRead(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sid := strings.TrimSpace(q.Get("session_id"))
	if sid == "" {
		writeErr(w, http.StatusBadRequest, "session_id is required")
		return
	}
	order := strings.ToLower(strings.TrimSpace(q.Get("order")))
	if order != "" && order != "oldest" && order != "newest" {
		writeErr(w, http.StatusBadRequest, `order must be "oldest" or "newest"`)
		return
	}
	tr, err := db.RecallRead(db.RecallReadOptions{
		SessionID: sid,
		From:      q.Get("from"),
		To:        q.Get("to"),
		Limit:     intParam(r, "limit"),
		Offset:    intParam(r, "offset"),
		Order:     order,
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tr)
}
