// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessions

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

type sseFrame struct {
	Event   string
	Data    map[string]any
	Comment string
}

// presence and queue frames are the owner's signals, independent of turns; the
// turn tests read past them and the signal tests read them on purpose.

// sseClient is one real HTTP connection to /events, parsed frame by frame.
type sseClient struct {
	t        *testing.T
	resp     *http.Response
	frames   chan sseFrame
	presence chan sseFrame
	queue    chan sseFrame
}

func dialEvents(t *testing.T, ctx context.Context, srv *httptest.Server, sid string) *sseClient {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/sessions/"+sid+"/events", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %q", ct)
	}
	c := &sseClient{t: t, resp: resp, frames: make(chan sseFrame, 64), presence: make(chan sseFrame, 64), queue: make(chan sseFrame, 64)}
	go readFrames(c)
	return c
}

// readFrames parses the connection's SSE stream into c.frames until it ends.
func readFrames(c *sseClient) {
	defer close(c.frames)
	r := bufio.NewReader(c.resp.Body)
	var f sseFrame
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "":
			if f.Event == "presence" && c.presence != nil {
				c.presence <- f
			} else if f.Event == "queue" && c.queue != nil {
				c.queue <- f
			} else if f.Event != "" || f.Data != nil || f.Comment != "" {
				c.frames <- f
			}
			f = sseFrame{}
		case strings.HasPrefix(line, ":"):
			f.Comment = strings.TrimSpace(line[1:])
		case strings.HasPrefix(line, "event: "):
			f.Event = line[len("event: "):]
		case strings.HasPrefix(line, "data: "):
			_ = json.Unmarshal([]byte(line[len("data: "):]), &f.Data)
		}
	}
}

// next returns the next non-comment frame.
func (c *sseClient) next() sseFrame {
	c.t.Helper()
	for {
		select {
		case f, ok := <-c.frames:
			if !ok {
				c.t.Fatal("connection closed")
			}
			if f.Comment != "" {
				continue
			}
			return f
		case <-time.After(2 * time.Second):
			c.t.Fatal("timed out waiting for a frame")
		}
	}
}

// expect reads frames and returns their event names plus chunk text, for
// comparing whole sequences.
func (c *sseClient) expect(n int) []string {
	c.t.Helper()
	var got []string
	for range n {
		f := c.next()
		s := f.Event
		if txt, ok := f.Data["text"].(string); ok {
			s += ":" + txt
		}
		got = append(got, s)
	}
	return got
}

func eventsServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	setupTestDB(t)
	clearStreams()
	sid := chat.NewUUID()
	if err := db.CreateSession(sid, config.DefaultSessionConfig()); err != nil {
		t.Fatalf("create session: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/{session_id}/events", HandleSessionEvents)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
		chat.Streams.Delete(sid)
	})
	return srv, sid
}

func chunk(s *chat.Stream, text string) {
	s.Append(map[string]any{"type": "chunk", "text": text})
}

func sameSeq(t *testing.T, who string, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s got %v, want %v", who, got, want)
	}
}

// The point of the event stream: two viewers, both idle when the turn starts, both see all of
// it live and in the same order.
func TestEventsTwoClientsWatchTheSameTurnLive(t *testing.T) {
	srv, sid := eventsServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := dialEvents(t, ctx, srv, sid)
	b := dialEvents(t, ctx, srv, sid)
	sameSeq(t, "a", a.expect(1), []string{"ready"})
	sameSeq(t, "b", b.expect(1), []string{"ready"})

	s := chat.OpenTurn(sid)
	chunk(s, "one")
	chunk(s, "two")
	s.Append(map[string]any{"type": "done"})
	s.Finish()

	want := []string{"turn_start", "chunk:one", "chunk:two", "done"}
	sameSeq(t, "a", a.expect(4), want)
	sameSeq(t, "b", b.expect(4), want)
}

func TestEventsJoiningMidTurnReplaysItFromTheStart(t *testing.T) {
	srv, sid := eventsServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := chat.OpenTurn(sid)
	chunk(s, "before")
	c := dialEvents(t, ctx, srv, sid)
	sameSeq(t, "late joiner", c.expect(3), []string{"ready", "turn_start", "chunk:before"})

	chunk(s, "after")
	s.Append(map[string]any{"type": "done"})
	s.Finish()
	sameSeq(t, "late joiner", c.expect(2), []string{"chunk:after", "done"})
}

// The queue chains turns back to back; each must arrive once, framed by its own
// turn_start, even though the second opens the moment the first finishes.
func TestEventsBackToBackTurnsArriveOnceEachInOrder(t *testing.T) {
	srv, sid := eventsServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := dialEvents(t, ctx, srv, sid)
	sameSeq(t, "c", c.expect(1), []string{"ready"})

	first := chat.OpenTurn(sid)
	chunk(first, "first")
	first.Finish()
	second := chat.OpenTurn(sid)
	chunk(second, "second")
	second.Finish()

	sameSeq(t, "c", c.expect(4), []string{"turn_start", "chunk:first", "turn_start", "chunk:second"})
}

func TestEventsDoesNotReplayATurnFinishedBeforeConnect(t *testing.T) {
	srv, sid := eventsServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	old := chat.OpenTurn(sid)
	chunk(old, "stale")
	old.Finish()

	c := dialEvents(t, ctx, srv, sid)
	sameSeq(t, "c", c.expect(1), []string{"ready"})
	fresh := chat.OpenTurn(sid)
	chunk(fresh, "fresh")
	fresh.Finish()
	sameSeq(t, "c", c.expect(2), []string{"turn_start", "chunk:fresh"})
}

func TestEventsSendsKeepalivesWhileIdle(t *testing.T) {
	prev := eventsKeepalive
	eventsKeepalive = 20 * time.Millisecond
	t.Cleanup(func() { eventsKeepalive = prev })

	srv, sid := eventsServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := dialEvents(t, ctx, srv, sid)
	c.expect(1)
	select {
	case f := <-c.frames:
		if f.Comment != "keepalive" {
			t.Fatalf("expected a keepalive comment, got %+v", f)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no keepalive while idle")
	}
}

func TestEventsHandlerReturnsWhenTheViewerLeaves(t *testing.T) {
	setupTestDB(t)
	clearStreams()
	sid := chat.NewUUID()
	if err := db.CreateSession(sid, config.DefaultSessionConfig()); err != nil {
		t.Fatalf("create session: %v", err)
	}
	returned := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/{session_id}/events", func(w http.ResponseWriter, r *http.Request) {
		HandleSessionEvents(w, r)
		close(returned)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c := dialEvents(t, ctx, srv, sid)
	c.expect(1)
	cancel()
	c.resp.Body.Close()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("handler kept running after the viewer disconnected")
	}
}

func TestEventsUnknownSessionIs404(t *testing.T) {
	setupTestDB(t)
	req := httptest.NewRequest("GET", "/api/sessions/nope/events", nil)
	req.SetPathValue("session_id", "nope")
	w := httptest.NewRecorder()
	HandleSessionEvents(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}
