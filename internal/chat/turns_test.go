// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// follow runs FollowTurns in the background and records every turn it is handed,
// consuming each until done. entered receives once per turn, before the gate.
func follow(t *testing.T, ctx context.Context, sid string, gate <-chan struct{}) (got func() []*Stream, entered <-chan struct{}, stopped <-chan struct{}) {
	t.Helper()
	var mu sync.Mutex
	var turns []*Stream
	done := make(chan struct{})
	in := make(chan struct{}, 16)
	go func() {
		defer close(done)
		FollowTurns(ctx, sid, nil, func(s *Stream) bool {
			in <- struct{}{}
			if gate != nil {
				<-gate
			}
			mu.Lock()
			turns = append(turns, s)
			mu.Unlock()
			for idx := 0; ; {
				_, next, fin := s.Wait(ctx, idx)
				if fin {
					return true
				}
				if ctx.Err() != nil {
					return false
				}
				idx = next
			}
		})
	}()
	return func() []*Stream {
		mu.Lock()
		defer mu.Unlock()
		return append([]*Stream(nil), turns...)
	}, in, done
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestOpenTurnFinishesAndLinksThePreviousTurn(t *testing.T) {
	sid := "turns-link"
	t.Cleanup(func() { Streams.Delete(sid) })

	first := OpenTurn(sid)
	second := OpenTurn(sid)
	if _, done := first.Snapshot(); !done {
		t.Error("previous turn was not finished, so its subscribers would hang")
	}
	if GetStream(sid) != second {
		t.Error("GetStream does not return the newly opened turn")
	}
	if n := first.waitNext(context.Background()); n != second {
		t.Error("previous turn is not linked to its successor")
	}
}

// The failure a wake-then-reread design has: turns that open and finish while
// the follower is busy must each still be delivered, once, in order.
func TestFollowTurnsDeliversEveryTurnEvenWhenTheFollowerIsSlow(t *testing.T) {
	sid := "turns-slow"
	t.Cleanup(func() { Streams.Delete(sid) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := OpenTurn(sid)
	gate := make(chan struct{})
	got, entered, _ := follow(t, ctx, sid, gate)
	<-entered // the follower holds the first turn and is now stuck on the gate

	// While it is stuck, three more turns come and go.
	first.Finish()
	var want = []*Stream{first}
	for range 3 {
		s := OpenTurn(sid)
		s.Append(map[string]any{"type": "chunk"})
		s.Finish()
		want = append(want, s)
	}
	close(gate)

	waitFor(t, "all four turns", func() bool { return len(got()) == len(want) })
	for i, s := range got() {
		if s != want[i] {
			t.Fatalf("turn %d delivered out of order or duplicated", i)
		}
	}
}

func TestFollowTurnsReplaysARunningTurnButNotAFinishedOne(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	running := "turns-running"
	t.Cleanup(func() { Streams.Delete(running) })
	live := OpenTurn(running)
	got, _, _ := follow(t, ctx, running, nil)
	waitFor(t, "the running turn", func() bool { return len(got()) == 1 })
	if got()[0] != live {
		t.Fatal("a turn running at connect time was not handed over")
	}
	live.Finish()

	finished := "turns-finished"
	t.Cleanup(func() { Streams.Delete(finished) })
	old := OpenTurn(finished)
	old.Finish()
	got2, _, _ := follow(t, ctx, finished, nil)
	time.Sleep(20 * time.Millisecond)
	if len(got2()) != 0 {
		t.Fatal("a turn already finished at connect time was replayed")
	}
	fresh := OpenTurn(finished)
	waitFor(t, "the next turn", func() bool { return len(got2()) == 1 })
	if got2()[0] != fresh {
		t.Fatal("the turn after a finished one was not delivered")
	}
}

// A session with no stream at all: the first turn must reach a follower that
// is already waiting, even though there is no previous stream to link from.
func TestFollowTurnsCatchesASessionsFirstTurn(t *testing.T) {
	sid := "turns-first"
	t.Cleanup(func() { Streams.Delete(sid) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a, _, _ := follow(t, ctx, sid, nil)
	b, _, _ := follow(t, ctx, sid, nil)
	time.Sleep(20 * time.Millisecond) // let both park on the waiter
	s := OpenTurn(sid)
	waitFor(t, "both followers", func() bool { return len(a()) == 1 && len(b()) == 1 })
	if a()[0] != s || b()[0] != s {
		t.Fatal("followers were handed the wrong stream")
	}
}

// A turn that opens and finishes the instant ready() returns must still be
// delivered: ready means "from here on you see every turn". Announcing it
// before the follower's place is fixed skips such a turn as already over.
func TestFollowTurnsDeliversATurnThatFinishesRightAfterReady(t *testing.T) {
	for i := range 200 {
		sid := fmt.Sprintf("turns-ready-%d", i)
		ctx, cancel := context.WithCancel(context.Background())
		var got, opened *Stream
		done := make(chan struct{})
		go func() {
			defer close(done)
			FollowTurns(ctx, sid, func() bool {
				opened = OpenTurn(sid)
				opened.Finish()
				return true
			}, func(s *Stream) bool {
				got = s
				return false
			})
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			cancel()
			t.Fatalf("run %d: a turn that finished right after ready was never delivered", i)
		}
		cancel()
		Streams.Delete(sid)
		if got != opened {
			t.Fatalf("run %d: delivered the wrong turn", i)
		}
	}
}

func TestFollowTurnsReturnsWhenContextEnds(t *testing.T) {
	sid := "turns-cancel"
	t.Cleanup(func() { Streams.Delete(sid) })
	ctx, cancel := context.WithCancel(context.Background())

	_, _, stopped := follow(t, ctx, sid, nil)
	cancel()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("FollowTurns did not return after cancellation")
	}
}
