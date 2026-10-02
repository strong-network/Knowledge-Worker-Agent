// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package scheduledtasks

import (
	"log"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
)

// The scheduler is a boot-started background loop (mirroring
// internal/projects/autosync.go): it ticks on an interval, finds due tasks, and
// fires them. Because it is in-process it only runs while the workspace is up —
// exactly the in-workspace scope scheduled tasks have. The first pass after boot doubles as
// catch-up evaluation for occurrences missed while the workspace was off.

const (
	defaultTickInterval = 1 * time.Minute
	// initialDelay lets the boot bootstrap (copilot/opencode install &
	// readiness) settle before the first catch-up pass, so caught-up runs don't
	// fail against a not-yet-ready backend.
	initialDelay = 45 * time.Second
	// freshFloor is the minimum "a due run is a normal scheduled fire, not a
	// catch-up" window. A run later than this is treated as missed-while-off.
	freshFloor = 10 * time.Minute
)

// runTask is the injected execution seam (stubbed in tests). Production runs the
// real ExecuteRun, which blocks until the run finishes so boot catch-up and
// concurrently-due tasks execute sequentially, not all at once.
var runTask = func(taskID, prompt, workdir, label, model, trigger string, scheduledFor time.Time) {
	ExecuteRun(taskID, prompt, workdir, label, model, trigger, scheduledFor)
}

// now is injected so tests can pin the clock.
var now = time.Now

// tickInterval resolves the cadence from KWA_SCHEDULED_TASKS_INTERVAL (a Go
// duration or bare seconds), defaulting to 1m. A value <= 0 disables the loop.
func tickInterval() time.Duration {
	raw := strings.TrimSpace(env.Get("KWA_SCHEDULED_TASKS_INTERVAL"))
	if raw == "" {
		return defaultTickInterval
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return d
	}
	if secs, err := time.ParseDuration(raw + "s"); err == nil {
		return secs
	}
	return defaultTickInterval
}

func schedulerDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(env.Get("KWA_SCHEDULED_TASKS"))) {
	case "0", "false", "no", "off":
		return true
	default:
		return false
	}
}

// StartScheduler launches the background scheduler ticker. Non-blocking; safe to
// call once at startup. Honors KWA_SCHEDULED_TASKS (off) and
// KWA_SCHEDULED_TASKS_INTERVAL. Runs an initial catch-up pass shortly after boot.
func StartScheduler() {
	if schedulerDisabled() {
		log.Printf("[scheduled-tasks] disabled via KWA_SCHEDULED_TASKS")
		return
	}
	interval := tickInterval()
	if interval <= 0 {
		log.Printf("[scheduled-tasks] disabled (interval <= 0)")
		return
	}
	log.Printf("[scheduled-tasks] scheduler every %s", interval)
	go func() {
		time.Sleep(initialDelay)
		EvaluateDue(interval)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			EvaluateDue(interval)
		}
	}()
}

// EvaluateDue runs one scheduling pass over every task. Exported so it can be
// triggered on demand and tested directly. Tasks are evaluated sequentially and
// each fired run blocks (via runTask), so runs never burst concurrently.
func EvaluateDue(interval time.Duration) {
	freshWindow := 2 * interval
	if freshWindow < freshFloor {
		freshWindow = freshFloor
	}
	current := now()
	for _, t := range db.ListScheduledTasks() {
		evaluateTask(t, current, freshWindow)
	}
}

// evaluateTask fires (or defers) a single task if it is due. It advances
// next_run_at past `current` first (coalescing every missed occurrence into one
// consideration) so the same occurrence can't fire twice, then decides:
//
//   - fresh (<= freshWindow late): a normal "scheduled" run;
//   - missed but <= StalenessBound late: a single "catch-up" run;
//   - missed and > StalenessBound late: no run — raise a pending decision the
//     user resolves with Run now / Skip, while the future schedule continues.
func evaluateTask(t db.ScheduledTask, current time.Time, freshWindow time.Duration) {
	if !t.Enabled {
		return
	}
	nextAt, ok := parseTime(t.NextRunAt)
	if !ok || nextAt.After(current) {
		return // no scheduled occurrence, or not due yet
	}

	first, ok := parseTime(t.FirstRunAt)
	if !ok {
		first = nextAt
	}
	label := strings.TrimSpace(t.Name)
	if label == "" {
		label = DeriveLabel(t.Prompt)
	}

	// The next future occurrence strictly after now — this both advances the
	// schedule and coalesces all missed occurrences into one.
	newNext := ""
	if future, has := NextRun(first, t.Repeat, current); has {
		newNext = formatTime(future)
	}
	_ = db.SetScheduledTaskNextRun(t.ID, newNext)

	lateness := current.Sub(nextAt)
	switch {
	case lateness <= freshWindow:
		runTask(t.ID, t.Prompt, t.Workdir, label, t.Model, "scheduled", nextAt)
	case lateness <= StalenessBound:
		runTask(t.ID, t.Prompt, t.Workdir, label, t.Model, "catch-up", nextAt)
	default:
		// Too stale to run silently: surface a pending decision instead.
		_ = db.SetScheduledTaskPendingCatchup(t.ID, formatTime(nextAt))
		log.Printf("[scheduled-tasks] task=%s missed run %s is %s late — pending user decision",
			t.ID, formatTime(nextAt), lateness.Round(time.Minute))
	}
}
