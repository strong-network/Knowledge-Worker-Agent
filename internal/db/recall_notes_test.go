// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"encoding/json"
	"strings"
	"testing"
)

// seedNoted creates a session with enough messages to appear in `list`, plus
// notes for the given days.
func seedNoted(t *testing.T, id, label string, days ...string) {
	t.Helper()
	seedChat(t, id, label, "opening request", "reply", "more", "and more")
	for _, d := range days {
		if err := SaveSessionNote(SessionNote{
			SessionID: id, Day: d, Note: "worked on " + d,
			SessionTitle: label, PromptVersion: 1,
		}); err != nil {
			t.Fatalf("save note %s/%s: %v", id, d, err)
		}
	}
}

func digestFor(t *testing.T, res RecallListResult, id string) RecallDigest {
	t.Helper()
	for _, d := range res.Sessions {
		if d.SessionID == id {
			return d
		}
	}
	t.Fatalf("session %s missing from list: %v", id, idsOf(res.Sessions))
	return RecallDigest{}
}

// --- notes on digests ---------------------------------------------------

func TestListCarriesNotesForNotedSessions(t *testing.T) {
	setupTestDB(t)
	seedNoted(t, "s-noted", "Noted chat", "2026-03-01", "2026-03-02")

	res, err := RecallList(RecallListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	d := digestFor(t, res, "s-noted")
	if len(d.Notes) != 2 {
		t.Fatalf("got %d notes, want 2: %v", len(d.Notes), d.Notes)
	}
	// Undated prose would force the agent back into `read` just to place
	// anything in time, which is the cost notes exist to remove.
	if !strings.HasPrefix(d.Notes[0], "2026-03-01") {
		t.Errorf("note is not dated: %q", d.Notes[0])
	}
}

// The coverage marker is the honesty rule: without it, a gap in the notes is
// indistinguishable from a day on which nothing happened.
func TestListReportsHowFarNotesReach(t *testing.T) {
	setupTestDB(t)
	seedNoted(t, "s-cov", "Covered", "2026-03-01", "2026-03-05", "2026-03-03")

	res, err := RecallList(RecallListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	d := digestFor(t, res, "s-cov")
	if d.NotesCoverThrough != "2026-03-05" {
		t.Errorf("notes_cover_through = %q, want the latest day 2026-03-05", d.NotesCoverThrough)
	}
}

func TestListLeavesUnnotedSessionsWithAnEmptyNoteList(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "s-plain", "Plain", "a", "b", "c", "d")

	res, err := RecallList(RecallListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	d := digestFor(t, res, "s-plain")
	if d.Notes == nil {
		t.Error("notes must be [] not null, so the shape is stable")
	}
	if len(d.Notes) != 0 || d.NotesCoverThrough != "" {
		t.Errorf("unnoted session claims coverage: %+v", d)
	}
	if d.Source != "available" {
		t.Errorf("source = %q, want available", d.Source)
	}
}

// --- deleted chats in list ----------------------------------------------

// The point of notes outliving a chat is that tidying the sidebar does not
// punch a hole in the record. A note nothing can enumerate would be reachable
// only by already knowing the session id, which defeats that entirely.
func TestListStillEnumeratesADeletedChatThroughItsNotes(t *testing.T) {
	setupTestDB(t)
	seedNoted(t, "s-doomed", "Doomed chat", "2026-03-01")
	if err := MarkNotesOrphaned("s-doomed"); err != nil {
		t.Fatalf("orphan: %v", err)
	}
	if err := DeleteSession("s-doomed"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	res, err := RecallList(RecallListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	d := digestFor(t, res, "s-doomed")
	if d.Source != "deleted" {
		t.Errorf("source = %q, want deleted", d.Source)
	}
	if d.Title != "Doomed chat" {
		t.Errorf("title = %q -- it must come from the snapshot, the config blob is gone", d.Title)
	}
	if len(d.Notes) != 1 {
		t.Errorf("got %d notes, want 1", len(d.Notes))
	}
	if d.MessageCount != 0 {
		t.Errorf("message_count = %d, want 0: there is nothing left to read", d.MessageCount)
	}
}

// A deleted chat must be counted in `total`, or paging walks a different
// sequence from the one the caller was told about.
func TestListCountsDeletedChatsInTheTotal(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "s-live", "Live", "a", "b", "c", "d")
	seedNoted(t, "s-dead", "Dead", "2026-03-01")
	if err := MarkNotesOrphaned("s-dead"); err != nil {
		t.Fatalf("orphan: %v", err)
	}
	if err := DeleteSession("s-dead"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	res, err := RecallList(RecallListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if res.Total != 2 {
		t.Errorf("total = %d, want 2 (one live, one deleted-but-noted)", res.Total)
	}
	if res.Returned != 2 {
		t.Errorf("returned = %d, want 2", res.Returned)
	}
}

// A chat that was deleted without leaving notes is simply gone; nothing should
// invent an entry for it.
func TestListDoesNotInventEntriesForUnnotedDeletedChats(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "s-nonotes", "No notes", "a", "b", "c", "d")
	if err := MarkNotesOrphaned("s-nonotes"); err != nil {
		t.Fatalf("orphan: %v", err)
	}
	if err := DeleteSession("s-nonotes"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	res, err := RecallList(RecallListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if contains(idsOf(res.Sessions), "s-nonotes") {
		t.Errorf("a chat deleted with no notes should leave nothing behind, got %v", idsOf(res.Sessions))
	}
}

// A live session must never be duplicated by a stray orphan mark.
func TestListDoesNotDuplicateALiveSessionMarkedOrphanedInError(t *testing.T) {
	setupTestDB(t)
	seedNoted(t, "s-alive", "Alive", "2026-03-01")
	if err := MarkNotesOrphaned("s-alive"); err != nil {
		t.Fatalf("orphan: %v", err)
	}

	res, err := RecallList(RecallListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	n := 0
	for _, id := range idsOf(res.Sessions) {
		if id == "s-alive" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("session appears %d times, want exactly 1: %v", n, idsOf(res.Sessions))
	}
	if digestFor(t, res, "s-alive").Source != "available" {
		t.Error("a session that still exists must report source=available")
	}
}

func TestListDateWindowAppliesToDeletedChatsToo(t *testing.T) {
	setupTestDB(t)
	seedNoted(t, "s-old", "Old", "2026-01-05")
	if err := MarkNotesOrphaned("s-old"); err != nil {
		t.Fatalf("orphan: %v", err)
	}
	if err := DeleteSession("s-old"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	in, err := RecallList(RecallListOptions{From: "2026-01-01", To: "2026-01-31"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !contains(idsOf(in.Sessions), "s-old") {
		t.Errorf("deleted chat missing from a window that covers its notes: %v", idsOf(in.Sessions))
	}

	out, err := RecallList(RecallListOptions{From: "2026-02-01", To: "2026-02-28"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if contains(idsOf(out.Sessions), "s-old") {
		t.Errorf("deleted chat leaked past its window: %v", idsOf(out.Sessions))
	}
}

// --- read on a deleted chat ---------------------------------------------

// An empty transcript reads as "the chat was empty". The truth is "the chat is
// gone". The spec calls that distinction non-optional.
func TestReadOfADeletedChatExplainsItselfInsteadOfReturningNothing(t *testing.T) {
	setupTestDB(t)
	seedNoted(t, "s-read-gone", "Gone chat", "2026-03-01", "2026-03-02")
	if err := MarkNotesOrphaned("s-read-gone"); err != nil {
		t.Fatalf("orphan: %v", err)
	}
	if err := DeleteSession("s-read-gone"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	tr, err := RecallRead(RecallReadOptions{SessionID: "s-read-gone"})
	if err != nil {
		t.Fatalf("read should answer, not fail: %v", err)
	}
	if tr.Source != "deleted" {
		t.Errorf("source = %q, want deleted", tr.Source)
	}
	if len(tr.Messages) != 0 {
		t.Errorf("a deleted chat has no messages, got %d", len(tr.Messages))
	}
	if len(tr.Notes) != 2 {
		t.Errorf("got %d notes, want 2 -- they are what survives", len(tr.Notes))
	}
	if tr.Title != "Gone chat" {
		t.Errorf("title = %q, want the snapshot", tr.Title)
	}
	if !strings.Contains(strings.ToLower(tr.Note), "deleted") {
		t.Errorf("the reason must be stated in words, got %q", tr.Note)
	}
}

// The key must be absent, not empty: `[]` is what a live chat with no matching
// messages returns, and the two answers must not be confusable.
func TestReadOfADeletedChatOmitsTheMessagesKeyEntirely(t *testing.T) {
	setupTestDB(t)
	seedNoted(t, "s-json", "JSON", "2026-03-01")
	if err := MarkNotesOrphaned("s-json"); err != nil {
		t.Fatalf("orphan: %v", err)
	}
	if err := DeleteSession("s-json"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	tr, err := RecallRead(RecallReadOptions{SessionID: "s-json"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	raw, err := json.Marshal(tr)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := got["messages"]; present {
		t.Errorf("`messages` must be absent for a deleted chat, got %s", raw)
	}
	if got["source"] != "deleted" {
		t.Errorf("source = %v, want deleted", got["source"])
	}
}

// A live chat whose window matched nothing is a different answer and must keep
// its (empty) array, or it becomes indistinguishable from a deleted one.
func TestReadOfALiveChatAlwaysKeepsTheMessagesKey(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "s-live-empty", "Live", "a", "b")

	tr, err := RecallRead(RecallReadOptions{
		SessionID: "s-live-empty", From: "2020-01-01", To: "2020-01-02",
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	raw, _ := json.Marshal(tr)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs, present := got["messages"]
	if !present {
		t.Fatalf("`messages` disappeared for a live chat with an empty window: %s", raw)
	}
	if msgs == nil {
		t.Errorf("`messages` must be [] not null: %s", raw)
	}
	if got["source"] != "available" {
		t.Errorf("source = %v, want available", got["source"])
	}
}

// An id that never existed is still a miss, not a deletion.
func TestReadOfAnUnknownSessionIsStillNotFound(t *testing.T) {
	setupTestDB(t)
	if _, err := RecallRead(RecallReadOptions{SessionID: "never-existed"}); err == nil {
		t.Error("expected not found")
	}
}

// --- delete consent -----------------------------------------------------

// The confirmation says what is at stake using this number; if it is not
// surfaced the user is asked to consent to something invisible.
func TestSessionListReportsHowManyNotesAChatHas(t *testing.T) {
	setupTestDB(t)
	seedNoted(t, "s-count", "Counted", "2026-03-01", "2026-03-02", "2026-03-03")
	seedChat(t, "s-uncounted", "Plain", "a", "b")

	var counted, plain *SessionListItem
	items := ListSessions()
	for i := range items {
		switch items[i].SessionID {
		case "s-count":
			counted = &items[i]
		case "s-uncounted":
			plain = &items[i]
		}
	}
	if counted == nil || plain == nil {
		t.Fatal("sessions missing from the list")
	}
	if counted.NoteCount != 3 {
		t.Errorf("note_count = %d, want 3", counted.NoteCount)
	}
	if plain.NoteCount != 0 {
		t.Errorf("note_count = %d for an unnoted chat, want 0", plain.NoteCount)
	}
}
