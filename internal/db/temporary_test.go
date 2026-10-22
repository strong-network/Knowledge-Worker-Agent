// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

// Temporary chats made before their clock existed get a full day from the
// upgrade, rather than being deleted on the first sweep for their age.
func TestOldTemporaryChatsGetAFullDay(t *testing.T) {
	setupTestDB(t)
	for _, row := range []struct{ id, cfg string }{{"old-temp", `{"temporary":true}`}, {"old-saved", `{}`}} {
		if _, err := DB.Exec(`INSERT INTO sessions (id, config, created_at) VALUES (?, ?, datetime('now', '-3 days'))`, row.id, row.cfg); err != nil {
			t.Fatal(err)
		}
	}
	if len(TemporaryChatsDue(time.Now())) != 1 {
		t.Fatal("setup: the old temporary chat should be due before its clock starts")
	}
	if err := startTemporaryClocks(); err != nil {
		t.Fatal(err)
	}
	if due := TemporaryChatsDue(time.Now()); len(due) != 0 {
		t.Errorf("due after the upgrade: %v", due)
	}
	if left, ok := TemporaryExpiresIn("old-temp", time.Now()); !ok || left < 86000 {
		t.Errorf("old temporary chat has %d s left (ok=%v), want about a day", left, ok)
	}
	var saved any
	DB.QueryRow(`SELECT activity_at FROM sessions WHERE id = 'old-saved'`).Scan(&saved)
	if saved != nil {
		t.Errorf("a saved chat's clock was started: %v", saved)
	}
}

// The list says how long a temporary chat has left, counted from its last
// message, and nothing for a saved chat.
func TestListSaysHowLongATemporaryChatHasLeft(t *testing.T) {
	setupTestDB(t)
	CreateSession("temp", config.SessionConfig{Temporary: true})
	CreateSession("saved", config.SessionConfig{})
	DB.Exec(`UPDATE sessions SET activity_at = datetime('now', '-23 hours') WHERE id = 'temp'`)
	left := map[string]int64{}
	temporary := map[string]bool{}
	for _, s := range ListSessions() {
		left[s.SessionID], temporary[s.SessionID] = s.ExpiresIn, s.Temporary
	}
	if !temporary["temp"] || left["temp"] < 3500 || left["temp"] > 3600 {
		t.Errorf("temporary chat: temporary=%v, %d s left, want about an hour", temporary["temp"], left["temp"])
	}
	if temporary["saved"] || left["saved"] != 0 {
		t.Errorf("saved chat: temporary=%v, %d s left", temporary["saved"], left["saved"])
	}
	if err := AddUserMessage("temp", "hello", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if l, _ := TemporaryExpiresIn("temp", time.Now()); l < 86000 {
		t.Errorf("a message didn't start the day again: %d s left", l)
	}
}
