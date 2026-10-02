// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"fmt"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

// seedChat creates a session with a label and n alternating messages.
func seedChat(t *testing.T, id, label string, contents ...string) {
	t.Helper()
	if err := CreateSession(id, config.SessionConfig{Label: label, Workdir: "/tmp"}); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
	for i, c := range contents {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if err := AddMessage(id, role, c); err != nil {
			t.Fatalf("add message to %s: %v", id, err)
		}
	}
}

func seedTemporaryChat(t *testing.T, id, label string, contents ...string) {
	t.Helper()
	if err := CreateSession(id, config.SessionConfig{
		Label: label, Workdir: "/tmp", Temporary: true,
	}); err != nil {
		t.Fatalf("create temp %s: %v", id, err)
	}
	for i, c := range contents {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if err := AddMessage(id, role, c); err != nil {
			t.Fatalf("add message to %s: %v", id, err)
		}
	}
}

func idsOf(ds []RecallDigest) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.SessionID)
	}
	return out
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// --- list ---------------------------------------------------------------

func TestRecallListReturnsDigests(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "s1", "Pricing model",
		"how should we price the enterprise tier",
		"three options, by seat, by workspace, or hybrid")

	res, err := RecallList(RecallListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := res.Sessions
	if len(got) != 1 {
		t.Fatalf("got %d digests, want 1: %v", len(got), idsOf(got))
	}
	d := got[0]
	if d.Title != "Pricing model" {
		t.Errorf("Title = %q, want %q", d.Title, "Pricing model")
	}
	if d.MessageCount != 2 {
		t.Errorf("MessageCount = %d, want 2", d.MessageCount)
	}
	if !strings.Contains(d.Intent, "price the enterprise tier") {
		t.Errorf("Intent = %q, want the first USER message", d.Intent)
	}
	if d.Source != "available" {
		t.Errorf("Source = %q, want %q", d.Source, "available")
	}
	if d.Notes == nil {
		t.Error("Notes is nil; must be [] so the JSON shape is stable")
	}
}

// TestRecallListExcludesTemporary is a privacy guarantee, not a preference: a
// Temporary chat is declared throwaway and is never backed up. If retrieval
// could surface it, the flag would be a lie.
//
// Negative control: drop the notTemporary clause from RecallList and this fails.
func TestRecallListExcludesTemporary(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "keep", "Durable", "one", "two")
	seedTemporaryChat(t, "temp", "Throwaway", "secret one", "secret two")

	res, err := RecallList(RecallListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ids := idsOf(res.Sessions)
	if contains(ids, "temp") {
		t.Fatalf("temporary chat surfaced in list: %v", ids)
	}
	if !contains(ids, "keep") {
		t.Fatalf("durable chat missing from list: %v", ids)
	}
}

// TestRecallListExcludesNearEmpty keeps enumeration useful: a chat with a
// single stray message is noise, not history.
func TestRecallListExcludesNearEmpty(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "real", "Real conversation", "a", "b")
	seedChat(t, "stub", "Barely started", "hello?")
	seedChat(t, "empty", "Never used")

	res, err := RecallList(RecallListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ids := idsOf(res.Sessions)
	if !contains(ids, "real") {
		t.Errorf("real chat missing: %v", ids)
	}
	if contains(ids, "stub") || contains(ids, "empty") {
		t.Errorf("near-empty chats surfaced: %v", ids)
	}
}

func TestRecallListLimitIsCapped(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "s1", "One", "a", "b")
	res, err := RecallList(RecallListOptions{Limit: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) > recallMaxList {
		t.Fatalf("returned %d digests, above the %d cap", len(res.Sessions), recallMaxList)
	}
}

