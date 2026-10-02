// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package projects

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// makeChat creates a session, links it to the project, adds messages, and
// backdates its updated_at to the given time.
func makeChat(t *testing.T, projectID, sid, label string, updatedAt time.Time, msgs ...string) {
	t.Helper()
	cfg := config.DefaultSessionConfig()
	cfg.Label = label
	if err := db.CreateSession(sid, cfg); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionProject(sid, projectID); err != nil {
		t.Fatal(err)
	}
	for i, m := range msgs {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if err := db.AddMessage(sid, role, m); err != nil {
			t.Fatal(err)
		}
	}
	// Backdate updated_at (AddMessage sets it to now). SQLite CURRENT_TIMESTAMP
	// format is UTC "2006-01-02 15:04:05".
	if _, err := db.DB.Exec(
		`UPDATE sessions SET updated_at = ? WHERE id = ?`,
		updatedAt.UTC().Format("2006-01-02 15:04:05"), sid,
	); err != nil {
		t.Fatal(err)
	}
}

func TestHandleSummarizeCandidates(t *testing.T) {
	setup(t)
	p := createProjectViaHandler(t, `{"name":"Acme Deal"}`)

	now := time.Now().UTC()
	makeChat(t, p.ID, "recent1", "Kickoff", now.Add(-2*24*time.Hour), "hi", "hello")
	makeChat(t, p.ID, "old1", "Old planning", now.Add(-30*24*time.Hour), "plan", "ok")
	// Empty chat (no messages) should be excluded entirely.
	makeChat(t, p.ID, "empty1", "Empty", now.Add(-1*time.Hour))

	req := httptest.NewRequest("GET", "/api/projects/"+p.ID+"/summarize/candidates", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	HandleSummarizeCandidates(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Chats []SummarizeCandidate `json:"chats"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)

	byID := map[string]SummarizeCandidate{}
	for _, c := range resp.Chats {
		byID[c.SessionID] = c
	}
	if len(resp.Chats) != 2 {
		t.Fatalf("expected 2 non-empty candidates, got %d: %+v", len(resp.Chats), resp.Chats)
	}
	if _, ok := byID["empty1"]; ok {
		t.Error("empty chat should be excluded")
	}
	if !byID["recent1"].ActiveLastWeek {
		t.Error("recent1 should be flagged active_last_week")
	}
	if byID["old1"].ActiveLastWeek {
		t.Error("old1 should NOT be flagged active_last_week")
	}
	if byID["recent1"].Title != "Kickoff" || byID["recent1"].Messages != 2 {
		t.Errorf("unexpected recent1 candidate: %+v", byID["recent1"])
	}
}

func TestHandleSummarizeCandidates_ProjectNotFound(t *testing.T) {
	setup(t)
	req := httptest.NewRequest("GET", "/api/projects/nope/summarize/candidates", nil)
	req.SetPathValue("id", "nope")
	w := httptest.NewRecorder()
	HandleSummarizeCandidates(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestBuildTranscript_OnlyProjectChatsWithMessages(t *testing.T) {
	setup(t)
	p := createProjectViaHandler(t, `{"name":"Acme"}`)
	other := createProjectViaHandler(t, `{"name":"Other"}`)

	now := time.Now().UTC()
	makeChat(t, p.ID, "a", "Chat A", now, "question A", "answer A")
	makeChat(t, other.ID, "b", "Chat B", now, "question B", "answer B") // different project
	makeChat(t, p.ID, "c", "Empty C", now)                             // no messages

	// Include all three ids, but only "a" should end up in the transcript.
	transcript, included := buildTranscript(p.ID, []string{"a", "b", "c"})
	if included != 1 {
		t.Fatalf("included = %d, want 1", included)
	}
	if !strings.Contains(transcript, "Chat A") || !strings.Contains(transcript, "answer A") {
		t.Errorf("transcript missing chat A content: %q", transcript)
	}
	if strings.Contains(transcript, "answer B") {
		t.Error("transcript must not include a chat from another project")
	}
}

func TestSummarize_RequiresSelection(t *testing.T) {
	setup(t)
	p := createProjectViaHandler(t, `{"name":"Acme"}`)
	req := httptest.NewRequest("POST", "/api/projects/"+p.ID+"/summarize", strings.NewReader(`{"session_ids":[]}`))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	HandleSummarize(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty selection, got %d: %s", w.Code, w.Body.String())
	}
}

func TestParseDBTimeAndIsRecent(t *testing.T) {
	now := time.Now().UTC()
	cutoff := now.Add(-activeWindow)
	// SQLite format.
	recent := now.Add(-24 * time.Hour).Format("2006-01-02 15:04:05")
	old := now.Add(-10 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	if !isRecent(recent, cutoff) {
		t.Error("expected recent timestamp to be within the window")
	}
	if isRecent(old, cutoff) {
		t.Error("expected old timestamp to be outside the window")
	}
	if !parseDBTime(now.Format(time.RFC3339)).Equal(parseDBTime(now.Format(time.RFC3339))) {
		t.Error("RFC3339 fallback should parse")
	}
	if !parseDBTime("").IsZero() {
		t.Error("empty string should parse to zero time")
	}
}
