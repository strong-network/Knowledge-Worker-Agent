// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"encoding/json"
	"time"
)

// Temporary chats: a chat marked temporary is deleted RetentionTemporary after
// its last message from the user or the agent.

// isTemporary matches a temporary chat's row. A missing flag reads as false.
const isTemporary = `COALESCE(json_extract(config, '$.temporary'), 0) = 1`

// lastActivity is a session row's last activity in unix seconds. Rows from
// before activity_at existed fall back to when the chat was created.
const lastActivity = `CAST(strftime('%s', COALESCE(s.activity_at, s.created_at)) AS INTEGER)`

// startTemporaryClocks gives the temporary chats made before activity_at existed
// their full time from now, rather than deleting them on the first sweep.
func startTemporaryClocks() error {
	_, err := DB.Exec(`UPDATE sessions SET activity_at = CURRENT_TIMESTAMP WHERE activity_at IS NULL AND ` + isTemporary)
	return err
}

func expiresIn(temporary bool, last int64, now time.Time) int64 {
	if !temporary {
		return 0
	}
	return max(0, last+int64(RetentionTemporary/time.Second)-now.Unix())
}

// SetTemporary makes a chat temporary or keeps it. Making it temporary starts
// its time again and takes it out of Starred and Pinned. Neither counts as
// using the chat, so its place among the other chats doesn't change.
func SetTemporary(id string, on bool) error {
	cfg, err := GetSessionConfig(id)
	if err != nil {
		return err
	}
	cfg.Temporary = on
	if on {
		cfg.Favorite, cfg.Pinned, cfg.Keep = false, false, false
	}
	raw, _ := json.Marshal(cfg)
	if on {
		_, err = DB.Exec(`UPDATE sessions SET config = ?, activity_at = CURRENT_TIMESTAMP WHERE id = ?`, string(raw), id)
	} else {
		_, err = DB.Exec(`UPDATE sessions SET config = ? WHERE id = ?`, string(raw), id)
	}
	return err
}

// TouchActivity records that someone just sent a message to the chat.
func TouchActivity(id string) {
	DB.Exec(`UPDATE sessions SET activity_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
}

// TemporaryExpiresIn is how many seconds a temporary chat has left; ok is false
// for a chat that doesn't exist or isn't temporary.
func TemporaryExpiresIn(id string, now time.Time) (left int64, ok bool) {
	var last int64
	err := DB.QueryRow(`SELECT `+lastActivity+` FROM sessions s WHERE s.id = ? AND `+isTemporary, id).Scan(&last)
	if err != nil {
		return 0, false
	}
	return expiresIn(true, last, now), true
}

// TemporaryChatsDue lists the temporary chats whose time ran out by now.
func TemporaryChatsDue(now time.Time) []string {
	ids := []string{}
	rows, err := DB.Query(`SELECT s.id FROM sessions s WHERE `+isTemporary+` AND `+lastActivity+` <= ?`,
		now.Add(-RetentionTemporary).Unix())
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

// SessionShared reports whether the chat is shared with coworkers now.
func SessionShared(id string) bool {
	var shared bool
	DB.QueryRow(`SELECT shared_at IS NOT NULL FROM sessions WHERE id = ?`, id).Scan(&shared)
	return shared
}

// RecordExpiredChat remembers a temporary chat deleted for inactivity.
func RecordExpiredChat(id string) error {
	_, err := DB.Exec(`INSERT OR REPLACE INTO expired_chats (id) VALUES (?)`, id)
	return err
}

// ChatExpired reports whether the chat was a temporary chat deleted for inactivity.
func ChatExpired(id string) bool {
	var n int
	DB.QueryRow(`SELECT COUNT(*) FROM expired_chats WHERE id = ?`, id).Scan(&n)
	return n > 0
}

// PruneExpiredChats forgets the chats deleted before the given time.
func PruneExpiredChats(before time.Time) error {
	_, err := DB.Exec(`DELETE FROM expired_chats WHERE expired_at < ?`, before.UTC().Format("2006-01-02 15:04:05"))
	return err
}
