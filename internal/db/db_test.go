// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"database/sql"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

func setupTestDB(t *testing.T) {
	t.Helper()
	config.Workspace = "/tmp"
	f, err := os.CreateTemp("", "test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	t.Cleanup(func() {
		Close()
		os.Remove(path)
	})
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
}

func TestCreateAndGetSession(t *testing.T) {
	setupTestDB(t)

	cfg := config.SessionConfig{
		Label:   "Test Session",
		Mode:    "autopilot",
		Workdir: "/tmp",
		Yolo:    true,
	}
	err := CreateSession("sess-1", cfg)
	if err != nil {
		t.Fatal(err)
	}

	if !SessionExists("sess-1") {
		t.Fatal("session should exist")
	}
	if SessionExists("nonexistent") {
		t.Fatal("session should not exist")
	}

	got, err := GetSessionConfig("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "Test Session" {
		t.Errorf("expected label 'Test Session', got %q", got.Label)
	}
	if got.Mode != "autopilot" {
		t.Errorf("expected mode 'autopilot', got %q", got.Mode)
	}
}

func TestUpdateSessionConfig(t *testing.T) {
	setupTestDB(t)

	cfg := config.SessionConfig{Label: "Original", Mode: "autopilot"}
	CreateSession("sess-2", cfg)

	cfg.Label = "Renamed"
	cfg.Model = "gpt-5.4"
	UpdateSessionConfig("sess-2", cfg)

	got, _ := GetSessionConfig("sess-2")
	if got.Label != "Renamed" {
		t.Errorf("expected label 'Renamed', got %q", got.Label)
	}
	if got.Model != "gpt-5.4" {
		t.Errorf("expected model 'gpt-5.4', got %q", got.Model)
	}
}

func TestMessages(t *testing.T) {
	setupTestDB(t)

	cfg := config.SessionConfig{Label: "Chat", Mode: "autopilot"}
	CreateSession("sess-3", cfg)

	AddMessage("sess-3", "user", "Hello")
	AddMessage("sess-3", "assistant", "Hi there!")
	AddMessage("sess-3", "user", "How are you?")

	msgs := GetMessages("sess-3")
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "Hello" {
		t.Errorf("unexpected first message: %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "Hi there!" {
		t.Errorf("unexpected second message: %+v", msgs[1])
	}
}

func TestUndoLastExchange(t *testing.T) {
	setupTestDB(t)

	cfg := config.SessionConfig{Label: "Undo", Mode: "autopilot"}
	CreateSession("sess-4", cfg)

	AddMessage("sess-4", "user", "First")
	AddMessage("sess-4", "assistant", "Response 1")
	AddMessage("sess-4", "user", "Second")
	AddMessage("sess-4", "assistant", "Response 2")

	removed, remaining := UndoLastExchange("sess-4")
	if removed != 2 {
		t.Errorf("expected 2 removed, got %d", removed)
	}
	if remaining != 2 {
		t.Errorf("expected 2 remaining, got %d", remaining)
	}

	msgs := GetMessages("sess-4")
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages after undo, got %d", len(msgs))
	}
}

func TestDeleteSession(t *testing.T) {
	setupTestDB(t)

	cfg := config.SessionConfig{Label: "Delete Me"}
	CreateSession("sess-5", cfg)
	AddMessage("sess-5", "user", "hello")

	DeleteSession("sess-5")
	if SessionExists("sess-5") {
		t.Error("session should be deleted")
	}
	msgs := GetMessages("sess-5")
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages after delete, got %d", len(msgs))
	}
}

func TestOpencodeSession(t *testing.T) {
	setupTestDB(t)

	cfg := config.SessionConfig{Label: "OC", Backend: config.BackendOpencode}
	CreateSession("sess-opencode", cfg)

	if got := GetOpencodeSession("sess-opencode"); got != "" {
		t.Errorf("expected empty opencode session, got %q", got)
	}

	if err := SetOpencodeSession("sess-opencode", "opencode-abc-123"); err != nil {
		t.Fatalf("SetOpencodeSession: %v", err)
	}
	if got := GetOpencodeSession("sess-opencode"); got != "opencode-abc-123" {
		t.Errorf("expected 'opencode-abc-123', got %q", got)
	}
}

