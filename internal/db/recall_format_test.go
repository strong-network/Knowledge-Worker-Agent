// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

// Wire-format tests for the timestamps recall returns.
//
// These exist because of a regression that shipped. Adding the deleted-chats
// UNION to `list` changed every timestamp in that response from
// "2026-05-11T11:42:51Z" to "2026-05-11 11:42:51", and nothing failed. The
// change was invisible in review (the edit was structural -- no timestamp
// expression was touched) and invisible in the tests (every assertion checked
// ordering or a date prefix, and both survive the change). It was caught by
// someone reading the API output.
//
// The mechanism is worth stating once: modernc.org/sqlite converts a column
// whose *declared type* is DATETIME into a time.Time, which Go then renders as
// RFC 3339. Selecting the same column through a subquery or compound SELECT
// loses the declared type, so the raw stored text comes back instead. The
// format was therefore never something the code chose -- it was a side effect
// of query shape, and any future rewrite could change it again just as quietly.
//
// So these tests assert the format itself, not a prefix, and assert it across
// all three verbs. Their job is to fail on the next structural rewrite.

import (
	"regexp"
	"testing"
)

// rfc3339Z is the format every timestamp in a recall response is expected to
// use: UTC, with the T separator and a Z suffix.
var rfc3339Z = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)

func assertRFC3339(t *testing.T, field, got string) {
	t.Helper()
	if !rfc3339Z.MatchString(got) {
		t.Errorf("%s = %q, want RFC 3339 like 2026-03-01T09:15:00Z", field, got)
	}
}

// The live half of the union. This is the case the regression actually broke:
// these sessions were formatted correctly before the UNION was introduced and
// silently changed afterwards, even though nothing about them was edited.
func TestListTimestampsAreRFC3339ForLiveSessions(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "s-live", "Live chat", "opening request", "reply", "more", "and more")

	res, err := RecallList(RecallListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	d := digestFor(t, res, "s-live")
	assertRFC3339(t, "started_at", d.StartedAt)
	assertRFC3339(t, "last_active_at", d.LastActiveAt)
}

// The deleted half. These timestamps are derived from note days rather than
// read from a DATETIME column, so they need formatting for a second reason:
// a note stores "2026-03-01" with no time part at all. Without an explicit
// format the same response would carry two different shapes.
func TestListTimestampsAreRFC3339ForDeletedSessions(t *testing.T) {
	setupTestDB(t)
	seedNoted(t, "s-gone", "Deleted chat", "2026-03-01", "2026-03-04")
	if err := MarkNotesOrphaned("s-gone"); err != nil {
		t.Fatalf("orphan: %v", err)
	}
	if err := DeleteSession("s-gone"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	res, err := RecallList(RecallListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	d := digestFor(t, res, "s-gone")
	assertRFC3339(t, "started_at", d.StartedAt)
	assertRFC3339(t, "last_active_at", d.LastActiveAt)

	// A bare day becomes midnight UTC rather than being padded arbitrarily.
	if d.StartedAt != "2026-03-01T00:00:00Z" {
		t.Errorf("started_at = %q, want the first noted day at midnight", d.StartedAt)
	}
	if d.LastActiveAt != "2026-03-04T00:00:00Z" {
		t.Errorf("last_active_at = %q, want the last noted day at midnight", d.LastActiveAt)
	}
}

// One response can contain both sources. An agent parsing it applies one rule,
// so the two halves have to agree -- this is the assertion that would have
// caught the regression at the moment it was introduced.
func TestListTimestampFormatIsConsistentAcrossSources(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "s-alive", "Still here", "opening request", "reply", "more", "and more")
	seedNoted(t, "s-dead", "Long gone", "2026-03-02")
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
	if len(res.Sessions) < 2 {
		t.Fatalf("expected both sources in the list, got %v", idsOf(res.Sessions))
	}
	for _, d := range res.Sessions {
		assertRFC3339(t, d.SessionID+".started_at", d.StartedAt)
		assertRFC3339(t, d.SessionID+".last_active_at", d.LastActiveAt)
	}
}

// `read` and `search` were never rewritten, so they still return the format by
// the driver's accident described above. Pinning them here means the accident
// is now a checked expectation: if a later change breaks them the way the UNION
// broke `list`, that fails here instead of reaching a reader.
func TestReadAndSearchTimestampsAreRFC3339(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "s-verbs", "Cross check", "opening request", "reply", "more", "and more")

	rd, err := RecallRead(RecallReadOptions{SessionID: "s-verbs"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rd.Messages) == 0 {
		t.Fatalf("read returned no messages")
	}
	for i, m := range rd.Messages {
		assertRFC3339(t, "read.messages[].at", m.At)
		if i > 2 {
			break
		}
	}

	sr, err := RecallSearch(RecallSearchOptions{Query: "opening"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(sr.Hits) == 0 {
		t.Fatalf("search returned no hits")
	}
	for _, h := range sr.Hits {
		assertRFC3339(t, "search.hits[].at", h.At)
	}
}

// The format has to stay sortable by SQL too. RFC 3339 is not SQLite's native
// datetime literal, so ORDER BY datetime(last_active) is doing a parse on it --
// if that parse ever failed, the ordering would collapse silently rather than
// error, and the list would come back in arbitrary order.
func TestListStillOrdersCorrectlyWithFormattedTimestamps(t *testing.T) {
	setupTestDB(t)
	seedNoted(t, "s-older", "Older", "2026-01-05")
	seedNoted(t, "s-newer", "Newer", "2026-06-05")
	for _, id := range []string{"s-older", "s-newer"} {
		if err := MarkNotesOrphaned(id); err != nil {
			t.Fatalf("orphan %s: %v", id, err)
		}
		if err := DeleteSession(id); err != nil {
			t.Fatalf("delete %s: %v", id, err)
		}
	}

	res, err := RecallList(RecallListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	ids := idsOf(res.Sessions)
	if len(ids) != 2 {
		t.Fatalf("got %v, want both noted sessions", ids)
	}
	if ids[0] != "s-newer" {
		t.Errorf("order = %v, want the most recently active first", ids)
	}
}
