// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"context"
	"sync"
)

// turnWaiter parks followers of a session with no stream yet; OpenTurn fills in
// the stream it opened before closing ch.
type turnWaiter struct {
	ch chan struct{}
	s  *Stream
}

// turnMu makes "no current stream, so wait" in FollowTurns atomic with the
// store in OpenTurn, so a session's first turn cannot slip between them.
var (
	turnMu   sync.Mutex
	turnWake = map[string]*turnWaiter{}
)

// Author is who wrote a prompt. The zero value is the workspace owner.
type Author struct {
	ID   string
	Name string
}

// guestName is the name the model is told wrote the prompt, "" for the owner.
func (a Author) guestName() string {
	if a.ID == "" {
		return ""
	}
	return a.Name
}

// OpenTurn registers a fresh stream as the session's current turn. Any previous
// stream is linked to it and finished, so its subscribers exit and followers
// walk on to the new turn.
func OpenTurn(sessionID string) *Stream { return OpenTurnFor(sessionID, "", Author{}) }

// OpenTurnFor is OpenTurn for a turn whose prompt and author are known. Both
// are set before the stream is published, so followers can read them freely.
func OpenTurnFor(sessionID, prompt string, author Author) *Stream {
	return openTurn(sessionID, NewUUID(), prompt, author)
}

func openTurn(sessionID, turnID, prompt string, author Author) *Stream {
	s := newStream(sessionID)
	s.TurnID, s.Prompt, s.Author = turnID, prompt, author
	turnMu.Lock()
	prev, _ := Streams.Swap(sessionID, s)
	w := turnWake[sessionID]
	delete(turnWake, sessionID)
	turnMu.Unlock()

	if p, ok := prev.(*Stream); ok {
		p.linkNext(s)
		p.Finish()
	}
	if w != nil {
		w.s = s
		close(w.ch)
	}
	return s
}

// FollowTurns calls fn for each turn of the session in order, exactly once: the
// current turn if it is still running, then every turn opened afterwards, even
// ones that open and finish before the follower gets to run. fn should consume
// the turn until it is done. Returns when ctx ends or fn returns false.
//
// ready, if set, runs once the follower's place in the chain is fixed and
// before any turn is handed over; returning false stops. Announcing readiness
// any earlier leaves a gap in which a quick turn is skipped as "already over".
func FollowTurns(ctx context.Context, sessionID string, ready func() bool, fn func(*Stream) bool) {
	turnMu.Lock()
	cur := GetStream(sessionID)
	var w *turnWaiter
	if cur == nil {
		if w = turnWake[sessionID]; w == nil {
			w = &turnWaiter{ch: make(chan struct{})}
			turnWake[sessionID] = w
		}
	}
	turnMu.Unlock()

	if ready != nil && !ready() {
		return
	}
	if cur == nil {
		select {
		case <-ctx.Done():
			return
		case <-w.ch:
		}
		cur = w.s
	} else if _, done := cur.Snapshot(); done {
		// Finished before we arrived: it is in history, not ours to replay.
		if cur = cur.waitNext(ctx); cur == nil {
			return
		}
	}
	for {
		if !fn(cur) {
			return
		}
		if cur = cur.waitNext(ctx); cur == nil {
			return
		}
	}
}

func (s *Stream) linkNext(n *Stream) {
	s.mu.Lock()
	if s.next != nil {
		s.mu.Unlock()
		return
	}
	s.next = n
	ch := s.nextOpen
	s.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// waitNext blocks until the turn after s opens, returning nil if ctx ends first.
func (s *Stream) waitNext(ctx context.Context) *Stream {
	s.mu.Lock()
	n, ch := s.next, s.nextOpen
	s.mu.Unlock()
	if n != nil {
		return n
	}
	if ch == nil {
		// A Stream built as a literal (tests) is never linked.
		<-ctx.Done()
		return nil
	}
	select {
	case <-ctx.Done():
		return nil
	case <-ch:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.next
}