func TestListSessions(t *testing.T) {
	setupTestDB(t)

	CreateSession("s1", config.SessionConfig{Label: "First", Mode: "autopilot"})
	CreateSession("s2", config.SessionConfig{Label: "Second", Mode: "plan"})
	AddMessage("s1", "user", "hi")
	AddMessage("s1", "assistant", "hello")

	items := ListSessions()
	if len(items) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(items))
	}
	// Find s1
	var s1 *SessionListItem
	for i := range items {
		if items[i].SessionID == "s1" {
			s1 = &items[i]
		}
	}
	if s1 == nil {
		t.Fatal("session s1 not found")
	}
	if s1.Messages != 2 {
		t.Errorf("expected 2 messages for s1, got %d", s1.Messages)
	}
	if s1.Label != "First" {
		t.Errorf("expected label 'First', got %q", s1.Label)
	}
	if s1.Backend != config.BackendOpencode {
		t.Errorf("expected default backend %q, got %q", config.BackendOpencode, s1.Backend)
	}
}

func TestDeprecatedCopilotSession(t *testing.T) {
	setupTestDB(t)

	// Simulate a legacy row written by an older build: the raw config still
	// carries "backend":"copilot". We insert it directly because CreateSession
	// normalizes the backend to "opencode" on write.
	if _, err := DB.Exec(
		`INSERT INTO sessions (id, config) VALUES (?, ?)`,
		"legacy-copilot", `{"label":"Old Copilot chat","backend":"copilot"}`,
	); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	// A normal opencode session for contrast.
	CreateSession("modern-oc", config.SessionConfig{Label: "New chat", Backend: config.BackendOpencode})

	if !IsSessionDeprecated("legacy-copilot") {
		t.Error("expected legacy copilot session to be deprecated")
	}
	if IsSessionDeprecated("modern-oc") {
		t.Error("expected opencode session to NOT be deprecated")
	}
	if IsSessionDeprecated("does-not-exist") {
		t.Error("expected unknown session to NOT be deprecated")
	}

	items := ListSessions()
	got := map[string]bool{}
	for _, it := range items {
		got[it.SessionID] = it.Deprecated
		// Backend is always normalized to opencode, even for the legacy row.
		if it.Backend != config.BackendOpencode {
			t.Errorf("session %s: expected normalized backend %q, got %q",
				it.SessionID, config.BackendOpencode, it.Backend)
		}
	}
	if !got["legacy-copilot"] {
		t.Error("ListSessions: legacy-copilot should be marked Deprecated")
	}
	if got["modern-oc"] {
		t.Error("ListSessions: modern-oc should NOT be marked Deprecated")
	}
}

func TestModelStatsPersistAcrossInit(t *testing.T) {
	config.Workspace = "/tmp"
	f, err := os.CreateTemp("", "stats-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	t.Cleanup(func() {
		Close()
		os.Remove(path)
	})

	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	if err := IncrementModelStats("gpt-5.5", 1, 100, 250, 3, 2, 30, 4, 0.05); err != nil {
		t.Fatal(err)
	}
	if err := IncrementModelStats("gpt-5.5", 2, 50, 75, 4, 1, 10, 2, 0.025); err != nil {
		t.Fatal(err)
	}
	if err := IncrementModelStats("", 0, 10, 20, 0, 0, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	Close()

	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	stats := GetModelStats()
	if len(stats) != 2 {
		t.Fatalf("expected 2 model stats, got %d", len(stats))
	}

	var got ModelStat
	for _, stat := range stats {
		if stat.Model == "gpt-5.5" {
			got = stat
			break
		}
	}
	if got.Model == "" {
		t.Fatal("expected gpt-5.5 stats")
	}
	if got.Requests != 2 || got.Count != 2 {
		t.Errorf("expected 2 requests/count, got requests=%d count=%d", got.Requests, got.Count)
	}
	if got.PremiumReqs != 3 {
		t.Errorf("expected premium_reqs=3, got %d", got.PremiumReqs)
	}
	if got.TokensIn != 150 || got.TotalInput != 150 {
		t.Errorf("expected input tokens=150, got tokens_in=%d total_input=%d", got.TokensIn, got.TotalInput)
	}
	if got.TokensOut != 325 || got.TotalOutput != 325 {
		t.Errorf("expected output tokens=325, got tokens_out=%d total_output=%d", got.TokensOut, got.TotalOutput)
	}
	if got.ToolCalls != 7 {
		t.Errorf("expected tool_calls=7, got %d", got.ToolCalls)
	}
	if got.FilesModified != 3 {
		t.Errorf("expected files_modified=3, got %d", got.FilesModified)
	}
	if got.LinesAdded != 40 {
		t.Errorf("expected lines_added=40, got %d", got.LinesAdded)
	}
	if got.LinesRemoved != 6 {
		t.Errorf("expected lines_removed=6, got %d", got.LinesRemoved)
	}
	if got.Cost < 0.0749 || got.Cost > 0.0751 {
		t.Errorf("expected cost≈0.075, got %v", got.Cost)
	}
}

