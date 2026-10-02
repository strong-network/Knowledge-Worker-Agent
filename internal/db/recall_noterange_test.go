// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"strings"
	"testing"
)

// backdate moves a session's timestamps so a test can build a date window that
// does not include today. RecallList matches a session when its lifespan
// *overlaps* the window (updated_at >= from AND created_at <= to), which is
// what makes a long-running chat appear in a query about last week -- and is
// exactly the case where unfiltered notes did the most damage.
func backdate(t *testing.T, id, created, updated string) {
	t.Helper()
	if _, err := DB.Exec(
		`UPDATE sessions SET created_at = ?, updated_at = ? WHERE id = ?`,
		created, updated, id); err != nil {
		t.Fatalf("backdate %s: %v", id, err)
	}
}

// --- item 1: notes are limited to the requested range --------------------

// The bug this closes: `list` filtered sessions by date but attached every note
// those sessions had ever accumulated. A ten-day query came back 73% out of
// range by volume -- correctly dated, but mostly about other weeks.
func TestListLimitsNotesToTheRequestedRange(t *testing.T) {
	setupTestDB(t)
	seedNoted(t, "s-span", "Long-running chat",
		"2026-03-01", "2026-06-15", "2026-09-08", "2026-09-09")
	backdate(t, "s-span", "2026-03-01 09:00:00", "2026-09-30 09:00:00")

	res, err := RecallList(RecallListOptions{From: "2026-09-01", To: "2026-09-30"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	d := digestFor(t, res, "s-span")
	if len(d.Notes) != 2 {
		t.Fatalf("got %d notes, want the 2 inside the window: %v", len(d.Notes), d.Notes)
	}
	for _, n := range d.Notes {
		if !strings.HasPrefix(n, "2026-09-") {
			t.Errorf("note from outside the window leaked in: %q", n)
		}
	}
}

// The subtlety that must survive the fix. notes_cover_through is the honesty
// marker: it says how far the notes actually reach, so a quiet day can be told
// apart from an unsummarised one. Derive it from the *filtered* slice and it
// silently starts meaning "the last note in your window" while still claiming
// to be the high-water mark -- which is worse than the bug being fixed.
func TestListReportsTrueCoverageWhenTheLastNoteIsOutsideTheRange(t *testing.T) {
	setupTestDB(t)
	seedNoted(t, "s-cover", "Straddles the window", "2026-03-01", "2026-09-09")
	backdate(t, "s-cover", "2026-03-01 09:00:00", "2026-09-30 09:00:00")

	res, err := RecallList(RecallListOptions{From: "2026-01-01", To: "2026-06-30"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	d := digestFor(t, res, "s-cover")
	if len(d.Notes) != 1 || !strings.HasPrefix(d.Notes[0], "2026-03-01") {
		t.Fatalf("expected only the in-window note, got %v", d.Notes)
	}
	if d.NotesCoverThrough != "2026-09-09" {
		t.Errorf("notes_cover_through = %q, want the chat's last note 2026-09-09 -- "+
			"reporting the last note in the window turns the marker into a lie",
			d.NotesCoverThrough)
	}
}

// A session active in the window is still a session active in the window, even
// if nothing was summarised then. Dropping it would hide activity; the empty
// notes array plus a truthful coverage marker says what is actually known.
func TestListKeepsSessionsWhoseNotesAllFallOutsideTheRange(t *testing.T) {
	setupTestDB(t)
	seedNoted(t, "s-outside", "Noted only in March", "2026-03-01")
	backdate(t, "s-outside", "2026-03-01 09:00:00", "2026-09-30 09:00:00")

	res, err := RecallList(RecallListOptions{From: "2026-08-01", To: "2026-08-31"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	d := digestFor(t, res, "s-outside")
	if len(d.Notes) != 0 {
		t.Fatalf("expected no notes in this window, got %v", d.Notes)
	}
	if d.Notes == nil {
		t.Error("notes must encode as [] rather than null")
	}
	if d.NotesCoverThrough != "2026-03-01" {
		t.Errorf("notes_cover_through = %q, want 2026-03-01 -- an empty note list with no "+
			"coverage marker reads as 'nothing ever happened here'", d.NotesCoverThrough)
	}
}

// No range asked for means no range applied. The filter must not quietly
// become a default that hides notes from a caller who never mentioned dates.
func TestListWithoutARangeStillAttachesEveryNote(t *testing.T) {
	setupTestDB(t)
	seedNoted(t, "s-all", "Everything", "2026-03-01", "2026-06-15", "2026-09-09")

	res, err := RecallList(RecallListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if d := digestFor(t, res, "s-all"); len(d.Notes) != 3 {
		t.Fatalf("got %d notes, want all 3: %v", len(d.Notes), d.Notes)
	}
}

// An open-ended range is bounded on one side only.
func TestListAppliesAHalfOpenNoteRange(t *testing.T) {
	setupTestDB(t)
	seedNoted(t, "s-half", "Half open", "2026-03-01", "2026-09-09")
	backdate(t, "s-half", "2026-03-01 09:00:00", "2026-09-30 09:00:00")

	res, err := RecallList(RecallListOptions{From: "2026-06-01"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	d := digestFor(t, res, "s-half")
	if len(d.Notes) != 1 || !strings.HasPrefix(d.Notes[0], "2026-09-09") {
		t.Fatalf("expected only notes on/after 2026-06-01, got %v", d.Notes)
	}
}

// --- item 2: asking for no notes at all ----------------------------------

func TestListNotesNoneOmitsNoteTextButKeepsCoverage(t *testing.T) {
	setupTestDB(t)
	seedNoted(t, "s-none", "Cheap sweep", "2026-03-01", "2026-09-09")

	res, err := RecallList(RecallListOptions{Notes: NotesNone})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	d := digestFor(t, res, "s-none")
	if len(d.Notes) != 0 {
		t.Fatalf("notes=none still returned note text: %v", d.Notes)
	}
	// Coverage is one short string and it is the only thing separating "you
	// asked for no notes" from "this chat has none".
	if d.NotesCoverThrough != "2026-09-09" {
		t.Errorf("notes_cover_through = %q, want 2026-09-09 even with notes=none", d.NotesCoverThrough)
	}
	// The session itself must still be fully described -- that is the point.
	if d.Title == "" || d.MessageCount == 0 {
		t.Errorf("notes=none degraded the digest itself: %+v", d)
	}
}

func TestListDefaultsToAttachingNotes(t *testing.T) {
	setupTestDB(t)
	seedNoted(t, "s-default", "Default", "2026-03-01")

	res, err := RecallList(RecallListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if d := digestFor(t, res, "s-default"); len(d.Notes) != 1 {
		t.Fatalf("an unset Notes mode must behave like %q, got %v", NotesRange, d.Notes)
	}
}

// --- the queries underneath ----------------------------------------------

func TestNotesForSessionsInRangeFiltersByDay(t *testing.T) {
	setupTestDB(t)
	seedNoted(t, "s-q", "Query", "2026-03-01", "2026-06-15", "2026-09-09")

	got, err := NotesForSessionsInRange([]string{"s-q"}, "2026-06-01", "2026-06-30")
	if err != nil {
		t.Fatalf("range query: %v", err)
	}
	if len(got["s-q"]) != 1 || got["s-q"][0].Day != "2026-06-15" {
		t.Fatalf("expected only 2026-06-15, got %+v", got["s-q"])
	}
}

// The unfiltered call is what every other consumer still gets.
func TestNotesForSessionsIsUnboundedByDefault(t *testing.T) {
	setupTestDB(t)
	seedNoted(t, "s-unb", "Unbounded", "2026-03-01", "2026-09-09")

	got, err := NotesForSessions([]string{"s-unb"})
	if err != nil {
		t.Fatalf("notes: %v", err)
	}
	if len(got["s-unb"]) != 2 {
		t.Fatalf("expected both notes, got %+v", got["s-unb"])
	}
}

func TestNoteCoverageReportsTheLastDayRegardlessOfAnyRange(t *testing.T) {
	setupTestDB(t)
	seedNoted(t, "s-c1", "One", "2026-03-01", "2026-09-09")
	seedNoted(t, "s-c2", "Two", "2026-05-05")
	seedChat(t, "s-c3", "No notes", "a", "b", "c", "d")

	got, err := NoteCoverage([]string{"s-c1", "s-c2", "s-c3"})
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if got["s-c1"] != "2026-09-09" {
		t.Errorf("s-c1 coverage = %q, want 2026-09-09", got["s-c1"])
	}
	if got["s-c2"] != "2026-05-05" {
		t.Errorf("s-c2 coverage = %q, want 2026-05-05", got["s-c2"])
	}
	if _, ok := got["s-c3"]; ok {
		t.Error("a session with no notes must be absent, not present with an empty day")
	}
}
