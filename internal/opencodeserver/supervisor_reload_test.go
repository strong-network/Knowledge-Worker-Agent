// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeserver

import "testing"

// TestReloadMarksInstancesStale verifies Reload flags every pooled instance for
// replacement without needing a real `opencode serve` process. A stale instance
// is the mechanism that makes the next turn re-read on-disk agent files.
func TestReloadMarksInstancesStale(t *testing.T) {
	s := NewSupervisor(Options{Bin: "opencode"})
	in := &instance{workdir: "/tmp/x"}
	s.instances["/tmp/x"] = in

	if in.isStale() {
		t.Fatal("instance should not start stale")
	}
	s.Reload()
	if !in.isStale() {
		t.Fatal("Reload should mark the instance stale")
	}
}

// TestReloadNoInstancesIsNoop ensures Reload is safe when the pool is empty.
func TestReloadNoInstancesIsNoop(t *testing.T) {
	s := NewSupervisor(Options{Bin: "opencode"})
	s.Reload() // must not panic
	if s.Count() != 0 {
		t.Fatalf("expected 0 instances, got %d", s.Count())
	}
}