func TestModelStatsMigrationAddsChangeColumns(t *testing.T) {
	config.Workspace = "/tmp"
	f, err := os.CreateTemp("", "stats-migration-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	t.Cleanup(func() {
		Close()
		os.Remove(path)
	})

	oldDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = oldDB.Exec(`
		CREATE TABLE model_stats (
			model TEXT PRIMARY KEY,
			requests INTEGER NOT NULL DEFAULT 0,
			premium_reqs INTEGER NOT NULL DEFAULT 0,
			tokens_in INTEGER NOT NULL DEFAULT 0,
			tokens_out INTEGER NOT NULL DEFAULT 0,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO model_stats (model, requests, premium_reqs, tokens_in, tokens_out)
		VALUES ('gpt-5.5', 1, 1, 100, 200);
	`)
	if closeErr := oldDB.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	if err := IncrementModelStats("gpt-5.5", 0, 50, 75, 5, 2, 12, 3, 0.0125); err != nil {
		t.Fatal(err)
	}

	stats := GetModelStats()
	if len(stats) != 1 {
		t.Fatalf("expected 1 model stat, got %d", len(stats))
	}
	got := stats[0]
	if got.Requests != 2 || got.TokensIn != 150 || got.TokensOut != 275 {
		t.Errorf("existing totals were not preserved and incremented: %+v", got)
	}
	if got.ToolCalls != 5 {
		t.Errorf("tool_calls column was not added/incremented correctly: %+v", got)
	}
	if got.FilesModified != 2 || got.LinesAdded != 12 || got.LinesRemoved != 3 {
		t.Errorf("change columns were not added/incremented correctly: %+v", got)
	}
	if got.Cost < 0.0124 || got.Cost > 0.0126 {
		t.Errorf("cost column was not added/incremented correctly: %+v", got)
	}
}

func TestSessionStatsSummary(t *testing.T) {
	setupTestDB(t)

	CreateSession("sess-a", config.SessionConfig{Label: "A"})
	CreateSession("sess-b", config.SessionConfig{Label: "B"})
	CreateSession("sess-c", config.SessionConfig{Label: "C"})

	if err := IncrementSessionStats("sess-a", 3); err != nil {
		t.Fatal(err)
	}
	if err := IncrementSessionStats("sess-a", 1); err != nil {
		t.Fatal(err)
	}
	if err := IncrementSessionStats("sess-b", 0); err != nil {
		t.Fatal(err)
	}

	summary := GetUsageSummary()
	if summary.TotalSessions != 3 {
		t.Errorf("expected total_sessions=3, got %d", summary.TotalSessions)
	}
	if summary.ActiveSessions != 2 {
		t.Errorf("expected active_sessions=2, got %d", summary.ActiveSessions)
	}
	if summary.TotalRequests != 3 {
		t.Errorf("expected total_requests=3, got %d", summary.TotalRequests)
	}
	if summary.TotalToolCalls != 4 {
		t.Errorf("expected total_tool_calls=4, got %d", summary.TotalToolCalls)
	}
	if summary.AvgToolCallsPerSession < 1.33 || summary.AvgToolCallsPerSession > 1.34 {
		t.Errorf("expected avg per session around 1.33, got %f", summary.AvgToolCallsPerSession)
	}
	if summary.AvgToolCallsPerActiveSession != 2 {
		t.Errorf("expected avg per active session=2, got %f", summary.AvgToolCallsPerActiveSession)
	}
	if summary.AvgToolCallsPerRequest < 1.33 || summary.AvgToolCallsPerRequest > 1.34 {
		t.Errorf("expected avg per request around 1.33, got %f", summary.AvgToolCallsPerRequest)
	}
}

