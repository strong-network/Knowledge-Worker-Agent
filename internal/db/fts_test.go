// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

// seedSession creates a session with a label and returns its id.
func seedSession(t *testing.T, id, label string) string {
	t.Helper()
	if err := CreateSession(id, config.SessionConfig{Label: label, Workdir: "/tmp"}); err != nil {
		t.Fatalf("create session %s: %v", id, err)
	}
	return id
}

// ftsMatches counts rows the INDEX returns for a term.
//
// Deliberately not `SELECT COUNT(*) FROM messages_fts`: on an external-content
// table that proxies the content table and reports a clean index even when the
// index is stale or empty (see the measurement note in fts.go). Every assertion
// here goes through MATCH or fts5vocab for that reason.
func ftsMatches(t *testing.T, term string) int {
	t.Helper()
	var n int
	err := DB.QueryRow(
		`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH ?`, `"`+term+`"`,
	).Scan(&n)
	if err != nil {
		t.Fatalf("match %q: %v", term, err)
	}
	return n
}

// ftsTermCount is the number of distinct terms in the index: 0 means empty.
func ftsTermCount(t *testing.T) int {
	t.Helper()
	n, err := ftsTerms()
	if err != nil {
		t.Fatalf("fts terms: %v", err)
	}
	return n
}

func ftsIntegrityOK(t *testing.T) error {
	t.Helper()
	_, err := DB.Exec(`INSERT INTO messages_fts(messages_fts) VALUES ('integrity-check')`)
	return err
}

// TestFTSIntegrityCheckIsNotEvidence records why no other test in this file
// leans on 'integrity-check'.
//
// It reports ok on an index that has been emptied out from under a populated
// content table -- a state where every search silently returns nothing. It
// checks internal b-tree consistency, not agreement with `messages`, so it can
// only ever catch corruption, never staleness. Kept as a smoke test with its
// limits stated, so nobody later mistakes a green integrity-check for proof
// that the index is correct.
func TestFTSIntegrityCheckIsNotEvidence(t *testing.T) {
	setupTestDB(t)
	seedSession(t, "s1", "Historic")
	if err := AddMessage("s1", "user", "archaeology of the old corpus"); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`INSERT INTO messages_fts(messages_fts) VALUES ('delete-all')`); err != nil {
		t.Fatal(err)
	}
	if got := ftsTermCount(t); got != 0 {
		t.Fatalf("index holds %d terms after delete-all, want 0", got)
	}
	if err := ftsIntegrityOK(t); err != nil {
		t.Fatalf("integrity-check errored: %v", err)
	}
	// The point: it passed, on an index that would answer every query wrongly.
}

func TestFTSCreatedByMigrate(t *testing.T) {
	setupTestDB(t)
	if !FTSReady() {
		t.Fatal("FTSReady() = false after Init; migrate() should have created messages_fts")
	}
	v, ok := metaGet(ftsVersionKey)
	if !ok {
		t.Fatal("fts version not recorded in app_meta")
	}
	if v != "1" {
		t.Fatalf("recorded fts version = %q, want \"1\"", v)
	}
}

func TestFTSIndexesInsertedMessages(t *testing.T) {
	setupTestDB(t)
	seedSession(t, "s1", "Pricing chat")
	if err := AddMessage("s1", "user", "what did we decide about the enterprise pricing tier"); err != nil {
		t.Fatal(err)
	}
	if n := ftsMatches(t, "pricing"); n != 1 {
		t.Fatalf("matched %d rows, want 1 — the AFTER INSERT trigger is not indexing", n)
	}
}

// TestFTSDeletesOnSessionCascade is the test the whole trigger-based design
// rests on, and the reason fts.go does not hook Go call sites instead.
//
// Deleting a chat removes its messages by ON DELETE CASCADE; no Go code issues
// a DELETE against `messages` on that path. If SQLite did not fire AFTER DELETE
// triggers for cascaded rows, the index would keep serving snippets of
// conversations the user had deleted.
//
// The assertion is MATCH-based on purpose. Negative control, run before this
// was written: with the messages_fts_ad trigger removed, MATCH still returns 1
// after the cascade — while `COUNT(*) FROM messages_fts` returns 0 and
// 'integrity-check' returns ok. Only the MATCH assertion can fail here, so only
// the MATCH assertion is evidence.
func TestFTSDeletesOnSessionCascade(t *testing.T) {
	setupTestDB(t)
	seedSession(t, "s1", "Doomed chat")
	if err := AddMessage("s1", "user", "supercalifragilistic secret"); err != nil {
		t.Fatal(err)
	}
	if got := ftsMatches(t, "supercalifragilistic"); got != 1 {
		t.Fatalf("indexed %d rows before delete, want 1", got)
	}

	// Delete the SESSION, not the messages: the cascade is the thing under test.
	if err := DeleteSession("s1"); err != nil {
		t.Fatal(err)
	}

	var msgs int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&msgs); err != nil {
		t.Fatal(err)
	}
	if msgs != 0 {
		t.Fatalf("cascade did not remove messages (%d left); the premise of this test is broken", msgs)
	}
	if got := ftsMatches(t, "supercalifragilistic"); got != 0 {
		t.Fatalf("index still matches %d rows after cascade delete — deleted chats would remain searchable", got)
	}
	if got := ftsTermCount(t); got != 0 {
		t.Fatalf("index still holds %d terms after deleting the only chat", got)
	}
}

