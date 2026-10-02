// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package scheduledtasks

import (
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// stubRunSeams replaces the chat seams so no real backend is touched, and
// records whether a run session was created at all.
func stubRunSeams(t *testing.T, created *bool) {
	t.Helper()
	prevCreate, prevStart := createRunSession, startRun
	createRunSession = func(label, workdir, model string) (string, error) {
		*created = true
		return chat.NewUUID(), nil
	}
	startRun = func(sessionID, prompt string) *chat.Stream { return nil }
	t.Cleanup(func() {
		createRunSession, startRun = prevCreate, prevStart
	})
}

// TestExecuteRunFailsOnUnavailableModel is the guard for the one place the provider registry
// deliberately does NOT reuse ResolveModel's fallback.
//
// The chat path substitutes the server default when a pin is unrunnable, so a
// user whose provider changed can keep working. Doing that here would let an
// unattended task pinned to an expensive reasoning model quietly run on
// whatever else is available and report success, with nobody watching.
func TestExecuteRunFailsOnUnavailableModel(t *testing.T) {
	setupTasksDB(t)
	config.SetAvailableModels([]string{"mistral/small"})
	t.Cleanup(func() { config.SetAvailableModels(nil) })

	now := time.Now()
	mkTask(t, "task-1", "none", now, now, true)

	created := false
	stubRunSeams(t, &created)

	ExecuteRun("task-1", "do it", "", "Task", "github-copilot/claude-opus-5", "scheduled", now)

	if created {
		t.Error("a run session was created for an unavailable model")
	}
	runs := db.ListTaskRuns("task-1")
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	if runs[0].Status != "failed" {
		t.Errorf("status = %q, want failed", runs[0].Status)
	}
	// Naming the model is the point: "the run failed" alone does not tell the
	// owner that the task needs repointing.
	if !strings.Contains(runs[0].Error, "github-copilot/claude-opus-5") {
		t.Errorf("error %q does not name the model", runs[0].Error)
	}
}

func TestExecuteRunProceedsWhenModelIsAvailable(t *testing.T) {
	setupTasksDB(t)
	config.SetAvailableModels([]string{"mistral/small", "mistral/large"})
	t.Cleanup(func() { config.SetAvailableModels(nil) })

	now := time.Now()
	mkTask(t, "task-2", "none", now, now, true)

	created := false
	stubRunSeams(t, &created)

	ExecuteRun("task-2", "do it", "", "Task", "mistral/large", "scheduled", now)

	if !created {
		t.Fatal("pre-flight rejected an available model")
	}
}

func TestExecuteRunProceedsWithNoPinnedModel(t *testing.T) {
	// No pin means "use the workspace default", which is drawn from the
	// discovered list by construction. Nothing to pre-flight.
	setupTasksDB(t)
	config.SetAvailableModels([]string{"mistral/small"})
	t.Cleanup(func() { config.SetAvailableModels(nil) })

	now := time.Now()
	mkTask(t, "task-3", "none", now, now, true)

	created := false
	stubRunSeams(t, &created)

	ExecuteRun("task-3", "do it", "", "Task", "", "scheduled", now)

	if !created {
		t.Fatal("pre-flight rejected a task with no pinned model")
	}
}

func TestExecuteRunProceedsWhenDiscoveryHasNotRun(t *testing.T) {
	// An empty available list means "unknown", not "nothing is available". A
	// discovery that has not run yet, or failed, must not cancel every
	// scheduled task in the workspace.
	setupTasksDB(t)
	config.SetAvailableModels(nil)

	now := time.Now()
	mkTask(t, "task-4", "none", now, now, true)

	created := false
	stubRunSeams(t, &created)

	ExecuteRun("task-4", "do it", "", "Task", "github-copilot/claude-opus-5", "scheduled", now)

	if !created {
		t.Fatal("pre-flight rejected a task while the model list was unknown")
	}
}