func TestSessionDraftRoundTrip(t *testing.T) {
	setupTestDB(t)

	if err := CreateSession("sess-draft", config.SessionConfig{Label: "x"}); err != nil {
		t.Fatal(err)
	}

	// Default draft is the empty string.
	if got := GetSessionDraft("sess-draft"); got != "" {
		t.Errorf("expected empty default draft, got %q", got)
	}

	// Save and read back.
	if err := SetSessionDraft("sess-draft", "hello world"); err != nil {
		t.Fatal(err)
	}
	if got := GetSessionDraft("sess-draft"); got != "hello world" {
		t.Errorf("expected %q, got %q", "hello world", got)
	}

	// Clearing draft works.
	if err := SetSessionDraft("sess-draft", ""); err != nil {
		t.Fatal(err)
	}
	if got := GetSessionDraft("sess-draft"); got != "" {
		t.Errorf("expected empty draft after clear, got %q", got)
	}

	// Unknown session returns empty without crashing.
	if got := GetSessionDraft("nope"); got != "" {
		t.Errorf("expected empty draft for unknown session, got %q", got)
	}
}

func TestListSessionsIncludesDraft(t *testing.T) {
	setupTestDB(t)

	if err := CreateSession("sess-list-draft", config.SessionConfig{Label: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := SetSessionDraft("sess-list-draft", "saved-draft"); err != nil {
		t.Fatal(err)
	}

	items := ListSessions()
	var found bool
	for _, it := range items {
		if it.SessionID == "sess-list-draft" {
			found = true
			if it.Draft != "saved-draft" {
				t.Errorf("expected draft to be returned in session list, got %q", it.Draft)
			}
		}
	}
	if !found {
		t.Fatal("session not returned by ListSessions")
	}
}

func TestSetSessionDraftDoesNotBumpUpdatedAt(t *testing.T) {
	setupTestDB(t)

	if err := CreateSession("sess-no-bump", config.SessionConfig{Label: "x"}); err != nil {
		t.Fatal(err)
	}

	var before string
	if err := DB.QueryRow(`SELECT updated_at FROM sessions WHERE id = ?`, "sess-no-bump").Scan(&before); err != nil {
		t.Fatal(err)
	}

	// SQLite CURRENT_TIMESTAMP has 1-second granularity, so wait > 1s to be
	// sure we'd see a different value if the update were touching updated_at.
	time.Sleep(1100 * time.Millisecond)

	if err := SetSessionDraft("sess-no-bump", "anything"); err != nil {
		t.Fatal(err)
	}

	var after string
	if err := DB.QueryRow(`SELECT updated_at FROM sessions WHERE id = ?`, "sess-no-bump").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("draft save should not bump updated_at: before=%q after=%q", before, after)
	}
}

// Silence unused import warning when sql isn't used.
var _ = sql.ErrNoRows

func TestMcpStatusCache(t *testing.T) {
	setupTestDB(t)

	// Unknown before any write.
	if _, known := GetMcpStatus("atlassian"); known {
		t.Error("expected atlassian to be unknown initially")
	}

	SetMcpStatus("atlassian", true)
	if authed, known := GetMcpStatus("atlassian"); !known || !authed {
		t.Errorf("expected atlassian authed+known, got authed=%v known=%v", authed, known)
	}

	// Upsert flips the value.
	SetMcpStatus("atlassian", false)
	if authed, _ := GetMcpStatus("atlassian"); authed {
		t.Error("expected atlassian to be false after logout write")
	}

	SetMcpStatus("obsidian", true)
	all := AllMcpStatus()
	if all["atlassian"] != false || all["obsidian"] != true {
		t.Errorf("AllMcpStatus mismatch: %v", all)
	}
}

func TestDailyCost_RecordedByIncrementModelStats(t *testing.T) {
	setupTestDB(t)

	// Two turns today should roll into a single day bucket.
	if err := IncrementModelStats("gpt-5.5", 1, 100, 250, 3, 2, 30, 4, 0.05); err != nil {
		t.Fatal(err)
	}
	if err := IncrementModelStats("claude-sonnet-4.6", 0, 10, 20, 1, 0, 0, 0, 0.025); err != nil {
		t.Fatal(err)
	}

	week := GetDailyCost(7)
	if len(week) != 7 {
		t.Fatalf("expected 7 days, got %d", len(week))
	}
	today := time.Now().Format("2006-01-02")
	last := week[len(week)-1]
	if last.Day != today {
		t.Errorf("last entry should be today (%s), got %s", today, last.Day)
	}
	if math.Abs(last.Cost-0.075) > 1e-9 {
		t.Errorf("today's cost should be 0.075, got %v", last.Cost)
	}
	if last.Requests != 2 {
		t.Errorf("today's requests should be 2, got %d", last.Requests)
	}
}

func TestGetDailyCost_ZeroFillsAndOrders(t *testing.T) {
	setupTestDB(t)

	// Insert a cost 3 days ago directly (bypassing "today" recording).
	threeAgo := time.Now().AddDate(0, 0, -3).Format("2006-01-02")
	if _, err := DB.Exec(
		`INSERT INTO daily_cost (day, cost, requests) VALUES (?, ?, ?)`,
		threeAgo, 1.23, 4,
	); err != nil {
		t.Fatal(err)
	}

	week := GetDailyCost(7)
	if len(week) != 7 {
		t.Fatalf("expected 7 days, got %d", len(week))
	}
	// Oldest first, strictly increasing dates.
	for i := 1; i < len(week); i++ {
		if week[i-1].Day >= week[i].Day {
			t.Errorf("days not strictly ascending at %d: %s >= %s", i, week[i-1].Day, week[i].Day)
		}
	}
	// The seeded day carries its cost; all others are zero-filled.
	var seeded, zeros int
	for _, d := range week {
		if d.Day == threeAgo {
			seeded++
			if math.Abs(d.Cost-1.23) > 1e-9 || d.Requests != 4 {
				t.Errorf("seeded day wrong: %+v", d)
			}
		} else if d.Cost == 0 && d.Requests == 0 {
			zeros++
		}
	}
	if seeded != 1 {
		t.Errorf("expected exactly 1 seeded day in window, got %d", seeded)
	}
	if zeros != 6 {
		t.Errorf("expected 6 zero-filled days, got %d", zeros)
	}
}

func TestGetDailyCost_ExcludesOutsideWindowAndClamps(t *testing.T) {
	setupTestDB(t)

	// A cost 10 days ago must not appear in a 7-day window.
	old := time.Now().AddDate(0, 0, -10).Format("2006-01-02")
	if _, err := DB.Exec(`INSERT INTO daily_cost (day, cost, requests) VALUES (?, ?, ?)`, old, 9.99, 1); err != nil {
		t.Fatal(err)
	}
	for _, d := range GetDailyCost(7) {
		if d.Day == old {
			t.Errorf("day %s outside the 7-day window should be excluded", old)
		}
	}

	// days<1 clamps to 1; days>90 clamps to 90.
	if got := GetDailyCost(0); len(got) != 1 {
		t.Errorf("GetDailyCost(0) should clamp to 1 day, got %d", len(got))
	}
	if got := GetDailyCost(1000); len(got) != 90 {
		t.Errorf("GetDailyCost(1000) should clamp to 90 days, got %d", len(got))
	}
}

// ── Per-message metadata (timestamps + usage) ────────────────────────────────

// The UI shows how long ago each message was sent, so GetMessages has to
// return created_at, and it has to be unambiguous: SQLite writes
// CURRENT_TIMESTAMP as UTC but formats it without a zone, which a browser is
// free to read as local time. It must arrive as RFC3339.
func TestGetMessages_ReturnsRFC3339CreatedAt(t *testing.T) {
	setupTestDB(t)
	CreateSession("s", config.SessionConfig{Workdir: "/tmp"})
	AddMessage("s", "user", "hello")

	msgs := GetMessages("s")
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	ts, err := time.Parse(time.RFC3339, msgs[0].CreatedAt)
	if err != nil {
		t.Fatalf("created_at %q is not RFC3339: %v", msgs[0].CreatedAt, err)
	}
	// A timezone mix-up shows up here as an hours-wide gap, which is exactly
	// the "timestamps look wrong" symptom this guards against.
	if d := time.Since(ts); d < -time.Minute || d > 5*time.Minute {
		t.Errorf("created_at %v is %v from now — timezone handling is wrong", ts, d)
	}
}

// Writing a new message must not disturb the ones already stored. This is the
// reported bug: returning to an old chat and sending a message appeared to
// reset every earlier timestamp to the current time.
func TestAddMessage_LeavesEarlierTimestampsAlone(t *testing.T) {
	setupTestDB(t)
	CreateSession("s", config.SessionConfig{Workdir: "/tmp"})

	// Backdate a message so a rewrite would be obvious (CURRENT_TIMESTAMP has
	// one-second granularity, so same-second inserts are indistinguishable).
	if _, err := DB.Exec(
		`INSERT INTO messages (session_id, role, content, created_at)
		 VALUES ('s', 'user', 'old', '2020-01-02 03:04:05')`); err != nil {
		t.Fatal(err)
	}
	const want = "2020-01-02T03:04:05Z"
	if got := GetMessages("s")[0].CreatedAt; got != want {
		t.Fatalf("seeded created_at = %q, want %q", got, want)
	}

	// A later turn: user prompt + assistant reply.
	AddMessage("s", "user", "new")
	AddMessageWithUsage("s", "assistant", "reply", `{"cost":0.5}`)

	msgs := GetMessages("s")
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}
	if msgs[0].CreatedAt != want {
		t.Errorf("earlier message was re-stamped: %q, want %q", msgs[0].CreatedAt, want)
	}
	// Ordering must stay stable and oldest-first.
	if msgs[0].Content != "old" || msgs[1].Content != "new" || msgs[2].Content != "reply" {
		t.Errorf("messages out of order: %q, %q, %q",
			msgs[0].Content, msgs[1].Content, msgs[2].Content)
	}
}

