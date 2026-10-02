// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import "testing"

func TestTrackedTurnsRemainActiveUntilAllFinish(t *testing.T) {
	if HasActiveTasks() {
		t.Fatal("unexpected active task")
	}
	a, b := make(chan *Event, 1), make(chan *Event, 1)
	activeTurns.Add(2)
	outA, outB := finishTrackedTurn(a), finishTrackedTurn(b)
	if !HasActiveTasks() {
		t.Fatal("quiet tasks must remain active")
	}
	event := &Event{Kind: "error", Text: "backend unavailable"}
	a <- event
	close(a)
	if got := <-outA; got != event {
		t.Fatal("event changed")
	}
	for range outA {
	}
	if !HasActiveTasks() {
		t.Fatal("one finished task cleared another active task")
	}
	close(b)
	for range outB {
	}
	if HasActiveTasks() {
		t.Fatal("finished/error tasks kept workspace active")
	}
}
