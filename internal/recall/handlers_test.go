// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package recall

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

func setupDB(t *testing.T) {
	t.Helper()
	config.Workspace = "/tmp"
	f, err := os.CreateTemp("", "recall-*.db")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	t.Cleanup(func() {
		db.Close()
		os.Remove(path)
	})
	if err := db.Init(path); err != nil {
		t.Fatal(err)
	}
}

func seed(t *testing.T, id, label string, temporary bool, contents ...string) {
	t.Helper()
	if err := db.CreateSession(id, config.SessionConfig{
		Label: label, Workdir: "/tmp", Temporary: temporary,
	}); err != nil {
		t.Fatal(err)
	}
	for i, c := range contents {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if err := db.AddMessage(id, role, c); err != nil {
			t.Fatal(err)
		}
	}
}

func do(t *testing.T, h http.HandlerFunc, url string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, url, nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response %q is not JSON: %v", rec.Body.String(), err)
	}
	return rec.Code, body
}

func TestHandleList(t *testing.T) {
	setupDB(t)
	seed(t, "s1", "Pricing", false, "how do we price this", "by seat")
	seed(t, "tmp", "Throwaway", true, "secret", "shh")

	status, body := do(t, HandleList, "/api/recall/sessions")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, body)
	}
	sessions, ok := body["sessions"].([]any)
	if !ok {
		t.Fatalf("no sessions array: %v", body)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1 (the temporary one must be excluded)", len(sessions))
	}
	if sessions[0].(map[string]any)["session_id"] != "s1" {
		t.Errorf("wrong session: %v", sessions[0])
	}
}

// TestHandleListEmptyIsArrayNotNull follows the repo's null-slice rule
// (AGENTS.md §6): an agent decoding `null` where it expects a list is a bug
// waiting to happen.
func TestHandleListEmptyIsArrayNotNull(t *testing.T) {
	setupDB(t)
	rec := httptest.NewRecorder()
	HandleList(rec, httptest.NewRequest(http.MethodGet, "/api/recall/sessions", nil))
	got := rec.Body.String()
	if !strings.Contains(got, `"sessions":[]`) {
		t.Errorf("empty list body = %q, want an empty array for sessions", got)
	}
	if strings.Contains(got, `"sessions":null`) {
		t.Errorf("empty list body = %q, want [] not null", got)
	}
}

// TestHandleListReportsTotal: `list` capped at 200 while the workspace held 215
// chats and said nothing about the 15 it dropped. The envelope now carries the
// full count so a caller can tell a complete answer from a partial one.
func TestHandleListReportsTotal(t *testing.T) {
	setupDB(t)
	for i := 0; i < 5; i++ {
		seed(t, "s"+strconv.Itoa(i), "Chat", false, "question", "answer")
	}

	status, body := do(t, HandleList, "/api/recall/sessions?limit=2")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, body)
	}
	if body["total"] != float64(5) {
		t.Errorf("total = %v, want 5", body["total"])
	}
	if body["returned"] != float64(2) {
		t.Errorf("returned = %v, want 2", body["returned"])
	}
	if body["truncated"] != true {
		t.Errorf("truncated = %v, want true", body["truncated"])
	}
	if body["note"] == nil {
		t.Error("no note explaining that 3 sessions were withheld")
	}
}

// TestHandleListOffsetIsForwarded proves the query parameter reaches the data
// layer -- a handler that accepts `offset` and ignores it is exactly the class
// of bug being fixed here.
func TestHandleListOffsetIsForwarded(t *testing.T) {
	setupDB(t)
	for i := 0; i < 5; i++ {
		seed(t, "s"+strconv.Itoa(i), "Chat", false, "question", "answer")
	}

	_, first := do(t, HandleList, "/api/recall/sessions?limit=2&offset=0")
	_, second := do(t, HandleList, "/api/recall/sessions?limit=2&offset=2")

	idOf := func(b map[string]any, i int) any {
		return b["sessions"].([]any)[i].(map[string]any)["session_id"]
	}
	if idOf(first, 0) == idOf(second, 0) {
		t.Fatalf("offset=2 returned the same first session %v -- offset is ignored", idOf(first, 0))
	}
	if second["offset"] != float64(2) {
		t.Errorf("offset = %v, want it echoed as 2", second["offset"])
	}
}