// Usage is stored per assistant message so every response can show its own
// figures, not just the most recent one.
func TestMessageUsage_RoundTripsPerMessage(t *testing.T) {
	setupTestDB(t)
	CreateSession("s", config.SessionConfig{Workdir: "/tmp"})

	AddMessage("s", "user", "q")
	AddMessageWithUsage("s", "assistant", "a1", `{"tokens_out":926,"cost":0.24}`)
	AddMessage("s", "user", "q2")
	AddMessageWithUsage("s", "assistant", "a2", `{"tokens_out":10,"cost":0.01}`)

	msgs := GetMessages("s")
	if len(msgs) != 4 {
		t.Fatalf("got %d messages, want 4", len(msgs))
	}
	// User messages carry no usage, and it must be omitted rather than sent
	// as an empty string the UI would have to special-case.
	for _, i := range []int{0, 2} {
		if msgs[i].Usage != nil {
			t.Errorf("user message %d has usage %s", i, msgs[i].Usage)
		}
	}
	// Each response keeps its own numbers.
	var first, second struct {
		TokensOut int     `json:"tokens_out"`
		Cost      float64 `json:"cost"`
	}
	if err := json.Unmarshal(msgs[1].Usage, &first); err != nil {
		t.Fatalf("first usage: %v", err)
	}
	if err := json.Unmarshal(msgs[3].Usage, &second); err != nil {
		t.Fatalf("second usage: %v", err)
	}
	if first.TokensOut != 926 || first.Cost != 0.24 {
		t.Errorf("first usage = %+v", first)
	}
	if second.TokensOut != 10 || second.Cost != 0.01 {
		t.Errorf("second usage = %+v", second)
	}
}

