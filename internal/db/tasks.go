// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import "time"

// ScheduledTask is a task definition: a prompt run unattended on a
// schedule. All of a task's runs share one working directory (workdir), so a
// run can build on files earlier runs left behind. Times (first_run_at,
// next_run_at, pending_catchup_at) are RFC3339 strings; empty means "unset".
//
//   - Repeat is one of: none, daily, weekdays, weekly.
//   - NextRunAt is the next occurrence the scheduler will fire (empty for a
//     one-shot task that has already run).
//   - PendingCatchupAt, when set, is a missed occurrence that was > the
//     staleness bound late at boot and awaits a user Run-now / Skip decision.
type ScheduledTask struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Prompt           string `json:"prompt"`
	Workdir          string `json:"workdir"`
	Repeat           string `json:"repeat"`
	// Model is the provider/model id runs execute with (empty = the workspace
	// default), chosen per task.
	Model            string `json:"model"`
	FirstRunAt       string `json:"first_run_at"`
	NextRunAt        string `json:"next_run_at"`
	Enabled          bool   `json:"enabled"`
	PendingCatchupAt string `json:"pending_catchup_at"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
	RunCount         int    `json:"run_count"`
}

// TaskRun is one execution of a scheduled task. It links to the chat session it
// produced (SessionID) and records how it was triggered and how it ended.
//
//   - Trigger: scheduled | catch-up | manual.
//   - Status:  running | succeeded | failed.
//   - Opened:  whether the user has opened the produced chat yet (the per-run
//     "new run" marker; cleared on first open).
//   - ScheduledFor: the occurrence time this run stands in for (RFC3339).
type TaskRun struct {
	ID           string `json:"id"`
	TaskID       string `json:"task_id"`
	SessionID    string `json:"session_id"`
	Trigger      string `json:"trigger"`
	Status       string `json:"status"`
	Error        string `json:"error"`
	Opened       bool   `json:"opened"`
	ScheduledFor string `json:"scheduled_for"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// CreateScheduledTask inserts a new task. The caller is responsible for having
