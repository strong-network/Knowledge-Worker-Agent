// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import "sync/atomic"

// Count turns rather than browser connections or the long-lived opencode
// server. A turn stays active during quiet model/tool work and subagent work,
// including when its SSE subscriber disconnects. Completed turns do not keep
// the workspace awake. Multiple simultaneous turns share one heartbeat loop.
var activeTurns atomic.Int64

// HasActiveTasks is the workspace heartbeat's activity source.
func HasActiveTasks() bool { return activeTurns.Load() > 0 }

// finishTrackedTurn relays events without tying activity to an HTTP subscriber.
// The caller increments before backend setup; this releases the count for all
// terminal paths (including startup errors, cancellation and normal completion).
func finishTrackedTurn(events <-chan *Event) <-chan *Event {
	out := make(chan *Event, 64)
	go func() {
		defer close(out)
		defer activeTurns.Add(-1)
		for ev := range events {
			out <- ev
		}
	}()
	return out
}
