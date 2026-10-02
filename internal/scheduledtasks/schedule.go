// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package scheduledtasks implements unattended, scheduled agent runs.
//
// This file holds the pure scheduling math (no I/O), so it is unit-testable in
// isolation. The scheduler loop (scheduler.go), the run executor (runner.go),
// and the HTTP surface (handlers.go) build on top of it.
package scheduledtasks

import (
	"fmt"
	"strings"
	"time"
)

// Repeat presets. Arbitrary cron and multi-weekday pickers are not offered.
const (
	RepeatNone     = "none"
	RepeatDaily    = "daily"
	RepeatWeekdays = "weekdays"
	RepeatWeekly   = "weekly"
)

// StalenessBound is the catch-up cutoff: a missed occurrence caught up at
// boot runs automatically only if it is at most this late; beyond it, the task
// raises a pending Run-now / Skip decision instead of running silently.
const StalenessBound = 2 * time.Hour

// ValidRepeat reports whether r is a recognized preset.
func ValidRepeat(r string) bool {
	switch r {
	case RepeatNone, RepeatDaily, RepeatWeekdays, RepeatWeekly:
		return true
	default:
		return false
	}
}

func isWeekend(t time.Time) bool {
	wd := t.Weekday()
	return wd == time.Saturday || wd == time.Sunday
}

// firstOccurrence normalizes the first-run time for a repeat preset. For the
// weekdays preset a first-run that lands on a weekend is bumped to the next
// weekday at the same time-of-day; all other presets use first-run as-is (the
// weekly weekday is simply whatever first-run falls on).
func firstOccurrence(first time.Time, repeat string) time.Time {
	if repeat == RepeatWeekdays {
		for isWeekend(first) {
			first = first.AddDate(0, 0, 1)
		}
	}
	return first
}

// step returns the occurrence immediately after t for a repeating preset. It
// returns the zero time for RepeatNone (no next occurrence).
func step(t time.Time, repeat string) time.Time {
	switch repeat {
	case RepeatDaily:
		return t.AddDate(0, 0, 1)
	case RepeatWeekdays:
		n := t.AddDate(0, 0, 1)
		for isWeekend(n) {
			n = n.AddDate(0, 0, 1)
		}
		return n
	case RepeatWeekly:
		return t.AddDate(0, 0, 7)
	default:
		return time.Time{}
	}
}

// Evaluate walks a task's schedule relative to now and returns:
//
//   - lastDue: the most recent occurrence at or before now (zero if none is due
//     yet), and okDue reporting whether one exists;
//   - next: the next occurrence strictly after now (zero if none), and hasNext
//     reporting whether one exists.
//
// For RepeatNone the single occurrence is the (normalized) first-run time:
// before it fires it is `next`; once past, it is `lastDue` with no next.
//
// Whole-week fast-forward keeps the fine loop bounded even when first-run is far
// in the past. Adding 7 days preserves the weekday, so the jump is valid for
// daily, weekdays, and weekly alike.
func Evaluate(first time.Time, repeat string, now time.Time) (lastDue time.Time, okDue bool, next time.Time, hasNext bool) {
	occ := firstOccurrence(first, repeat)

	if !ValidRepeat(repeat) || repeat == RepeatNone {
		if occ.After(now) {
			return time.Time{}, false, occ, true
		}
		return occ, true, time.Time{}, false
	}

	// Coarse fast-forward to within ~a week of now (staying at or before now).
	if d := now.Sub(occ); d > 7*24*time.Hour {
		weeks := int(d/(7*24*time.Hour)) - 1
		if weeks > 0 {
			occ = occ.AddDate(0, 0, 7*weeks)
		}
	}

	var last time.Time
	found := false
	for i := 0; i < 100000; i++ {
		if occ.After(now) {
			return last, found, occ, true
		}
		last = occ
		found = true
		occ = step(occ, repeat)
	}
	// Safety valve (should be unreachable): report what we have.
	return last, found, occ, true
}

// NextRun returns the next occurrence strictly after `after` for a task, and
// whether one exists. Used to seed next_run_at on create / after a fire / on
// re-enable (where after is "now").
func NextRun(first time.Time, repeat string, after time.Time) (time.Time, bool) {
	_, _, next, ok := Evaluate(first, repeat, after)
	return next, ok
}

// Summary renders a short human schedule label ("daily 8:00", "weekdays 8:30",
// "Mon 9:00", "once Jul 14 8:00") in the given time's own location.
func Summary(first time.Time, repeat string) string {
	hhmm := fmt.Sprintf("%d:%02d", first.Hour(), first.Minute())
	switch repeat {
	case RepeatDaily:
		return "daily " + hhmm
	case RepeatWeekdays:
		return "weekdays " + hhmm
	case RepeatWeekly:
		return first.Format("Mon") + " " + hhmm
	default:
		return "once " + first.Format("Jan 2") + " " + hhmm
	}
}

// DeriveLabel produces a task label from the first few words of its prompt,
// used when the user leaves the name blank.
func DeriveLabel(prompt string) string {
	fields := strings.Fields(strings.TrimSpace(prompt))
	if len(fields) == 0 {
		return "Scheduled task"
	}
	if len(fields) > 6 {
		fields = fields[:6]
	}
	label := strings.Join(fields, " ")
	if len(label) > 60 {
		label = strings.TrimSpace(label[:60]) + "…"
	}
	return label
}

// parseTime parses an RFC3339 timestamp; returns zero time and ok=false on
// empty or invalid input.
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// formatTime renders a time as RFC3339 (empty string for the zero time).
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
