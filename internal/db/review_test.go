// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"strconv"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

func daysAgo(d int) time.Time { return time.Now().Add(-time.Duration(d) * 24 * time.Hour) }

// chatWithMessage creates a reviewable chat: one message, idle since ts.
func chatWithMessage(t *testing.T, id string, cfg config.SessionConfig, ts time.Time) {
	t.Helper()
	if err := CreateSession(id, cfg); err != nil {
		t.Fatal(err)
	}
	if err := AddMessage(id, "user", "hello"); err != nil {
		t.Fatal(err)
	}
	// AddMessage bumps updated_at, so the idle time is forced afterwards.
	setSessionUpdatedAt(t, id, ts)
}

func hasItem(items []TidyItem, id string) bool {
	for _, it := range items {
		if it.ID == id {
			return true
		}
	}
	return false
}

func TestReviewCandidatesOffersOnlyIdleChatsThatHoldSomething(t *testing.T) {
	setupTestDB(t)
	window := 30 * 24 * time.Hour

	chatWithMessage(t, "idle", config.SessionConfig{Label: "Old work"}, daysAgo(45))
	chatWithMessage(t, "recent", config.SessionConfig{Label: "This week"}, daysAgo(3))
	chatWithMessage(t, "kept", config.SessionConfig{Label: "Kept", Keep: true}, daysAgo(45))

	// Empty chats belong to the sweeper, which deletes them without asking.
	// Surfacing them here would ask the user about nothing.
	if err := CreateSession("empty", config.SessionConfig{Label: "Abandoned"}); err != nil {
		t.Fatal(err)
	}
	setSessionUpdatedAt(t, "empty", daysAgo(45))

	items, total := ReviewCandidates(time.Now(), window)

	if !hasItem(items, "idle") {
		t.Error("a chat idle for 45 days with a message should be offered")
	}
	for _, id := range []string{"recent", "kept", "empty"} {
		if hasItem(items, id) {
			t.Errorf("%s must not be offered for review", id)
		}
	}
	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
}

// Nothing here schedules a deletion. Listing a chat must leave it exactly as it
// was — same row, same messages, same timestamp.
func TestReviewCandidatesChangesNothing(t *testing.T) {
	setupTestDB(t)
	chatWithMessage(t, "idle", config.SessionConfig{Label: "Old work"}, daysAgo(45))

	var before string
	if err := DB.QueryRow(`SELECT updated_at FROM sessions WHERE id = ?`, "idle").Scan(&before); err != nil {
		t.Fatal(err)
	}

	ReviewCandidates(time.Now(), 30*24*time.Hour)
	ReviewCandidates(time.Now(), 30*24*time.Hour)

	var after string
	if err := DB.QueryRow(`SELECT updated_at FROM sessions WHERE id = ?`, "idle").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Errorf("listing changed updated_at: %q → %q", before, after)
	}
	if !SessionExists("idle") {
		t.Error("listing deleted the chat")
	}
	if CountMessages("idle") != 1 {
		t.Error("listing lost a message")
	}
}

