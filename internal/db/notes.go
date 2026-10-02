// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

// Cross-session retrieval: storage for generated day notes.
//
// A note summarises one (session, day) of activity. Notes are append-only and
// never rewritten -- see the spec's *Day notes*. The rule that makes that safe
// is enforced in PendingNoteDays: **only closed days are ever offered**. A note
// written for the day in progress would freeze a summary covering only the
// morning, and because the row then exists no later pass would replace it,
// while the no-rewrite rule forbids fixing it. The afternoon would be lost
// permanently.
//
// Two properties of this table are deliberate and load-bearing:
//
//   - **No foreign key.** A note outlives the chat it describes (spec:
//     *Retention*). Deleting a chat is routine hygiene; losing the record of
//     what was done in July is not.
//   - **Self-describing.** Because the sessions row can be gone, the title and
//     project are snapshotted onto the note at write time. The title in
//     particular does not live in a column at all -- it lives inside the
//     sessions.config JSON blob, which disappears with the row.

import (
	"database/sql"
	"fmt"
	"strings"
)

const (
	// noteMinDayMessages is the fewest messages a day needs before it is worth
	// summarising. A single stray message is not a day's work, and the spec is
	// explicit that a note which manufactures significance is worse than no
	// note at all.
	noteMinDayMessages = 2

	// noteMinSessionMessages, with the multi-day test below, defines the
	// population that gets notes at all. Short single-day chats are excluded
	// because their free digest (title + first user message) already describes
	// them -- measured on the real corpus, the digest only breaks down on long
	// or multi-day sessions, where the opening request stops predicting what
	// the chat became. This is not a cost saving; it is declining to spend
	// tokens where notes cannot beat what is already free.
	noteMinSessionMessages = 20
)

// SessionNote is one stored day note.
type SessionNote struct {
	SessionID     string `json:"session_id"`
	Day           string `json:"day"`
	Note          string `json:"note"`
	SessionTitle  string `json:"session_title"`
	ProjectID     string `json:"project_id"`
	CoversFrom    int64  `json:"covers_from"`
	CoversTo      int64  `json:"covers_to"`
	MsgCount      int    `json:"msg_count"`
	Model         string `json:"model"`
	PromptVersion int    `json:"prompt_version"`
	CreatedAt     string `json:"created_at"`
	// OrphanedAt is set when the described session is deleted. Empty means the
	// conversation can still be read.
	OrphanedAt string `json:"orphaned_at,omitempty"`
}

// NoteCandidate is one (session, day) of activity that has no note yet.
type NoteCandidate struct {
	SessionID  string
	Day        string
	CoversFrom int64
	CoversTo   int64
	MsgCount   int
	Title      string
	ProjectID  string
}

