// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer is a scriptable stand-in for `opencode serve`, exposing just the
// two endpoints RunTurn's streaming path uses: GET /event (SSE) and
// POST /session/{id}/prompt_async. Tests control the SSE frames and the prompt
// response.
//
// Ordering mirrors the real server: /event emits "server.connected" immediately
// (which fires RunTurn's ready signal), then waits for prompt_async to arrive
// before emitting the scripted assistant frames — the model doesn't reply until
// prompted.
type fakeServer struct {
	srv *httptest.Server

	// frames are written to the /event stream after the prompt is received, in
	// order, each followed by a flush. If holdOpen is true the handler blocks
	// (keeping the stream open) after writing all frames until the request
	// context is cancelled.
	frames   []string
	holdOpen bool

	// emitBeforePrompt, when true, writes frames immediately without waiting
	// for the prompt (used to exercise the no-events/abort paths).
	emitBeforePrompt bool

	// promptStatus is the HTTP status returned by prompt_async (default 204).
	promptStatus int
	// promptDelay delays the prompt_async response, used to widen the window
	// where the prompt goroutine is still in flight while the consumer exits.
	promptDelay time.Duration

	mu         sync.Mutex
	promptHits int
	eventHits  int
	prompted   chan struct{} // closed when the first prompt_async arrives
}

func newFakeServer() *fakeServer {
	f := &fakeServer{promptStatus: http.StatusNoContent, prompted: make(chan struct{})}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fakeServer) close() { f.srv.Close() }

func (f *fakeServer) client() *Client { return NewClient(f.srv.URL, "", "") }

func (f *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/event":
		f.mu.Lock()
		f.eventHits++
		frames := f.frames
		hold := f.holdOpen
		early := f.emitBeforePrompt
		f.mu.Unlock()

		fl, _ := w.(http.Flusher)
		// Always emit server.connected first (the real server does), so the
		// ready signal fires and the prompt can be sent.
		fmt.Fprintf(w, "data: %s\n\n", `{"type":"server.connected","properties":{}}`)
		if fl != nil {
			fl.Flush()
		}
		// Wait for the prompt before replying, unless the test wants frames
		// emitted eagerly.
		if !early {
			select {
			case <-f.prompted:
			case <-r.Context().Done():
				return
			}
		}
		for _, fr := range frames {
			fmt.Fprintf(w, "data: %s\n\n", fr)
			if fl != nil {
				fl.Flush()
			}
		}
		if hold {
			<-r.Context().Done() // keep the stream open until the turn cancels
		}

	case strings.HasSuffix(r.URL.Path, "/prompt_async"):
		f.mu.Lock()
		f.promptHits++
		first := f.promptHits == 1
		delay := f.promptDelay
		status := f.promptStatus
		f.mu.Unlock()
		if first {
			close(f.prompted)
		}
		if delay > 0 {
			time.Sleep(delay)
		}
		w.WriteHeader(status)

	default:
		w.WriteHeader(http.StatusOK)
	}
}

func (f *fakeServer) promptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.promptHits
}

// waitPromptCount polls until prompt_async has been hit at least n times or the
// deadline elapses. The prompt is sent from a goroutine after the SSE stream is
// established, so a turn can finish (via session.idle) before the async prompt
// round-trip is recorded; tests poll rather than checking synchronously.
func (f *fakeServer) waitPromptCount(t *testing.T, n int, deadline time.Duration) {
	t.Helper()
	stop := time.Now().Add(deadline)
	for time.Now().Before(stop) {
		if f.promptCount() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("prompt_async hits = %d, want >= %d within %s", f.promptCount(), n, deadline)
}

// drain collects all events from a turn until the channel closes, or fails the
// test if that takes longer than the deadline.
func drain(t *testing.T, r *TurnResult, deadline time.Duration) []Event {
	t.Helper()
	var got []Event
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for {
		select {
		case e, ok := <-r.Events:
			if !ok {
				return got
			}
			got = append(got, e)
		case <-timer.C:
			t.Fatalf("turn did not complete within %s; collected %d events so far", deadline, len(got))
			return got
		}
	}
}

func kinds(evs []Event) []EventKind {
	out := make([]EventKind, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Kind)
	}
	return out
}