func TestRecallListFiltersByProject(t *testing.T) {
	setupTestDB(t)
	if err := CreateProject("p1", "Alpha", "", "/tmp", ""); err != nil {
		t.Fatal(err)
	}
	seedChat(t, "in", "In project", "a", "b")
	seedChat(t, "out", "No project", "a", "b")
	if err := SetSessionProject("in", "p1"); err != nil {
		t.Fatal(err)
	}

	res, err := RecallList(RecallListOptions{Project: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	got := res.Sessions
	if len(got) != 1 || got[0].SessionID != "in" {
		t.Fatalf("project filter returned %v, want [in]", idsOf(got))
	}
	if got[0].Project != "Alpha" {
		t.Errorf("Project = %q, want the project NAME %q", got[0].Project, "Alpha")
	}
}

// TestRecallListDateBoundIncludesWholeEndDay pins the off-by-one that would
// look like missing data rather than a bug: `to=<today>` must not exclude
// everything written after midnight.
func TestRecallListDateBoundIncludesWholeEndDay(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "s1", "Today", "a", "b")

	var day string
	if err := DB.QueryRow(`SELECT date(created_at) FROM sessions WHERE id = 's1'`).Scan(&day); err != nil {
		t.Fatal(err)
	}

	res, err := RecallList(RecallListOptions{From: day, To: day})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 {
		t.Fatalf("same-day from/to returned %d digests, want 1 — the end bound is not inclusive", len(res.Sessions))
	}
}

// --- search -------------------------------------------------------------

func TestRecallSearchRanksAndSnippets(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "s1", "Pricing", "we discussed the pricing tier at length", "noted")
	seedChat(t, "s2", "Unrelated", "kubernetes ingress config", "noted")

	res, err := RecallSearch(RecallSearchOptions{Query: "pricing"})
	if err != nil {
		t.Fatal(err)
	}
	hits := res.Hits
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1: %+v", len(hits), hits)
	}
	if hits[0].SessionID != "s1" {
		t.Errorf("SessionID = %q, want s1", hits[0].SessionID)
	}
	if hits[0].Title != "Pricing" {
		t.Errorf("Title = %q, want the owning chat's title", hits[0].Title)
	}
	if !strings.Contains(hits[0].Snippet, "[pricing]") {
		t.Errorf("Snippet = %q, want the match delimited", hits[0].Snippet)
	}
}