// PendingNoteDays returns the backlog: (session, day) pairs that have messages,
// belong to a day that has closed, and either have no note yet or carry a note
// written by an older prompt -- newest first, capped.
//
// Newest first because recall value decays with age. The point of a note is to
// let a future session find what was decided recently; "what did we agree about
// the schema yesterday" is the question that gets asked, and it was the one the
// backlog answered last. Draining oldest-first meant a workspace enabling the
// feature got months-old days summarised while the week it actually wanted
// stayed empty for as long as the backfill took.
//
// `today` is the first day NOT to summarise, as YYYY-MM-DD UTC. It is a
// parameter rather than date('now') so the job is testable without waiting for
// midnight, and so the boundary is decided in exactly one place.
//
// `promptVersion` is the version the caller is about to write. Days whose note
// was written by an older prompt are re-offered, which is what makes improving
// the instruction possible at all: notes are never edited in place, so without
// this a bad summary would be permanent and the only remedy would be deleting
// rows by hand. Passing the version in (rather than importing the generator's
// constant) keeps this package free of a dependency on the thing that calls it.
//
// Note that a re-offered day is regenerated from its messages, never from the
// old note -- so a note can never re-compress its own output. That also means
// **orphaned notes are never re-offered**: their session is gone, so the JOIN
// below finds nothing. That is the desired behaviour rather than an accident.
// A note whose source has been deleted is irreplaceable, and offering it for
// regeneration could only ever destroy it.
//
// An empty result means no work and, importantly, no model calls -- so running
// this on every boot costs nothing on a workspace that reboots repeatedly.
func PendingNoteDays(today string, promptVersion, limit int) ([]NoteCandidate, error) {
	if limit <= 0 {
		return []NoteCandidate{}, nil
	}
	rows, err := DB.Query(`
		SELECT m.session_id,
		       date(m.created_at)          AS day,
		       MIN(m.id), MAX(m.id), COUNT(*),
		       COALESCE(s.config, '{}'),
		       COALESCE(s.project_id, '')
		FROM messages m
		JOIN sessions s ON s.id = m.session_id
		WHERE date(m.created_at) < ?
		  AND `+notTemporary+`
		  AND NOT EXISTS (
		      SELECT 1 FROM session_notes n
		      WHERE n.session_id = m.session_id AND n.day = date(m.created_at)
		        AND n.prompt_version >= ?
		  )
		  AND m.session_id IN (
		      SELECT session_id FROM messages
		      GROUP BY session_id
		      HAVING COUNT(*) >= ? OR COUNT(DISTINCT date(created_at)) > 1
		  )
		GROUP BY m.session_id, day
		HAVING COUNT(*) >= ?
			ORDER BY day DESC, m.session_id ASC
		LIMIT ?`,
		today, promptVersion, noteMinSessionMessages, noteMinDayMessages, limit)
	if err != nil {
		return nil, fmt.Errorf("pending note days: %w", err)
	}
	defer rows.Close()

	out := []NoteCandidate{}
	for rows.Next() {
		var c NoteCandidate
		var cfgJSON string
		if err := rows.Scan(&c.SessionID, &c.Day, &c.CoversFrom, &c.CoversTo,
			&c.MsgCount, &cfgJSON, &c.ProjectID); err != nil {
			return nil, fmt.Errorf("pending note days scan: %w", err)
		}
		c.Title = labelOf(cfgJSON)
		out = append(out, c)
	}
	return out, rows.Err()
}