// TestRunTurn_SubagentChildSessionKeptAliveButNotEnding verifies that a
// subagent (`task` tool) running in a child session (a) does NOT end the parent
// turn when the child goes idle, and (b) has its internal steps demoted to a
// heartbeat rather than rendered as top-level rows. Only the ROOT session's
// idle terminates the turn.
func TestRunTurn_SubagentChildSessionKeptAliveButNotEnding(t *testing.T) {
	f := newFakeServer()
	defer f.close()
	f.frames = []string{
		// Subagent child session is introduced with parentID == root.
		`{"type":"session.updated","properties":{"sessionID":"ses_child","info":{"id":"ses_child","parentID":"ses_1"}}}`,
		// The child's internal work must NOT surface as a top-level chunk.
		`{"type":"message.part.updated","properties":{"sessionID":"ses_child","part":{"type":"text","text":"subagent internal work"}}}`,
		// The child going idle must NOT end the parent turn.
		`{"type":"session.idle","properties":{"sessionID":"ses_child"}}`,
		// The parent resumes and finishes.
		`{"type":"message.part.updated","properties":{"sessionID":"ses_1","part":{"type":"text","text":"final answer"}}}`,
		`{"type":"session.idle","properties":{"sessionID":"ses_1"}}`,
	}

	req := TurnRequest{AppSessionID: "app_sub", Prompt: "hi"}
	r := runTurnWith(context.Background(), f.client(), "ses_1", req, 2*time.Second)

	got := drain(t, r, 3*time.Second)

	var chunks []string
	sawStep := false
	results := 0
	for _, e := range got {
		switch e.Kind {
		case KindChunk:
			chunks = append(chunks, e.Text)
		case KindStep:
			sawStep = true
		case KindResult:
			results++
		}
		if e.Kind == KindChunk && e.Text == "subagent internal work" {
			t.Fatalf("subagent internal work leaked as a top-level chunk: %v", kinds(got))
		}
	}
	if !sawStep {
		t.Errorf("expected a heartbeat from the subagent child session; got %v", kinds(got))
	}
	if len(chunks) != 1 || chunks[0] != "final answer" {
		t.Errorf("expected only the parent's final answer as a chunk, got %v", chunks)
	}
	if len(got) == 0 || got[len(got)-1].Kind != KindResult {
		t.Fatalf("turn should end with a terminal result, got %v", kinds(got))
	}
	if results != 1 {
		t.Errorf("expected exactly one terminal result (child idle must not end the turn), got %d: %v", results, kinds(got))
	}
	f.waitPromptCount(t, 1, time.Second)
}

func TestRunTurn_NormalIdle(t *testing.T) {
	f := newFakeServer()
	defer f.close()
	f.frames = []string{
		`{"type":"message.part.updated","properties":{"sessionID":"ses_1","part":{"type":"text","text":"hello"}}}`,
		`{"type":"session.idle","properties":{"sessionID":"ses_1"}}`,
	}

	req := TurnRequest{AppSessionID: "app_1", Prompt: "hi"}
	r := runTurnWith(context.Background(), f.client(), "ses_1", req, 2*time.Second)

	got := drain(t, r, 3*time.Second)
	// Expect exactly a chunk then a terminal result.
	if len(got) != 2 || got[0].Kind != KindChunk || got[1].Kind != KindResult {
		t.Fatalf("unexpected events: %v", kinds(got))
	}
	if got[0].Text != "hello" {
		t.Errorf("chunk text = %q", got[0].Text)
	}
	f.waitPromptCount(t, 1, time.Second)
}

