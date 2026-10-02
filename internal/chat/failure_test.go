// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"path/filepath"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// runTurn feeds events through the dispatcher as one turn and returns what was
// saved as the assistant's reply. between runs after the listed events, before
// the rest, standing in for something that happens mid-turn (a stop).
func runTurn(t *testing.T, before []*Event, between func(*Stream), after ...*Event) (saved string, frames []map[string]any) {
	t.Helper()
	if err := db.Init(filepath.Join(t.TempDir(), "test.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	const sid = "sess-failure"
	cfg := config.DefaultSessionConfig()
	cfg.Temporary = true // no durable backup
	if err := db.CreateSession(sid, cfg); err != nil {
		t.Fatal(err)
	}
	s := newStream(sid)
	s.TurnID = "turn-1"
	// Unbuffered: each send returns once the dispatcher has taken the event.
	ch := make(chan *Event)
	done := make(chan struct{})
	go func() { runDispatcher(sid, cfg, ch, s); close(done) }()
	for _, e := range before {
		ch <- e
	}
	if between != nil {
		between(s)
	}
	for _, e := range after {
		ch <- e
	}
	close(ch)
	<-done
	for _, m := range db.GetMessages(sid) {
		if m.Role == "assistant" {
			saved = m.Content
		}
	}
	frames, _ = s.Snapshot()
	return saved, frames
}

const reasoningErr = "Multiple reasoning_opaque values received in a single response. Only one thinking part per response is supported."

// The failure seen live must survive a reload: it used to be dropped, so a
// failed turn came back as an answer cut off mid-sentence, or as nothing.
func TestFailedTurnIsSavedWithItsError(t *testing.T) {
	cases := []struct {
		name   string
		events []*Event
		want   string
	}{
		{"after partial text", []*Event{{Kind: "chunk", Text: "Important: every signing setting lives in"}, {Kind: "error", Text: reasoningErr}},
			"Important: every signing setting lives in\n\n⚠ " + reasoningErr},
		{"with no text at all", []*Event{{Kind: "error", Text: reasoningErr}},
			"⚠ " + reasoningErr},
		{"after text ending in a blank line", []*Event{{Kind: "chunk", Text: "Checked.\n\n"}, {Kind: "error", Text: reasoningErr}},
			"Checked.\n\n⚠ " + reasoningErr},
		{"two errors, in order", []*Event{{Kind: "error", Text: reasoningErr}, {Kind: "error", Text: "opencode ended without response"}},
			"⚠ " + reasoningErr + "\n\n⚠ opencode ended without response"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			saved, frames := runTurn(t, c.events, nil)
			if saved != c.want {
				t.Errorf("saved %q\nwant  %q", saved, c.want)
			}
			if !hasFrame(frames, "error") {
				t.Error("the error frame was not sent live")
			}
		})
	}
}

func TestEmptyResponseErrorIsSaved(t *testing.T) {
	saved, _ := runTurn(t, []*Event{{Kind: "result"}}, nil)
	if want := "⚠ opencode returned no response (model may not exist or an authentication error occurred)"; saved != want {
		t.Errorf("saved %q, want %q", saved, want)
	}
}

// A stop kills the process, which then reports an error of its own. That is
// the stop, not a failure of the turn, and must not be saved as one.
func TestStoppedTurnDoesNotSaveTheKillError(t *testing.T) {
	saved, _ := runTurn(t,
		[]*Event{{Kind: "chunk", Text: "Working on it"}},
		func(s *Stream) { s.Finish() },
		&Event{Kind: "error", Text: "opencode process exited without producing any output"})
	if saved != "Working on it" {
		t.Errorf("saved %q, want the partial reply only", saved)
	}
}

func TestStopSessionFinishesTheStreamBeforeTheKill(t *testing.T) {
	const sid = "sess-stop-order"
	s := newStream(sid)
	Streams.Store(sid, s)
	t.Cleanup(func() { Streams.Delete(sid) })
	finishedAtKill := false
	Procs.Store(sid, &ActiveProcess{Abort: func() { finishedAtKill = s.Finished() }})

	StopSession(sid)
	if !finishedAtKill {
		t.Error("the process was killed while the stream was still open, so its error would be saved as a failure")
	}
}

func hasFrame(frames []map[string]any, typ string) bool {
	for _, f := range frames {
		if f["type"] == typ {
			return true
		}
	}
	return false
}
