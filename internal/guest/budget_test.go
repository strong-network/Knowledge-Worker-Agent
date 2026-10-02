// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package guest

import (
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// A second guest, as if another person, or the same one after clearing cookies.
func (e *guestEnv) anotherGuest(name string) *guestEnv {
	e.t.Helper()
	jar, _ := cookiejar.New(nil)
	c := *e.c
	c.Jar = jar
	other := *e
	other.c = &c
	other.identify(name)
	return &other
}

// The budget is the chat's, so neither a second guest nor a fresh cookie
// resets it, and one busy chat does not starve another.
func TestGuestPromptsSpendTheChatsBudget(t *testing.T) {
	e := newEnv(t)
	now := time.Unix(1_000_000, 0)
	prevBurst, prevRefill, prevClock := promptBurst, promptRefill, clock
	promptBurst, promptRefill, clock = 2, time.Minute, func() time.Time { return now }
	t.Cleanup(func() { promptBurst, promptRefill, clock = prevBurst, prevRefill, prevClock })

	e.identify("Sarah")
	other := e.anotherGuest("Omar")
	chat.OpenTurn(e.shared)
	send := func(g *guestEnv, sid string) (int, http.Header) {
		code, _, h := g.do("POST", "/guest/api/chat", `{"session_id":"`+sid+`","prompt":"next"}`, sameOrigin)
		return code, h
	}
	for i := 0; i < 2; i++ {
		if code, _ := send(e, e.shared); code != 202 {
			t.Fatalf("prompt %d = %d", i, code)
		}
	}
	code, h := send(other, e.shared)
	if code != 429 || h.Get("Retry-After") != "60" {
		t.Errorf("third prompt, from another guest = %d retry=%q", code, h.Get("Retry-After"))
	}
	if n := chat.QueueLen(e.shared); n != 2 {
		t.Errorf("queue holds %d prompts, want the 2 accepted", n)
	}

	second := chat.NewUUID()
	db.CreateSession(second, config.SessionConfig{Label: "Other"})
	db.SetSessionShared(second, true)
	chat.OpenTurn(second)
	t.Cleanup(func() { chat.Streams.Delete(second); chat.ClearQueue(second) })
	if code, _ := send(e, second); code != 202 {
		t.Errorf("another chat was starved: %d", code)
	}

	now = now.Add(time.Minute)
	if code, _ := send(other, e.shared); code != 202 {
		t.Errorf("after a refill interval = %d", code)
	}
	if code, _ := send(other, e.shared); code != 429 {
		t.Errorf("the refill gave back more than one prompt: %d", code)
	}
}
