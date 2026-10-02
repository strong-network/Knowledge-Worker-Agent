// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package daynotes

// The backlog job: one goroutine that works through days which have no note.
//
// Two rules keep it from becoming expensive or wrong.
//
// **Only closed days are eligible.** db.PendingNoteDays never offers today.
// Notes are append-only -- a note written at lunchtime would permanently record
// only the morning, because no later pass rewrites an existing row. Waiting for
// the day to end is what makes "write once" safe.
//
// **Every pass is capped.** A workspace meeting this job for the first time has
// a backlog of months (174 closed session-days on the corpus this was designed
// against). Generating all of them at boot would be a large, unannounced bill
// arriving in one burst. The backlog is drained newest-first, a few days per
// pass, so the cost is spread rather than delivered as a single surprise on the
// first boot after an upgrade.

import (
	"context"
	"log"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

const (
	// perPassLimit is how many days one pass will summarise. At the measured
	// ~$0.0044 per note this is well under a cent per pass, and it means the
	// backfill of a long-running workspace arrives over days rather than as a
	// single burst on the first boot after an upgrade.
	perPassLimit = 25

	// passInterval is how often the job wakes once it has caught up. Days close
	// once a day, so in steady state there is at most a handful of work waiting
	// and there is no reason to look more often than this.
	passInterval = 6 * time.Hour

	// drainInterval is the gap between passes while a backlog is still being
	// worked through. A full batch means there is more waiting, and making the
	// user wait six hours for the next 25 is pointless: the model calls are the
	// only real cost and they are the same calls either way, just delivered
	// sooner. Measured on a real workspace a batch of 25 takes about three
	// minutes, so this is a short pause between batches rather than a busy loop.
	//
	// Before this the backfill was paced entirely by the sleep: 100 notes took
	// ~13.5 hours of wall clock for ~10 minutes of actual work, and only while
	// the workspace happened to be awake. That is not a safety property, just a
	// slow one -- the spend is bounded by the size of the backlog, not by how
	// long we take to get through it.
	drainInterval = 1 * time.Minute

	// startupDelay holds the first pass back until startup has settled. Nothing
	// here is urgent and it keeps model calls off the critical path of the
	// user's first request.
	startupDelay = 5 * time.Minute
)

// PassResult reports what one pass considered and did.
type PassResult struct {
	// Considered is how many pending days the pass looked at.
	Considered int
	// Written is how many notes were stored.
	Written int
	// Failed is how many days produced no usable note. They stay in the
	// backlog and are retried on a later pass.
	Failed int
}

// nextDelay reports how long to wait before the next pass.
//
// A full batch means the backlog is not empty, so the job keeps going at
// drainInterval instead of sleeping off the rest of the cycle. Anything short
// of a full batch means it has caught up and can settle to passInterval.
//
// The Written > 0 condition is what stops that becoming a retry loop. Failures
// stay in the backlog by design, so a batch that fails wholesale is still a
// full batch and would otherwise re-offer the same days a minute later, for
// ever, burning a model call on each. Requiring evidence of progress means a
// poisoned batch backs off to the slow cadence instead of spinning.
func (r PassResult) nextDelay() time.Duration {
	if r.Written > 0 && r.Considered >= perPassLimit {
		return drainInterval
	}
	return passInterval
}

// Start launches the backlog job: once shortly after startup, then periodically.
//
// What it is doing is logged rather than passing silently. Notes appear in an
// API response whose shape exists whether or not the job has caught up, so
// "nothing to do yet" and "broken" look identical from the outside: an empty
// `notes` array either way. One line at startup says the job is running and at
// what cadence, so an empty array can be read against it.
func Start() {
	log.Printf("[DAYNOTE] first pass in %s, then every %s (%s while catching up), up to %d days per pass",
		startupDelay, passInterval, drainInterval, perPassLimit)
	go func() {
		time.Sleep(startupDelay)
		for {
			// Sleep rather than a ticker: the gap depends on what the pass
			// found, and it is measured from the end of the pass so a slow
			// batch can never overlap the next one.
			time.Sleep(RunOnce(time.Now()).nextDelay())
		}
	}()
}

// RunOnce works through one capped batch of the backlog and logs what it did.
// Exported so it can be driven directly from tests without waiting on a ticker.
//
// now decides which days have closed; it is a parameter rather than time.Now()
// so the day boundary is testable and is decided in exactly one place.
func RunOnce(now time.Time) PassResult {
	var res PassResult

	model := noteModel()
	if model == "" {
		// No small-class model resolved. Notes are not worth running on a
		// frontier model unattended (see noteModel), so the pass is skipped --
		// loudly, because an empty `notes` array otherwise looks like a bug.
		log.Printf("[DAYNOTE] no small-class model available (nominate one as the `small` preset); skipping pass")
		return res
	}

	today := now.UTC().Format("2006-01-02")
	pending, err := db.PendingNoteDays(today, PromptVersion, perPassLimit)
	if err != nil {
		log.Printf("[DAYNOTE] could not read the backlog: %v", err)
		return res
	}
	res.Considered = len(pending)
	if len(pending) == 0 {
		return res
	}

	for _, c := range pending {
		if writeNote(c, model) {
			res.Written++
		} else {
			res.Failed++
		}
	}
	log.Printf("[DAYNOTE] pass complete: considered=%d written=%d failed=%d; next pass in %s",
		res.Considered, res.Written, res.Failed, res.nextDelay())
	return res
}

// writeNote generates and stores the note for one candidate day. It reports
// success; every failure is logged and swallowed, because a missing note is the
// status quo and the day simply stays in the backlog.
func writeNote(c db.NoteCandidate, model string) bool {
	msgs, err := db.DayMessages(c.SessionID, c.Day)
	if err != nil {
		log.Printf("[DAYNOTE] %s %s: could not read messages: %v", c.SessionID, c.Day, err)
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	note, err := Generate(ctx, config.OpencodeBin, model, msgs)
	if err != nil {
		log.Printf("[DAYNOTE] %s %s: %v", c.SessionID, c.Day, err)
		return false
	}

	// Title and project are snapshotted onto the note here, while the session
	// still exists. After a delete they are the only record of what the chat
	// was called -- the title does not live in a column, it lives inside the
	// sessions.config blob that disappears with the row.
	if err := db.SaveSessionNote(db.SessionNote{
		SessionID:     c.SessionID,
		Day:           c.Day,
		Note:          note,
		SessionTitle:  c.Title,
		ProjectID:     c.ProjectID,
		CoversFrom:    c.CoversFrom,
		CoversTo:      c.CoversTo,
		MsgCount:      c.MsgCount,
		Model:         model,
		PromptVersion: PromptVersion,
	}); err != nil {
		log.Printf("[DAYNOTE] %s %s: could not store note: %v", c.SessionID, c.Day, err)
		return false
	}
	return true
}

// noteModel picks the model that writes notes, or "" when the workspace offers
// nothing cheap enough to justify a background sweep.
//
// This delegates to config.SmallModel rather than ResolveModel, and the
// difference is the point. Titling can reasonably fall back to the session's
// own model: it is one line, produced in response to a message the user just
// sent, on the model they already chose to pay for. A note pass is the opposite
// shape -- tens of generations, unprompted, over history the user is not
// looking at. Ending that chain at the workspace default would mean a job
// nobody asked for silently billing frontier-model rates across a backlog.
//
// So there is no fall-through here. If no small-class model resolves, notes do
// not run, and RunOnce says why.
func noteModel() string {
	return config.SmallModel()
}
