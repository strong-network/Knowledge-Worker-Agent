// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

// Cross-session retrieval: how generated notes reach the three retrieval verbs.
//
// The spec's rule for every response is *report what is knowable, never imply
// more*. Two things follow, and both live here so no verb can forget them.
//
//   - A digest that carries notes must also say **how far the notes reach**. A
//     note list with no coverage marker reads as complete, so an agent seeing
//     notes for the 3rd and 4th cannot tell whether nothing happened on the 5th
//     or whether the 5th simply has not been summarised yet.
//   - A note can **outlive its chat** (see *Retention*). Anything built from a
//     note must therefore distinguish "I can show you the conversation" from
//     "I have only a note claiming this happened" -- which is what `source` is
//     for, and why `read` refuses rather than returning an empty transcript.

import (
	"fmt"
	"strings"
)

// The two values of `source`. They are the agent's only signal that the
// underlying conversation may be gone.
const (
	sourceAvailable = "available"
	sourceDeleted   = "deleted"
)

// attachNotes fills Notes and NotesCoverThrough on a page of digests, in one
// query rather than one per session.
//
// fromDay/toDay limit which notes are attached; empty bounds attach all of
// them. When wantNotes is false the note text is not fetched at all -- the
// point of that mode is to not pay for it -- but coverage still is: it is one
// short string per session, and it is the only thing that distinguishes "you
// asked for no notes" from "this chat has none".
//
// NotesCoverThrough is deliberately computed from a *separate*, unfiltered
// query -- see NoteCoverage for why that separation is the point rather than
// an inefficiency.
func attachNotes(digests []RecallDigest, fromDay, toDay string, wantNotes bool) error {
	if len(digests) == 0 {
		return nil
	}
	ids := make([]string, 0, len(digests))
	for _, d := range digests {
		ids = append(ids, d.SessionID)
	}
	coverage, err := NoteCoverage(ids)
	if err != nil {
		return fmt.Errorf("attach notes: %w", err)
	}
	for i := range digests {
		// Set unconditionally: a session whose every note falls outside the
		// window still has notes, and saying how far they reach is exactly what
		// stops the empty list reading as "nothing happened".
		digests[i].NotesCoverThrough = coverage[digests[i].SessionID]
	}
	if !wantNotes {
		return nil
	}

	bySession, err := NotesForSessionsInRange(ids, fromDay, toDay)
	if err != nil {
		return fmt.Errorf("attach notes: %w", err)
	}
	for i := range digests {
		notes := bySession[digests[i].SessionID]
		if len(notes) == 0 {
			continue
		}
		texts := make([]string, 0, len(notes))
		for _, n := range notes {
			// The day is prefixed onto the text because notes are the answer to
			// "what happened last week": undated prose would force the agent
			// back into `read` just to place anything in time.
			texts = append(texts, n.Day+": "+n.Note)
		}
		digests[i].Notes = texts
	}
	return nil
}

// deletedTranscript builds the `read` response for a chat whose messages are
// gone but whose notes survive. It reports false when the id is not known at
// all, so a genuine miss stays a miss.
//
// The response carries no messages array (see RecallTranscript.Messages) and a
// Note that says in words what happened. Numbers alone were not enough for the
// analogous case of a truncated transcript -- an agent with every field it needed
// to notice a cap still concluded it had read the whole chat -- so the reason
// is spelled out rather than left to be inferred from source="deleted".
func deletedTranscript(sessionID string) (RecallTranscript, bool) {
	title, notes, err := DeletedSessionNote(sessionID)
	if err != nil || len(notes) == 0 {
		return RecallTranscript{}, false
	}
	if title == "" {
		title = "(deleted chat)"
	}
	return RecallTranscript{
		SessionID: sessionID,
		Title:     title,
		Source:    sourceDeleted,
		Notes:     notes,
		Note: fmt.Sprintf(
			"This chat was deleted, so its messages cannot be read. What survives is %d day note(s), included here as `notes`. Treat them as a summary recorded at the time, not as quotable source text.",
			len(notes)),
	}, true
}

// rfc3339 wraps a SQL expression so it comes back as "2006-01-02T15:04:05Z".
//
// This has to be explicit, and the reason is a trap worth naming. A bare
// DATETIME column arrives already in that shape, but not because anything here
// asked for it: modernc.org/sqlite inspects the column's *declared type*, and
// converts a DATETIME to a time.Time that Go then renders as RFC 3339. Wrap the
// same column in a subquery or a compound SELECT and the declared type is lost,
// so the driver hands back the raw stored text ("2026-05-11 11:42:51") instead
// -- a silent format change in the API, caused by a query rewrite that looked
// purely structural.
//
// That is exactly what adding the deleted-chats UNION to `list` did. Formatting
// in SQL pins the output to the format the other two verbs already return and
// makes it independent of driver behaviour.
//
// A bare day ("2026-05-11", which is all an orphaned note has) becomes midnight
// UTC, so every timestamp in the response parses with one rule.
func rfc3339(expr string) string {
	return `strftime('%Y-%m-%dT%H:%M:%SZ', ` + expr + `)`
}

// orphanedListClause builds the WHERE fragment that selects deleted-but-noted
// chats for `list`, applying the same window and project filters the live half
// of the union uses. Returned as a fragment rather than a whole query so both
// halves are filtered identically and page together.
func orphanedListClause(opt RecallListOptions) (string, []any) {
	where := []string{"n.orphaned_at IS NOT NULL"}
	var args []any

	// A note is only orphaned once its session row is gone; check anyway, so a
	// note marked in error cannot produce a duplicate of a chat that still
	// exists in the live half of the union.
	where = append(where, "NOT EXISTS (SELECT 1 FROM sessions s2 WHERE s2.id = n.session_id)")

	if p := strings.TrimSpace(opt.Project); p != "" {
		where = append(where, "n.project_id = ?")
		args = append(args, p)
	}
	// Filtered on the note's own day rather than on session timestamps, which
	// no longer exist. `day` is YYYY-MM-DD, so the bounds compare as strings
	// only if they are days too -- normalizeDay yields a datetime, so cut it
	// back to the date part.
	if f := dayPart(opt.From); f != "" {
		where = append(where, "n.day >= ?")
		args = append(args, f)
	}
	if t := dayPart(opt.To); t != "" {
		where = append(where, "n.day <= ?")
		args = append(args, t)
	}
	return strings.Join(where, " AND "), args
}

// dayPart reduces a caller-supplied bound to a YYYY-MM-DD day.
func dayPart(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 10 {
		return ""
	}
	return s[:10]
}

// DeletedSessionNote assembles what can still be told about a deleted chat: its
// snapshotted title and every note, oldest first.
//
// `read` uses this instead of returning an empty messages array. An empty
// transcript reads as "the chat was empty"; this reads as "the chat is gone,
// and here is what was recorded about it" -- the distinction the spec's
// Retention section calls non-optional.
func DeletedSessionNote(sessionID string) (title string, notes []string, err error) {
	rows, qErr := DB.Query(`
		SELECT COALESCE(session_title, ''), day, note
		FROM session_notes WHERE session_id = ? ORDER BY day ASC`, sessionID)
	if qErr != nil {
		return "", nil, fmt.Errorf("deleted session note: %w", qErr)
	}
	defer rows.Close()

	notes = []string{}
	for rows.Next() {
		var t, day, note string
		if err := rows.Scan(&t, &day, &note); err != nil {
			return "", nil, fmt.Errorf("deleted session note scan: %w", err)
		}
		if t != "" {
			title = t
		}
		notes = append(notes, day+": "+note)
	}
	return title, notes, rows.Err()
}
