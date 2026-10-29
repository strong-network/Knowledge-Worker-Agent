// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessions

import (
	"log"
	"net/http"
	"strconv"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// HandleSearch serves GET /api/search, for the Search page: the chats whose
// titles or messages match q, best first, one per chat. from and to bound the
// messages' dates; limit and offset page through the chats.
func HandleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	res, err := db.SearchChats(db.ChatSearchOptions{
		Query: q.Get("q"), From: q.Get("from"), To: q.Get("to"), Limit: limit, Offset: offset,
	})
	if err != nil {
		log.Printf("[search] %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Search isn't available right now. Try again in a moment."})
		return
	}
	writeJSON(w, http.StatusOK, res)
}
