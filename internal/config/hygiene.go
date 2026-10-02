// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strconv"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
)

// Workspace hygiene.
//
// Chat sessions accumulate without bound, so the sidebar and the database fill
// with items that exist for no reason. The settings here govern when we clean
// up and when we ask.
//
// The distinction that matters: EmptyAfter is the only window that causes an
// automatic deletion, and it applies only to chats that contain nothing at all.
// ReviewAfter decides when we *ask* about a chat, never when we delete it — a
// chat with a single message is only ever removed by an explicit user action.
// That is why ReviewAfter can be tuned freely: getting it wrong makes the review
// list noisier or quieter, but it cannot lose work.

var (
	// HygieneEnabled gates the whole feature. When false the sweeper does not
	// run and the review surface reports nothing, leaving behaviour exactly as
	// it was before workspace hygiene existed.
	HygieneEnabled bool

	// HygieneReviewAfter is how long a chat must be idle before it is offered
	// for review. Measured against sessions.updated_at, which real activity
	// bumps and background sync and draft saves deliberately do not.
	HygieneReviewAfter time.Duration

	// HygieneSnooze is how long "Keep for now" defers an item. Deliberately
	// equal to HygieneReviewAfter by default, so a user who defers everything
	// is asked once per interval rather than repeatedly.
	HygieneSnooze time.Duration

	// HygieneEmptyAfter is how long a chat with no messages and no draft must
	// be idle before it is deleted automatically. Zero disables that sweep
	// entirely.
	HygieneEmptyAfter time.Duration
)

// Hygiene defaults. Thirty days is the shortest window that describes anything
// real on a measured workspace: at 60, 90 or 180 days nothing matches at all.
const (
	DefaultHygieneReviewAfter = 30 * 24 * time.Hour
	DefaultHygieneSnooze      = 30 * 24 * time.Hour
	DefaultHygieneEmptyAfter  = 24 * time.Hour
)

// initHygiene reads the hygiene settings from the environment. Called by Init.
func initHygiene() {
	HygieneEnabled = envBool("KWA_HYGIENE_ENABLED", true)
	HygieneReviewAfter = envDuration("KWA_HYGIENE_REVIEW_AFTER", DefaultHygieneReviewAfter)
	HygieneSnooze = envDuration("KWA_HYGIENE_SNOOZE", DefaultHygieneSnooze)
	HygieneEmptyAfter = envDuration("KWA_HYGIENE_EMPTY_AFTER", DefaultHygieneEmptyAfter)

	// A negative window would make everything instantly eligible. Treat it as
	// "disabled" rather than "delete immediately", which is the reading that
	// cannot destroy anything.
	if HygieneEmptyAfter < 0 {
		HygieneEmptyAfter = 0
	}
	// A non-positive review window would surface every chat ever created. Fall
	// back to the default rather than flooding the list.
	if HygieneReviewAfter <= 0 {
		HygieneReviewAfter = DefaultHygieneReviewAfter
	}
	if HygieneSnooze <= 0 {
		HygieneSnooze = DefaultHygieneSnooze
	}
}

// envBool reads a boolean env var, accepting the spellings used elsewhere in
// the codebase (1/true/yes/on and 0/false/no/off, any case). An unset or
// unrecognized value keeps the default, so a typo cannot silently turn a safety
// switch off.
func envBool(key string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(env.Get(key))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

// envDuration reads a duration env var. It accepts a Go duration ("720h",
// "90m") and, for convenience, a bare number of seconds ("3600"). An unset or
// unparseable value keeps the default.
func envDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(env.Get(key))
	if raw == "" {
		return fallback
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		return time.Duration(secs) * time.Second
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return d
	}
	return fallback
}
