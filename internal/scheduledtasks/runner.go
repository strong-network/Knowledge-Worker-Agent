// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package scheduledtasks

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// runOutcomeTimeout bounds how long a single unattended run may stream before
// we stop waiting for its outcome. The chat process keeps running and the
// produced chat remains openable; we just stop blocking the scheduler on it.
const runOutcomeTimeout = 30 * time.Minute

// Injected seams so the package is unit-testable without the real chat backend
// (mirrors the pattern in internal/projects/autosync.go).
var (
	// createRunSession persists a new chat session bound to the task's shared
	// working directory and returns its id. Each run is its own session
	// ("a new chat the user can open and continue"), but they all share workdir
	// so a run can read files earlier runs left behind.
	createRunSession = func(label, workdir, model string) (string, error) {
		cfg := config.DefaultSessionConfig()
		cfg.Backend = config.NormalizeBackend(cfg.Backend)
		cfg.Yolo = true
		if strings.TrimSpace(label) != "" {
			cfg.Label = label
		}
		if strings.TrimSpace(workdir) != "" {
			cfg.Workdir = workdir
		}
		if strings.TrimSpace(model) != "" {
			cfg.Model = model
		}
		sid := chat.NewUUID()
		if err := db.CreateSession(sid, cfg); err != nil {
			return "", err
		}
		return sid, nil
	}

	// startRun sends the prompt on the session and returns the live stream to
	// watch for the run's outcome. Because the session is brand new it is never
	// already streaming, so SendOrEnqueue starts immediately.
	startRun = func(sessionID, prompt string) *chat.Stream {
		cfg, err := db.GetSessionConfig(sessionID)
		if err != nil {
			cfg = config.DefaultSessionConfig()
		}
		stream, _ := chat.SendOrEnqueue(sessionID, prompt, cfg)
		return stream
	}
)

// ExecuteRun runs one occurrence of a task to completion and records its
// outcome. It blocks until the run finishes (or times out), so callers that
// need runs serialized at boot can simply call it sequentially.
//
// trigger is one of "scheduled", "catch-up", or "manual". scheduledFor is the
// occurrence time this run stands in for (the fire time for manual runs).
func ExecuteRun(taskID, prompt, workdir, label, model, trigger string, scheduledFor time.Time) {
	runID := chat.NewUUID()
	run := db.TaskRun{
		ID:           runID,
		TaskID:       taskID,
		Trigger:      trigger,
		Status:       "running",
		ScheduledFor: formatTime(scheduledFor),
	}

	// Pre-flight the pinned model before anything is created.
	//
	// The ordinary chat path deliberately substitutes the server default when a
	// session's pin is no longer runnable, so a user whose provider changed can
	// keep chatting. That trade is wrong here: an unattended run has nobody to
	// notice the substitution, so a task pinned to an expensive reasoning model
	// would quietly keep producing output from whatever else happens to be
	// available, and the result would look like a successful run. Fail loudly
	// instead, naming the model, so the failure is visible in the run history
	// and the task can be repointed.
	if m := strings.TrimSpace(model); m != "" && !config.ModelRunnable(m) {
		run.Status = "failed"
		run.Error = "model " + m + " is not available in this workspace — sign in to its provider or change the task's model"
		if e := db.CreateTaskRun(run); e != nil {
			log.Printf("[scheduled-tasks] task=%s record failed run: %v", taskID, e)
		}
		log.Printf("[scheduled-tasks] task=%s run=%s (%s) -> failed %s", taskID, runID, trigger, run.Error)
		return
	}

	sid, err := createRunSession(label, workdir, model)
	if err != nil {
		run.Status = "failed"
		run.Error = "could not create run session: " + err.Error()
		if e := db.CreateTaskRun(run); e != nil {
			log.Printf("[scheduled-tasks] task=%s record failed run: %v", taskID, e)
		}
		return
	}
	run.SessionID = sid
	if err := db.CreateTaskRun(run); err != nil {
		log.Printf("[scheduled-tasks] task=%s create run row: %v", taskID, err)
	}

	stream := startRun(sid, prompt)
	if stream == nil {
		_ = db.SetTaskRunOutcome(runID, "failed", "could not start the run")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), runOutcomeTimeout)
	defer cancel()
	status, detail := waitOutcome(ctx, stream)
	if err := db.SetTaskRunOutcome(runID, status, detail); err != nil {
		log.Printf("[scheduled-tasks] task=%s record outcome: %v", taskID, err)
	}
	log.Printf("[scheduled-tasks] task=%s run=%s (%s) -> %s %s", taskID, runID, trigger, status, detail)
}

// waitOutcome drains a stream to its terminal "done" event and classifies the
// run. Any "error" event marks the run failed (first message wins); an
// "mcp_auth_required" event is the headless-auth failure, surfaced with a
// clear reason. If the wait times out or is cancelled, the run is failed.
func waitOutcome(ctx context.Context, s *chat.Stream) (status, detail string) {
	idx := 0
	var firstErr string
	for {
		batch, next, done := s.Wait(ctx, idx)
		idx = next
		for _, ev := range batch {
			switch ev["type"] {
			case "error":
				if firstErr == "" {
					firstErr = firstNonEmpty(strFromEvent(ev, "message"), strFromEvent(ev, "error"))
				}
			case "mcp_auth_required":
				if firstErr == "" {
					server := strFromEvent(ev, "server")
					if server != "" {
						firstErr = server + " authentication required (cannot log in during an unattended run)"
					} else {
						firstErr = "a tool needs interactive authentication, which is unavailable during an unattended run"
					}
				}
			}
		}
		if done {
			if firstErr != "" {
				return "failed", firstErr
			}
			return "succeeded", ""
		}
		// Wait returns (nil, idx, false) on context cancellation.
		if err := ctx.Err(); err != nil {
			if firstErr != "" {
				return "failed", firstErr
			}
			return "failed", "the run did not finish in time"
		}
	}
}

func strFromEvent(ev map[string]any, key string) string {
	if v, ok := ev[key].(string); ok {
		return v
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
