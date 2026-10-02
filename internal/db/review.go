// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"encoding/json"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

// Workspace hygiene: the review list.
//
// This replaces the retention model, which computed an *expiry* and
// warned in advance of it. Nothing expires any more. A chat that has been idle
// past the review window is offered to the user, who deletes it or defers it;
// a chat that is never reviewed is never deleted, it is simply offered again.
//
// Because nothing expires, the old advance-notice fields (EligibleAt, Eligible,
// DaysUntilEligible) describe nothing and are gone. What the user needs in
// order to decide is what replaced them: how long it has been idle, how much is
// in it, and how much disk it is using.

// ReviewListLimit caps how many items one review shows. The measured backlog on
// a real workspace was 145; showing all of them turns a review into a chore and
// puts a select-all one click away from deleting five months of history. The
// full total is reported alongside so nothing is hidden — the rest simply waits
// for the next pass.
const ReviewListLimit = 50

// TidyItem is one chat offered for review.
type TidyItem struct {
	// Kind is always "chat" today. Projects are a deliberate durable artifact
	// and are not swept up in a hygiene review; the field remains so the
	// cleanup endpoint can stay uniform.
	Kind string `json:"kind"`
	// ID is the session id.
	ID string `json:"id"`
	// Label is a human-friendly name.
	Label string `json:"label"`
	// LastActivity is the RFC3339 timestamp the idle clock is measured from
	// (the session's updated_at).
	LastActivity string `json:"last_activity"`
	// IdleDays is whole days since LastActivity.
	IdleDays int `json:"idle_days"`
	// MessageCount is how many messages the chat holds. Always at least 1:
	// chats with none are deleted automatically and never reach this list.
	MessageCount int `json:"message_count"`
	// SizeBytes is the size of the chat's auto-created workspace folder, or 0
	// when it has none (a user-chosen directory is never measured, because it
	// is not ours and will not be deleted).
	SizeBytes int64 `json:"size_bytes"`
	// Workdir is the folder that would be removed along with the chat. Empty
	// when nothing would be removed.
	Workdir string `json:"workdir"`
	// Starred is the user's own "this one matters" flag (SessionConfig.Favorite,
	// shown as a star in the sidebar). It does not exclude a chat from review --
	// that would build exactly the invisible pile this feature exists to drain --
	// but it is the one signal of importance the user has already given us, so
	// the review keeps these apart from the rest rather than mixed in.
	Starred bool `json:"starred"`
}

// ReviewCandidates returns the chats worth reviewing as of now, oldest first,
// and the total number that qualified before the limit was applied.
//
// A chat qualifies when it has been idle longer than idleFor, holds at least
// one message, is not snoozed, and is not marked Keep. Nothing here is
// deleted or scheduled for deletion; this is the list the user is asked about.
// Never returns nil.
func ReviewCandidates(now time.Time, idleFor time.Duration) (items []TidyItem, total int) {
	items = []TidyItem{}
	if idleFor <= 0 {
		return items, 0
	}
	cutoff := now.Add(-idleFor)

	rows, err := DB.Query(`
		SELECT s.id, s.config, s.updated_at, s.tidy_snooze_until,
			(SELECT COUNT(*) FROM messages m WHERE m.session_id = s.id) AS msg_count
		FROM sessions s
		WHERE s.project_id = ''
		  AND s.shared_at IS NULL
		ORDER BY s.updated_at ASC
	`)
	if err != nil {
		return items, 0
	}
	defer rows.Close()

	for rows.Next() {
		var id, cfgJSON, updatedAt, snoozeUntil string
		var msgCount int
		if err := rows.Scan(&id, &cfgJSON, &updatedAt, &snoozeUntil, &msgCount); err != nil {
			continue
		}
		// An empty chat is the sweeper's business, not the user's.
		if msgCount == 0 {
			continue
		}
		last := parseTime(updatedAt)
		if last.IsZero() || !last.Before(cutoff) {
			continue
		}
		if snoozed(snoozeUntil, now) {
			continue
		}
		var cfg config.SessionConfig
		_ = json.Unmarshal([]byte(cfgJSON), &cfg)
		if cfg.Keep {
			continue
		}

		total++
		if len(items) >= ReviewListLimit {
			// Keep counting so the user is told how many there are in total,
			// but stop building the list.
			continue
		}
		label := cfg.Label
		if label == "" {
			label = "Untitled"
		}
		items = append(items, TidyItem{
			Kind:         "chat",
			ID:           id,
			Label:        label,
			LastActivity: last.UTC().Format(time.RFC3339),
			IdleDays:     int(now.Sub(last).Hours() / 24),
			MessageCount: msgCount,
			Workdir:      cfg.Workdir,
			Starred:      cfg.Favorite,
		})
	}
	return items, total
}

// SnoozeSession defers a chat's next review until now+snooze. Passing a
// non-positive duration clears the snooze, bringing the chat back to the list
// immediately.
//
// This deliberately does not touch updated_at: snoozing is not activity, and
// treating it as such would reset the idle clock and quietly make the chat look
// fresh in the sidebar.
func SnoozeSession(id string, now time.Time, snooze time.Duration) error {
	until := ""
	if snooze > 0 {
		until = now.Add(snooze).UTC().Format(time.RFC3339)
	}
	_, err := DB.Exec(`UPDATE sessions SET tidy_snooze_until = ? WHERE id = ?`, until, id)
	return err
}

// SessionWorkdirs returns the working directory of every session, including
// project chats, and reports whether the database could be read at all.
//
// The orphan scan compares directories on disk against this set, so a partial
// answer is dangerous: a session missing from it looks like an orphan. The
// boolean lets the caller distinguish "no sessions exist" from "the query
// failed", and refuse to scan in either case.
func SessionWorkdirs() (map[string]bool, bool) {
	dirs := map[string]bool{}
	rows, err := DB.Query(`SELECT config FROM sessions`)
	if err != nil {
		return dirs, false
	}
	defer rows.Close()

	for rows.Next() {
		var cfgJSON string
		if err := rows.Scan(&cfgJSON); err != nil {
			// One unreadable row means the set is incomplete, and an incomplete
			// set makes live directories look orphaned. Refuse the whole scan.
			return dirs, false
		}
		var cfg config.SessionConfig
		if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
			return dirs, false
		}
		if cfg.Workdir != "" {
			dirs[cfg.Workdir] = true
		}
	}
	if err := rows.Err(); err != nil {
		return dirs, false
	}
	return dirs, true
}

// snoozed reports whether a snooze timestamp is still in the future. An
// unparseable value is treated as not snoozed: the cost is asking the user
// about a chat they deferred, which is a great deal cheaper than never asking
// again.
func snoozed(until string, now time.Time) bool {
	if until == "" {
		return false
	}
	t := parseTime(until)
	if t.IsZero() {
		return false
	}
	return now.Before(t)
}

// parseTime parses the timestamp layouts our DATETIME columns hold: RFC3339
// (what we write via time.Now().Format) and SQLite's own "2006-01-02 15:04:05"
// from CURRENT_TIMESTAMP, in UTC. Returns the zero time if unparseable, which
// every caller treats as "leave this alone".
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, sqliteTimeLayout} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