// TestRunTurn_AbortMidStreamNoPanic is the H2 regression: the consumer's
// context is cancelled while the /event stream is still open AND the prompt
// goroutine is still in flight (delayed). The old code wrote the prompt error
// straight to ch after the consumer closed it — a send-on-closed-channel panic.
func TestRunTurn_AbortMidStreamNoPanic(t *testing.T) {
	f := newFakeServer()
	defer f.close()
	// One chunk, then hold the stream open (never idle) so the turn only ends
	// via ctx cancel. Delay the prompt response so it lands after cancellation.
	f.frames = []string{
		`{"type":"message.part.updated","properties":{"sessionID":"ses_1","part":{"type":"text","text":"working"}}}`,
	}
	f.holdOpen = true
	f.promptDelay = 300 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	req := TurnRequest{AppSessionID: "app_abort", Prompt: "hi"}
	r := runTurnWith(ctx, f.client(), "ses_1", req, 5*time.Second)

	// Consume the first chunk, then cancel mid-turn.
	select {
	case e := <-r.Events:
		if e.Kind != KindChunk {
			t.Fatalf("first event = %v, want chunk", e.Kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no first event")
	}
	cancel()

	// The channel must close cleanly (no panic) shortly after cancel, even
	// though the delayed prompt response is still arriving.
	got := drain(t, r, 3*time.Second)
	_ = got // content irrelevant; the point is it terminates without panicking
}

func TestRunTurn_PromptError(t *testing.T) {
	f := newFakeServer()
	defer f.close()
	f.promptStatus = http.StatusInternalServerError
	f.holdOpen = true // keep stream open so the only terminator is the prompt error

	req := TurnRequest{AppSessionID: "app_perr", Prompt: "hi"}
	r := runTurnWith(context.Background(), f.client(), "ses_1", req, 5*time.Second)

	got := drain(t, r, 3*time.Second)
	if !hasErrorContaining(got, "send prompt") {
		t.Fatalf("expected a 'send prompt' error event, got %v", kinds(got))
	}
}

// TestRunTurn_NoEventsTimesOut is the pre-first-event watchdog: the stream
// connects but never emits a usable event; the turn must end with an error
// rather than hang.
func TestRunTurn_NoEventsTimesOut(t *testing.T) {
	f := newFakeServer()
	defer f.close()
	f.holdOpen = true // only server.connected, then silence

	req := TurnRequest{AppSessionID: "app_noev", Prompt: "hi"}
	r := runTurnWith(context.Background(), f.client(), "ses_1", req, 200*time.Millisecond)

	got := drain(t, r, 3*time.Second)
	if !hasErrorContaining(got, "no events") {
		t.Fatalf("expected a no-events error, got %v", kinds(got))
	}
}

// TestRunTurn_StallAfterFirstEvent is the H3 fix: events start flowing but the
// stream then goes silent without session.idle. The inactivity watchdog must
// fire and end the turn instead of blocking forever.
func TestRunTurn_StallAfterFirstEvent(t *testing.T) {
	f := newFakeServer()
	defer f.close()
	f.frames = []string{
		`{"type":"message.part.updated","properties":{"sessionID":"ses_1","part":{"type":"text","text":"partial"}}}`,
	}
	f.holdOpen = true // after the one chunk, go silent (no idle)

	req := TurnRequest{AppSessionID: "app_stall", Prompt: "hi"}
	r := runTurnWith(context.Background(), f.client(), "ses_1", req, 200*time.Millisecond)

	got := drain(t, r, 3*time.Second)
	if got[0].Kind != KindChunk {
		t.Fatalf("first event = %v, want chunk", got[0].Kind)
	}
	if !hasErrorContaining(got, "stopped responding") {
		t.Fatalf("expected a mid-turn stall error, got %v", kinds(got))
	}
}

// TestRunTurn_PromptSent asserts the prompt is sent after the SSE stream is
// established (M1). It emits a chunk before idle so the turn resembles a real
// one (a degenerate immediate-idle turn can race prompt-send against the
// same-instant stream teardown, which is not what this test is about).
func TestRunTurn_PromptSent(t *testing.T) {
	f := newFakeServer()
	defer f.close()
	f.frames = []string{
		`{"type":"message.part.updated","properties":{"sessionID":"ses_1","part":{"type":"text","text":"ok"}}}`,
		`{"type":"session.idle","properties":{"sessionID":"ses_1"}}`,
	}
	req := TurnRequest{AppSessionID: "app_ps", Prompt: "hi"}
	r := runTurnWith(context.Background(), f.client(), "ses_1", req, 2*time.Second)
	drain(t, r, 3*time.Second)
	f.waitPromptCount(t, 1, time.Second)
}

func hasErrorContaining(evs []Event, substr string) bool {
	for _, e := range evs {
		if e.Kind == KindError && strings.Contains(e.Text, substr) {
			return true
		}
	}
	return false
}