// provisioned workdir on disk before/after.
func CreateScheduledTask(t ScheduledTask) error {
	_, err := DB.Exec(`
		INSERT INTO scheduled_tasks
			(id, name, prompt, workdir, repeat, model, first_run_at, next_run_at, enabled, pending_catchup_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.Prompt, t.Workdir, t.Repeat, t.Model,
		t.FirstRunAt, t.NextRunAt, boolToInt(t.Enabled), t.PendingCatchupAt,
	)
	return err
}

// GetScheduledTask returns one task (with its live run count) or nil.
func GetScheduledTask(id string) *ScheduledTask {
	row := DB.QueryRow(`
		SELECT t.id, t.name, t.prompt, t.workdir, t.repeat, t.model, t.first_run_at,
			t.next_run_at, t.enabled, t.pending_catchup_at, t.created_at, t.updated_at,
			(SELECT COUNT(*) FROM task_runs r WHERE r.task_id = t.id) AS run_count
		FROM scheduled_tasks t WHERE t.id = ?
	`, id)
	t, err := scanScheduledTask(row)
	if err != nil {
		return nil
	}
	return t
}

// ListScheduledTasks returns all tasks, most-recently-updated first, each with
// a live run count. Never returns nil.
func ListScheduledTasks() []ScheduledTask {
	rows, err := DB.Query(`
		SELECT t.id, t.name, t.prompt, t.workdir, t.repeat, t.model, t.first_run_at,
			t.next_run_at, t.enabled, t.pending_catchup_at, t.created_at, t.updated_at,
			(SELECT COUNT(*) FROM task_runs r WHERE r.task_id = t.id) AS run_count
		FROM scheduled_tasks t
		ORDER BY t.updated_at DESC, t.created_at DESC
	`)
	if err != nil {
		return []ScheduledTask{}
	}
	defer rows.Close()

	out := []ScheduledTask{}
	for rows.Next() {
		t, err := scanScheduledTask(rows)
		if err != nil {
			continue
		}
		out = append(out, *t)
	}
	return out
}

func scanScheduledTask(row rowScanner) (*ScheduledTask, error) {
	var t ScheduledTask
	var enabled int
	if err := row.Scan(
		&t.ID, &t.Name, &t.Prompt, &t.Workdir, &t.Repeat, &t.Model, &t.FirstRunAt,
		&t.NextRunAt, &enabled, &t.PendingCatchupAt, &t.CreatedAt, &t.UpdatedAt,
		&t.RunCount,
	); err != nil {
		return nil, err
	}
	t.Enabled = enabled != 0
	return &t, nil
}

// UpdateScheduledTaskMeta updates the user-editable fields of a task (name,
// prompt, workdir, repeat, model, first/next run times). Bumps updated_at.
func UpdateScheduledTaskMeta(id, name, prompt, workdir, repeat, model, firstRunAt, nextRunAt string) error {
	_, err := DB.Exec(`
		UPDATE scheduled_tasks
		SET name = ?, prompt = ?, workdir = ?, repeat = ?, model = ?, first_run_at = ?, next_run_at = ?, updated_at = ?
		WHERE id = ?`,
		name, prompt, workdir, repeat, model, firstRunAt, nextRunAt,
		time.Now().UTC().Format(time.RFC3339), id,
	)
	return err
}

// SetScheduledTaskNextRun sets a task's next scheduled occurrence (empty clears
// it, marking a one-shot task done). Does not bump updated_at so scheduler
// bookkeeping doesn't reorder the user's list.
func SetScheduledTaskNextRun(id, nextRunAt string) error {
	_, err := DB.Exec(`UPDATE scheduled_tasks SET next_run_at = ? WHERE id = ?`, nextRunAt, id)
	return err
}

// SetScheduledTaskPendingCatchup sets (or clears, with "") the pending
// catch-up decision timestamp for a task.
func SetScheduledTaskPendingCatchup(id, pendingAt string) error {
	_, err := DB.Exec(`UPDATE scheduled_tasks SET pending_catchup_at = ? WHERE id = ?`, pendingAt, id)
	return err
}

// SetScheduledTaskEnabled toggles a task on/off. Disabling also clears any
// pending catch-up decision (a disabled task is not evaluated and shows
// no pending decision). The caller supplies the fresh next_run_at to seed when
// re-enabling (schedule starts fresh from reactivation), or "" when disabling.
func SetScheduledTaskEnabled(id string, enabled bool, nextRunAt string) error {
	pending := ""
	_, err := DB.Exec(`
		UPDATE scheduled_tasks
		SET enabled = ?, next_run_at = ?, pending_catchup_at = ?, updated_at = ?
		WHERE id = ?`,
		boolToInt(enabled), nextRunAt, pending, time.Now().UTC().Format(time.RFC3339), id,
	)
	return err
}

// DeleteScheduledTask removes a task and its run records (task_runs cascades).
// The produced chat sessions are a separate concern handled by the caller
// (which decides whether to also delete them).
func DeleteScheduledTask(id string) error {
	_, err := DB.Exec(`DELETE FROM scheduled_tasks WHERE id = ?`, id)
	return err
}

// --- task_runs ---

// CreateTaskRun inserts a new run row (typically in the "running" state).
func CreateTaskRun(r TaskRun) error {
	_, err := DB.Exec(`
		INSERT INTO task_runs
			(id, task_id, session_id, trigger, status, error, opened, scheduled_for)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.TaskID, r.SessionID, r.Trigger, r.Status, r.Error,
		boolToInt(r.Opened), r.ScheduledFor,
	)
	return err
}

// SetTaskRunOutcome records a run's terminal status (succeeded/failed) and
// error detail, bumping updated_at.
func SetTaskRunOutcome(id, status, errDetail string) error {
	_, err := DB.Exec(`
		UPDATE task_runs SET status = ?, error = ?, updated_at = ? WHERE id = ?`,
		status, errDetail, time.Now().UTC().Format(time.RFC3339), id,
	)
	return err
}

// SetTaskRunOpened marks a run's produced chat as opened, clearing its new-run
// marker.
func SetTaskRunOpened(id string, opened bool) error {
	_, err := DB.Exec(`UPDATE task_runs SET opened = ? WHERE id = ?`, boolToInt(opened), id)
	return err
}

// ListTaskRuns returns a task's runs, newest first. Never returns nil.
func ListTaskRuns(taskID string) []TaskRun {
	rows, err := DB.Query(`
		SELECT id, task_id, session_id, trigger, status, error, opened, scheduled_for, created_at, updated_at
		FROM task_runs WHERE task_id = ?
		ORDER BY created_at DESC, id DESC
	`, taskID)
	if err != nil {
		return []TaskRun{}
	}
	defer rows.Close()

	out := []TaskRun{}
	for rows.Next() {
		r, err := scanTaskRun(rows)
		if err != nil {
			continue
		}
		out = append(out, *r)
	}
	return out
}

func scanTaskRun(row rowScanner) (*TaskRun, error) {
	var r TaskRun
	var opened int
	if err := row.Scan(
		&r.ID, &r.TaskID, &r.SessionID, &r.Trigger, &r.Status, &r.Error,
		&opened, &r.ScheduledFor, &r.CreatedAt, &r.UpdatedAt,
	); err != nil {
		return nil, err
	}
	r.Opened = opened != 0
	return &r, nil
}
