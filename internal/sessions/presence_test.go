// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// presenceServer serves owner connections and guest connections for any guest
// id, the way the main and guest listeners each call ServeEvents.
func presenceServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	setupTestDB(t)
	clearStreams()
	sid := chat.NewUUID()
	if err := db.CreateSession(sid, config.DefaultSessionConfig()); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /owner/{sid}", func(w http.ResponseWriter, r *http.Request) {
		ServeEvents(w, r, r.PathValue("sid"), EventsOptions{})
	})
	mux.HandleFunc("GET /guest/{guest}/{sid}", func(w http.ResponseWriter, r *http.Request) {
		ServeEvents(w, r, r.PathValue("sid"), EventsOptions{GuestID: r.PathValue("guest")})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	return srv, sid
}

func dialRaw(t *testing.T, ctx context.Context, url string, splitPresence bool) *sseClient {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("dial %s: %v", url, err)
	}
	c := &sseClient{t: t, resp: resp, frames: make(chan sseFrame, 64)}
	if splitPresence {
		c.presence = make(chan sseFrame, 64)
		c.queue = make(chan sseFrame, 64)
	}
	go readFrames(c)
	return c
}

func (c *sseClient) nextPresence() []string {
	c.t.Helper()
	select {
	case f := <-c.presence:
		raw, _ := f.Data["guests"].([]any)
		out := []string{}
		for _, g := range raw {
			out = append(out, g.(string))
		}
		return out
	case <-time.After(2 * time.Second):
		c.t.Fatal("no presence frame")
	}
	return nil
}

// The owner learns who is connected, once per guest however many tabs
// they have, first right after ready and then on every change.
func TestOwnerSeesGuestsComeAndGo(t *testing.T) {
	srv, sid := presenceServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Unsplit, so the order between ready and presence is visible.
	raw := dialRaw(t, ctx, srv.URL+"/owner/"+sid, false)
	if f := raw.next(); f.Event != "ready" {
		t.Fatalf("first frame %q, want ready", f.Event)
	}
	if f := raw.next(); f.Event != "presence" {
		t.Fatalf("second frame %q, want presence", f.Event)
	}
	if f := raw.next(); f.Event != "queue" {
		t.Fatalf("third frame %q, want queue", f.Event)
	}

	owner := dialRaw(t, ctx, srv.URL+"/owner/"+sid, true)
	owner.expect(1) // ready
	sameSeq(t, "initial", owner.nextPresence(), []string{})

	gctx1, leave1 := context.WithCancel(ctx)
	tab1 := dialRaw(t, gctx1, srv.URL+"/guest/g-1/"+sid, false)
	tab1.expect(1)
	sameSeq(t, "g-1 joins", owner.nextPresence(), []string{"g-1"})

	gctx2, leave2 := context.WithCancel(ctx)
	tab2 := dialRaw(t, gctx2, srv.URL+"/guest/g-1/"+sid, false)
	tab2.expect(1)
	sameSeq(t, "g-1's second tab counts once", owner.nextPresence(), []string{"g-1"})

	leave1()
	sameSeq(t, "one tab left", owner.nextPresence(), []string{"g-1"})
	leave2()
	sameSeq(t, "both tabs left", owner.nextPresence(), []string{})

	// A second owner tab is not a guest.
	if got := chat.Present(sid); len(got) != 0 {
		t.Errorf("owner connections counted as present: %v", got)
	}
}

// Presence is the owner's to know. A guest never receives it.
func TestGuestsNeverReceivePresence(t *testing.T) {
	srv, sid := presenceServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := dialRaw(t, ctx, srv.URL+"/guest/g-1/"+sid, false)
	g.expect(1)
	other := dialRaw(t, ctx, srv.URL+"/guest/g-2/"+sid, false)
	other.expect(1)
	s := chat.OpenTurn(sid)
	chunk(s, "hi")
	s.Finish()
	for _, c := range []*sseClient{g, other} {
		for _, name := range c.expect(2) {
			if name == "presence" {
				t.Fatal("a guest received a presence frame")
			}
		}
	}
}

func TestDropGuestPromptsKeepsTheOwners(t *testing.T) {
	sid := chat.NewUUID()
	t.Cleanup(func() { chat.ClearQueue(sid) })
	chat.EnqueuePrompt(sid, "owner first")
	chat.EnqueuePrompt(sid, "guest", chat.Turn{Author: chat.Author{ID: "g-1", Name: "Sarah"}})
	chat.EnqueuePrompt(sid, "owner second")
	if n := chat.DropGuestPrompts(sid); n != 1 {
		t.Errorf("dropped %d, want 1", n)
	}
	q := chat.SnapshotQueue(sid)
	if len(q) != 2 || q[0].Prompt != "owner first" || q[1].Prompt != "owner second" {
		t.Errorf("queue after drop: %+v", q)
	}
}

func (c *sseClient) nextQueue() []string {
	c.t.Helper()
	select {
	case f := <-c.queue:
		raw, _ := f.Data["queue"].([]any)
		out := []string{}
		for _, it := range raw {
			m := it.(map[string]any)
			who, _ := m["author_name"].(string)
			out = append(out, who+":"+m["prompt"].(string))
		}
		return out
	case <-time.After(2 * time.Second):
		c.t.Fatal("no queue frame")
	}
	return nil
}

// A guest's queued prompt reaches the owner's tab as it happens, author and
// all, and so does its leaving the queue; guests never get the queue.
func TestOwnerSeesTheQueueChange(t *testing.T) {
	srv, sid := presenceServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(func() { chat.ClearQueue(sid) })
	owner := dialRaw(t, ctx, srv.URL+"/owner/"+sid, true)
	owner.expect(1)
	sameSeq(t, "initial queue", owner.nextQueue(), []string{})
	guest := dialRaw(t, ctx, srv.URL+"/guest/g-1/"+sid, false)
	guest.expect(1)
	owner.nextPresence()

	chat.EnqueuePrompt(sid, "what next?", chat.Turn{Author: chat.Author{ID: "g-1", Name: "Sarah"}})
	sameSeq(t, "guest queued", owner.nextQueue(), []string{"Sarah:what next?"})
	chat.DropGuestPrompts(sid)
	sameSeq(t, "dropped", owner.nextQueue(), []string{})

	s := chat.OpenTurn(sid)
	chunk(s, "hi")
	s.Finish()
	for _, name := range guest.expect(2) {
		if name == "queue" || name == "presence" {
			t.Fatal("a guest received an owner signal")
		}
	}
}
