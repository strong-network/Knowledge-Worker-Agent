// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"slices"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

func searchChats(t *testing.T, q string) ChatSearchResult {
	t.Helper()
	res, err := SearchChats(ChatSearchOptions{Query: q})
	if err != nil {
		t.Fatalf("search %q: %v", q, err)
	}
	return res
}

func chatIDs(res ChatSearchResult) []string {
	out := []string{}
	for _, c := range res.Chats {
		out = append(out, c.SessionID)
	}
	return out
}

// addMessageAt adds a message with a given time, as SQLite stores it (UTC).
func addMessageAt(t *testing.T, session, role, content, at string) {
	t.Helper()
	if _, err := DB.Exec(`INSERT INTO messages (session_id, role, content, created_at) VALUES (?,?,?,?)`,
		session, role, content, at); err != nil {
		t.Fatal(err)
	}
}

func TestSearchChatsFindsTheStartOfWordsAndTypos(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "k8s", "Cluster notes", "How do I upgrade our Kubernetes cluster?", "Drain the nodes first.")
	seedChat(t, "other", "Lunch", "Where shall we eat?", "The canteen.")

	for _, q := range []string{"kubernetes", "kuber", "kubernets", "Kubrenetes"} {
		res := searchChats(t, q)
		if !slices.Equal(chatIDs(res), []string{"k8s"}) {
			t.Errorf("%q found %v, want the Kubernetes chat only", q, chatIDs(res))
		}
	}
	if res := searchChats(t, "kuber"); !res.Chats[0].Exact {
		t.Error("the start of a word is an exact match")
	}
	res := searchChats(t, "kubernets")
	if res.Chats[0].Exact {
		t.Error("a match through a typo is not exact")
	}
	if !slices.Contains(res.Variants, "kubernetes") {
		t.Errorf("variants = %v, want kubernetes among them", res.Variants)
	}
	if want := []SearchTerm{{Text: "kubernets", Prefix: true}}; !slices.Equal(res.Terms, want) {
		t.Errorf("terms = %+v, want %+v", res.Terms, want)
	}
}

// Short words are matched as typed: almost every short word is a typo of
// another.
func TestSearchChatsAllowsNoTyposInShortWords(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "cat", "Pets", "my cat sleeps")
	if res := searchChats(t, "car"); len(res.Chats) != 0 || len(res.Variants) != 0 {
		t.Errorf("car found %v with variants %v", chatIDs(res), res.Variants)
	}
}

func TestSearchChatsRanksExactAboveTypos(t *testing.T) {
	setupTestDB(t)
	// The typo chat mentions its word more often, so bm25 alone would rank it first.
	seedChat(t, "typo", "B", "deplay deplay deplay deplay")
	seedChat(t, "exact", "A", "We deploy on Fridays, with a long explanation of how and why that works for us.")
	res := searchChats(t, "deploy")
	if !slices.Equal(chatIDs(res), []string{"exact", "typo"}) {
		t.Fatalf("order = %v, want exact before typo", chatIDs(res))
	}
	if !res.Chats[0].Exact || res.Chats[1].Exact {
		t.Errorf("exact flags = %v, %v", res.Chats[0].Exact, res.Chats[1].Exact)
	}
}

func TestSearchChatsReturnsOneResultPerChatWithItsBestMessage(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "s1", "Budget",
		"Can you draft the invoice?",
		"Here is the invoice draft.",
		"Thanks, now send the invoice to finance, invoice number 42, as an invoice.",
		"Done.")
	res := searchChats(t, "invoice")
	if len(res.Chats) != 1 || res.Total != 1 {
		t.Fatalf("got %d chats (total %d), want 1", len(res.Chats), res.Total)
	}
	hit := res.Chats[0]
	if hit.Matches != 3 {
		t.Errorf("matches = %d, want 3", hit.Matches)
	}
	if hit.Message == nil {
		t.Fatal("no message")
	}
	if !strings.Contains(strings.ToLower(hit.Message.Snippet), "invoice") {
		t.Errorf("snippet %q doesn't show the match", hit.Message.Snippet)
	}
	if hit.Message.Role != "user" || hit.Message.CreatedAt == "" {
		t.Errorf("message = %+v", hit.Message)
	}
	// The id is the one the chat's history gives the message.
	var found bool
	for _, m := range GetMessages("s1") {
		if m.ID == hit.Message.ID {
			found = strings.Contains(m.Content, "number 42")
		}
	}
	if !found {
		t.Errorf("message id %d isn't the best match in the history", hit.Message.ID)
	}
}

func TestSearchChatsIncludesTemporaryChats(t *testing.T) {
	setupTestDB(t)
	seedTemporaryChat(t, "tmp", "Scratch", "a throwaway question about zeppelins")
	res := searchChats(t, "zeppelins")
	if len(res.Chats) != 1 || !res.Chats[0].Temporary {
		t.Fatalf("got %+v, want the temporary chat, marked", res.Chats)
	}
}

func TestSearchChatsMatchesTitles(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "content", "Misc", "the quarterly numbers look fine")
	seedChat(t, "title", "Quarterly planning", "Let's start with headcount.", "Sure.")
	seedChat(t, "empty", "Quarterly review")

	res := searchChats(t, "quarterly")
	if !slices.Equal(chatIDs(res), []string{"title", "content"}) {
		t.Fatalf("order = %v, want the title match first and no empty chat", chatIDs(res))
	}
	hit := res.Chats[0]
	if !hit.TitleMatch || hit.Message != nil || hit.Excerpt != "Let's start with headcount." {
		t.Errorf("title-only hit = %+v", hit)
	}
	if res := searchChats(t, "quartrly"); len(res.Chats) == 0 || res.Chats[0].SessionID != "title" || !res.Chats[0].TitleMatch {
		t.Errorf("a typo in a title word: %+v", res.Chats)
	}
}

