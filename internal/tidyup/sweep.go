// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Workspace hygiene: the automatic sweep.
//
// One goroutine, one job. It deletes chats that contain nothing — no messages,
// no draft, nothing running — and have been idle past the empty-chat window.
// Everything else it leaves alone; chats that contain work are only ever
// removed by the user, through the review surface.
package tidyup

import (
	"log"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/workdirs"
)

const (
	// sweepInterval is how often the sweeper runs. Daily is far more often than
	// the 24-hour empty-chat window needs, and keeps a long-lived server from
	// drifting.
	sweepInterval = 24 * time.Hour

	// sweepStartupDelay holds the first sweep back until after startup has
	// settled. Nothing here is urgent, and it keeps deletion off the critical
	// path of a user's first request.
	sweepStartupDelay = 5 * time.Minute
)

// SweepResult reports what one sweep considered and did.
type SweepResult struct {
	// Considered is how many chats matched the database criteria.
	Considered int
	// Deleted is how many were actually removed.
	Deleted int
	// Skipped is how many were passed over because they stopped qualifying —
	// a process started, or the chat was used between the scan and the delete.
	Skipped int
	// WorkdirsRemoved is how many auto-created folders were deleted alongside.
	WorkdirsRemoved int
}

// StartSweeper launches the background hygiene sweep: once shortly after
// startup, then daily. It is a no-op when hygiene is disabled, so a deployment
// under a retention mandate can turn the whole thing off with one variable.
func StartSweeper() {
	if !config.HygieneEnabled {
		log.Printf("[hygiene] disabled (KWA_HYGIENE_ENABLED); no automatic cleanup will run")
		return
	}
	go func() {
		time.Sleep(sweepStartupDelay)
		SweepOnce(time.Now())

		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()
		for range ticker.C {
			SweepOnce(time.Now())
		}
	}()
}

// SweepOnce runs a single sweep and logs what it did. Exported so it can be
// driven directly from tests without waiting on a ticker.
func SweepOnce(now time.Time) SweepResult {
	var res SweepResult
	if !config.HygieneEnabled || config.HygieneEmptyAfter <= 0 {
		return res
	}

	candidates := db.EmptyChatCandidates(now, config.HygieneEmptyAfter)
	res.Considered = len(candidates)

	for _, c := range candidates {
		// A chat with a live process or an open stream is being used right now,
		// whatever its timestamps say. The database cannot see that, so it is
		// checked here — and checked before the delete, so a turn that starts
		// mid-sweep is caught by the conditional delete instead.
		if isActive(c.ID) {
			res.Skipped++
			continue
		}
		deleted, err := db.DeleteEmptySession(c.ID, now, config.HygieneEmptyAfter)
		if err != nil {
			log.Printf("[hygiene] failed to delete empty chat %s: %v", c.ID, err)
			res.Skipped++
			continue
		}
		if !deleted {
			// The chat stopped being empty between the scan and the delete.
			// Its files stay exactly where they are.
			res.Skipped++
			continue
		}
		res.Deleted++
		chat.ClearQueue(c.ID)
		if workdirs.RemoveAutoChat(c.Workdir) {
			res.WorkdirsRemoved++
		}
	}

	if res.Considered > 0 {
		log.Printf("[hygiene] sweep: considered=%d deleted=%d skipped=%d workdirs_removed=%d (empty chats idle over %s)",
			res.Considered, res.Deleted, res.Skipped, res.WorkdirsRemoved, config.HygieneEmptyAfter)
	}
	return res
}

// isActive reports whether a session has work in flight — a running backend
// process, or a stream a browser may still be attached to.
func isActive(sessionID string) bool {
	if _, ok := chat.Procs.Load(sessionID); ok {
		return true
	}
	if _, ok := chat.Streams.Load(sessionID); ok {
		return true
	}
	return false
}
