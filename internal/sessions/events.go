// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessions

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// eventsKeepalive is how often an /events connection writes an SSE comment, so
// proxies keep an idle connection open and a vanished client is noticed.
var eventsKeepalive = 25 * time.Second

// EventsOptions shapes one viewer's /events connection.
type EventsOptions struct {
	// GuestID is empty for the owner.
	GuestID string
	// Filter rewrites or drops each frame of a turn before it is written; nil
	// passes frames unchanged. The connection's own frames are not filtered.
	Filter func(map[string]any) (map[string]any, bool)
	// StillAllowed is re-checked on every keepalive; false ends the connection
	// with sharing_ended, so access withdrawn by any route is noticed.
	StillAllowed func() bool
	// Options is a guest's share_options frame: sent after ready, then again
	// whenever the owner changes what guests may do.
	Options func() map[string]any
}

// HandleSessionEvents holds one long-lived connection per viewer of a session.
// It sends `ready`, then for every turn that runs while the viewer is
// connected, including one already running, `turn_start` followed by that
// turn's frames from its first event. A turn finished before connect is not
// replayed: it is in history.
func HandleSessionEvents(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	if !db.SessionExists(sid) {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}
	ServeEvents(w, r, sid, EventsOptions{})
}

// ServeEvents runs an /events connection for a session the caller has already
// authorised. When the viewer is closed (chat.CloseViewers, or StillAllowed
// turning false) it sends a final frame whose type is the reason, then ends.
func ServeEvents(w http.ResponseWriter, r *http.Request, sid string, opts EventsOptions) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Connection", "keep-alive")

	viewer := chat.AddViewer(sid, opts.GuestID)
	defer chat.RemoveViewer(sid, viewer)

	ctx, cancel := context.WithCancel(r.Context())
	out := &sseConn{w: w, flusher: flusher, cancel: cancel, filter: opts.Filter}
	var bg sync.WaitGroup
	bg.Add(2)
	go func() {
		defer bg.Done()
		select {
		case <-viewer.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	go func() {
		defer bg.Done()
		out.keepalive(ctx, eventsKeepalive, func() {
			if opts.StillAllowed != nil && !opts.StillAllowed() {
				viewer.Close("sharing_ended")
			}
		})
	}()

	log.Printf("[SSE] session=%s events attach guest=%t", sid, opts.GuestID != "")
	if opts.GuestID == "" {
		bg.Add(1)
		go func() {
			defer bg.Done()
			out.ownerSignals(ctx, sid, viewer)
		}()
	} else if opts.Options != nil {
		bg.Add(1)
		go func() {
			defer bg.Done()
			out.guestSignals(ctx, viewer, opts.Options)
		}()
	}
	chat.FollowTurns(ctx, sid,
		func() bool {
			ok := out.send(map[string]any{"type": "ready", "session_id": sid})
			out.readyOnce()
			return ok
		},
		func(s *chat.Stream) bool { return out.send(turnStart(sid, s)) && out.forward(ctx, s) },
	)
	cancel()
	bg.Wait()
	if reason := viewer.Reason(); reason != "" && r.Context().Err() == nil {
		out.send(map[string]any{"type": reason, "session_id": sid})
	}
	log.Printf("[SSE] session=%s events detach reason=%q", sid, viewer.Reason())
}

// turnStart announces a turn: who asked and what, so a viewer can show the
// question before the first chunk of the answer. A null author is the owner.
func turnStart(sid string, s *chat.Stream) map[string]any {
	ev := map[string]any{"type": "turn_start", "session_id": sid, "turn_id": s.TurnID, "prompt": s.Prompt, "author_id": nil, "author_name": nil}
	if s.Author.ID != "" {
		ev["author_id"], ev["author_name"] = s.Author.ID, s.Author.Name
	}
	return ev
}

// sseConn serialises writes from the handler and its keepalive goroutine.
type sseConn struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	flusher http.Flusher
	cancel  context.CancelFunc
	filter  func(map[string]any) (map[string]any, bool)

	readyMu sync.Once
	ready   chan struct{}
}

func (c *sseConn) readyOnce() { c.readyMu.Do(func() { close(c.readyChan()) }) }

func (c *sseConn) readyChan() chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ready == nil {
		c.ready = make(chan struct{})
	}
	return c.ready
}

// ownerSignals tells an owner connection what it cannot see from turns: which
// guests are connected and what is queued, a guest's prompts included.
// Each is sent once after ready, so the first frame the owner reads is ready,
// then again on every change. Guests never get either.
func (c *sseConn) ownerSignals(ctx context.Context, sid string, v *chat.Viewer) {
	select {
	case <-c.readyChan():
	case <-ctx.Done():
		return
	}
	presence := func() bool {
		return c.send(map[string]any{"type": "presence", "session_id": sid, "guests": chat.Present(sid)})
	}
	queue := func() bool {
		return c.send(map[string]any{"type": "queue", "session_id": sid, "queue": chat.SnapshotQueue(sid)})
	}
	if !presence() || !queue() {
		return
	}
	for {
		ok := true
		select {
		case <-v.Changed():
			ok = presence()
		case <-v.Queued():
			ok = queue()
		case <-ctx.Done():
			return
		}
		if !ok {
			return
		}
	}
}

func (c *sseConn) write(b []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.w.Write(b); err != nil {
		c.cancel()
		return false
	}
	c.flusher.Flush()
	return true
}

// guestSignals tells a guest connection what the owner allows, once after
// ready and again on every change, so a toggle reaches pages already open.
func (c *sseConn) guestSignals(ctx context.Context, v *chat.Viewer, options func() map[string]any) {
	select {
	case <-c.readyChan():
	case <-ctx.Done():
		return
	}
	for {
		if !c.send(options()) {
			return
		}
		select {
		case <-v.Options():
		case <-ctx.Done():
			return
		}
	}
}

func (c *sseConn) send(ev map[string]any) bool { return c.write(formatSSE(ev)) }

// keepalive writes a comment every interval, running check first.
func (c *sseConn) keepalive(ctx context.Context, every time.Duration, check func()) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
			if ctx.Err() != nil || !c.write([]byte(": keepalive\n\n")) {
				return
			}
		}
	}
}

// forward writes a turn's frames, filtered, from its first event until it
// finishes. It reports false when the viewer has gone.
func (c *sseConn) forward(ctx context.Context, s *chat.Stream) bool {
	idx := 0
	for {
		batch, next, done := s.Wait(ctx, idx)
		if batch == nil && !done {
			return false
		}
		idx = next
		for _, ev := range batch {
			if c.filter != nil {
				var keep bool
				if ev, keep = c.filter(ev); !keep {
					continue
				}
			}
			if !c.send(ev) {
				return false
			}
		}
		if done {
			return true
		}
	}
}
