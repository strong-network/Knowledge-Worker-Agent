// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"testing"
)

// `search` ranks by relevance, so the page a caller gets back is not the
// oldest or newest slice of the match set -- it is the best-ranked slice, in
// no particular date order. The reply already said a cut had been made
// (`total`, `truncated`, the note), but not which direction the rest lay in.
//
// The failure that produced this change: a query about a design decision
// returned one recent hit followed by five from July. The September message
// that reversed the July decision ranked just past the page. Nothing in the
// reply suggested anything newer existed, so the honest reading was "we
// decided this in July" -- wrong, and confidently so.

// backdateMessage pins one message's timestamp so a test can assert on dates.
// Written in SQLite's own storage format (naive, space-separated), which is
// what CURRENT_TIMESTAMP produces, so the tests exercise the same conversion
// the real rows go through.
func backdateMessage(t *testing.T, content, at string) {
	t.Helper()
	res, err := DB.Exec(`UPDATE messages SET created_at = ? WHERE content = ?`, at, content)
	if err != nil {
		t.Fatalf("backdate message %q: %v", content, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		t.Fatalf("backdate matched no message with content %q", content)
	}
}

// seedSpanChat lays out the shape of the real failure: a cluster of older
// messages and one newer one that ranks last.
func seedSpanChat(t *testing.T) {
	t.Helper()
	seedChat(t, "s-span", "Span fixture",
		"typography decision alpha",
		"typography decision beta",
		"typography decision gamma",
		"typography decision delta")
	backdateMessage(t, "typography decision alpha", "2026-07-13 11:19:11")
	backdateMessage(t, "typography decision beta", "2026-07-13 14:05:17")
	backdateMessage(t, "typography decision gamma", "2026-08-31 13:23:36")
	backdateMessage(t, "typography decision delta", "2026-09-02 14:48:27")
}

func TestSearchSpanCoversTheWholeMatchSetNotThePage(t *testing.T) {
	setupTestDB(t)
	seedSpanChat(t)

	// One hit returned out of four matches. The span must still describe all
	// four, or it is just a restatement of the page and tells the caller
	// nothing it did not already have.
	res, err := RecallSearch(RecallSearchOptions{Query: "typography", Limit: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Total != 4 || res.Returned != 1 {
		t.Fatalf("fixture did not truncate: total=%d returned=%d", res.Total, res.Returned)
	}
	if res.Span == nil {
		t.Fatal("no span on a truncated search; the caller cannot tell which way the rest lies")
	}
	if res.Span.Oldest != "2026-07-13T11:19:11Z" {
		t.Errorf("span.oldest = %q, want the oldest match, not the oldest hit", res.Span.Oldest)
	}
	if res.Span.Newest != "2026-09-02T14:48:27Z" {
		t.Errorf("span.newest = %q, want the newest match, not the newest hit", res.Span.Newest)
	}
}

// The span exists to be compared against the hit timestamps, so it has to be
// in the same format as them. It is not, by default: MIN/MAX strip the
// column's declared type, the driver stops converting, and the raw SQLite
// value comes back as "2026-07-13 11:19:11" -- no T, no Z. A span that cannot
// be compared with what it sits next to is worse than none.
func TestSearchSpanUsesTheSameTimestampFormatAsHits(t *testing.T) {
	setupTestDB(t)
	seedSpanChat(t)

	res, err := RecallSearch(RecallSearchOptions{Query: "typography"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Span == nil {
		t.Fatal("no span")
	}
	if len(res.Hits) == 0 {
		t.Fatal("no hits to compare against")
	}

	// Assert against the hits rather than a literal, so the two can never
	// drift apart without this failing.
	oldest, newest := res.Hits[0].At, res.Hits[0].At
	for _, h := range res.Hits {
		if h.At < oldest {
			oldest = h.At
		}
		if h.At > newest {
			newest = h.At
		}
	}
	if res.Span.Oldest != oldest {
		t.Errorf("span.oldest = %q but the oldest hit is %q; formats or values differ", res.Span.Oldest, oldest)
	}
	if res.Span.Newest != newest {
		t.Errorf("span.newest = %q but the newest hit is %q; formats or values differ", res.Span.Newest, newest)
	}
}

// The trap. The fallback re-runs the count with the tokens OR-ed and swaps in
// the new total, so the span has to be recomputed from that same count. The
// ALL count that got us here matched nothing, so carrying its span over would
// put an empty range next to a total and a page describing a real one.
func TestSearchSpanIsRecomputedForTheAnyFallback(t *testing.T) {
	setupTestDB(t)
	seedSpanChat(t)

	// No message holds both terms, so this falls back to ANY. "zebra" appears
	// nowhere, which keeps the ALL set genuinely empty.
	res, err := RecallSearch(RecallSearchOptions{Query: "typography zebra"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Match != matchAny {
		t.Fatalf("match = %q, want the ANY fallback to have fired", res.Match)
	}
	if res.Total == 0 {
		t.Fatal("fallback found nothing; fixture is wrong")
	}
	if res.Span == nil {
		t.Fatal("fallback reply has no span, though it has hits to span")
	}
	if res.Span.Oldest != "2026-07-13T11:19:11Z" || res.Span.Newest != "2026-09-02T14:48:27Z" {
		t.Errorf("span = %+v; it describes the empty ALL set, not the ANY set that was returned", *res.Span)
	}
}

// Nothing matched, so there is no range. Two empty strings would read as one.
func TestSearchOmitsSpanWhenNothingMatched(t *testing.T) {
	setupTestDB(t)
	seedSpanChat(t)

	res, err := RecallSearch(RecallSearchOptions{Query: "zebra"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Total != 0 {
		t.Fatalf("fixture matched something: total=%d", res.Total)
	}
	if res.Span != nil {
		t.Errorf("span = %+v on a zero-match reply, want none", *res.Span)
	}
}

// The span describes the set the caller actually asked for. If it ignored the
// filters it would permanently claim newer material exists, and an agent
// following that advice would re-query forever.
func TestSearchSpanRespectsTheDateFilter(t *testing.T) {
	setupTestDB(t)
	seedSpanChat(t)

	res, err := RecallSearch(RecallSearchOptions{Query: "typography", To: "2026-08-01"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Total != 2 {
		t.Fatalf("total = %d, want the 2 July matches", res.Total)
	}
	if res.Span == nil {
		t.Fatal("no span")
	}
	if res.Span.Newest != "2026-07-13T14:05:17Z" {
		t.Errorf("span.newest = %q, want the newest match inside the filter", res.Span.Newest)
	}
}
