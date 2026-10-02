// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env/envtest"
)

func TestEnvDurationAcceptsGoDurationsAndBareSeconds(t *testing.T) {
	cases := []struct {
		name string
		set  string
		want time.Duration
	}{
		{"unset keeps the default", "", 30 * time.Minute},
		{"go duration", "720h", 720 * time.Hour},
		{"go duration with minutes", "90m", 90 * time.Minute},
		{"bare seconds", "3600", time.Hour},
		{"zero", "0", 0},
		{"surrounding whitespace", "  48h  ", 48 * time.Hour},
		// A typo must not silently become an aggressive window.
		{"nonsense keeps the default", "soon", 30 * time.Minute},
		{"wrong unit keeps the default", "30 days", 30 * time.Minute},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SDS_TEST_DURATION", tc.set)
			if got := envDuration("SDS_TEST_DURATION", 30*time.Minute); got != tc.want {
				t.Errorf("envDuration(%q) = %s, want %s", tc.set, got, tc.want)
			}
		})
	}
}

func TestEnvBoolKeepsTheDefaultForAnythingUnrecognized(t *testing.T) {
	cases := []struct {
		set  string
		want bool
	}{
		{"1", true}, {"true", true}, {"TRUE", true}, {"yes", true}, {"on", true},
		{"0", false}, {"false", false}, {"No", false}, {"off", false},
		// The default here is true, so these all stay true.
		{"", true}, {"maybe", true}, {"  ", true},
	}

	for _, tc := range cases {
		t.Run(tc.set, func(t *testing.T) {
			t.Setenv("SDS_TEST_BOOL", tc.set)
			if got := envBool("SDS_TEST_BOOL", true); got != tc.want {
				t.Errorf("envBool(%q) = %v, want %v", tc.set, got, tc.want)
			}
		})
	}
}

// Hygiene deletes things on a timer, so a nonsensical window must fail towards
// doing nothing rather than towards doing it immediately.
func TestInitHygieneRefusesWindowsThatWouldDeleteEverything(t *testing.T) {
	t.Setenv("KWA_HYGIENE_EMPTY_AFTER", "-5h")
	t.Setenv("KWA_HYGIENE_REVIEW_AFTER", "0")
	t.Setenv("KWA_HYGIENE_SNOOZE", "-1h")
	initHygiene()

	if HygieneEmptyAfter != 0 {
		t.Errorf("a negative empty-chat window should disable the sweep, got %s", HygieneEmptyAfter)
	}
	if HygieneReviewAfter != DefaultHygieneReviewAfter {
		t.Errorf("a zero review window should fall back to the default, got %s", HygieneReviewAfter)
	}
	if HygieneSnooze != DefaultHygieneSnooze {
		t.Errorf("a negative snooze should fall back to the default, got %s", HygieneSnooze)
	}
}

func TestInitHygieneReadsTheEnvironment(t *testing.T) {
	t.Setenv("KWA_HYGIENE_ENABLED", "false")
	t.Setenv("KWA_HYGIENE_REVIEW_AFTER", "168h")
	t.Setenv("KWA_HYGIENE_EMPTY_AFTER", "1h")
	initHygiene()

	if HygieneEnabled {
		t.Error("expected hygiene to be disabled")
	}
	if HygieneReviewAfter != 168*time.Hour {
		t.Errorf("review window = %s, want 168h", HygieneReviewAfter)
	}
	if HygieneEmptyAfter != time.Hour {
		t.Errorf("empty-chat window = %s, want 1h", HygieneEmptyAfter)
	}
}

func TestInitHygieneDefaults(t *testing.T) {
	envtest.Clear(t, "KWA_HYGIENE_ENABLED", "KWA_HYGIENE_REVIEW_AFTER", "KWA_HYGIENE_SNOOZE", "KWA_HYGIENE_EMPTY_AFTER")
	initHygiene()

	if !HygieneEnabled {
		t.Error("hygiene should be on by default")
	}
	if HygieneReviewAfter != 30*24*time.Hour {
		t.Errorf("review window = %s, want 30 days", HygieneReviewAfter)
	}
	if HygieneEmptyAfter != 24*time.Hour {
		t.Errorf("empty-chat window = %s, want 24h", HygieneEmptyAfter)
	}
}
