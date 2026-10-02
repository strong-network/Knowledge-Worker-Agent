// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"encoding/json"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

// Workspace hygiene: the queries behind automatic cleanup.
//
// Only one class of chat is ever deleted without asking: one that contains
// nothing. "Nothing" is a conjunction, and every part of it is re-checked in
// SQL at the moment of deletion rather than only beforehand, because the gap
// between deciding and acting is exactly where a chat stops being empty.

// sqliteTimeLayout is the timestamp format SQLite's CURRENT_TIMESTAMP produces
// and datetime() normalizes to. Two values in this layout compare as strings in
// chronological order, which is what the delete below relies on.
const sqliteTimeLayout = "2006-01-02 15:04:05"

// EmptyChat is a candidate for automatic deletion: a chat with no messages and
// no draft that has been idle past the empty-chat window.
type EmptyChat struct {
	// ID is the session id.
	ID string
	// Workdir is the session's working directory, captured before deletion
	// because nothing records it afterwards. It is only ever removed if it
	// passes the workdirs rails.
	Workdir string
	// Label is used for logging only.
	Label string
}

// EmptyChatCandidates returns chats that look deletable as of now: no messages,
// no saved draft, last activity older than idleFor, and not marked Keep.
//
// The result is a *candidate* list. Callers must still delete through
// DeleteEmptySession, which re-checks the same conditions atomically — between
// this query and the delete the user may have opened the chat and sent a
// message. Never returns nil.
func EmptyChatCandidates(now time.Time, idleFor time.Duration) []EmptyChat {
	out := []EmptyChat{}
	if idleFor <= 0 {
		return out
	}
	cutoff := now.Add(-idleFor)

	// Note the draft condition: drafts deliberately do not bump updated_at (a
	// draft should not promote a chat to the top of the sidebar), so a chat
	// with a long unsent prompt typed into it can look idle. Without this a tab
	// left open overnight would be deleted with the user's text still in it.
	rows, err := DB.Query(`
		SELECT s.id, s.config, s.updated_at
		FROM sessions s
		WHERE s.draft = ''
		  AND s.shared_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.session_id = s.id)
	`)
	if err != nil {
		return out
	}
	defer rows.Close()

	for rows.Next() {
		var id, cfgJSON, updatedAt string
		if err := rows.Scan(&id, &cfgJSON, &updatedAt); err != nil {
			continue
		}
		// An unparseable timestamp means we cannot tell how idle the chat is.
		// Skip it: leaving a chat alone is always recoverable, deleting it is
		// not.
		last := parseTime(updatedAt)
		if last.IsZero() || !last.Before(cutoff) {
			continue
		}
		var cfg config.SessionConfig
		_ = json.Unmarshal([]byte(cfgJSON), &cfg)
		if cfg.Keep {
			continue
		}
		out = append(out, EmptyChat{ID: id, Workdir: cfg.Workdir, Label: cfg.Label})
	}
	return out
}

// DeleteEmptySession deletes the session only if it is still empty and still
// idle, and reports whether it did.
//
// The conditions are repeated here on purpose. This is a single statement, so
// the emptiness check and the delete cannot be separated by a concurrent write:
// if a message arrived, or a draft was saved, or the chat was used, after
// EmptyChatCandidates ran, the WHERE clause no longer matches and nothing is
// removed. A false return means the chat earned its place, and the caller must
// leave its files alone.
//
// updated_at is wrapped in datetime() because the column holds two layouts —
// CURRENT_TIMESTAMP writes "2006-01-02 15:04:05" while some rows carry RFC3339
// — and comparing those as raw strings orders them wrongly. datetime()
// normalizes both, and yields NULL for anything it cannot parse, so an
// unreadable timestamp fails the comparison and the chat survives.
func DeleteEmptySession(id string, now time.Time, idleFor time.Duration) (bool, error) {
	if idleFor <= 0 {
		return false, nil
	}
	cutoff := now.Add(-idleFor).UTC().Format(sqliteTimeLayout)
	res, err := DB.Exec(`
		DELETE FROM sessions
		WHERE id = ?
		  AND draft = ''
		  AND shared_at IS NULL
		  AND datetime(updated_at) < ?
		  AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.session_id = sessions.id)
	`, id, cutoff)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
