// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

// Cross-session retrieval: the full-text index behind the `search` verb.
//
// This is the "derived + cheap" half of the two-kinds-of-derived-data rule in
// the spec: it is *disposable*. Nothing here is ever the system of record --
// every row can be rebuilt from `messages` in under a second on the measured
// corpus, so a schema change cannot corrupt anything that matters and the
// repair path is always "throw it away and rebuild".
//
// Design notes, each of which was verified by running it rather than assumed:
//
//   - **FTS5 works under modernc.org/sqlite.** CGO is forbidden here
//     (AGENTS.md §6), so this was the question that decided the whole retrieval
//     approach. MATCH, bm25 ranking via ORDER BY rank, and snippet() all work.
//
//   - **External-content table.** `content='messages'` means the index stores
//     only the inverted index, not a second copy of 5.8 MB of message text.
//
//   - **Triggers, not Go call sites, keep it in sync.** The decisive reason is
//     that messages are usually deleted *indirectly*: deleting a chat removes
//     its rows by ON DELETE CASCADE, and no Go code in this repo ever issues a
//     DELETE against `messages` for that path. A Go-side hook on AddMessage
//     would therefore have kept the index fresh on insert and left it
//     permanently stale on delete -- returning snippets of conversations the
//     user had deleted, which is the worst failure this feature could have.
//     Tested explicitly: SQLite *does* fire AFTER DELETE triggers for rows
//     removed by a foreign-key cascade. TestFTSDeletesOnSessionCascade pins it.
//
//   - **How the index is measured, and the trap in it.** On an external-content
//     table, `SELECT COUNT(*) FROM messages_fts` does NOT count indexed rows --
//     it proxies straight through to `messages`. Measured: after a cascade
//     delete with the delete trigger *removed*, COUNT(*) still reported 0 (a
//     clean index) while `MATCH` still returned the deleted row; and after
//     'delete-all' emptied the index completely, COUNT(*) still reported 1.
//     FTS5's own 'integrity-check' returned ok in both of those broken states
//     too, so it is not a safety net either. Anything that needs to know what
//     is actually *in* the index must therefore go through MATCH or the
//     fts5vocab table (ftsTerms below). A check written against COUNT(*) is
//     not merely useless, it is worse than nothing: it silently always passes.

import (
	"fmt"
	"log"
)

// ftsVersion is bumped whenever the index definition changes in a way that
// makes existing rows wrong (tokenizer, indexed columns, what gets indexed).
// A mismatch against the recorded value drops and rebuilds the index at boot.
// This is cheap precisely because the index is disposable.
const ftsVersion = 1

const ftsVersionKey = "fts_version"

// ftsSchema creates the index and the triggers that maintain it.
//
// The triggers use the FTS5 external-content idiom: a delete is recorded by
// inserting a 'delete' command row carrying the *old* text, because an
// external-content index cannot look up what it no longer has.
const ftsSchema = `
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
	content,
	content='messages',
	content_rowid='id'
);
CREATE TRIGGER IF NOT EXISTS messages_fts_ai AFTER INSERT ON messages BEGIN
	INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
END;
CREATE TRIGGER IF NOT EXISTS messages_fts_ad AFTER DELETE ON messages BEGIN
	INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.id, old.content);
END;
CREATE TRIGGER IF NOT EXISTS messages_fts_au AFTER UPDATE ON messages BEGIN
	INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.id, old.content);
	INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
END;
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts_v USING fts5vocab(messages_fts, 'row');
`

// ensureFTS creates the index if absent, rebuilds it when the version constant
// has moved, and otherwise sanity-checks that it is populated.
//
// Every failure is non-fatal and logged: a workspace must still boot and serve
// chat when the index is broken. The retrieval verbs degrade to reporting that
// search is unavailable -- they never try to repair it, because the MCP process
// that serves them has no write access (see the spec's reader/writer split).
func ensureFTS() error {
	if _, err := DB.Exec(ftsSchema); err != nil {
		return fmt.Errorf("create fts index: %w", err)
	}

	recorded, _ := metaGet(ftsVersionKey)
	want := fmt.Sprintf("%d", ftsVersion)
	if recorded != want {
		if err := RebuildFTS(); err != nil {
			return fmt.Errorf("rebuild fts index (version %q -> %q): %w", recorded, want, err)
		}
		if err := metaSet(ftsVersionKey, want); err != nil {
			return fmt.Errorf("record fts version: %w", err)
		}
		log.Printf("[fts] index rebuilt at version %s", want)
		return nil
	}

	// Sanity check the index population. This catches the one case the version
	// stamp cannot: an index that exists and is current, but is empty because it
	// was created on a database that already had messages in it (CREATE VIRTUAL
	// TABLE does not backfill, and the triggers only see future writes).
	//
	// Counted through fts5vocab, NOT `COUNT(*) FROM messages_fts` -- see the
	// measurement note in the file header. The obvious spelling of this check
	// silently always passes.
	var msgs int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&msgs); err != nil {
		return fmt.Errorf("count messages: %w", err)
	}
	terms, err := ftsTerms()
	if err != nil {
		return fmt.Errorf("count fts terms: %w", err)
	}
	if msgs > 0 && terms == 0 {
		log.Printf("[fts] index empty with %d messages; rebuilding", msgs)
		return RebuildFTS()
	}
	return nil
}

// ftsTerms reports how many distinct terms the index holds. Zero with messages
// present means the index is empty; it is the cheapest honest signal available.
func ftsTerms() (int, error) {
	var n int
	err := DB.QueryRow(`SELECT COUNT(*) FROM messages_fts_v`).Scan(&n)
	return n, err
}

// RebuildFTS reconstructs the index from `messages`, which is always the
// authority. Exported so a future repair endpoint or test can force it.
func RebuildFTS() error {
	_, err := DB.Exec(`INSERT INTO messages_fts(messages_fts) VALUES ('rebuild')`)
	return err
}

// FTSReady reports whether the index exists and is usable for querying.
//
// The retrieval endpoints call this so they can say "search is unavailable"
// rather than return zero hits, which would be indistinguishable from "nothing
// matched" -- the same report-what-is-knowable rule the spec applies to note
// coverage.
func FTSReady() bool {
	var n int
	err := DB.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'messages_fts'`,
	).Scan(&n)
	return err == nil && n > 0
}
