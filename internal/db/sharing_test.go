// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

func TestUserMessageAuthorIsStoredAndOwnerStaysNull(t *testing.T) {
	setupTestDB(t)
	if err := CreateSession("auth-1", config.SessionConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := AddUserMessage("auth-1", "from the owner", "", "ignored", ""); err != nil {
		t.Fatal(err)
	}
	if err := AddUserMessage("auth-1", "from a guest", "g-7", "Sarah", ""); err != nil {
		t.Fatal(err)
	}
	AddMessage("auth-1", "assistant", "reply")

	msgs := GetMessages("auth-1")
	if len(msgs) != 3 {
		t.Fatalf("got %d messages", len(msgs))
	}
	if msgs[0].AuthorID != "" || msgs[0].AuthorName != "" {
		t.Errorf("owner message carries an author: %+v", msgs[0])
	}
	if msgs[1].AuthorID != "g-7" || msgs[1].AuthorName != "Sarah" {
		t.Errorf("guest author not read back: %+v", msgs[1])
	}
	var nulls int
	DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id='auth-1' AND author_id IS NULL`).Scan(&nulls)
	if nulls != 2 {
		t.Errorf("want the owner and assistant rows stored as NULL, got %d NULL rows", nulls)
	}
}

func TestSessionSharingState(t *testing.T) {
	setupTestDB(t)
	if err := CreateSession("share-1", config.SessionConfig{}); err != nil {
		t.Fatal(err)
	}
	if IsSessionShared("share-1") {
		t.Fatal("a new session is shared")
	}
	if err := SetSessionShared("share-1", true); err != nil {
		t.Fatal(err)
	}
	if !IsSessionShared("share-1") {
		t.Fatal("sharing did not take effect")
	}
	if err := SetSessionShared("share-1", false); err != nil {
		t.Fatal(err)
	}
	if IsSessionShared("share-1") {
		t.Fatal("unsharing did not take effect")
	}
	if IsSessionShared("no-such-session") {
		t.Fatal("an unknown session reads as shared")
	}
}

func TestGuestsAreRecordedPerSessionAndListedMostRecentFirst(t *testing.T) {
	setupTestDB(t)
	for _, id := range []string{"gs-1", "gs-2"} {
		if err := CreateSession(id, config.SessionConfig{}); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := ListGuests("gs-1"); got == nil || len(got) != 0 {
		t.Fatalf("a chat nobody opened lists %#v, want an empty non-nil slice", got)
	}
	RecordGuest("gs-1", "g-a", "Sarah")
	RecordGuest("gs-1", "g-b", "Tom")
	RecordGuest("gs-2", "g-a", "Sarah")
	// Pin the times: CURRENT_TIMESTAMP has one-second resolution.
	DB.Exec(`UPDATE session_guests SET first_seen='2026-01-01 09:00:00', last_seen='2026-01-01 09:00:00' WHERE guest_id='g-a'`)
	DB.Exec(`UPDATE session_guests SET first_seen='2026-01-01 10:00:00', last_seen='2026-01-01 10:00:00' WHERE guest_id='g-b'`)

	// Sarah comes back to gs-1 under a new name: one row, renamed, now most recent.
	if err := RecordGuest("gs-1", "g-a", "Sarah K"); err != nil {
		t.Fatal(err)
	}
	got, err := ListGuests("gs-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "g-a" || got[0].Name != "Sarah K" || got[1].ID != "g-b" {
		t.Fatalf("got %+v", got)
	}
	if got[0].FirstSeen != "2026-01-01T09:00:00Z" || got[0].LastSeen <= got[0].FirstSeen {
		t.Errorf("a return visit must keep first_seen and move last_seen: %+v", got[0])
	}
	if other, _ := ListGuests("gs-2"); len(other) != 1 || other[0].Name != "Sarah" {
		t.Errorf("a visit to one chat leaked into another: %+v", other)
	}

	// Deleting the chat takes its participants with it.
	if err := DeleteSession("gs-1"); err != nil {
		t.Fatal(err)
	}
	var n int
	DB.QueryRow(`SELECT COUNT(*) FROM session_guests WHERE session_id='gs-1'`).Scan(&n)
	if n != 0 {
		t.Errorf("%d participant rows outlived their chat", n)
	}
}

func TestSessionRowsSayWhetherSharedAndHowManyOpenedIt(t *testing.T) {
	setupTestDB(t)
	CreateSession("row-shared", config.SessionConfig{})
	CreateSession("row-plain", config.SessionConfig{})
	SetSessionShared("row-shared", true)
	RecordGuest("row-shared", "g-1", "Sarah")
	RecordGuest("row-shared", "g-2", "Tom")
	got := map[string]SessionListItem{}
	for _, it := range ListSessions() {
		got[it.SessionID] = it
	}
	if s := got["row-shared"]; !s.Shared || s.GuestCount != 2 {
		t.Errorf("shared row = %+v", s)
	}
	if p := got["row-plain"]; p.Shared || p.GuestCount != 0 {
		t.Errorf("plain row = %+v", p)
	}
	if ids := SharedSessionIDs(); len(ids) != 1 || ids[0] != "row-shared" {
		t.Errorf("SharedSessionIDs = %v", ids)
	}
}

func TestAudienceIsRememberedAndStartsEmpty(t *testing.T) {
	setupTestDB(t)
	if a := RememberedAudience(); a.Project || a.UserIDs == nil || len(a.UserIDs) != 0 {
		t.Errorf("a fresh workspace remembers %+v, want nobody", a)
	}
	if err := RememberAudience(Audience{UserIDs: []uint64{7, 9007199254740993}, Project: true}); err != nil {
		t.Fatal(err)
	}
	if a := RememberedAudience(); !a.Project || len(a.UserIDs) != 2 || a.UserIDs[1] != 9007199254740993 {
		t.Errorf("remembered %+v", a)
	}
}

// Tidy-up never deletes a shared chat, on any of its paths.
func TestTidyUpNeverOffersOrSweepsASharedChat(t *testing.T) {
	setupTestDB(t)
	now := time.Now()
	emptyChat(t, "empty-shared", config.SessionConfig{}, hoursAgo(24*400))
	emptyChat(t, "empty-plain", config.SessionConfig{}, hoursAgo(24*400))
	for _, id := range []string{"idle-shared", "idle-plain"} {
		CreateSession(id, config.SessionConfig{})
		AddMessage(id, "user", "hello")
		setSessionUpdatedAt(t, id, hoursAgo(24*400))
	}
	SetSessionShared("empty-shared", true)
	SetSessionShared("idle-shared", true)

	if c := EmptyChatCandidates(now, time.Hour); hasCandidate(c, "empty-shared") || !hasCandidate(c, "empty-plain") {
		t.Errorf("sweep candidates %+v", c)
	}
	// The delete re-checks on its own, for a chat shared after the list was read.
	if deleted, _ := DeleteEmptySession("empty-shared", now, time.Hour); deleted {
		t.Error("the sweep deleted a shared chat")
	}
	items, _ := ReviewCandidates(now, time.Hour)
	var offered []string
	for _, it := range items {
		offered = append(offered, it.ID)
	}
	if len(offered) != 1 || offered[0] != "idle-plain" {
		t.Errorf("review offered %v, want only idle-plain", offered)
	}
}