func TestSearchChatsKeepsToTheTimeRange(t *testing.T) {
	setupTestDB(t)
	CreateSession("s", config.SessionConfig{Label: "Trips", Workdir: "/tmp"})
	addMessageAt(t, "s", "user", "plan the Lisbon trip", "2026-09-01 10:00:00")
	addMessageAt(t, "s", "assistant", "Lisbon in October is mild", "2026-09-09 22:30:00")

	cases := []struct {
		from, to string
		want     int
	}{
		{"", "", 2},
		{"2026-09-02", "", 1},
		{"", "2026-09-08", 1},
		{"2026-09-09", "2026-09-09", 1}, // the whole of the end day
		{"2026-09-10T00:00:00Z", "", 0},
	}
	for _, c := range cases {
		res, err := SearchChats(ChatSearchOptions{Query: "lisbon", From: c.from, To: c.to})
		if err != nil {
			t.Fatal(err)
		}
		got := 0
		if len(res.Chats) == 1 {
			got = res.Chats[0].Matches
		}
		if got != c.want {
			t.Errorf("from %q to %q: %d matches, want %d", c.from, c.to, got, c.want)
		}
	}
}

func TestSearchChatsMatchesQuotedPhrasesExactly(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "together", "A", "I bought a red apple")
	seedChat(t, "apart", "B", "the apple was red")
	seedChat(t, "longer", "C", "a redder applesauce")

	if res := searchChats(t, `"red apple"`); !slices.Equal(chatIDs(res), []string{"together"}) {
		t.Errorf("phrase found %v", chatIDs(res))
	}
	if res := searchChats(t, `"app"`); len(res.Chats) != 0 {
		t.Errorf("a quoted word matched the start of a longer one: %v", chatIDs(res))
	}
}

func TestSearchChatsFallsBackToAnyWord(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "a", "A", "notes about giraffes")
	seedChat(t, "b", "B", "notes about penguins")

	res := searchChats(t, "giraffes penguins")
	if res.Match != matchAny || len(res.Chats) != 2 {
		t.Errorf("match = %q with %v, want any with both chats", res.Match, chatIDs(res))
	}
	if res := searchChats(t, "notes giraffes"); res.Match != matchAll || !slices.Equal(chatIDs(res), []string{"a"}) {
		t.Errorf("match = %q with %v, want all with one chat", res.Match, chatIDs(res))
	}
}

func TestSearchChatsPages(t *testing.T) {
	setupTestDB(t)
	for _, id := range []string{"c1", "c2", "c3"} {
		seedChat(t, id, id, "shared topic: marmalade")
	}
	res, err := SearchChats(ChatSearchOptions{Query: "marmalade", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	next, err := SearchChats(ChatSearchOptions{Query: "marmalade", Limit: 2, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	all := append(chatIDs(res), chatIDs(next)...)
	slices.Sort(all)
	if res.Total != 3 || next.Total != 3 || !slices.Equal(all, []string{"c1", "c2", "c3"}) {
		t.Errorf("pages %v + %v (total %d)", chatIDs(res), chatIDs(next), res.Total)
	}
}

func TestSearchChatsTreatsOperatorsAsText(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "s", "S", "use foo:bar and -baz")
	for _, q := range []string{`foo:bar`, `-baz`, `"`, `NEAR(foo bar)`, `foo* OR "`, `)(`} {
		if _, err := SearchChats(ChatSearchOptions{Query: q}); err != nil {
			t.Errorf("%q: %v", q, err)
		}
	}
	if res := searchChats(t, "foo:bar"); len(res.Chats) != 1 {
		t.Errorf("foo:bar found %v", chatIDs(res))
	}
	if res := searchChats(t, "  "); len(res.Chats) != 0 || res.Total != 0 {
		t.Errorf("a blank query found %v", chatIDs(res))
	}
}

// Typo variants come from a cached vocabulary, which must see new messages.
func TestSearchChatsSeesNewWordsForTypos(t *testing.T) {
	setupTestDB(t)
	seedChat(t, "s", "S", "first message")
	searchChats(t, "sandwiches")
	AddMessage("s", "user", "we ordered sandwiches")
	if res := searchChats(t, "sandwhiches"); len(res.Chats) != 1 {
		t.Errorf("a typo of a new word found %v (variants %v)", chatIDs(res), res.Variants)
	}
}

func TestEditDistance(t *testing.T) {
	cases := []struct {
		a, b string
		max  int
		want int
	}{
		{"deploy", "deploy", 2, 0},
		{"deploy", "deplyo", 2, 1}, // swapped neighbours are one edit
		{"kubernets", "kubernetes", 2, 1},
		{"cat", "dog", 1, 2}, // gives up at max+1
		{"abc", "abcdef", 1, 2},
	}
	for _, c := range cases {
		if got := editDistance([]rune(c.a), []rune(c.b), c.max); got != c.want {
			t.Errorf("editDistance(%q, %q, %d) = %d, want %d", c.a, c.b, c.max, got, c.want)
		}
	}
}

func TestPlainTextStripsMarkdown(t *testing.T) {
	in := "## Plan\n\n- **Step one:** see [the guide](https://example.com)\n- `make test` | done"
	if got, want := plainText(in), "Plan Step one: see the guide make test done"; got != want {
		t.Errorf("plainText = %q, want %q", got, want)
	}
}