// Messages written before the usage column existed have an empty value; it
// must not surface as invalid JSON.
func TestMessageUsage_EmptyIsOmitted(t *testing.T) {
	setupTestDB(t)
	CreateSession("s", config.SessionConfig{Workdir: "/tmp"})
	if _, err := DB.Exec(
		`INSERT INTO messages (session_id, role, content, usage)
		 VALUES ('s', 'assistant', 'legacy', '')`); err != nil {
		t.Fatal(err)
	}
	msgs := GetMessages("s")
	if msgs[0].Usage != nil {
		t.Errorf("empty usage should be omitted, got %q", msgs[0].Usage)
	}
	b, err := json.Marshal(msgs[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"usage"`) {
		t.Errorf("usage key should be absent from JSON: %s", b)
	}
}

func TestCountMessages(t *testing.T) {
	setupTestDB(t)
	cfg := config.DefaultSessionConfig()
	if err := CreateSession("sess-count", cfg); err != nil {
		t.Fatal(err)
	}
	if n := CountMessages("sess-count"); n != 0 {
		t.Fatalf("empty session should have 0 messages, got %d", n)
	}
	AddMessage("sess-count", "user", "first")
	if n := CountMessages("sess-count"); n != 1 {
		t.Fatalf("after the first prompt the count should be 1, got %d", n)
	}
	AddMessage("sess-count", "assistant", "reply")
	AddMessage("sess-count", "user", "second")
	if n := CountMessages("sess-count"); n != 3 {
		t.Fatalf("count = %d", n)
	}
	// An unknown session is not an error, just empty.
	if n := CountMessages("nope"); n != 0 {
		t.Fatalf("unknown session should count 0, got %d", n)
	}
}
