// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessions

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

func TestHandleSearch(t *testing.T) {
	setupTestDB(t)
	db.CreateSession("s1", config.SessionConfig{Label: "Trip", Workdir: "/tmp"})
	db.AddMessage("s1", "user", "book a flight to Oslo")

	w := httptest.NewRecorder()
	HandleSearch(w, httptest.NewRequest("GET", "/api/search?q=oslo&from=2000-01-01&limit=5", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var res db.ChatSearchResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Chats) != 1 || res.Chats[0].SessionID != "s1" || res.Chats[0].Message == nil {
		t.Fatalf("got %+v", res.Chats)
	}

	w = httptest.NewRecorder()
	HandleHistory(w, httptest.NewRequest("GET", "/api/history?session_id=s1", nil))
	var msgs []db.ChatMessage
	json.Unmarshal(w.Body.Bytes(), &msgs)
	if len(msgs) != 1 || msgs[0].ID != res.Chats[0].Message.ID {
		t.Errorf("history %+v doesn't carry the result's message id %d", msgs, res.Chats[0].Message.ID)
	}
}
