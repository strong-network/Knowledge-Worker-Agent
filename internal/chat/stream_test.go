// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"context"
	"testing"
	"time"
)

func TestStream_AppendAndSnapshot(t *testing.T) {
	s := newStream("sid1")
	if got, done := s.Snapshot(); len(got) != 0 || done {
		t.Fatalf("fresh stream should be empty/not-done: got=%v done=%v", got, done)
	}

	s.Append(map[string]any{"type": "chunk", "text": "hi"})
	s.Append(map[string]any{"type": "chunk", "text": " there"})

	got, done := s.Snapshot()
	if done {
		t.Errorf("expected not done")
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %d", len(got))
	}
	if got[0]["text"] != "hi" || got[1]["text"] != " there" {
		t.Errorf("unexpected events: %+v", got)
	}

	s.Finish()
	if _, d := s.Snapshot(); !d {
		t.Errorf("expected done after Finish")
	}

	// Finish is idempotent
	s.Finish()
}

func TestStream_WaitReturnsBufferedEvents(t *testing.T) {
	s := newStream("sid")
	s.Append(map[string]any{"type": "chunk", "text": "a"})
	s.Append(map[string]any{"type": "chunk", "text": "b"})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	batch, idx, done := s.Wait(ctx, 0)
	if done {
		t.Errorf("expected not done")
	}
	if idx != 2 || len(batch) != 2 {
		t.Errorf("expected idx=2 len=2, got idx=%d len=%d", idx, len(batch))
	}

	// From idx=2, blocks until appended → use a goroutine to append.
	go func() {
		time.Sleep(20 * time.Millisecond)
		s.Append(map[string]any{"type": "chunk", "text": "c"})
	}()
	batch, idx, done = s.Wait(ctx, 2)
	if done {
		t.Errorf("expected not done after append")
	}
	if idx != 3 || len(batch) != 1 || batch[0]["text"] != "c" {
		t.Errorf("expected single 'c' batch, got idx=%d batch=%+v", idx, batch)
	}
}

func TestStream_WaitReturnsOnFinish(t *testing.T) {
	s := newStream("sid")
	go func() {
		time.Sleep(20 * time.Millisecond)
		s.Finish()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	batch, idx, done := s.Wait(ctx, 0)
	if !done {
		t.Errorf("expected done")
	}
	if len(batch) != 0 {
		t.Errorf("expected no batch, got %+v", batch)
	}
	if idx != 0 {
		t.Errorf("expected idx=0, got %d", idx)
	}
}

func TestStream_WaitReturnsOnContextCancel(t *testing.T) {
	s := newStream("sid")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	batch, idx, done := s.Wait(ctx, 0)
	if done {
		t.Errorf("did not expect done on cancel")
	}
	if batch != nil {
		t.Errorf("expected nil batch on cancel, got %+v", batch)
	}
	if idx != 0 {
		t.Errorf("expected idx=0, got %d", idx)
	}
}

func TestIsStreamingAndGetStream(t *testing.T) {
	const sid = "test-streaming-flag"
	defer Streams.Delete(sid)

	if IsStreaming(sid) {
		t.Errorf("expected no stream initially")
	}
	if GetStream(sid) != nil {
		t.Errorf("expected GetStream nil initially")
	}

	s := newStream(sid)
	Streams.Store(sid, s)
	if !IsStreaming(sid) {
		t.Errorf("expected IsStreaming=true while live")
	}
	if GetStream(sid) != s {
		t.Errorf("GetStream should return the stored stream")
	}

	s.Finish()
	if IsStreaming(sid) {
		t.Errorf("expected IsStreaming=false after Finish")
	}
	// GetStream still returns the finished stream so reconnects can replay.
	if GetStream(sid) != s {
		t.Errorf("GetStream should still return finished stream for replay")
	}
}

func TestStream_MultipleSubscribersSeeAllEvents(t *testing.T) {
	s := newStream("sid")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	collect := func(out *[]map[string]any, doneCh chan struct{}) {
		idx := 0
		for {
			batch, newIdx, done := s.Wait(ctx, idx)
			*out = append(*out, batch...)
			idx = newIdx
			if done {
				close(doneCh)
				return
			}
		}
	}

	var a, b []map[string]any
	dA, dB := make(chan struct{}), make(chan struct{})
	go collect(&a, dA)
	go collect(&b, dB)

	// Give subscribers a chance to start waiting.
	time.Sleep(10 * time.Millisecond)
	s.Append(map[string]any{"type": "chunk", "text": "1"})
	s.Append(map[string]any{"type": "chunk", "text": "2"})
	time.Sleep(10 * time.Millisecond)
	s.Finish()

	<-dA
	<-dB
	if len(a) != 2 || len(b) != 2 {
		t.Errorf("both subscribers should see 2 events, got a=%d b=%d", len(a), len(b))
	}
	if a[0]["text"] != "1" || a[1]["text"] != "2" {
		t.Errorf("unexpected order in a: %+v", a)
	}
}