func TestRecallSearchExcludesTemporary(t *testing.T) {
	setupTestDB(t)
	seedTemporaryChat(t, "temp", "Throwaway", "the pricing tier is confidential", "ok")
	seedChat(t, "keep", "Durable", "the pricing tier is fine to share", "ok")

	res, err := RecallSearch(RecallSearchOptions{Query: "pricing"})
	if err != nil {
		t.Fatal(err)
	}
	hits := res.Hits
	for _, h := range hits {
		if h.SessionID == "temp" {
			t.Fatalf("temporary chat surfaced in search: %+v", h)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1 (the durable chat)", len(hits))
	}
}

func TestRecallSearchFiltersByRole(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "s1", "Chat", "quokka question", "quokka answer")

	resUser, err := RecallSearch(RecallSearchOptions{Query: "quokka", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	user := resUser.Hits
	if len(user) != 1 || user[0].Role != "user" {
		t.Fatalf("role=user returned %+v", user)
	}
	resBoth, err := RecallSearch(RecallSearchOptions{Query: "quokka"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resBoth.Hits) != 2 {
		t.Fatalf("unfiltered returned %d hits, want 2", len(resBoth.Hits))
	}
}

func TestRecallSearchEmptyQuery(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "s1", "Chat", "a", "b")
	res, err := RecallSearch(RecallSearchOptions{Query: "   "})
	if err != nil {
		t.Fatalf("blank query should be empty, not an error: %v", err)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("blank query returned %d hits", len(res.Hits))
	}
}

// TestRecallSearchSurvivesOperatorChars is the reason ftsQuery exists. An agent
// passing a natural phrase must not hit an FTS5 syntax error, and must not have
// a stray dash silently reinterpreted as a NOT.
func TestRecallSearchSurvivesOperatorChars(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "s1", "Chat", "the cost-per-seat model was rejected", "ok")

	for _, q := range []string{
		`cost-per-seat`,
		`"unbalanced`,
		`NEAR(a b)`,
		`col:value`,
		`AND OR NOT`,
		`***`,
		`(unclosed`,
	} {
		if _, err := RecallSearch(RecallSearchOptions{Query: q}); err != nil {
			t.Errorf("query %q returned an error: %v", q, err)
		}
	}
}

func TestFTSQuerySanitising(t *testing.T) {
	cases := map[string]string{
		"pricing":       `"pricing"`,
		"cost-per-seat": `"cost-per-seat"`,
		"two words":     `"two" "words"`,
		"pric*":         `"pric"*`,
		`say "hi"`:      `"say" """hi"""`,
		`*`:             `"*"`,
		``:              `""`,
		`   `:           `""`,
	}
	for in, want := range cases {
		if got := ftsQuery(in); got != want {
			t.Errorf("ftsQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRecallSearchReportsUnavailableIndex pins the report-what-is-knowable
// rule: with no index, search must say so rather than return zero hits, which
// is indistinguishable from "nothing matched".
func TestRecallSearchReportsUnavailableIndex(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "s1", "Chat", "pricing tier", "ok")
	if _, err := DB.Exec(`DROP TABLE messages_fts`); err != nil {
		t.Fatal(err)
	}

	_, err := RecallSearch(RecallSearchOptions{Query: "pricing"})
	if err == nil {
		t.Fatal("search with no index returned no error; a caller cannot tell this from an empty result")
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("error = %v, want it to say the index is unavailable", err)
	}
}

// --- read ---------------------------------------------------------------

func TestRecallRead(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "s1", "Transcript", "first", "second", "third")

	tr, err := RecallRead(RecallReadOptions{SessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Title != "Transcript" {
		t.Errorf("Title = %q", tr.Title)
	}
	if len(tr.Messages) != 3 {
		t.Fatalf("got %d messages, want 3", len(tr.Messages))
	}
	if tr.Messages[0].Content != "first" || tr.Messages[2].Content != "third" {
		t.Errorf("messages out of order: %+v", tr.Messages)
	}
	if tr.Truncated {
		t.Error("Truncated = true for a complete transcript")
	}
}

// TestRecallReadReportsTruncation matters because a transcript cut short
// without saying so lets the agent conclude a conversation ended where it did
// not.
func TestRecallReadReportsTruncation(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "s1", "Long", "a", "b", "c", "d", "e")

	tr, err := RecallRead(RecallReadOptions{SessionID: "s1", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(tr.Messages))
	}
	if !tr.Truncated {
		t.Fatal("Truncated = false after cutting 5 messages to 2")
	}
}

// TestRecallReadRefusesTemporary also checks the refusal is indistinguishable
// from "no such session", so retrieval cannot be used to probe for the
// existence of a chat it may not read.
//
// "Indistinguishable" means the same message template: both errors echo back
// only the id the caller already supplied, so nothing about whether the chat
// exists leaks.
func TestRecallReadRefusesTemporary(t *testing.T) {
	setupTestDB(t)
	seedTemporaryChat(t, "temp", "Throwaway", "secret", "more secret")

	_, errTemp := RecallRead(RecallReadOptions{SessionID: "temp"})
	if errTemp == nil {
		t.Fatal("read returned a temporary chat's transcript")
	}
	_, errMissing := RecallRead(RecallReadOptions{SessionID: "does-not-exist"})
	if errMissing == nil {
		t.Fatal("read of a missing session returned no error")
	}

	wantTemp := "session not found: temp"
	wantMissing := "session not found: does-not-exist"
	if errTemp.Error() != wantTemp {
		t.Errorf("temporary error = %q, want %q", errTemp.Error(), wantTemp)
	}
	if errMissing.Error() != wantMissing {
		t.Errorf("missing error = %q, want %q", errMissing.Error(), wantMissing)
	}
}

// --- windowing: the bug an agent hit in production ------------------------
//
// Reported symptom: asked to summarise the last 3 days of a 434-message chat,
// the agent called recall_read with limit=500 and then 1000, received the same
// 200 oldest messages (Aug 14-19) both times, and concluded the tool simply
// could not reach recent activity. Every one of the following is a way that
// failure could have been caught earlier.

// seedNumbered makes a chat with n messages whose content is its index, so a
// test can assert exactly WHICH slice came back rather than just how many.
func seedNumbered(t *testing.T, id string, n int) {
	t.Helper()
	if err := CreateSession(id, config.SessionConfig{Label: "Long chat", Workdir: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if err := AddMessage(id, role, fmt.Sprintf("message-%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
}

func contentsOf(msgs []RecallMessage) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Content)
	}
	return out
}

// TestRecallReadOverLimitIsReported is the exact reported scenario: ask for
// more than the cap on an oversized chat and be told, unmistakably, that this
// is not the whole thing.
func TestRecallReadOverLimitIsReported(t *testing.T) {
	setupTestDB(t)
	seedNumbered(t, "long", recallMaxRead+34) // 234, like the real 434-message case

	tr, err := RecallRead(RecallReadOptions{SessionID: "long", Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Returned != recallMaxRead {
		t.Fatalf("returned %d messages, want the cap %d", tr.Returned, recallMaxRead)
	}
	if tr.TotalMessages != recallMaxRead+34 {
		t.Errorf("total_messages = %d, want %d", tr.TotalMessages, recallMaxRead+34)
	}
	if !tr.Truncated {
		t.Error("truncated = false while withholding 34 messages")
	}
	if tr.RequestedLimit != 1000 {
		t.Errorf("requested_limit = %d, want 1000 echoed back so the clamp is visible", tr.RequestedLimit)
	}
	if tr.Note == "" {
		t.Fatal("no note explaining the clamp — this is what the agent needed and did not get")
	}
	for _, want := range []string{"NOT the whole chat", "order=", "offset="} {
		if !strings.Contains(tr.Note, want) {
			t.Errorf("note %q does not mention %q", tr.Note, want)
		}
	}
}

// TestRecallReadNewestReachesTheTail is the capability that was missing
// outright: there was no way to see the end of a long chat.
func TestRecallReadNewestReachesTheTail(t *testing.T) {
	setupTestDB(t)
	seedNumbered(t, "long", 250)

	tr, err := RecallRead(RecallReadOptions{SessionID: "long", Order: "newest", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	got := contentsOf(tr.Messages)
	want := []string{"message-245", "message-246", "message-247", "message-248", "message-249"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order=newest returned %v, want the LAST five in chronological order %v", got, want)
	}
	if tr.Order != "newest" {
		t.Errorf("order = %q, want it echoed", tr.Order)
	}
}

// TestRecallReadOldestIsStillTheDefault guards the existing contract: a
// transcript is normally read from the beginning.
func TestRecallReadOldestIsStillTheDefault(t *testing.T) {
	setupTestDB(t)
	seedNumbered(t, "long", 250)

	tr, err := RecallRead(RecallReadOptions{SessionID: "long", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	got := contentsOf(tr.Messages)
	want := []string{"message-000", "message-001", "message-002"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("default order returned %v, want %v", got, want)
	}
	if tr.Order != "oldest" {
		t.Errorf("order = %q, want \"oldest\"", tr.Order)
	}
}

func TestRecallReadOffsetPagesForward(t *testing.T) {
	setupTestDB(t)
	seedNumbered(t, "long", 250)

	tr, err := RecallRead(RecallReadOptions{SessionID: "long", Limit: 3, Offset: 5})
	if err != nil {
		t.Fatal(err)
	}
	got := contentsOf(tr.Messages)
	want := []string{"message-005", "message-006", "message-007"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("offset=5 returned %v, want %v", got, want)
	}
	if tr.Offset != 5 {
		t.Errorf("offset = %d, want it echoed", tr.Offset)
	}
}

// TestRecallReadOffsetFromNewestEnd pins the composed semantic: offset counts
// from whichever end you chose, so paging backwards through recent history
// works without arithmetic against the total.
func TestRecallReadOffsetFromNewestEnd(t *testing.T) {
	setupTestDB(t)
	seedNumbered(t, "long", 250)

	tr, err := RecallRead(RecallReadOptions{SessionID: "long", Order: "newest", Limit: 3, Offset: 3})
	if err != nil {
		t.Fatal(err)
	}
	got := contentsOf(tr.Messages)
	want := []string{"message-244", "message-245", "message-246"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order=newest offset=3 returned %v, want the three before the last three %v", got, want)
	}
}

// TestRecallReadFullPaginationCoversEverything is the property that matters
// more than any single page: repeated paging must eventually yield every
// message exactly once. Before this change a 434-message chat had 234 messages
// that were unreachable by any sequence of calls.
func TestRecallReadFullPaginationCoversEverything(t *testing.T) {
	setupTestDB(t)
	const n = 250
	seedNumbered(t, "long", n)

	seen := map[string]int{}
	for offset := 0; ; {
		tr, err := RecallRead(RecallReadOptions{SessionID: "long", Limit: 60, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range tr.Messages {
			seen[m.Content]++
		}
		offset += tr.Returned
		if !tr.Truncated {
			break
		}
		if tr.Returned == 0 {
			t.Fatal("truncated but returned nothing — pagination cannot terminate")
		}
	}
	if len(seen) != n {
		t.Fatalf("paging saw %d distinct messages, want %d", len(seen), n)
	}
	for c, count := range seen {
		if count != 1 {
			t.Errorf("message %s returned %d times; pages overlap", c, count)
		}
	}
}

// TestRecallReadDateWindow is the direct route to "the last 3 days", which is
// what was actually asked for.
func TestRecallReadDateWindow(t *testing.T) {
	setupTestDB(t)
	if err := CreateSession("s1", config.SessionConfig{Label: "Spanning", Workdir: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct{ content, at string }{
		{"ancient", "2026-01-01 10:00:00"},
		{"old", "2026-02-01 10:00:00"},
		{"recent-a", "2026-09-08 10:00:00"},
		{"recent-b", "2026-09-09 08:00:00"},
	} {
		if _, err := DB.Exec(
			`INSERT INTO messages (session_id, role, content, created_at) VALUES (?,?,?,?)`,
			"s1", "user", m.content, m.at); err != nil {
			t.Fatal(err)
		}
	}

	tr, err := RecallRead(RecallReadOptions{SessionID: "s1", From: "2026-09-07", To: "2026-09-09"})
	if err != nil {
		t.Fatal(err)
	}
	got := contentsOf(tr.Messages)
	if strings.Join(got, ",") != "recent-a,recent-b" {
		t.Fatalf("date window returned %v, want [recent-a recent-b]", got)
	}
	if tr.Matched != 2 {
		t.Errorf("matched = %d, want 2", tr.Matched)
	}
	if tr.TotalMessages != 4 {
		t.Errorf("total_messages = %d, want 4 (the whole chat, not the window)", tr.TotalMessages)
	}
	if tr.Truncated {
		t.Error("truncated = true although the whole window was returned")
	}
	if !strings.Contains(tr.Note, "date range") {
		t.Errorf("note %q should say the window narrowed the result", tr.Note)
	}
}

// TestRecallReadDateWindowIncludesWholeEndDay is the same off-by-one guarded
// on list: "to = today" must include today's messages, not stop at midnight.
func TestRecallReadDateWindowIncludesWholeEndDay(t *testing.T) {
	setupTestDB(t)
	if err := CreateSession("s1", config.SessionConfig{Label: "Today", Workdir: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(
		`INSERT INTO messages (session_id, role, content, created_at) VALUES (?,?,?,?)`,
		"s1", "user", "afternoon", "2026-09-09 16:45:00"); err != nil {
		t.Fatal(err)
	}

	tr, err := RecallRead(RecallReadOptions{SessionID: "s1", From: "2026-09-09", To: "2026-09-09"})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Returned != 1 {
		t.Fatalf("same-day window returned %d messages, want 1 — the end bound stops at midnight", tr.Returned)
	}
}

func TestRecallReadCompleteChatIsNotFlaggedTruncated(t *testing.T) {
	setupTestDB(t)
	seedNumbered(t, "short", 5)

	tr, err := RecallRead(RecallReadOptions{SessionID: "short"})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Truncated {
		t.Error("truncated = true for a chat returned in full")
	}
	if tr.Note != "" {
		t.Errorf("note = %q, want empty when there is nothing to warn about", tr.Note)
	}
	if tr.Returned != 5 || tr.TotalMessages != 5 {
		t.Errorf("returned=%d total=%d, want 5/5", tr.Returned, tr.TotalMessages)
	}
}

// --- list paging ---------------------------------------------------------

// TestRecallListReportsTotal covers the same silent-cap bug on the other verb:
// the live workspace had 215 sessions and list returned 200 of them without
// saying so.
func TestRecallListReportsTotal(t *testing.T) {
	setupTestDB(t)
	for i := 0; i < 6; i++ {
		seedChat(t, fmt.Sprintf("s%d", i), fmt.Sprintf("Chat %d", i), "a", "b")
	}

	res, err := RecallList(RecallListOptions{Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 6 {
		t.Errorf("total = %d, want 6", res.Total)
	}
	if res.Returned != 4 {
		t.Errorf("returned = %d, want 4", res.Returned)
	}
	if !res.Truncated {
		t.Error("truncated = false while withholding 2 sessions")
	}
	if !strings.Contains(res.Note, "of 6") {
		t.Errorf("note %q should say how many there are in total", res.Note)
	}
}

func TestRecallListOffsetPages(t *testing.T) {
	setupTestDB(t)
	for i := 0; i < 6; i++ {
		seedChat(t, fmt.Sprintf("s%d", i), fmt.Sprintf("Chat %d", i), "a", "b")
	}

	seen := map[string]bool{}
	for offset := 0; ; {
		res, err := RecallList(RecallListOptions{Limit: 4, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range res.Sessions {
			if seen[d.SessionID] {
				t.Errorf("session %s returned on two pages", d.SessionID)
			}
			seen[d.SessionID] = true
		}
		offset += res.Returned
		if !res.Truncated {
			break
		}
	}
	if len(seen) != 6 {
		t.Fatalf("paging saw %d sessions, want 6", len(seen))
	}
}

func TestRecallListCompleteIsNotFlaggedTruncated(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "s1", "Only", "a", "b")

	res, err := RecallList(RecallListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Truncated || res.Note != "" {
		t.Errorf("truncated=%v note=%q for a complete list", res.Truncated, res.Note)
	}
	if res.Total != 1 || res.Returned != 1 {
		t.Errorf("total=%d returned=%d, want 1/1", res.Total, res.Returned)
	}
}

// --- search reporting -----------------------------------------------------

func TestRecallSearchReportsTotal(t *testing.T) {
	setupTestDB(t)
	seedNumbered(t, "long", 10)
	// Every seeded message contains "message", so all 10 match.
	res, err := RecallSearch(RecallSearchOptions{Query: "message", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 10 {
		t.Errorf("total = %d, want 10", res.Total)
	}
	if res.Returned != 3 {
		t.Errorf("returned = %d, want 3", res.Returned)
	}
	if !res.Truncated {
		t.Error("truncated = false while withholding 7 matches")
	}
	if !strings.Contains(res.Note, "best-ranked") {
		t.Errorf("note %q should explain these are the best-ranked subset", res.Note)
	}
}

// --- per-verb caps --------------------------------------------------------
//
// One shared cap of 200 covered all three verbs at first, which conflated two
// different questions: how much should an unasked-for call return, and how much
// is a deliberate call allowed to ask for. Splitting them lets the cheap,
// size-bounded verbs go further without making the careless call bigger, and
// without loosening `read`, whose rows have no size bound at all.

// TestClampLimitSeparatesDefaultFromMaximum pins the numbers, since the whole
// point of the change is that they are no longer the same number.
func TestClampLimitSeparatesDefaultFromMaximum(t *testing.T) {
	cases := []struct {
		name      string
		requested int
		max       int
		want      int
	}{
		{"unset gets the shared default, not the verb maximum", 0, recallMaxSearch, recallDefaultLimit},
		{"negative is treated as unset", -5, recallMaxList, recallDefaultLimit},
		{"a modest ask is honoured", 25, recallMaxList, 25},
		{"an ask above the default is honoured up to the maximum", 400, recallMaxList, 400},
		{"list is cut at its own ceiling", 10_000, recallMaxList, recallMaxList},
		{"search is cut at its own, higher ceiling", 10_000, recallMaxSearch, recallMaxSearch},
		{"read keeps the strictest ceiling", 10_000, recallMaxRead, recallMaxRead},
	}
	for _, c := range cases {
		if got := clampLimit(c.requested, c.max); got != c.want {
			t.Errorf("%s: clampLimit(%d, %d) = %d, want %d",
				c.name, c.requested, c.max, got, c.want)
		}
	}

	// The ceilings are deliberately ordered by how bounded a row is: a search
	// snippet is a fixed window, a digest is bounded by recallIntentChars, and
	// a message is bounded by nothing.
	if !(recallMaxSearch > recallMaxList && recallMaxList > recallMaxRead) {
		t.Errorf("ceilings out of order: search=%d list=%d read=%d",
			recallMaxSearch, recallMaxList, recallMaxRead)
	}
	if recallDefaultLimit > recallMaxRead {
		t.Errorf("default %d exceeds the read maximum %d, so an unasked-for read would be clamped",
			recallDefaultLimit, recallMaxRead)
	}
}

// seedManySessions makes n listable chats (two messages each clears the
// near-empty cutoff).
func seedManySessions(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		seedChat(t, fmt.Sprintf("s%03d", i), fmt.Sprintf("Chat %d", i), "question", "answer")
	}
}

// TestRecallListGoesPastTwoHundred is the user-visible half of the change: a
// workspace with more than 200 chats can now be enumerated in one call, and the
// old ceiling no longer silently decides for the caller.
func TestRecallListGoesPastTwoHundred(t *testing.T) {
	setupTestDB(t)
	const n = 260
	seedManySessions(t, n)

	t.Run("an explicit larger limit is honoured", func(t *testing.T) {
		res, err := RecallList(RecallListOptions{Limit: n})
		if err != nil {
			t.Fatal(err)
		}
		if res.Returned != n {
			t.Fatalf("returned %d of %d sessions; the old 200 ceiling is still in force", res.Returned, n)
		}
		if res.Truncated {
			t.Error("truncated = true after returning everything")
		}
		if res.Note != "" {
			t.Errorf("note = %q on a complete answer", res.Note)
		}
	})

	t.Run("the default is unchanged", func(t *testing.T) {
		res, err := RecallList(RecallListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Returned != recallDefaultLimit {
			t.Fatalf("an unasked-for list returned %d, want the %d default -- raising the "+
				"ceiling must not make the ordinary call more expensive",
				res.Returned, recallDefaultLimit)
		}
		if !res.Truncated || res.Note == "" {
			t.Error("a defaulted, partial list must still say so")
		}
	})

	t.Run("above the ceiling is reported, not silently obeyed", func(t *testing.T) {
		res, err := RecallList(RecallListOptions{Limit: 10_000})
		if err != nil {
			t.Fatal(err)
		}
		if res.Returned > recallMaxList {
			t.Fatalf("returned %d, above the %d ceiling", res.Returned, recallMaxList)
		}
		if res.RequestedLimit != 10_000 {
			t.Errorf("requested_limit = %d, want 10000 echoed so the clamp is visible", res.RequestedLimit)
		}
	})
}

// TestRecallSearchGoesPastTwoHundred: hits are the cheapest and most tightly
// bounded row of the three (a snippet is a fixed window), so search is allowed
// the largest ceiling.
func TestRecallSearchGoesPastTwoHundred(t *testing.T) {
	setupTestDB(t)
	const n = 260
	seedNumbered(t, "long", n)

	res, err := RecallSearch(RecallSearchOptions{Query: "message", Limit: n})
	if err != nil {
		t.Fatal(err)
	}
	if res.Returned != n {
		t.Fatalf("returned %d of %d matches; the old 200 ceiling is still in force", res.Returned, n)
	}
	if res.Truncated {
		t.Error("truncated = true after returning every match")
	}

	def, err := RecallSearch(RecallSearchOptions{Query: "message"})
	if err != nil {
		t.Fatal(err)
	}
	if def.Returned != recallDefaultLimit {
		t.Errorf("defaulted search returned %d, want the %d default", def.Returned, recallDefaultLimit)
	}
}

// TestRecallReadCeilingIsUnchanged: the reason read keeps the strictest limit
// is that its rows are unbounded -- the measured worst case is a single 20KB
// message -- so a bigger slice cannot be costed in advance.
func TestRecallReadCeilingIsUnchanged(t *testing.T) {
	setupTestDB(t)
	seedNumbered(t, "long", recallMaxRead+20)

	tr, err := RecallRead(RecallReadOptions{SessionID: "long", Limit: recallMaxList})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Returned != recallMaxRead {
		t.Fatalf("returned %d messages; read must not inherit the larger list/search ceiling", tr.Returned)
	}
	if tr.RequestedLimit != recallMaxList {
		t.Errorf("requested_limit = %d, want %d echoed back", tr.RequestedLimit, recallMaxList)
	}
}

// --- search pagination ----------------------------------------------------

// TestRecallSearchOffsetPagesTheRanking closes the other half of the gap. Being
// told the result was truncated is only useful if there is a way to get the
// rest; before this, the note could only suggest narrowing the query, which
// finds *different* matches rather than the remaining ones.
func TestRecallSearchOffsetPagesTheRanking(t *testing.T) {
	setupTestDB(t)
	seedNumbered(t, "long", 30)

	first, err := RecallSearch(RecallSearchOptions{Query: "message", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	second, err := RecallSearch(RecallSearchOptions{Query: "message", Limit: 5, Offset: 5})
	if err != nil {
		t.Fatal(err)
	}

	if second.Offset != 5 {
		t.Errorf("offset = %d, want it echoed as 5", second.Offset)
	}
	if len(first.Hits) != 5 || len(second.Hits) != 5 {
		t.Fatalf("pages returned %d and %d hits, want 5 each", len(first.Hits), len(second.Hits))
	}

	seen := map[string]bool{}
	for _, h := range first.Hits {
		seen[h.At+h.Snippet] = true
	}
	for _, h := range second.Hits {
		if seen[h.At+h.Snippet] {
			t.Fatalf("offset=5 returned a hit already on page 1 (%q) -- offset is ignored", h.Snippet)
		}
	}
}

// TestRecallSearchTruncatedNoteOffersOffset: the note is the only place an
// agent is told what to do next, so it has to name the argument that works.
func TestRecallSearchTruncatedNoteOffersOffset(t *testing.T) {
	setupTestDB(t)
	seedNumbered(t, "long", 30)

	res, err := RecallSearch(RecallSearchOptions{Query: "message", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatal("truncated = false while withholding 25 matches")
	}
	if !strings.Contains(res.Note, "offset=5") {
		t.Errorf("note %q does not tell the agent how to reach the remaining matches", res.Note)
	}
}

// TestRecallSearchPagingReachesTheLastMatch walks the whole result set and
// requires it to be covered exactly once -- no gaps, no repeats.
func TestRecallSearchPagingReachesTheLastMatch(t *testing.T) {
	setupTestDB(t)
	const n = 25
	seedNumbered(t, "long", n)

	seen := map[string]int{}
	for offset := 0; offset < n; offset += 5 {
		res, err := RecallSearch(RecallSearchOptions{Query: "message", Limit: 5, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		if res.Total != n {
			t.Fatalf("total = %d at offset %d, want a stable %d", res.Total, offset, n)
		}
		for _, h := range res.Hits {
			seen[h.At+h.Snippet]++
		}
	}
	if len(seen) != n {
		t.Fatalf("paging covered %d distinct matches, want all %d", len(seen), n)
	}
	for k, c := range seen {
		if c != 1 {
			t.Errorf("match %q returned %d times across pages", k, c)
		}
	}

	past, err := RecallSearch(RecallSearchOptions{Query: "message", Limit: 5, Offset: n})
	if err != nil {
		t.Fatal(err)
	}
	if len(past.Hits) != 0 {
		t.Errorf("offset past the end returned %d hits, want none", len(past.Hits))
	}
	if past.Truncated {
		t.Error("truncated = true at the end of the ranking")
	}
}
