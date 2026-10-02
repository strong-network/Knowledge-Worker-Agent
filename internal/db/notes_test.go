// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

// seedDay adds messages to a session and backdates them onto a specific day, so
// the day-boundary rules can be exercised without waiting for midnight.
func seedDay(t *testing.T, sessionID, day string, contents ...string) {
	t.Helper()
	first := lastMessageID(t)
	for i, c := range contents {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if err := AddMessage(sessionID, role, c); err != nil {
			t.Fatalf("add message to %s: %v", sessionID, err)
		}
	}
	if _, err := DB.Exec(
		`UPDATE messages SET created_at = ? || ' 12:00:00' WHERE id > ? AND session_id = ?`,
		day, first, sessionID,
	); err != nil {
		t.Fatalf("backdate %s to %s: %v", sessionID, day, err)
	}
}

func lastMessageID(t *testing.T) int64 {
	t.Helper()
	var id int64
	if err := DB.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM messages`).Scan(&id); err != nil {
		t.Fatalf("last message id: %v", err)
	}
	return id
}

func mustSession(t *testing.T, id, label string) {
	t.Helper()
	if err := CreateSession(id, config.SessionConfig{Label: label, Workdir: "/tmp"}); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
}

// filler produces enough messages to clear noteMinSessionMessages.
func filler(n int, prefix string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = prefix + " message " + string(rune('a'+i%26))
	}
	return out
}

func daysOf(cs []NoteCandidate) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.SessionID+"/"+c.Day)
	}
	return out
}

// --- the backlog --------------------------------------------------------

// The rule that makes append-only notes safe: a note for the day in progress
// would permanently record only the part of the day that had happened when it
// ran, and nothing would ever rewrite it.
func TestPendingNoteDaysNeverOffersTheDayInProgress(t *testing.T) {
	setupTestDB(t)
	mustSession(t, "s-open", "Open chat")
	seedDay(t, "s-open", "2026-03-01", filler(25, "yesterday")...)
	seedDay(t, "s-open", "2026-03-02", filler(25, "today")...)

	got, err := PendingNoteDays("2026-03-02", 1, 50)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	days := daysOf(got)
	if !contains(days, "s-open/2026-03-01") {
		t.Errorf("closed day should be offered, got %v", days)
	}
	if contains(days, "s-open/2026-03-02") {
		t.Errorf("the day in progress must never be offered, got %v", days)
	}
}

// A short single-day chat is already described by its free digest (title plus
// opening message). Spending a model call there buys nothing.
func TestPendingNoteDaysSkipsShortSingleDayChats(t *testing.T) {
	setupTestDB(t)
	mustSession(t, "s-short", "Quick question")
	seedDay(t, "s-short", "2026-03-01", "hello", "hi", "thanks", "bye")

	got, err := PendingNoteDays("2026-03-05", 1, 50)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if contains(daysOf(got), "s-short/2026-03-01") {
		t.Errorf("short single-day chat should not be offered, got %v", daysOf(got))
	}
}

// The population is the one where the digest actually breaks down: chats that
// ran long, or ran across more than one day.
func TestPendingNoteDaysIncludesLongAndMultiDayChats(t *testing.T) {
	setupTestDB(t)
	mustSession(t, "s-long", "Long chat")
	seedDay(t, "s-long", "2026-03-01", filler(22, "long")...)

	// Short per day, but spread across two days -- the case where the opening
	// message stops predicting what the chat became.
	mustSession(t, "s-spread", "Spread chat")
	seedDay(t, "s-spread", "2026-03-01", "day one a", "day one b")
	seedDay(t, "s-spread", "2026-03-02", "day two a", "day two b")

	got, err := PendingNoteDays("2026-03-05", 1, 50)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	days := daysOf(got)
	for _, want := range []string{"s-long/2026-03-01", "s-spread/2026-03-01", "s-spread/2026-03-02"} {
		if !contains(days, want) {
			t.Errorf("expected %s in backlog, got %v", want, days)
		}
	}
}

// A day with a single stray message is not a day's work.
func TestPendingNoteDaysSkipsDaysBelowTheMessageFloor(t *testing.T) {
	setupTestDB(t)
	mustSession(t, "s-stray", "Stray")
	seedDay(t, "s-stray", "2026-03-01", filler(25, "real")...)
	seedDay(t, "s-stray", "2026-03-02", "one stray message")

	got, err := PendingNoteDays("2026-03-05", 1, 50)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if contains(daysOf(got), "s-stray/2026-03-02") {
		t.Errorf("single-message day should not be offered, got %v", daysOf(got))
	}
}

func TestPendingNoteDaysExcludesTemporaryChats(t *testing.T) {
	setupTestDB(t)
	if err := CreateSession("s-temp", config.SessionConfig{
		Label: "Temp", Workdir: "/tmp", Temporary: true,
	}); err != nil {
		t.Fatalf("create temp: %v", err)
	}
	seedDay(t, "s-temp", "2026-03-01", filler(25, "temp")...)

	got, err := PendingNoteDays("2026-03-05", 1, 50)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if contains(daysOf(got), "s-temp/2026-03-01") {
		t.Errorf("temporary chats must never be summarised, got %v", daysOf(got))
	}
}

// Once a day has a note it leaves the backlog, so repeated passes cost nothing.
func TestPendingNoteDaysDropsDaysThatAlreadyHaveANote(t *testing.T) {
	setupTestDB(t)
	mustSession(t, "s-done", "Done")
	seedDay(t, "s-done", "2026-03-01", filler(25, "done")...)

	before, _ := PendingNoteDays("2026-03-05", 1, 50)
	if !contains(daysOf(before), "s-done/2026-03-01") {
		t.Fatalf("setup: expected the day in the backlog, got %v", daysOf(before))
	}

	if err := SaveSessionNote(SessionNote{
		SessionID: "s-done", Day: "2026-03-01", Note: "did things", PromptVersion: 1,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	after, _ := PendingNoteDays("2026-03-05", 1, 50)
	if contains(daysOf(after), "s-done/2026-03-01") {
		t.Errorf("a noted day must leave the backlog, got %v", daysOf(after))
	}
}

// Raising the prompt version puts already-noted days back in the backlog. This
// is the mechanism that makes the instruction improvable at all: notes are
// never edited in place, so without a re-offer every note ever written would be
// permanent.
func TestPendingNoteDaysReoffersDaysNotedByAnOlderPrompt(t *testing.T) {
	setupTestDB(t)
	mustSession(t, "s-old-prompt", "Old prompt")
	seedDay(t, "s-old-prompt", "2026-03-01", filler(25, "work")...)
	if err := SaveSessionNote(SessionNote{
		SessionID: "s-old-prompt", Day: "2026-03-01", Note: "a v1 summary", PromptVersion: 1,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Still current at v1 -- nothing to do, and no model call.
	same, _ := PendingNoteDays("2026-03-05", 1, 50)
	if contains(daysOf(same), "s-old-prompt/2026-03-01") {
		t.Errorf("a day noted at the current version must stay out of the backlog, got %v", daysOf(same))
	}

	// The generator now writes v2, so the v1 note is stale and comes back.
	upgraded, _ := PendingNoteDays("2026-03-05", 2, 50)
	if !contains(daysOf(upgraded), "s-old-prompt/2026-03-01") {
		t.Errorf("a day noted by an older prompt must be re-offered, got %v", daysOf(upgraded))
	}
}

// A note whose session has been deleted is irreplaceable: the messages it was
// written from are gone. Re-offering it could only ever destroy it, so a
// version bump must leave orphaned notes alone.
func TestPendingNoteDaysNeverReoffersOrphanedNotes(t *testing.T) {
	setupTestDB(t)
	mustSession(t, "s-orphan-regen", "Doomed")
	seedDay(t, "s-orphan-regen", "2026-03-01", filler(25, "work")...)
	if err := SaveSessionNote(SessionNote{
		SessionID: "s-orphan-regen", Day: "2026-03-01", Note: "a v1 summary", PromptVersion: 1,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := MarkNotesOrphaned("s-orphan-regen"); err != nil {
		t.Fatalf("orphan: %v", err)
	}
	if err := DeleteSession("s-orphan-regen"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	got, _ := PendingNoteDays("2026-03-05", 99, 50)
	if contains(daysOf(got), "s-orphan-regen/2026-03-01") {
		t.Errorf("an orphaned note must never be re-offered; there is nothing to regenerate from, got %v", daysOf(got))
	}

	// And it is still there.
	notes, err := NotesForSessions([]string{"s-orphan-regen"})
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(notes["s-orphan-regen"]) != 1 {
		t.Fatalf("the orphaned note was lost: %v", notes["s-orphan-regen"])
	}
}

// A first-ever pass on a long-lived workspace must not try to summarise months
// of history at once, and must start at the oldest end so the backlog drains.
// Recency is the whole point of the ordering: the days a future session is most
// likely to ask about are the ones that just closed, so those are summarised
// first and a slow backfill degrades by leaving the distant past unsummarised
// rather than the last week.
func TestPendingNoteDaysIsCappedAndNewestFirst(t *testing.T) {
	setupTestDB(t)
	mustSession(t, "s-many", "Many days")
	for _, d := range []string{"2026-03-04", "2026-03-01", "2026-03-03", "2026-03-02"} {
		seedDay(t, "s-many", d, filler(6, "d"+d)...)
	}

	got, err := PendingNoteDays("2026-03-05", 1, 2)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("limit not honoured: got %d days %v", len(got), daysOf(got))
	}
	if got[0].Day != "2026-03-04" || got[1].Day != "2026-03-03" {
		t.Errorf("backlog must drain newest-first, got %v", daysOf(got))
	}
}

// A capped pass must not strand the newest day behind a wall of old ones. With
// a backlog larger than one batch this is the difference between yesterday
// being summarised in the first pass and in the last.
func TestPendingNoteDaysOffersTheNewestDayInTheFirstBatch(t *testing.T) {
	setupTestDB(t)
	mustSession(t, "s-backlog", "Long history")
	days := []string{}
	for d := 1; d <= 20; d++ {
		day := fmt.Sprintf("2026-03-%02d", d)
		days = append(days, day)
		seedDay(t, "s-backlog", day, filler(6, "b"+day)...)
	}
	newest := days[len(days)-1]

	got, err := PendingNoteDays("2026-04-01", 1, 5)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no candidates")
	}
	if got[0].Day != newest {
		t.Errorf("first candidate = %q, want the newest day %q; got %v",
			got[0].Day, newest, daysOf(got))
	}
}

// The candidate carries the numbers the note row is stamped with. If these are
// wrong the note claims to cover a range it does not.
func TestPendingNoteDaysReportsCoverage(t *testing.T) {
	setupTestDB(t)
	mustSession(t, "s-cover", "Coverage")
	seedDay(t, "s-cover", "2026-03-01", filler(25, "cover")...)

	got, err := PendingNoteDays("2026-03-05", 1, 50)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	var c *NoteCandidate
	for i := range got {
		if got[i].SessionID == "s-cover" {
			c = &got[i]
		}
	}
	if c == nil {
		t.Fatalf("session missing from backlog: %v", daysOf(got))
	}
	if c.MsgCount != 25 {
		t.Errorf("msg count = %d, want 25", c.MsgCount)
	}
	if c.CoversFrom <= 0 || c.CoversTo < c.CoversFrom {
		t.Errorf("coverage range is not sane: from=%d to=%d", c.CoversFrom, c.CoversTo)
	}
	if c.Title != "Coverage" {
		t.Errorf("title = %q, want %q -- the note snapshots it and the session may later be deleted", c.Title, "Coverage")
	}
}

// --- day messages -------------------------------------------------------

func TestDayMessagesReturnsOnlyThatDayInOrder(t *testing.T) {
	setupTestDB(t)
	mustSession(t, "s-day", "Day")
	seedDay(t, "s-day", "2026-03-01", "first", "second", "third")
	seedDay(t, "s-day", "2026-03-02", "other day")

	got, err := DayMessages("s-day", "2026-03-01")
	if err != nil {
		t.Fatalf("day messages: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d messages, want 3", len(got))
	}
	if got[0].Content != "first" || got[2].Content != "third" {
		t.Errorf("messages out of order: %v", got)
	}
}

// --- storing ------------------------------------------------------------

// Notes are append-only *within a prompt version*. A second write for the same
// day at the same version must not overwrite the first, which is what makes the
// job safe to retry and safe to run twice.
func TestSaveSessionNoteIsAppendOnly(t *testing.T) {
	setupTestDB(t)
	mustSession(t, "s-append", "Append")
	if err := SaveSessionNote(SessionNote{
		SessionID: "s-append", Day: "2026-03-01", Note: "the original", PromptVersion: 1,
	}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := SaveSessionNote(SessionNote{
		SessionID: "s-append", Day: "2026-03-01", Note: "a replacement", PromptVersion: 1,
	}); err != nil {
		t.Fatalf("second save: %v", err)
	}

	notes, err := NotesForSessions([]string{"s-append"})
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(notes["s-append"]) != 1 {
		t.Fatalf("got %d notes, want 1", len(notes["s-append"]))
	}
	if notes["s-append"][0].Note != "the original" {
		t.Errorf("note = %q, want the original to survive", notes["s-append"][0].Note)
	}
}

// The one exception to append-only: a newer prompt version replaces an older
// note. Without this the instruction could never be improved -- every note ever
// written would be permanent, and the only remedy would be editing rows by hand.
func TestSaveSessionNoteUpgradesToANewerPromptVersion(t *testing.T) {
	setupTestDB(t)
	mustSession(t, "s-upgrade", "Upgrade")
	if err := SaveSessionNote(SessionNote{
		SessionID: "s-upgrade", Day: "2026-03-01", Note: "the old summary",
		Model: "old-model", MsgCount: 4, PromptVersion: 1,
	}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := SaveSessionNote(SessionNote{
		SessionID: "s-upgrade", Day: "2026-03-01", Note: "the better summary",
		Model: "new-model", MsgCount: 9, PromptVersion: 2,
	}); err != nil {
		t.Fatalf("upgrade save: %v", err)
	}

	notes, err := NotesForSessions([]string{"s-upgrade"})
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(notes["s-upgrade"]) != 1 {
		t.Fatalf("got %d notes, want the upgrade to replace rather than add", len(notes["s-upgrade"]))
	}
	if notes["s-upgrade"][0].Note != "the better summary" {
		t.Errorf("note = %q, want the newer version to win", notes["s-upgrade"][0].Note)
	}

	// The whole row is refreshed, not just the text -- `model` and `msg_count`
	// describe how the note was produced, so leaving them behind would
	// misreport the note's own provenance.
	var model string
	var count, version int
	if err := DB.QueryRow(
		`SELECT model, msg_count, prompt_version FROM session_notes
		 WHERE session_id = ? AND day = ?`, "s-upgrade", "2026-03-01",
	).Scan(&model, &count, &version); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if model != "new-model" || count != 9 || version != 2 {
		t.Errorf("row = (%s, %d, v%d), want (new-model, 9, v2)", model, count, version)
	}
}

// Replacement is strictly one-directional. A late retry carrying an OLDER
// version must not undo an upgrade that already landed -- otherwise a slow pass
// finishing after a fast one would silently downgrade the note.
func TestSaveSessionNoteRefusesAnOlderPromptVersion(t *testing.T) {
	setupTestDB(t)
	mustSession(t, "s-stale", "Stale")
	if err := SaveSessionNote(SessionNote{
		SessionID: "s-stale", Day: "2026-03-01", Note: "the v2 summary", PromptVersion: 2,
	}); err != nil {
		t.Fatalf("save v2: %v", err)
	}
	if err := SaveSessionNote(SessionNote{
		SessionID: "s-stale", Day: "2026-03-01", Note: "a stale v1 summary", PromptVersion: 1,
	}); err != nil {
		t.Fatalf("save stale v1: %v", err)
	}

	notes, err := NotesForSessions([]string{"s-stale"})
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if notes["s-stale"][0].Note != "the v2 summary" {
		t.Errorf("note = %q, want the newer note to survive a stale write", notes["s-stale"][0].Note)
	}
}

// An upgrade must not resurrect an orphaned note as un-orphaned. orphaned_at
// describes what happened to the session, not to the text.
func TestSaveSessionNoteKeepsOrphanedMarkOnUpgrade(t *testing.T) {
	setupTestDB(t)
	mustSession(t, "s-orph-up", "Orphan upgrade")
	if err := SaveSessionNote(SessionNote{
		SessionID: "s-orph-up", Day: "2026-03-01", Note: "v1", PromptVersion: 1,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := MarkNotesOrphaned("s-orph-up"); err != nil {
		t.Fatalf("orphan: %v", err)
	}
	if err := SaveSessionNote(SessionNote{
		SessionID: "s-orph-up", Day: "2026-03-01", Note: "v2", PromptVersion: 2,
	}); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	var orphaned sql.NullString
	if err := DB.QueryRow(
		`SELECT orphaned_at FROM session_notes WHERE session_id = ? AND day = ?`,
		"s-orph-up", "2026-03-01").Scan(&orphaned); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if !orphaned.Valid || orphaned.String == "" {
		t.Error("upgrading the text cleared orphaned_at; the session is still deleted")
	}
}

func TestSaveSessionNoteRequiresSessionAndDay(t *testing.T) {
	setupTestDB(t)
	if err := SaveSessionNote(SessionNote{Day: "2026-03-01", Note: "x"}); err == nil {
		t.Error("expected an error for a note with no session id")
	}
	if err := SaveSessionNote(SessionNote{SessionID: "s", Note: "x"}); err == nil {
		t.Error("expected an error for a note with no day")
	}
}

// The prompt version is stamped by the caller that owns the prompt, so an
// improved prompt can be rolled out selectively.
func TestSaveSessionNoteStoresTheCallersPromptVersion(t *testing.T) {
	setupTestDB(t)
	mustSession(t, "s-ver", "Version")
	if err := SaveSessionNote(SessionNote{
		SessionID: "s-ver", Day: "2026-03-01", Note: "n", PromptVersion: 7,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	var v int
	if err := DB.QueryRow(
		`SELECT prompt_version FROM session_notes WHERE session_id = ?`, "s-ver",
	).Scan(&v); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if v != 7 {
		t.Errorf("prompt_version = %d, want 7", v)
	}
}

func TestNotesForSessionsGroupsBySessionAndOrdersByDay(t *testing.T) {
	setupTestDB(t)
	mustSession(t, "s-a", "A")
	mustSession(t, "s-b", "B")
	for _, d := range []string{"2026-03-03", "2026-03-01", "2026-03-02"} {
		if err := SaveSessionNote(SessionNote{
			SessionID: "s-a", Day: d, Note: "note for " + d, PromptVersion: 1,
		}); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	if err := SaveSessionNote(SessionNote{
		SessionID: "s-b", Day: "2026-03-01", Note: "b note", PromptVersion: 1,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := NotesForSessions([]string{"s-a", "s-b", "s-missing"})
	if err != nil {
		t.Fatalf("notes: %v", err)
	}
	if len(got["s-a"]) != 3 {
		t.Fatalf("s-a got %d notes, want 3", len(got["s-a"]))
	}
	if got["s-a"][0].Day != "2026-03-01" || got["s-a"][2].Day != "2026-03-03" {
		t.Errorf("notes must be ordered by day, got %v", got["s-a"])
	}
	if len(got["s-b"]) != 1 {
		t.Errorf("s-b got %d notes, want 1", len(got["s-b"]))
	}
	if _, ok := got["s-missing"]; ok {
		t.Error("a session with no notes should be absent, not present and empty")
	}
}

func TestNotesForSessionsHandlesNoIDs(t *testing.T) {
	setupTestDB(t)
	got, err := NotesForSessions(nil)
	if err != nil {
		t.Fatalf("notes: %v", err)
	}
	if got == nil {
		t.Error("must return an empty map, not nil")
	}
}

// --- retention ----------------------------------------------------------

// A note outlives the chat it describes. Deleting the chat must leave the note
// readable but honestly marked, so nothing later claims it can show a
// conversation that is gone.
func TestNotesSurviveTheSessionAndAreMarkedOrphaned(t *testing.T) {
	setupTestDB(t)
	mustSession(t, "s-gone", "Doomed")
	seedDay(t, "s-gone", "2026-03-01", filler(25, "doomed")...)
	if err := SaveSessionNote(SessionNote{
		SessionID: "s-gone", Day: "2026-03-01", Note: "what happened",
		SessionTitle: "Doomed", PromptVersion: 1,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := MarkNotesOrphaned("s-gone"); err != nil {
		t.Fatalf("orphan: %v", err)
	}
	if err := DeleteSession("s-gone"); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	got, err := NotesForSessions([]string{"s-gone"})
	if err != nil {
		t.Fatalf("notes: %v", err)
	}
	if len(got["s-gone"]) != 1 {
		t.Fatalf("the note must outlive the session, got %d", len(got["s-gone"]))
	}
	n := got["s-gone"][0]
	if n.Note != "what happened" {
		t.Errorf("note text lost: %q", n.Note)
	}
	if n.OrphanedAt == "" {
		t.Error("a note whose chat is gone must be marked, or it claims more than it can show")
	}
}

// Marking twice must not move the timestamp -- the record is of when the chat
// went, not of the last time something asked.
//
// The first mark is planted at a known past time rather than taken from the
// clock: CURRENT_TIMESTAMP has one-second resolution, so two real marks in the
// same second look identical whether or not the `orphaned_at IS NULL` guard is
// there, and the test would pass with the guard removed.
func TestMarkNotesOrphanedIsStable(t *testing.T) {
	setupTestDB(t)
	mustSession(t, "s-twice", "Twice")
	if err := SaveSessionNote(SessionNote{
		SessionID: "s-twice", Day: "2026-03-01", Note: "n", PromptVersion: 1,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	const planted = "2020-01-01 00:00:00"
	if _, err := DB.Exec(
		`UPDATE session_notes SET orphaned_at = ? WHERE session_id = ?`, planted, "s-twice",
	); err != nil {
		t.Fatalf("plant orphaned_at: %v", err)
	}

	if err := MarkNotesOrphaned("s-twice"); err != nil {
		t.Fatalf("mark: %v", err)
	}

	got, _ := NotesForSessions([]string{"s-twice"})
	if got["s-twice"][0].OrphanedAt != planted {
		t.Errorf("orphaned_at moved on re-mark: %q -> %q; it records when the chat went, not when something last asked",
			planted, got["s-twice"][0].OrphanedAt)
	}
}

// The consent path: someone deleting a chat because of what is in it must be
// able to take the summary with it.
func TestDeleteSessionNotesRemovesThemAndReportsHowMany(t *testing.T) {
	setupTestDB(t)
	mustSession(t, "s-purge", "Purge")
	for _, d := range []string{"2026-03-01", "2026-03-02"} {
		if err := SaveSessionNote(SessionNote{
			SessionID: "s-purge", Day: d, Note: "sensitive", PromptVersion: 1,
		}); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	if got := CountSessionNotes("s-purge"); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}

	n, err := DeleteSessionNotes("s-purge")
	if err != nil {
		t.Fatalf("delete notes: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted %d, want 2 -- the confirmation reports this number", n)
	}
	if got := CountSessionNotes("s-purge"); got != 0 {
		t.Errorf("count after delete = %d, want 0", got)
	}
}

func TestCountSessionNotesIsZeroForUnknownSession(t *testing.T) {
	setupTestDB(t)
	if got := CountSessionNotes("never-existed"); got != 0 {
		t.Errorf("count = %d, want 0", got)
	}
}

// --- schema -------------------------------------------------------------

// The absence of a foreign key is deliberate: with one, deleting a chat would
// cascade away the record of what was done that month.
func TestSessionNotesHasNoForeignKeyToSessions(t *testing.T) {
	setupTestDB(t)
	var ddl string
	if err := DB.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='session_notes'`,
	).Scan(&ddl); err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if strings.Contains(strings.ToUpper(ddl), "FOREIGN KEY") {
		t.Errorf("session_notes must not reference sessions -- notes outlive the chat.\nDDL: %s", ddl)
	}
}