func TestHandleSearch(t *testing.T) {
	setupDB(t)
	seed(t, "s1", "Pricing", false, "the pricing tier debate", "noted")

	status, body := do(t, HandleSearch, "/api/recall/search?q=pricing")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, body)
	}
	hits := body["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	if body["total"] != float64(1) {
		t.Errorf("total = %v, want 1", body["total"])
	}
	if body["truncated"] != false {
		t.Errorf("truncated = %v, want false when every match was returned", body["truncated"])
	}
}

func TestHandleSearchRequiresQuery(t *testing.T) {
	setupDB(t)
	status, body := do(t, HandleSearch, "/api/recall/search")
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%v)", status, body)
	}
}

// TestHandleSearchOffsetIsForwarded: a truncated search is only actionable if
// the caller can page past it, so the parameter has to survive the handler.
func TestHandleSearchOffsetIsForwarded(t *testing.T) {
	setupDB(t)
	seed(t, "s1", "Pricing", false,
		"pricing one", "pricing two", "pricing three", "pricing four")

	_, first := do(t, HandleSearch, "/api/recall/search?q=pricing&limit=2&offset=0")
	_, second := do(t, HandleSearch, "/api/recall/search?q=pricing&limit=2&offset=2")

	snippetOf := func(b map[string]any, i int) any {
		return b["hits"].([]any)[i].(map[string]any)["snippet"]
	}
	if snippetOf(first, 0) == snippetOf(second, 0) {
		t.Fatalf("offset=2 returned the same first hit %v -- offset is ignored", snippetOf(first, 0))
	}
	if second["offset"] != float64(2) {
		t.Errorf("offset = %v, want it echoed as 2", second["offset"])
	}
	if first["total"] != float64(4) {
		t.Errorf("total = %v, want all 4 matches counted", first["total"])
	}
}

func TestHandleSearchRejectsBadRole(t *testing.T) {
	setupDB(t)
	status, _ := do(t, HandleSearch, "/api/recall/search?q=x&role=system")
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown role", status)
	}
}

// TestHandleSearchReportsMissingIndex: 503 with an explanation, never 200 with
// an empty list. "No results" and "search is broken" must not look the same.
func TestHandleSearchReportsMissingIndex(t *testing.T) {
	setupDB(t)
	seed(t, "s1", "Pricing", false, "the pricing tier debate", "noted")
	if _, err := db.DB.Exec(`DROP TABLE messages_fts`); err != nil {
		t.Fatal(err)
	}

	status, body := do(t, HandleSearch, "/api/recall/search?q=pricing")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (%v)", status, body)
	}
	if body["error"] == nil {
		t.Error("503 carried no explanation")
	}
}

func TestHandleRead(t *testing.T) {
	setupDB(t)
	seed(t, "s1", "Transcript", false, "first", "second")

	status, body := do(t, HandleRead, "/api/recall/read?session_id=s1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, body)
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if body["truncated"] != false {
		t.Errorf("truncated = %v, want false", body["truncated"])
	}
	if body["total_messages"] != float64(2) {
		t.Errorf("total_messages = %v, want 2", body["total_messages"])
	}
	if body["order"] != "oldest" {
		t.Errorf("order = %v, want the default %q echoed back", body["order"], "oldest")
	}
	if body["note"] != nil {
		t.Errorf("note = %v, want none when the whole chat was returned", body["note"])
	}
}

// TestHandleReadRejectsBadOrder: a typo like order=recent must not be silently
// downgraded to the default, which would hand back the oldest messages while
// the caller believes it asked for the newest -- the original failure exactly.
func TestHandleReadRejectsBadOrder(t *testing.T) {
	setupDB(t)
	seed(t, "s1", "Transcript", false, "first", "second")

	status, body := do(t, HandleRead, "/api/recall/read?session_id=s1&order=recent")
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown order (%v)", status, body)
	}
}

