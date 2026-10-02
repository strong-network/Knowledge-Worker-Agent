// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package scheduledtasks

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// fireRecord captures a stubbed run so tests can assert without the chat backend.
type fireRecord struct {
	taskID       string
	trigger      string
	scheduledFor time.Time
}

// withStubbedRunner replaces the execution + clock seams for the duration of a
// test and returns a pointer to the recorded fires.
func withStubbedRunner(t *testing.T, clock time.Time) *[]fireRecord {
	t.Helper()
	var mu sync.Mutex
	fires := []fireRecord{}
	origRun, origNow := runTask, now
	runTask = func(taskID, prompt, workdir, label, model, trigger string, scheduledFor time.Time) {
		mu.Lock()
		defer mu.Unlock()
		fires = append(fires, fireRecord{taskID: taskID, trigger: trigger, scheduledFor: scheduledFor})
	}
	now = func() time.Time { return clock }
	t.Cleanup(func() { runTask, now = origRun, origNow })
	return &fires
}

func setupTasksDB(t *testing.T) {
	t.Helper()
	f, err := os.CreateTemp("", "tasks-*.db")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	t.Cleanup(func() {
		db.Close()
		os.Remove(path)
	})
	if err := db.Init(path); err != nil {
		t.Fatal(err)
	}
}

func mkTask(t *testing.T, id, repeat string, first, next time.Time, enabled bool) {
	t.Helper()
	task := db.ScheduledTask{
		ID:         id,
		Name:       id,
		Prompt:     "do the thing",
		Workdir:    "/tmp",
		Repeat:     repeat,
		FirstRunAt: first.Format(time.RFC3339),
		NextRunAt:  next.Format(time.RFC3339),
		Enabled:    enabled,
	}
	if err := db.CreateScheduledTask(task); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluateFiresFreshScheduled(t *testing.T) {
	setupTasksDB(t)
	base := mk(2026, time.July, 14, 8, 0)
	fires := withStubbedRunner(t, base.Add(1*time.Minute)) // 1 min after due
	mkTask(t, "fresh", RepeatDaily, base, base, true)

	EvaluateDue(defaultTickInterval)

	if len(*fires) != 1 {
		t.Fatalf("expected 1 fire, got %d", len(*fires))
	}
	if (*fires)[0].trigger != "scheduled" {
		t.Fatalf("trigger = %q, want scheduled", (*fires)[0].trigger)
	}
	// next_run_at advanced to the following day.
	got := db.GetScheduledTask("fresh")
	if got.NextRunAt != mk(2026, time.July, 15, 8, 0).Format(time.RFC3339) {
		t.Fatalf("next_run_at = %q, want Jul 15 08:00", got.NextRunAt)
	}
}

func TestEvaluateNotDueDoesNotFire(t *testing.T) {
	setupTasksDB(t)
	base := mk(2026, time.July, 14, 8, 0)
	fires := withStubbedRunner(t, base.Add(-1*time.Hour)) // before due
	mkTask(t, "future", RepeatDaily, base, base, true)

	EvaluateDue(defaultTickInterval)
	if len(*fires) != 0 {
		t.Fatalf("expected no fire, got %d", len(*fires))
	}
}

func TestEvaluateDisabledSkipped(t *testing.T) {
	setupTasksDB(t)
	base := mk(2026, time.July, 14, 8, 0)
	fires := withStubbedRunner(t, base.Add(1*time.Minute))
	mkTask(t, "off", RepeatDaily, base, base, false)

	EvaluateDue(defaultTickInterval)
	if len(*fires) != 0 {
		t.Fatalf("disabled task should not fire, got %d", len(*fires))
	}
}

func TestEvaluateCatchUpWithinBound(t *testing.T) {
	setupTasksDB(t)
	base := mk(2026, time.July, 14, 8, 0)
	// 90 min late: past freshWindow but within the 2h staleness bound.
	fires := withStubbedRunner(t, base.Add(90*time.Minute))
	mkTask(t, "catchup", RepeatDaily, base, base, true)

	EvaluateDue(defaultTickInterval)
	if len(*fires) != 1 || (*fires)[0].trigger != "catch-up" {
		t.Fatalf("expected one catch-up fire, got %+v", *fires)
	}
	if got := db.GetScheduledTask("catchup"); got.PendingCatchupAt != "" {
		t.Fatalf("catch-up within bound should not set pending, got %q", got.PendingCatchupAt)
	}
}

func TestEvaluateStalePendingDecision(t *testing.T) {
	setupTasksDB(t)
	base := mk(2026, time.July, 14, 8, 0)
	// 3h late: beyond the staleness bound → pending decision, no fire.
	fires := withStubbedRunner(t, base.Add(3*time.Hour))
	mkTask(t, "stale", RepeatDaily, base, base, true)

	EvaluateDue(defaultTickInterval)
	if len(*fires) != 0 {
		t.Fatalf("stale run should not auto-fire, got %d", len(*fires))
	}
	got := db.GetScheduledTask("stale")
	if got.PendingCatchupAt != base.Format(time.RFC3339) {
		t.Fatalf("pending_catchup_at = %q, want %q", got.PendingCatchupAt, base.Format(time.RFC3339))
	}
	// Schedule still advanced to the next future occurrence.
	if got.NextRunAt != mk(2026, time.July, 15, 8, 0).Format(time.RFC3339) {
		t.Fatalf("next_run_at = %q, want Jul 15 08:00", got.NextRunAt)
	}
}

func TestEvaluateCoalescesMissedOccurrences(t *testing.T) {
	setupTasksDB(t)
	base := mk(2026, time.July, 10, 8, 0)
	// Daily task, first due Jul 10; now Jul 14 08:30 → 4 missed days, but only
	// ONE consideration. It is >2h late so it becomes pending (not 4 runs).
	fires := withStubbedRunner(t, mk(2026, time.July, 14, 8, 30))
	mkTask(t, "coalesce", RepeatDaily, base, base, true)

	EvaluateDue(defaultTickInterval)
	if len(*fires) != 0 {
		t.Fatalf("stale coalesced task should not auto-fire, got %d", len(*fires))
	}
	got := db.GetScheduledTask("coalesce")
	// next advanced to Jul 15 (strictly after now), collapsing the backlog.
	if got.NextRunAt != mk(2026, time.July, 15, 8, 0).Format(time.RFC3339) {
		t.Fatalf("next_run_at = %q, want Jul 15 08:00 (coalesced)", got.NextRunAt)
	}
}

func TestEvaluateOneShotClearsNext(t *testing.T) {
	setupTasksDB(t)
	base := mk(2026, time.July, 14, 8, 0)
	fires := withStubbedRunner(t, base.Add(1*time.Minute))
	mkTask(t, "once", RepeatNone, base, base, true)

	EvaluateDue(defaultTickInterval)
	if len(*fires) != 1 || (*fires)[0].trigger != "scheduled" {
		t.Fatalf("expected one scheduled fire, got %+v", *fires)
	}
	if got := db.GetScheduledTask("once"); got.NextRunAt != "" {
		t.Fatalf("one-shot next_run_at should be cleared, got %q", got.NextRunAt)
	}
}