func TestFTSUpdateReindexes(t *testing.T) {
	setupTestDB(t)
	seedSession(t, "s1", "Edited")
	if err := AddMessage("s1", "user", "original wording here"); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`UPDATE messages SET content = ? WHERE session_id = ?`,
		"replacement wording here", "s1"); err != nil {
		t.Fatal(err)
	}

	if n := ftsMatches(t, "original"); n != 0 {
		t.Errorf("stale term still matches after UPDATE (%d hits)", n)
	}
	if n := ftsMatches(t, "replacement"); n != 1 {
		t.Errorf("new term matches %d rows after UPDATE, want 1", n)
	}
}

// TestRebuildFTSBackfills covers the upgrade path: an existing database gains
// the index only at migrate time, and CREATE VIRTUAL TABLE does not backfill.
// Without the rebuild, every message written before the upgrade would be
// invisible to search forever.
func TestRebuildFTSBackfills(t *testing.T) {
	setupTestDB(t)
	seedSession(t, "s1", "Historic")
	if err := AddMessage("s1", "user", "archaeology of the old corpus"); err != nil {
		t.Fatal(err)
	}

	// Simulate a pre-index database: empty the index behind the triggers' back.
	if _, err := DB.Exec(`INSERT INTO messages_fts(messages_fts) VALUES ('delete-all')`); err != nil {
		t.Fatal(err)
	}
	if got := ftsTermCount(t); got != 0 {
		t.Fatalf("delete-all left %d terms; cannot simulate an unindexed database", got)
	}

	if err := RebuildFTS(); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if got := ftsMatches(t, "archaeology"); got != 1 {
		t.Fatalf("after rebuild the term matches %d rows, want 1", got)
	}
}

// TestEnsureFTSRepairsEmptyIndex pins the row-count sanity check: an index that
// exists and carries the current version stamp, but is empty while messages
// exist, must be rebuilt rather than left to return zero hits forever.
func TestEnsureFTSRepairsEmptyIndex(t *testing.T) {
	setupTestDB(t)
	seedSession(t, "s1", "Historic")
	if err := AddMessage("s1", "user", "archaeology of the old corpus"); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`INSERT INTO messages_fts(messages_fts) VALUES ('delete-all')`); err != nil {
		t.Fatal(err)
	}

	// Version stamp is already current, so only the population check can save it.
	if err := ensureFTS(); err != nil {
		t.Fatalf("ensureFTS: %v", err)
	}
	if got := ftsMatches(t, "archaeology"); got != 1 {
		t.Fatalf("ensureFTS left the term matching %d rows, want 1 — the empty-index check did not fire", got)
	}
}

// TestEnsureFTSRebuildsOnVersionBump pins the other repair trigger.
func TestEnsureFTSRebuildsOnVersionBump(t *testing.T) {
	setupTestDB(t)
	seedSession(t, "s1", "Historic")
	if err := AddMessage("s1", "user", "archaeology of the old corpus"); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`INSERT INTO messages_fts(messages_fts) VALUES ('delete-all')`); err != nil {
		t.Fatal(err)
	}
	if err := metaSet(ftsVersionKey, "0"); err != nil {
		t.Fatal(err)
	}

	if err := ensureFTS(); err != nil {
		t.Fatalf("ensureFTS: %v", err)
	}
	if got := ftsMatches(t, "archaeology"); got != 1 {
		t.Fatalf("version bump did not rebuild: term matches %d rows, want 1", got)
	}
	if v, _ := metaGet(ftsVersionKey); v != "1" {
		t.Fatalf("version stamp = %q after rebuild, want \"1\"", v)
	}
}
