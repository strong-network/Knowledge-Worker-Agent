// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"strings"
	"testing"
)

// FTS5 MATCH joins tokens with an implicit AND, so every extra word makes a
// query strictly narrower -- the opposite of the natural reading, where
// describing what you want more fully ought to help. An agent asking a real
// question in eight words got nothing back, and nothing distinguished that
// from "this was never discussed". The failure was silent, and silence there
// produces false confidence.

func seedSearchable(t *testing.T) {
	t.Helper()
	seedChat(t, "s-fts", "Search fixture",
		"we deployed the ingress controller today",
		"the HAProxy config needed a rewrite",
		"NetScaler CPX notes",
		"unrelated chatter about lunch")
}

func TestSearchFallsBackToAnyWhenNoMessageHasEveryTerm(t *testing.T) {
	setupTestDB(t)
	seedSearchable(t)

	// No single message contains all of these.
	res, err := RecallSearch(RecallSearchOptions{Query: "ingress HAProxy NetScaler"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Total == 0 {
		t.Fatal("a query whose terms all exist somewhere returned nothing; the fallback did not fire")
	}
	if res.Match != "any" {
		t.Errorf("match = %q, want \"any\" so the caller can tell which question was answered", res.Match)
	}
}

// The fallback must announce itself. Hits that each answer only part of the
// query, presented as if they answered all of it, is the same false-confidence
// failure arriving by a different route.
func TestSearchFallbackSaysSoInTheNote(t *testing.T) {
	setupTestDB(t)
	seedSearchable(t)

	res, err := RecallSearch(RecallSearchOptions{Query: "ingress HAProxy NetScaler"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(res.Note, "ANY") {
		t.Errorf("note does not flag the weaker match: %q", res.Note)
	}
	if !strings.Contains(res.Note, "3 terms") {
		t.Errorf("note does not say how many terms failed to co-occur: %q", res.Note)
	}
}

// Negative control: when the strict query works, nothing changes. The fallback
// must never widen a search that already had an answer.
func TestSearchDoesNotFallBackWhenAllTermsMatch(t *testing.T) {
	setupTestDB(t)
	seedSearchable(t)

	res, err := RecallSearch(RecallSearchOptions{Query: "ingress controller"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Total != 1 {
		t.Fatalf("expected the single AND match, got %d", res.Total)
	}
	if res.Match != "all" {
		t.Errorf("match = %q, want \"all\"", res.Match)
	}
	if strings.Contains(res.Note, "ANY") {
		t.Errorf("a successful strict search must not carry a fallback note: %q", res.Note)
	}
}

// Negative control: a single word cannot fail for lack of co-occurrence, so
// there is nothing to retry. A genuine miss must stay a miss.
func TestSearchDoesNotFallBackForASingleTerm(t *testing.T) {
	setupTestDB(t)
	seedSearchable(t)

	res, err := RecallSearch(RecallSearchOptions{Query: "kubernetes"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Total != 0 {
		t.Fatalf("expected no hits for an absent word, got %d", res.Total)
	}
	if res.Match != "all" {
		t.Errorf("match = %q, want \"all\" -- a one-word miss is a real miss", res.Match)
	}
}

// If none of the terms appear anywhere, OR finds nothing either. The result
// must stay an honest zero rather than inventing a weaker answer.
func TestSearchStaysEmptyWhenNoTermMatchesAtAll(t *testing.T) {
	setupTestDB(t)
	seedSearchable(t)

	res, err := RecallSearch(RecallSearchOptions{Query: "kubernetes helmfile terraform"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Total != 0 {
		t.Fatalf("expected no hits, got %d", res.Total)
	}
	if res.Match != "all" {
		t.Errorf("match = %q, want \"all\" when the fallback also found nothing", res.Match)
	}
	if strings.Contains(res.Note, "ANY") {
		t.Errorf("an empty result must not claim a fallback happened: %q", res.Note)
	}
}

// The hits returned must come from the widened query, not be left over from
// the strict one. A count that disagrees with the rows is worse than either.
func TestSearchFallbackReturnsTheWidenedHits(t *testing.T) {
	setupTestDB(t)
	seedSearchable(t)

	res, err := RecallSearch(RecallSearchOptions{Query: "ingress HAProxy NetScaler"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Returned != res.Total {
		t.Fatalf("returned %d but total %d; the rows and the count came from different queries",
			res.Returned, res.Total)
	}
	if len(res.Hits) == 0 {
		t.Fatal("fallback reported hits but returned none")
	}
}

// The filters still apply to the widened query -- a fallback that quietly
// dropped the date or role restriction would answer a different question again.
func TestSearchFallbackStillHonoursFilters(t *testing.T) {
	setupTestDB(t)
	seedSearchable(t)

	res, err := RecallSearch(RecallSearchOptions{
		Query: "ingress HAProxy NetScaler",
		Role:  "user",
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	for _, h := range res.Hits {
		if h.Role != "user" {
			t.Fatalf("fallback returned a %q hit despite role=user", h.Role)
		}
	}
}

func TestFtsQueryAnyJoinsWithOr(t *testing.T) {
	if got := ftsQueryAny("alpha beta"); got != `"alpha" OR "beta"` {
		t.Errorf("ftsQueryAny = %q", got)
	}
	// The sanitising that makes a natural phrase safe must survive the OR form.
	if got := ftsQueryAny(`foo:bar -baz`); got != `"foo:bar" OR "-baz"` {
		t.Errorf("ftsQueryAny did not neutralise operators: %q", got)
	}
	// Nothing to match must still be valid FTS5: an empty expression is a
	// syntax error, so it has to become a literal that matches nothing.
	if got := ftsQueryAny("   "); got != `""` {
		t.Errorf("ftsQueryAny of nothing usable = %q, want the match-nothing literal", got)
	}
	// And it must agree with the AND form it falls back from.
	if ftsQueryAny("solo") != ftsQuery("solo") {
		t.Error("a single token must produce the same expression either way")
	}
}
