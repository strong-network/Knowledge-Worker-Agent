// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

func TestEnqueueAndSnapshot(t *testing.T) {
	resetQueuesForTest()
	sid := "sess-q1"

	if got := SnapshotQueue(sid); len(got) != 0 {
		t.Fatalf("fresh queue should be empty, got %v", got)
	}
	if QueueLen(sid) != 0 {
		t.Fatalf("fresh QueueLen should be 0")
	}

	a := EnqueuePrompt(sid, "first")
	b := EnqueuePrompt(sid, "second")

	if a.ID == "" || b.ID == "" || a.ID == b.ID {
		t.Errorf("expected unique non-empty IDs, got %q / %q", a.ID, b.ID)
	}
	if a.Prompt != "first" || b.Prompt != "second" {
		t.Errorf("unexpected prompts: %q / %q", a.Prompt, b.Prompt)
	}
	if a.EnqueuedAt.IsZero() {
		t.Errorf("EnqueuedAt should be set")
	}

	got := SnapshotQueue(sid)
	if len(got) != 2 || got[0].Prompt != "first" || got[1].Prompt != "second" {
		t.Fatalf("expected [first,second], got %+v", got)
	}
	if QueueLen(sid) != 2 {
		t.Errorf("expected QueueLen=2, got %d", QueueLen(sid))
	}
}

func TestSnapshotIsCopy(t *testing.T) {
	resetQueuesForTest()
	sid := "sess-q2"
	EnqueuePrompt(sid, "a")
	snap := SnapshotQueue(sid)
	snap[0].Prompt = "mutated"

	again := SnapshotQueue(sid)
	if again[0].Prompt != "a" {
		t.Errorf("snapshot should be a defensive copy; live store mutated to %q", again[0].Prompt)
	}
}

func TestRemoveQueuedAt(t *testing.T) {
	resetQueuesForTest()
	sid := "sess-q3"
	EnqueuePrompt(sid, "a")
	EnqueuePrompt(sid, "b")
	EnqueuePrompt(sid, "c")

	if !RemoveQueuedAt(sid, 1) {
		t.Fatalf("expected removal of index 1 to succeed")
	}
	got := SnapshotQueue(sid)
	if len(got) != 2 || got[0].Prompt != "a" || got[1].Prompt != "c" {
		t.Errorf("after removing 'b', expected [a,c], got %+v", got)
	}

	if RemoveQueuedAt(sid, 99) {
		t.Errorf("out-of-bounds removal should return false")
	}
	if RemoveQueuedAt(sid, -1) {
		t.Errorf("negative index removal should return false")
	}
}

func TestRemoveQueuedByID(t *testing.T) {
	resetQueuesForTest()
	sid := "sess-q4"
	a := EnqueuePrompt(sid, "alpha")
	b := EnqueuePrompt(sid, "beta")
	_ = b

	if !RemoveQueuedByID(sid, a.ID) {
		t.Fatalf("expected removal by id to succeed")
	}
	if QueueLen(sid) != 1 {
		t.Errorf("expected len=1 after remove, got %d", QueueLen(sid))
	}
	if RemoveQueuedByID(sid, "nope") {
		t.Errorf("removing unknown id should return false")
	}
	if RemoveQueuedByID(sid, a.ID) {
		t.Errorf("removing already-removed id should return false")
	}
}

func TestClearQueue(t *testing.T) {
	resetQueuesForTest()
	sid := "sess-q5"
	EnqueuePrompt(sid, "a")
	EnqueuePrompt(sid, "b")
	ClearQueue(sid)
	if QueueLen(sid) != 0 {
		t.Errorf("expected empty after clear, got len=%d", QueueLen(sid))
	}
	// Clearing an unknown session should not panic.
	ClearQueue("never-existed")
}

func TestPopNextFIFO(t *testing.T) {
	resetQueuesForTest()
	sid := "sess-q6"
	EnqueuePrompt(sid, "1")
	EnqueuePrompt(sid, "2")
	EnqueuePrompt(sid, "3")

	first, ok := popNext(sid)
	if !ok || first.Prompt != "1" {
		t.Fatalf("expected first pop=1, got %+v ok=%v", first, ok)
	}
	second, ok := popNext(sid)
	if !ok || second.Prompt != "2" {
		t.Fatalf("expected second pop=2, got %+v ok=%v", second, ok)
	}
	third, ok := popNext(sid)
	if !ok || third.Prompt != "3" {
		t.Fatalf("expected third pop=3, got %+v ok=%v", third, ok)
	}
	_, ok = popNext(sid)
	if ok {
		t.Errorf("empty queue should pop ok=false")
	}
}

func TestQueuesIsolatedPerSession(t *testing.T) {
	resetQueuesForTest()
	EnqueuePrompt("s-A", "msg-A")
	EnqueuePrompt("s-B", "msg-B1")
	EnqueuePrompt("s-B", "msg-B2")

	if QueueLen("s-A") != 1 || QueueLen("s-B") != 2 {
		t.Errorf("expected A=1 B=2, got A=%d B=%d", QueueLen("s-A"), QueueLen("s-B"))
	}
	if SnapshotQueue("s-A")[0].Prompt != "msg-A" {
		t.Errorf("session A queue corrupted")
	}
	if SnapshotQueue("s-other") != nil && len(SnapshotQueue("s-other")) != 0 {
		t.Errorf("unknown session should have empty queue")
	}
}

func TestSendOrEnqueueWhileStreaming(t *testing.T) {
	resetQueuesForTest()
	sid := "sess-streaming"
	defer Streams.Delete(sid)

	// Simulate a live stream by storing a non-finished Stream in Streams.
	live := newStream(sid)
	Streams.Store(sid, live)
	defer live.Finish()

	stream, queued := SendOrEnqueue(sid, "queued prompt", config.SessionConfig{})
	if stream != nil {
		t.Fatalf("expected nil stream when already streaming")
	}
	if queued == nil {
		t.Fatalf("expected non-nil queued result")
	}
	if queued.Prompt != "queued prompt" {
		t.Errorf("queued.Prompt = %q, want %q", queued.Prompt, "queued prompt")
	}
	if QueueLen(sid) != 1 {
		t.Errorf("expected QueueLen=1, got %d", QueueLen(sid))
	}
}

func TestStartNextQueuedNoOpWhileStreaming(t *testing.T) {
	resetQueuesForTest()
	sid := "sess-noop"
	defer Streams.Delete(sid)

	live := newStream(sid)
	Streams.Store(sid, live)
	defer live.Finish()

	EnqueuePrompt(sid, "p1")

	if got := StartNextQueued(sid); got != nil {
		t.Errorf("StartNextQueued should be no-op while streaming; got %v", got)
	}
	if QueueLen(sid) != 1 {
		t.Errorf("queue should remain unchanged, got len=%d", QueueLen(sid))
	}
}

func TestStartNextQueuedEmpty(t *testing.T) {
	resetQueuesForTest()
	if got := StartNextQueued("never-queued"); got != nil {
		t.Errorf("StartNextQueued on empty queue should return nil, got %v", got)
	}
}
