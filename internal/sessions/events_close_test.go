// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// eventsServerWith serves /events through ServeEvents with the given options,
// alongside the real delete route, so tests can end a chat the way users do.
func eventsServerWith(t *testing.T, opts EventsOptions) (*httptest.Server, string) {
	t.Helper()
	setupTestDB(t)
	clearStreams()
	sid := chat.NewUUID()
	if err := db.CreateSession(sid, config.DefaultSessionConfig()); err != nil {
		t.Fatalf("create session: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/{session_id}/events", func(w http.ResponseWriter, r *http.Request) {
		ServeEvents(w, r, r.PathValue("session_id"), opts)
	})
	mux.HandleFunc("DELETE /api/sessions/{session_id}", HandleDeleteSession)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
		chat.Streams.Delete(sid)
	})
	return srv, sid
}

// closedAfter reads frames until the server ends the connection and returns
// the last one.
func (c *sseClient) closedAfter() sseFrame {
	c.t.Helper()
	var last sseFrame
	for {
		select {
		case f, ok := <-c.frames:
			if !ok {
				return last
			}
			if f.Comment == "" {
				last = f
			}
		case <-time.After(3 * time.Second):
			c.t.Fatal("connection was not ended")
		}
	}
}

func TestEventsDeletingTheChatEndsItsViewers(t *testing.T) {
	srv, sid := eventsServerWith(t, EventsOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := dialEvents(t, ctx, srv, sid)
	c.expect(1)

	req, _ := http.NewRequest("DELETE", srv.URL+"/api/sessions/"+sid, nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if last := c.closedAfter(); last.Event != "session_deleted" {
		t.Fatalf("owner's last frame was %q, want session_deleted", last.Event)
	}
}

func TestEventsGuestOnlyCloseLeavesTheOwnerConnected(t *testing.T) {
	setupTestDB(t)
	clearStreams()
	sid := chat.NewUUID()
	if err := db.CreateSession(sid, config.DefaultSessionConfig()); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /owner/{session_id}", func(w http.ResponseWriter, r *http.Request) {
		ServeEvents(w, r, r.PathValue("session_id"), EventsOptions{})
	})
	mux.HandleFunc("GET /guest/{session_id}", func(w http.ResponseWriter, r *http.Request) {
		ServeEvents(w, r, r.PathValue("session_id"), EventsOptions{GuestID: "g-1"})
	})
	srv := httptest.NewServer(mux)
	defer func() { srv.CloseClientConnections(); srv.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dial := func(path string) *sseClient {
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+path+sid, nil)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		c := &sseClient{t: t, resp: resp, frames: make(chan sseFrame, 64), presence: make(chan sseFrame, 64), queue: make(chan sseFrame, 64)}
		go readFrames(c)
		return c
	}
	owner, guest := dial("/owner/"), dial("/guest/")
	owner.expect(1)
	guest.expect(1)

	if n := chat.CloseViewers(sid, "sharing_ended", ""); n != 1 {
		t.Fatalf("closed %d viewers, want just the guest", n)
	}
	if last := guest.closedAfter(); last.Event != "sharing_ended" {
		t.Fatalf("guest's last frame %q", last.Event)
	}
	s := chat.OpenTurn(sid)
	chunk(s, "still here")
	s.Finish()
	sameSeq(t, "owner", owner.expect(2), []string{"turn_start", "chunk:still here"})
}

func TestEventsWithdrawnAccessEndsWithSharingEnded(t *testing.T) {
	prev := eventsKeepalive
	eventsKeepalive = 20 * time.Millisecond
	t.Cleanup(func() { eventsKeepalive = prev })

	var allowed atomic.Bool
	allowed.Store(true)
	srv, sid := eventsServerWith(t, EventsOptions{GuestID: "g-1", StillAllowed: allowed.Load})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := dialEvents(t, ctx, srv, sid)
	c.expect(1)

	allowed.Store(false)
	if last := c.closedAfter(); last.Event != "sharing_ended" {
		t.Fatalf("last frame %q, want sharing_ended", last.Event)
	}
}

func TestEventsTurnStartSaysWhoAskedAndWhat(t *testing.T) {
	srv, sid := eventsServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := dialEvents(t, ctx, srv, sid)
	c.expect(1)

	s := chat.OpenTurnFor(sid, "what's the plan?", chat.Author{ID: "g-1", Name: "Sarah"})
	s.Finish()
	f := c.next()
	if f.Event != "turn_start" || f.Data["turn_id"] != s.TurnID || f.Data["prompt"] != "what's the plan?" || f.Data["author_id"] != "g-1" || f.Data["author_name"] != "Sarah" {
		t.Fatalf("turn_start = %+v", f.Data)
	}

	owner := chat.OpenTurnFor(sid, "mine", chat.Author{})
	owner.Finish()
	f = c.next()
	if v, ok := f.Data["author_id"]; !ok || v != nil {
		t.Fatalf("owner turn_start author_id = %v (present=%t), want explicit null", v, ok)
	}
}

func TestEventsFilterAppliesToTurnFramesOnly(t *testing.T) {
	srv, sid := eventsServerWith(t, EventsOptions{
		GuestID: "g-1",
		Filter: func(ev map[string]any) (map[string]any, bool) {
			if ev["text"] == "secret" {
				return nil, false
			}
			return ev, true
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := dialEvents(t, ctx, srv, sid)
	sameSeq(t, "c", c.expect(1), []string{"ready"})

	s := chat.OpenTurn(sid)
	chunk(s, "secret")
	chunk(s, "public")
	s.Finish()
	sameSeq(t, "c", c.expect(2), []string{"turn_start", "chunk:public"})
}
