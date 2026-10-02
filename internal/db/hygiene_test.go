// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

func hoursAgo(h int) time.Time { return time.Now().Add(-time.Duration(h) * time.Hour) }

// setSessionUpdatedAt forces a session's last-activity timestamp, which is the
// clock every hygiene decision is measured against.
func setSessionUpdatedAt(t *testing.T, id string, ts time.Time) {
	t.Helper()
	if _, err := DB.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`,
		ts.UTC().Format(time.RFC3339), id); err != nil {
		t.Fatal(err)
	}
}

// emptyChat creates a chat with no messages whose last activity is at ts.
func emptyChat(t *testing.T, id string, cfg config.SessionConfig, ts time.Time) {
	t.Helper()
	if err := CreateSession(id, cfg); err != nil {
		t.Fatal(err)
	}
	setSessionUpdatedAt(t, id, ts)
}

func hasCandidate(items []EmptyChat, id string) bool {
	for _, it := range items {
		if it.ID == id {
			return true
		}
	}
	return false
}

// The whole of automatic deletion rests on this query being narrow. Each
// condition is checked on its own so a future change cannot quietly widen it.
func TestEmptyChatCandidatesOnlyMatchesChatsWithNothingInThem(t *testing.T) {
	setupTestDB(t)
	window := 24 * time.Hour

	emptyChat(t, "idle-empty", config.SessionConfig{Label: "Abandoned"}, hoursAgo(48))
	emptyChat(t, "recent-empty", config.SessionConfig{Label: "Just made"}, hoursAgo(2))
	emptyChat(t, "kept", config.SessionConfig{Label: "Kept", Keep: true}, hoursAgo(48))

	// Idle and empty, but the user typed something and never sent it. Drafts do
	// not bump updated_at, so this chat looks idle while holding real work.
	emptyChat(t, "has-draft", config.SessionConfig{Label: "Half-written"}, hoursAgo(48))
	if err := SetSessionDraft("has-draft", "a long prompt I have not sent yet"); err != nil {
		t.Fatal(err)
	}

	// Idle, but it contains a message.
	emptyChat(t, "has-message", config.SessionConfig{Label: "Real chat"}, hoursAgo(48))
	if err := AddMessage("has-message", "user", "hello"); err != nil {
		t.Fatal(err)
	}
	setSessionUpdatedAt(t, "has-message", hoursAgo(48))

	got := EmptyChatCandidates(time.Now(), window)

	if !hasCandidate(got, "idle-empty") {
		t.Error("an idle chat with no messages and no draft should be a candidate")
	}
	for _, id := range []string{"recent-empty", "kept", "has-draft", "has-message"} {
		if hasCandidate(got, id) {
			t.Errorf("%s must not be a candidate", id)
		}
	}
}

func TestEmptyChatCandidatesCarriesTheWorkdirForwards(t *testing.T) {
	setupTestDB(t)
	emptyChat(t, "s1", config.SessionConfig{Label: "Abandoned", Workdir: "/home/developer/chat-abc123def456"}, hoursAgo(48))

	got := EmptyChatCandidates(time.Now(), 24*time.Hour)
	if len(got) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(got))
	}
	// The workdir has to come from the config read here: once the row is gone,
	// nothing records which directory belonged to the chat.
	if got[0].Workdir != "/home/developer/chat-abc123def456" {
		t.Errorf("workdir = %q, want the session's configured workdir", got[0].Workdir)
	}
}

// A zero or negative window disables the sweep rather than making everything
// instantly eligible.
func TestEmptyChatCandidatesReturnsNothingForANonPositiveWindow(t *testing.T) {
	setupTestDB(t)
	emptyChat(t, "s1", config.SessionConfig{Label: "Abandoned"}, hoursAgo(500))

	for _, w := range []time.Duration{0, -time.Hour} {
		if got := EmptyChatCandidates(time.Now(), w); len(got) != 0 {
			t.Errorf("window %s: expected no candidates, got %d", w, len(got))
		}
	}
}

func TestDeleteEmptySessionRemovesAnIdleEmptyChat(t *testing.T) {
	setupTestDB(t)
	emptyChat(t, "s1", config.SessionConfig{Label: "Abandoned"}, hoursAgo(48))

	deleted, err := DeleteEmptySession("s1", time.Now(), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("expected the chat to be deleted")
	}
	if SessionExists("s1") {
		t.Error("expected the session row to be gone")
	}
}

// The reason the conditions are repeated in the DELETE rather than trusted from
// the scan: a message can arrive in between. If it does, the chat must survive
// — and the caller must be told, so it leaves the files alone.
func TestDeleteEmptySessionRefusesOnceTheChatStopsBeingEmpty(t *testing.T) {
	setupTestDB(t)
	window := 24 * time.Hour

	cases := []struct {
		name    string
		id      string
		disturb func(t *testing.T, id string)
	}{
		{
			name: "a message arrived after the scan",
			id:   "got-message",
			disturb: func(t *testing.T, id string) {
				if err := AddMessage(id, "user", "actually, hello"); err != nil {
					t.Fatal(err)
				}
				// Force the timestamp back: even looking idle, the message alone
				// must save it.
				setSessionUpdatedAt(t, id, hoursAgo(48))
			},
		},
		{
			name: "a draft was saved after the scan",
			id:   "got-draft",
			disturb: func(t *testing.T, id string) {
				if err := SetSessionDraft(id, "wait, I was writing this"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "the chat was used, so it is no longer idle",
			id:   "got-used",
			disturb: func(t *testing.T, id string) {
				setSessionUpdatedAt(t, id, time.Now())
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emptyChat(t, tc.id, config.SessionConfig{Label: "Scratch"}, hoursAgo(48))
			// It qualifies at scan time...
			if !hasCandidate(EmptyChatCandidates(time.Now(), window), tc.id) {
				t.Fatalf("%s should be a candidate before being disturbed", tc.id)
			}
			// ...and then something happens.
			tc.disturb(t, tc.id)

			deleted, err := DeleteEmptySession(tc.id, time.Now(), window)
			if err != nil {
				t.Fatal(err)
			}
			if deleted {
				t.Error("expected the delete to be refused")
			}
			if !SessionExists(tc.id) {
				t.Error("expected the session to survive")
			}
		})
	}
}

// updated_at holds two layouts: CURRENT_TIMESTAMP writes "2006-01-02 15:04:05"
// and other rows carry RFC3339. The delete wraps the column in datetime() to
// normalize them, so this checks both directions for both layouts — an old chat
// must still be deleted (proving datetime() parses the RFC3339 "Z" suffix at
// all, rather than yielding NULL and silently never matching) and a recent one
// must still survive.
func TestDeleteEmptySessionHandlesBothTimestampLayouts(t *testing.T) {
	setupTestDB(t)
	window := 24 * time.Hour

	writeUpdatedAt := func(t *testing.T, id, ts string) {
		t.Helper()
		if err := CreateSession(id, config.SessionConfig{Label: id}); err != nil {
			t.Fatal(err)
		}
		if _, err := DB.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`, ts, id); err != nil {
			t.Fatal(err)
		}
	}

	const nativeLayout = "2006-01-02 15:04:05"
	old := time.Now().Add(-72 * time.Hour).UTC()
	recent := time.Now().Add(-2 * time.Hour).UTC()

	cases := []struct {
		id         string
		ts         string
		wantDelete bool
	}{
		{"old-native", old.Format(nativeLayout), true},
		{"old-rfc3339", old.Format(time.RFC3339), true},
		{"recent-native", recent.Format(nativeLayout), false},
		{"recent-rfc3339", recent.Format(time.RFC3339), false},
	}

	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			writeUpdatedAt(t, tc.id, tc.ts)
			deleted, err := DeleteEmptySession(tc.id, time.Now(), window)
			if err != nil {
				t.Fatal(err)
			}
			if deleted != tc.wantDelete {
				t.Errorf("updated_at=%q: deleted = %v, want %v", tc.ts, deleted, tc.wantDelete)
			}
		})
	}
}

// An unreadable timestamp means we cannot say how idle a chat is, so it stays.
func TestDeleteEmptySessionLeavesAnUnparseableTimestampAlone(t *testing.T) {
	setupTestDB(t)
	if err := CreateSession("s1", config.SessionConfig{Label: "Odd"}); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`, "not a timestamp", "s1"); err != nil {
		t.Fatal(err)
	}

	deleted, err := DeleteEmptySession("s1", time.Now(), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if deleted {
		t.Error("a chat with an unreadable timestamp must not be deleted")
	}
	if hasCandidate(EmptyChatCandidates(time.Now(), 24*time.Hour), "s1") {
		t.Error("a chat with an unreadable timestamp must not even be a candidate")
	}
}