func TestReviewCandidatesReportsIdleTimeAndMessageCount(t *testing.T) {
	setupTestDB(t)
	if err := CreateSession("s1", config.SessionConfig{Label: "Old work", Workdir: "/tmp/chat-abc"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := AddMessage("s1", "user", "hello"); err != nil {
			t.Fatal(err)
		}
	}
	setSessionUpdatedAt(t, "s1", daysAgo(45))

	items, _ := ReviewCandidates(time.Now(), 30*24*time.Hour)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	it := items[0]
	if it.IdleDays != 45 {
		t.Errorf("idle days = %d, want 45", it.IdleDays)
	}
	if it.MessageCount != 3 {
		t.Errorf("message count = %d, want 3", it.MessageCount)
	}
	if it.Label != "Old work" {
		t.Errorf("label = %q", it.Label)
	}
	if it.Workdir != "/tmp/chat-abc" {
		t.Errorf("workdir = %q, want the chat's folder so the caller can size it", it.Workdir)
	}
	if it.LastActivity == "" {
		t.Error("last activity must be reported: it is what the user decides on")
	}
}

// Starring a chat says "this one matters", not "never ask me about it". It
// still comes up for review -- exempting it would rebuild the invisible pile --
// but it is flagged so the review can keep it apart from the rest.
func TestReviewCandidatesFlagsStarredChatsWithoutExcludingThem(t *testing.T) {
	setupTestDB(t)
	chatWithMessage(t, "starred", config.SessionConfig{Label: "Important", Favorite: true}, daysAgo(45))
	chatWithMessage(t, "plain", config.SessionConfig{Label: "Ordinary"}, daysAgo(45))

	items, total := ReviewCandidates(time.Now(), 30*24*time.Hour)

	if total != 2 || len(items) != 2 {
		t.Fatalf("both chats should be offered, got %d of %d", len(items), total)
	}
	for _, it := range items {
		switch it.ID {
		case "starred":
			if !it.Starred {
				t.Error("a starred chat must be reported as starred, or the review cannot separate it")
			}
		case "plain":
			if it.Starred {
				t.Error("an unstarred chat must not be reported as starred")
			}
		}
	}
}

// The list is capped so one select-all cannot reach five months of history, but
// the true total is still reported — a cap that hides the extent would be worse
// than no cap.
func TestReviewCandidatesCapsTheListButReportsTheTrueTotal(t *testing.T) {
	setupTestDB(t)
	const created = ReviewListLimit + 12
	for i := 0; i < created; i++ {
		id := "s" + strconv.Itoa(i)
		chatWithMessage(t, id, config.SessionConfig{Label: id}, daysAgo(40+i))
	}

	items, total := ReviewCandidates(time.Now(), 30*24*time.Hour)

	if len(items) != ReviewListLimit {
		t.Errorf("listed %d items, want the limit of %d", len(items), ReviewListLimit)
	}
	if total != created {
		t.Errorf("total = %d, want %d", total, created)
	}
	// Oldest first, so a capped list shows the ones most likely to be junk.
	for i := 1; i < len(items); i++ {
		if items[i-1].LastActivity > items[i].LastActivity {
			t.Fatalf("items are not oldest-first at index %d", i)
		}
	}
}

func TestSnoozeHidesAChatUntilTheWindowElapses(t *testing.T) {
	setupTestDB(t)
	window := 30 * 24 * time.Hour
	chatWithMessage(t, "s1", config.SessionConfig{Label: "Old work"}, daysAgo(45))

	if items, _ := ReviewCandidates(time.Now(), window); !hasItem(items, "s1") {
		t.Fatal("expected the chat to be offered before snoozing")
	}

	now := time.Now()
	if err := SnoozeSession("s1", now, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}

	if items, total := ReviewCandidates(now, window); hasItem(items, "s1") || total != 0 {
		t.Error("a snoozed chat must not be offered, and must not count towards the total")
	}
	// ...but it comes back. "Keep for now" defers the question, it does not
	// answer it forever.
	later := now.Add(31 * 24 * time.Hour)
	if items, _ := ReviewCandidates(later, window); !hasItem(items, "s1") {
		t.Error("a snoozed chat must be offered again once the snooze elapses")
	}
}

// Snoozing is not activity. If it bumped updated_at the chat would jump to the
// top of the sidebar and its idle clock would restart.
func TestSnoozeDoesNotCountAsActivity(t *testing.T) {
	setupTestDB(t)
	chatWithMessage(t, "s1", config.SessionConfig{Label: "Old work"}, daysAgo(45))

	var before string
	if err := DB.QueryRow(`SELECT updated_at FROM sessions WHERE id = ?`, "s1").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := SnoozeSession("s1", time.Now(), 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := DB.QueryRow(`SELECT updated_at FROM sessions WHERE id = ?`, "s1").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Errorf("snoozing changed updated_at: %q → %q", before, after)
	}
}

func TestSnoozeWithANonPositiveWindowClearsIt(t *testing.T) {
	setupTestDB(t)
	window := 30 * 24 * time.Hour
	chatWithMessage(t, "s1", config.SessionConfig{Label: "Old work"}, daysAgo(45))

	now := time.Now()
	if err := SnoozeSession("s1", now, window); err != nil {
		t.Fatal(err)
	}
	if err := SnoozeSession("s1", now, 0); err != nil {
		t.Fatal(err)
	}
	if items, _ := ReviewCandidates(now, window); !hasItem(items, "s1") {
		t.Error("clearing the snooze should bring the chat back immediately")
	}
}

// The orphan scan deletes directories that are not in this set, so an
// incomplete answer is dangerous. It must say so rather than return a partial
// map that makes live folders look abandoned.
func TestSessionWorkdirsReportsEveryChatIncludingProjectChats(t *testing.T) {
	setupTestDB(t)
	if err := CreateSession("loose", config.SessionConfig{Workdir: "/tmp/chat-aaa"}); err != nil {
		t.Fatal(err)
	}
	if err := CreateSession("in-project", config.SessionConfig{Workdir: "/tmp/chat-bbb"}); err != nil {
		t.Fatal(err)
	}
	if err := SetSessionProject("in-project", "p1"); err != nil {
		t.Fatal(err)
	}
	if err := CreateSession("no-workdir", config.SessionConfig{}); err != nil {
		t.Fatal(err)
	}

	dirs, ok := SessionWorkdirs()
	if !ok {
		t.Fatal("expected the session list to be readable")
	}
	if !dirs["/tmp/chat-aaa"] {
		t.Error("a loose chat's workdir must be known")
	}
	if !dirs["/tmp/chat-bbb"] {
		t.Error("a project chat's workdir must be known too, or it looks orphaned")
	}
	if len(dirs) != 2 {
		t.Errorf("got %d workdirs, want 2 (a chat with no workdir contributes nothing)", len(dirs))
	}
}