// TestHandleReadOrderNewestIsForwarded is the regression test for the reported
// bug: a long chat must be readable from its recent end over HTTP.
func TestHandleReadOrderNewestIsForwarded(t *testing.T) {
	setupDB(t)
	seed(t, "s1", "Transcript", false, "first", "second", "third", "fourth")

	status, body := do(t, HandleRead, "/api/recall/read?session_id=s1&order=newest&limit=2")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, body)
	}
	var got []string
	for _, m := range body["messages"].([]any) {
		got = append(got, m.(map[string]any)["content"].(string))
	}
	want := []string{"third", "fourth"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order=newest returned %v, want the last two in chronological order %v", got, want)
	}
	if body["order"] != "newest" {
		t.Errorf("order = %v, want it echoed as newest", body["order"])
	}
	if body["note"] == nil {
		t.Error("a partial transcript carried no note")
	}
}

// TestHandleReadOffsetIsForwarded guards the pagination parameter end to end.
func TestHandleReadOffsetIsForwarded(t *testing.T) {
	setupDB(t)
	seed(t, "s1", "Transcript", false, "first", "second", "third", "fourth")

	_, body := do(t, HandleRead, "/api/recall/read?session_id=s1&limit=2&offset=2")
	var got []string
	for _, m := range body["messages"].([]any) {
		got = append(got, m.(map[string]any)["content"].(string))
	}
	want := []string{"third", "fourth"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("offset=2 returned %v, want %v", got, want)
	}
	if body["offset"] != float64(2) {
		t.Errorf("offset = %v, want it echoed as 2", body["offset"])
	}
}

// TestHandleReadDateWindowIsForwarded: the user's actual question was "what
// happened in the last 3 days", so a date-bounded read has to work.
func TestHandleReadDateWindowIsForwarded(t *testing.T) {
	setupDB(t)
	seed(t, "s1", "Transcript", false, "first", "second")

	today := time.Now().Format("2006-01-02")
	_, in := do(t, HandleRead, "/api/recall/read?session_id=s1&from="+today+"&to="+today)
	if n := len(in["messages"].([]any)); n != 2 {
		t.Fatalf("today's window returned %d messages, want 2", n)
	}

	_, out := do(t, HandleRead, "/api/recall/read?session_id=s1&from=1999-01-01&to=1999-01-02")
	if n := len(out["messages"].([]any)); n != 0 {
		t.Fatalf("a window with no messages returned %d, want 0 -- from/to is ignored", n)
	}
	if out["total_messages"] != float64(2) {
		t.Errorf("total_messages = %v, want the whole chat's 2 even when the window is empty", out["total_messages"])
	}
}

func TestHandleReadRequiresSessionID(t *testing.T) {
	setupDB(t)
	status, _ := do(t, HandleRead, "/api/recall/read")
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
}

func TestHandleReadRefusesTemporaryAndMissingAlike(t *testing.T) {
	setupDB(t)
	seed(t, "tmp", "Throwaway", true, "secret", "shh")

	tempStatus, _ := do(t, HandleRead, "/api/recall/read?session_id=tmp")
	missStatus, _ := do(t, HandleRead, "/api/recall/read?session_id=nope")
	if tempStatus != http.StatusNotFound || missStatus != http.StatusNotFound {
		t.Fatalf("temporary = %d, missing = %d; both must be 404", tempStatus, missStatus)
	}
}

// --- notes mode ----------------------------------------------------------

