// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"database/sql"
	"encoding/json"
)

// AddUserMessage records the prompt that opens a chat turn, with its author and
// the turn's id. An empty authorID means the workspace owner and is
// stored as NULL, as is an empty turnID.
func AddUserMessage(sessionID, content, authorID, authorName, turnID string) error {
	var id, name any
	if authorID != "" {
		id, name = authorID, authorName
	}
	_, err := DB.Exec(
		`INSERT INTO messages (session_id, role, content, author_id, author_name, turn_id) VALUES (?, 'user', ?, ?, ?, ?)`,
		sessionID, content, id, name, nullable(turnID),
	)
	if err == nil {
		DB.Exec(`UPDATE sessions SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, sessionID)
	}
	return err
}

// SharedSessionIDs returns the chats shared now. Never nil.
func SharedSessionIDs() []string {
	ids := []string{}
	rows, err := DB.Query(`SELECT id FROM sessions WHERE shared_at IS NOT NULL ORDER BY shared_at`)
	if err != nil {
		return ids
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// shareAudienceKey holds the audience chosen while no Workspace App exists,
// which the next share creates it with.
const shareAudienceKey = "share_audience"

// Audience is who the Workspace App is shared with.
type Audience struct {
	UserIDs []uint64 `json:"user_ids"`
	Project bool     `json:"project"`
}

// RememberedAudience returns the audience last saved, or an empty one: an owner
// who never chose anyone has shared with no one.
func RememberedAudience() Audience {
	a := Audience{UserIDs: []uint64{}}
	if v, ok := metaGet(shareAudienceKey); ok {
		_ = json.Unmarshal([]byte(v), &a)
	}
	if a.UserIDs == nil {
		a.UserIDs = []uint64{}
	}
	return a
}

// RememberAudience saves the audience for the next share.
func RememberAudience(a Audience) error {
	if a.UserIDs == nil {
		a.UserIDs = []uint64{}
	}
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return metaSet(shareAudienceKey, string(b))
}

// nullable stores an empty string as NULL.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// IsSessionShared reports whether the session exists and is shared.
func IsSessionShared(sessionID string) bool {
	var at sql.NullString
	if err := DB.QueryRow(`SELECT shared_at FROM sessions WHERE id = ?`, sessionID).Scan(&at); err != nil {
		return false
	}
	return at.Valid
}

// ShareOptions are what the owner lets guests do in one shared chat.
type ShareOptions struct {
	AllowPermissions bool `json:"allow_permissions"`
	AllowFiles       bool `json:"allow_files"`
}

// SessionShareOptions returns the chat's options, all off unless it is shared.
func SessionShareOptions(sessionID string) ShareOptions {
	var o ShareOptions
	var at sql.NullString
	err := DB.QueryRow(`SELECT shared_at, share_allow_permissions, share_allow_files FROM sessions WHERE id = ?`, sessionID).
		Scan(&at, &o.AllowPermissions, &o.AllowFiles)
	if err != nil || !at.Valid {
		return ShareOptions{}
	}
	return o
}

// SetShareOptions changes a shared chat's options, reporting false when the
// chat is not shared: options exist only for the share they were set for.
func SetShareOptions(sessionID string, o ShareOptions) (bool, error) {
	res, err := DB.Exec(
		`UPDATE sessions SET share_allow_permissions = ?, share_allow_files = ? WHERE id = ? AND shared_at IS NOT NULL`,
		o.AllowPermissions, o.AllowFiles, sessionID,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetSessionShared starts or stops sharing a session. Stopping also turns the
// options off, so the next share starts from nothing allowed.
func SetSessionShared(sessionID string, shared bool) error {
	q := `UPDATE sessions SET shared_at = NULL, share_allow_permissions = 0, share_allow_files = 0 WHERE id = ?`
	if shared {
		q = `UPDATE sessions SET shared_at = COALESCE(shared_at, CURRENT_TIMESTAMP) WHERE id = ?`
	}
	_, err := DB.Exec(q, sessionID)
	return err
}

// Guest is one person who has opened a shared chat. FirstSeen and
// LastSeen are RFC3339 UTC, as ChatMessage.CreatedAt.
type Guest struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
}

// RecordGuest notes that a guest opened the chat: the first visit adds them,
// a later one moves last_seen and takes the name they now go by.
func RecordGuest(sessionID, guestID, name string) error {
	_, err := DB.Exec(
		`INSERT INTO session_guests (session_id, guest_id, name) VALUES (?, ?, ?)
		 ON CONFLICT (session_id, guest_id) DO UPDATE SET name = excluded.name, last_seen = CURRENT_TIMESTAMP`,
		sessionID, guestID, name,
	)
	return err
}

// ListGuests returns everyone who has opened the chat, most recently seen first.
func ListGuests(sessionID string) ([]Guest, error) {
	rows, err := DB.Query(
		`SELECT guest_id, name, strftime('%Y-%m-%dT%H:%M:%SZ', first_seen), strftime('%Y-%m-%dT%H:%M:%SZ', last_seen) FROM session_guests
		 WHERE session_id = ? ORDER BY last_seen DESC, first_seen DESC`,
		sessionID,
	)
	if err != nil {
		return []Guest{}, err
	}
	defer rows.Close()
	out := []Guest{}
	for rows.Next() {
		var g Guest
		if err := rows.Scan(&g.ID, &g.Name, &g.FirstSeen, &g.LastSeen); err != nil {
			return []Guest{}, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
