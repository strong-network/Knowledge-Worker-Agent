// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package scheduledtasks

import (
	"testing"
	"time"
)

// mk builds a local time for readable test cases.
func mk(y int, mo time.Month, d, h, min int) time.Time {
	return time.Date(y, mo, d, h, min, 0, 0, time.Local)
}

func TestEvaluateNone(t *testing.T) {
	first := mk(2026, time.July, 14, 8, 0)

	// Before it fires: it is the next occurrence, nothing due.
	now := mk(2026, time.July, 14, 7, 0)
	lastDue, okDue, next, hasNext := Evaluate(first, RepeatNone, now)
	if okDue {
		t.Fatalf("none: nothing should be due before first run, got %v", lastDue)
	}
	if !hasNext || !next.Equal(first) {
		t.Fatalf("none: next should be first run, got hasNext=%v next=%v", hasNext, next)
	}

	// After it fires: it is lastDue, no next.
	now = mk(2026, time.July, 14, 9, 0)
	lastDue, okDue, _, hasNext = Evaluate(first, RepeatNone, now)
	if !okDue || !lastDue.Equal(first) {
		t.Fatalf("none: first run should be due, got okDue=%v lastDue=%v", okDue, lastDue)
	}
	if hasNext {
		t.Fatalf("none: no next occurrence expected after a one-shot fires")
	}
}

func TestEvaluateDailyNext(t *testing.T) {
	first := mk(2026, time.July, 14, 8, 0)
	now := mk(2026, time.July, 20, 10, 0) // several days later, after 8:00
	lastDue, okDue, next, hasNext := Evaluate(first, RepeatDaily, now)
	if !okDue || !lastDue.Equal(mk(2026, time.July, 20, 8, 0)) {
		t.Fatalf("daily lastDue = %v, want Jul 20 08:00", lastDue)
	}
	if !hasNext || !next.Equal(mk(2026, time.July, 21, 8, 0)) {
		t.Fatalf("daily next = %v, want Jul 21 08:00", next)
	}
}

func TestEvaluateWeekdaysSkipsWeekend(t *testing.T) {
	// Jul 14 2026 is a Tuesday. Advance to Friday after fire → next is Monday.
	first := mk(2026, time.July, 14, 8, 30)
	friday := mk(2026, time.July, 17, 9, 0)
	if friday.Weekday() != time.Friday {
		t.Fatalf("fixture wrong: Jul 17 2026 is %v", friday.Weekday())
	}
	_, _, next, hasNext := Evaluate(first, RepeatWeekdays, friday)
	if !hasNext {
		t.Fatal("weekdays: expected a next occurrence")
	}
	if next.Weekday() != time.Monday || !next.Equal(mk(2026, time.July, 20, 8, 30)) {
		t.Fatalf("weekdays next = %v (%v), want Mon Jul 20 08:30", next, next.Weekday())
	}
}

func TestFirstOccurrenceWeekdaysWeekendStart(t *testing.T) {
	// Jul 18 2026 is a Saturday; weekdays preset should bump to Monday Jul 20.
	sat := mk(2026, time.July, 18, 8, 0)
	if sat.Weekday() != time.Saturday {
		t.Fatalf("fixture wrong: Jul 18 2026 is %v", sat.Weekday())
	}
	now := mk(2026, time.July, 18, 7, 0)
	_, _, next, hasNext := Evaluate(sat, RepeatWeekdays, now)
	if !hasNext || !next.Equal(mk(2026, time.July, 20, 8, 0)) {
		t.Fatalf("weekdays weekend-start next = %v, want Mon Jul 20 08:00", next)
	}
}

func TestEvaluateWeekly(t *testing.T) {
	// Jul 13 2026 is a Monday.
	first := mk(2026, time.July, 13, 9, 0)
	if first.Weekday() != time.Monday {
		t.Fatalf("fixture wrong: Jul 13 2026 is %v", first.Weekday())
	}
	now := mk(2026, time.July, 22, 12, 0) // Wed, after two Mondays (13th, 20th)
	lastDue, okDue, next, hasNext := Evaluate(first, RepeatWeekly, now)
	if !okDue || !lastDue.Equal(mk(2026, time.July, 20, 9, 0)) {
		t.Fatalf("weekly lastDue = %v, want Mon Jul 20 09:00", lastDue)
	}
	if !hasNext || !next.Equal(mk(2026, time.July, 27, 9, 0)) {
		t.Fatalf("weekly next = %v, want Mon Jul 27 09:00", next)
	}
}

func TestEvaluateFastForwardFarPast(t *testing.T) {
	first := mk(2020, time.January, 1, 8, 0)
	now := mk(2026, time.July, 14, 8, 30) // ~6.5 years of daily occurrences
	lastDue, okDue, next, hasNext := Evaluate(first, RepeatDaily, now)
	if !okDue || !lastDue.Equal(mk(2026, time.July, 14, 8, 0)) {
		t.Fatalf("fast-forward daily lastDue = %v, want Jul 14 08:00", lastDue)
	}
	if !hasNext || !next.Equal(mk(2026, time.July, 15, 8, 0)) {
		t.Fatalf("fast-forward daily next = %v, want Jul 15 08:00", next)
	}
}

func TestNextRunOnEnable(t *testing.T) {
	// Re-enabling starts fresh: next occurrence strictly after "now".
	first := mk(2026, time.July, 14, 8, 0)
	now := mk(2026, time.July, 14, 8, 0) // exactly on an occurrence → not "after"
	next, ok := NextRun(first, RepeatDaily, now)
	if !ok || !next.Equal(mk(2026, time.July, 15, 8, 0)) {
		t.Fatalf("NextRun = %v (ok=%v), want Jul 15 08:00", next, ok)
	}
}

func TestSummary(t *testing.T) {
	first := mk(2026, time.July, 13, 8, 5) // Monday
	cases := map[string]string{
		RepeatDaily:    "daily 8:05",
		RepeatWeekdays: "weekdays 8:05",
		RepeatWeekly:   "Mon 8:05",
	}
	for repeat, want := range cases {
		if got := Summary(first, repeat); got != want {
			t.Errorf("Summary(%s) = %q, want %q", repeat, got, want)
		}
	}
	if got := Summary(first, RepeatNone); got != "once Jul 13 8:05" {
		t.Errorf("Summary(none) = %q, want %q", got, "once Jul 13 8:05")
	}
}

func TestDeriveLabel(t *testing.T) {
	if got := DeriveLabel("  Summarize overnight activity and post a briefing to the channel  "); got != "Summarize overnight activity and post a" {
		t.Errorf("DeriveLabel = %q", got)
	}
	if got := DeriveLabel("   "); got != "Scheduled task" {
		t.Errorf("DeriveLabel empty = %q", got)
	}
}

func TestParseFormatRoundTrip(t *testing.T) {
	first := mk(2026, time.July, 14, 8, 0)
	s := formatTime(first)
	got, ok := parseTime(s)
	if !ok || !got.Equal(first) {
		t.Fatalf("round-trip failed: %q → %v (ok=%v)", s, got, ok)
	}
	if formatTime(time.Time{}) != "" {
		t.Fatalf("zero time should format empty")
	}
	if _, ok := parseTime(""); ok {
		t.Fatalf("empty string should not parse")
	}
}