// notes=none is the cheap sweep: the sessions still come back fully described,
// the note text does not.
func TestListNotesNoneDropsNoteText(t *testing.T) {
	setupDB(t)
	seed(t, "s-n", "Noted", false, "one", "two", "three", "four")
	if err := db.SaveSessionNote(db.SessionNote{
		SessionID: "s-n", Day: "2026-03-01", Note: "did a thing",
		SessionTitle: "Noted", PromptVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}

	var withNotes, without db.RecallListResult
	decodeList(t, "/api/recall/sessions", &withNotes)
	decodeList(t, "/api/recall/sessions?notes=none", &without)

	if len(digest(t, withNotes, "s-n").Notes) != 1 {
		t.Fatal("expected the default to attach notes")
	}
	d := digest(t, without, "s-n")
	if len(d.Notes) != 0 {
		t.Errorf("notes=none returned note text: %v", d.Notes)
	}
	if d.NotesCoverThrough != "2026-03-01" {
		t.Errorf("notes=none dropped the coverage marker: %q", d.NotesCoverThrough)
	}
	if d.Title == "" {
		t.Error("notes=none degraded the digest itself")
	}
}

// An unrecognised mode is rejected rather than silently treated as a default:
// a caller who typed notes=off must not be told, by a normal-looking reply,
// that it worked.
func TestListRejectsAnUnknownNotesMode(t *testing.T) {
	setupDB(t)
	req := httptest.NewRequest("GET", "/api/recall/sessions?notes=off", nil)
	w := httptest.NewRecorder()
	HandleList(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown notes mode, got %d: %s", w.Code, w.Body.String())
	}
}

func decodeList(t *testing.T, url string, into *db.RecallListResult) {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	HandleList(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", url, w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), into); err != nil {
		t.Fatal(err)
	}
}

func digest(t *testing.T, res db.RecallListResult, id string) db.RecallDigest {
	t.Helper()
	for _, d := range res.Sessions {
		if d.SessionID == id {
			return d
		}
	}
	t.Fatalf("session %s missing from list", id)
	return db.RecallDigest{}
}

// The JSON shape of `span` is the part the agent sees, so pin it here as well
// as in the db package: a field that is present but named differently, or
// nested differently, is invisible to the tool description that tells the
// agent to look at it.
func TestHandleSearchReportsTheSpanOfTheWholeMatchSet(t *testing.T) {
	setupDB(t)
	seed(t, "s1", "Pricing", false, "pricing alpha", "pricing beta", "pricing gamma")
	if _, err := db.DB.Exec(
		`UPDATE messages SET created_at = ? WHERE content = ?`,
		"2026-07-13 11:19:11", "pricing alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(
		`UPDATE messages SET created_at = ? WHERE content = ?`,
		"2026-08-01 09:00:00", "pricing beta"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(
		`UPDATE messages SET created_at = ? WHERE content = ?`,
		"2026-09-02 14:48:27", "pricing gamma"); err != nil {
		t.Fatal(err)
	}

	// limit=1: the caller holds one hit but must still learn the match set
	// runs to September.
	status, body := do(t, HandleSearch, "/api/recall/search?q=pricing&limit=1")
	if status != http.StatusOK {
		t.Fatalf("status %d: %v", status, body)
	}
	span, ok := body["span"].(map[string]any)
	if !ok {
		t.Fatalf("no span object in the reply: %v", body)
	}
	if span["oldest"] != "2026-07-13T11:19:11Z" {
		t.Errorf("span.oldest = %v, want the oldest match", span["oldest"])
	}
	if span["newest"] != "2026-09-02T14:48:27Z" {
		t.Errorf("span.newest = %v, want the newest match", span["newest"])
	}
}

// Zero matches, no range: the key must be absent rather than present-and-empty,
// which would read as "everything matched at the epoch".
func TestHandleSearchOmitsSpanWhenNothingMatched(t *testing.T) {
	setupDB(t)
	seed(t, "s1", "Pricing", false, "pricing alpha", "pricing beta")

	status, body := do(t, HandleSearch, "/api/recall/search?q=zebra")
	if status != http.StatusOK {
		t.Fatalf("status %d: %v", status, body)
	}
	if _, present := body["span"]; present {
		t.Errorf("span present on a zero-match reply: %v", body["span"])
	}
}