// DayMessages returns one day's messages for a session, in order. This is the
// raw material a note is generated from -- never a previous note, so a note can
// never re-compress its own output.
func DayMessages(sessionID, day string) ([]RecallMessage, error) {
	rows, err := DB.Query(
		`SELECT role, created_at, content FROM messages
		 WHERE session_id = ? AND date(created_at) = ? ORDER BY id ASC`,
		sessionID, day)
	if err != nil {
		return nil, fmt.Errorf("day messages: %w", err)
	}
	defer rows.Close()

	out := []RecallMessage{}
	for rows.Next() {
		var m RecallMessage
		if err := rows.Scan(&m.Role, &m.At, &m.Content); err != nil {
			return nil, fmt.Errorf("day messages scan: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SaveSessionNote writes one note.
//
// Notes are append-only with exactly one exception: a note written by a NEWER
// prompt version replaces an older one. The conflict clause enforces that
// direction, so the guarantee is "a note is never silently rewritten by an
// equal or older generation" rather than "a row never changes".
//
// Doing the comparison in SQL rather than with a read-then-write keeps two
// passes racing on the same day safe, and keeps a retry of an older version
// from clobbering a newer note that landed in between.
//
// orphaned_at is deliberately not in the SET list: it records what happened to
// the *session*, not to the note, and regenerating text must not resurrect a
// note as un-orphaned. (In practice a regeneration cannot reach an orphaned row
// at all -- PendingNoteDays cannot offer one -- so this is belt and braces.)
func SaveSessionNote(n SessionNote) error {
	if strings.TrimSpace(n.SessionID) == "" || strings.TrimSpace(n.Day) == "" {
		return fmt.Errorf("save note: session id and day are required")
	}
	_, err := DB.Exec(`
		INSERT INTO session_notes
		  (session_id, day, note, session_title, project_id,
		   covers_from, covers_to, msg_count, model, prompt_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, day) DO UPDATE SET
		  note           = excluded.note,
		  session_title  = excluded.session_title,
		  project_id     = excluded.project_id,
		  covers_from    = excluded.covers_from,
		  covers_to      = excluded.covers_to,
		  msg_count      = excluded.msg_count,
		  model          = excluded.model,
		  prompt_version = excluded.prompt_version,
		  created_at     = CURRENT_TIMESTAMP
		WHERE excluded.prompt_version > session_notes.prompt_version`,
		n.SessionID, n.Day, n.Note, n.SessionTitle, n.ProjectID,
		n.CoversFrom, n.CoversTo, n.MsgCount, n.Model, n.PromptVersion)
	if err != nil {
		return fmt.Errorf("save note: %w", err)
	}
	return nil
}

// NotesForSessions returns the notes for the given sessions, keyed by session
// id and ordered by day. Sessions with no notes are simply absent from the map.
func NotesForSessions(ids []string) (map[string][]SessionNote, error) {
	return NotesForSessionsInRange(ids, "", "")
}

// NotesForSessionsInRange is NotesForSessions limited to a day range. Empty
// bounds are unbounded, so ("", "") is every note.
//
// The range exists because `list` filters *sessions* by date but used to
// attach every note those sessions had ever accumulated. A one-day query
// returned notes going back months: correctly dated, but overwhelmingly
// outside what was asked for, which is a correctness trap as much as a size
// one -- an agent summarising a week was handed mostly other weeks.
//
// Day is stored as 'YYYY-MM-DD', so the comparison is a plain string BETWEEN.
func NotesForSessionsInRange(ids []string, fromDay, toDay string) (map[string][]SessionNote, error) {
	out := map[string][]SessionNote{}
	if len(ids) == 0 {
		return out, nil
	}
	place := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+2)
	for _, id := range ids {
		args = append(args, id)
	}
	where := ""
	if fromDay != "" {
		where += ` AND day >= ?`
		args = append(args, fromDay)
	}
	if toDay != "" {
		where += ` AND day <= ?`
		args = append(args, toDay)
	}
	rows, err := DB.Query(`
		SELECT session_id, day, note, COALESCE(orphaned_at, '')
		FROM session_notes
		WHERE session_id IN (`+place+`)`+where+`
		ORDER BY day ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("notes for sessions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var n SessionNote
		if err := rows.Scan(&n.SessionID, &n.Day, &n.Note, &n.OrphanedAt); err != nil {
			return nil, fmt.Errorf("notes for sessions scan: %w", err)
		}
		out[n.SessionID] = append(out[n.SessionID], n)
	}
	return out, rows.Err()
}

// NoteCoverage returns the last day each session has a note for, keyed by
// session id. Sessions with no notes are absent.
//
// This is deliberately NOT derived from whatever notes a caller happened to
// ask for. It is the high-water mark, and its whole job is to let a reader
// tell a quiet day from an unsummarised one. Computing it from a filtered
// slice would report "the last note in your window" while still claiming to
// mean "the last note there is" -- turning an honesty marker into a
// misleading one, which is worse than the gap it was added to close.
func NoteCoverage(ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	place := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := DB.Query(`
		SELECT session_id, MAX(day)
		FROM session_notes
		WHERE session_id IN (`+place+`)
		GROUP BY session_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("note coverage: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var sid, day string
		if err := rows.Scan(&sid, &day); err != nil {
			return nil, fmt.Errorf("note coverage scan: %w", err)
		}
		out[sid] = day
	}
	return out, rows.Err()
}

// MarkNotesOrphaned records that the described chat is gone. It is called on
// session delete and must not fail the delete: an unmarked note is a note that
// claims more than it can show, which is the failure this whole field exists to
// prevent -- but a chat the user asked to delete must still go.
func MarkNotesOrphaned(sessionID string) error {
	_, err := DB.Exec(
		`UPDATE session_notes SET orphaned_at = CURRENT_TIMESTAMP
		 WHERE session_id = ? AND orphaned_at IS NULL`, sessionID)
	if err != nil {
		return fmt.Errorf("mark notes orphaned: %w", err)
	}
	return nil
}

// DeleteSessionNotes removes a session's notes outright and reports how many
// went. This is the consent path: keeping notes is the right default for
// routine cleanup, but someone deleting a chat *because of what is in it* must
// be able to remove the summary in the same action, or the deletion does not
// actually delete the thing they wanted gone.
func DeleteSessionNotes(sessionID string) (int, error) {
	res, err := DB.Exec(`DELETE FROM session_notes WHERE session_id = ?`, sessionID)
	if err != nil {
		return 0, fmt.Errorf("delete notes: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// CountSessionNotes reports how many notes a session has. The delete
// confirmation uses it to say what is at stake rather than asking abstractly.
func CountSessionNotes(sessionID string) int {
	var n int
	if err := DB.QueryRow(
		`SELECT COUNT(*) FROM session_notes WHERE session_id = ?`, sessionID,
	).Scan(&n); err != nil && err != sql.ErrNoRows {
		return 0
	}
	return n
}
