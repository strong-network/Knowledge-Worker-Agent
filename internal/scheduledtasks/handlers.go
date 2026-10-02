// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package scheduledtasks

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// Handlers exposes the scheduled-tasks management API. Register with
// RegisterRoutes. The execution seam (runTask) and clock (now) live in
// scheduler.go so the whole package shares one injectable set of seams.
type Handlers struct{}

// RegisterRoutes wires the scheduled-tasks endpoints onto mux:
//
//	GET    /api/scheduled-tasks                     list tasks (with run counts)
//	POST   /api/scheduled-tasks                     create a task
//	PUT    /api/scheduled-tasks/{id}                update task metadata
//	DELETE /api/scheduled-tasks/{id}                delete a task (and its runs)
//	POST   /api/scheduled-tasks/{id}/enabled        enable/disable a task
//	POST   /api/scheduled-tasks/{id}/run            run now (manual trigger)
//	POST   /api/scheduled-tasks/{id}/pending        resolve a pending catch-up
//	GET    /api/scheduled-tasks/{id}/runs           list a task's runs
//	POST   /api/scheduled-tasks/runs/{runId}/opened mark a run's chat opened
func (h *Handlers) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/scheduled-tasks", h.list)
	mux.HandleFunc("POST /api/scheduled-tasks", h.create)
	mux.HandleFunc("PUT /api/scheduled-tasks/{id}", h.update)
	mux.HandleFunc("DELETE /api/scheduled-tasks/{id}", h.delete)
	mux.HandleFunc("POST /api/scheduled-tasks/{id}/enabled", h.setEnabled)
	mux.HandleFunc("POST /api/scheduled-tasks/{id}/run", h.runNow)
	mux.HandleFunc("POST /api/scheduled-tasks/{id}/pending", h.resolvePending)
	mux.HandleFunc("GET /api/scheduled-tasks/{id}/runs", h.listRuns)
	mux.HandleFunc("POST /api/scheduled-tasks/runs/{runId}/opened", h.markOpened)
}

// taskPayload is the create/update request body. Times are RFC3339 strings in
// the workspace's local time; the server derives next_run_at from them.
type taskPayload struct {
	Name       string `json:"name"`
	Prompt     string `json:"prompt"`
	Workdir    string `json:"workdir"`
	Repeat     string `json:"repeat"`
	Model      string `json:"model"`
	FirstRunAt string `json:"first_run_at"`
	Enabled    *bool  `json:"enabled"`
}

// taskView is the enriched task representation returned to the client: the
// stored row plus a human-readable schedule summary.
type taskView struct {
	db.ScheduledTask
	Summary string `json:"summary"`
}

func viewOf(t db.ScheduledTask) taskView {
	first, _ := parseTime(t.FirstRunAt)
	return taskView{ScheduledTask: t, Summary: Summary(first, t.Repeat)}
}

func (h *Handlers) list(w http.ResponseWriter, r *http.Request) {
	tasks := db.ListScheduledTasks()
	views := make([]taskView, 0, len(tasks))
	for _, t := range tasks {
		views = append(views, viewOf(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": views})
}

func (h *Handlers) create(w http.ResponseWriter, r *http.Request) {
	var p taskPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "The request body wasn't valid.")
		return
	}
	p.Name = strings.TrimSpace(p.Name)
	p.Prompt = strings.TrimSpace(p.Prompt)
	p.Repeat = strings.TrimSpace(p.Repeat)
	if p.Prompt == "" {
		writeError(w, http.StatusBadRequest, "A task needs a prompt to run.")
		return
	}
	if p.Repeat == "" {
		p.Repeat = RepeatNone
	}
	if !ValidRepeat(p.Repeat) {
		writeError(w, http.StatusBadRequest, "That repeat option isn't recognized.")
		return
	}
	first, ok := parseTime(p.FirstRunAt)
	if !ok {
		writeError(w, http.StatusBadRequest, "Pick a valid date and time for the first run.")
		return
	}

	label := p.Name
	if label == "" {
		label = DeriveLabel(p.Prompt)
	}
	taskID := chat.NewUUID()
	workdir := strings.TrimSpace(p.Workdir)
	if workdir == "" {
		provisioned, err := provisionTaskWorkdir(label, taskID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Couldn't create a working folder for this task.")
			return
		}
		workdir = provisioned
	}

	enabled := true
	if p.Enabled != nil {
		enabled = *p.Enabled
	}

	task := db.ScheduledTask{
		ID:         taskID,
		Name:       p.Name,
		Prompt:     p.Prompt,
		Workdir:    workdir,
		Repeat:     p.Repeat,
		Model:      strings.TrimSpace(p.Model),
		FirstRunAt: formatTime(firstOccurrence(first, p.Repeat)),
		NextRunAt:  initialNext(first, p.Repeat),
		Enabled:    enabled,
	}
	if err := db.CreateScheduledTask(task); err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't save the scheduled task.")
		return
	}
	writeJSON(w, http.StatusCreated, viewOf(*db.GetScheduledTask(task.ID)))
}

func (h *Handlers) update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing := db.GetScheduledTask(id)
	if existing == nil {
		writeError(w, http.StatusNotFound, "That scheduled task doesn't exist.")
		return
	}
	var p taskPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "The request body wasn't valid.")
		return
	}
	p.Name = strings.TrimSpace(p.Name)
	p.Prompt = strings.TrimSpace(p.Prompt)
	p.Repeat = strings.TrimSpace(p.Repeat)
	if p.Prompt == "" {
		writeError(w, http.StatusBadRequest, "A task needs a prompt to run.")
		return
	}
	if p.Repeat == "" {
		p.Repeat = RepeatNone
	}
	if !ValidRepeat(p.Repeat) {
		writeError(w, http.StatusBadRequest, "That repeat option isn't recognized.")
		return
	}
	first, ok := parseTime(p.FirstRunAt)
	if !ok {
		writeError(w, http.StatusBadRequest, "Pick a valid date and time for the first run.")
		return
	}
	// Workdir is immutable once set (all runs share it); ignore any change.
	if err := db.UpdateScheduledTaskMeta(id, p.Name, p.Prompt, existing.Workdir,
		p.Repeat, strings.TrimSpace(p.Model), formatTime(firstOccurrence(first, p.Repeat)), initialNext(first, p.Repeat)); err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't update the scheduled task.")
		return
	}
	writeJSON(w, http.StatusOK, viewOf(*db.GetScheduledTask(id)))
}

func (h *Handlers) delete(w http.ResponseWriter, r *http.Request) {
	if err := db.DeleteScheduledTask(r.PathValue("id")); err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't delete the scheduled task.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handlers) setEnabled(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing := db.GetScheduledTask(id)
	if existing == nil {
		writeError(w, http.StatusNotFound, "That scheduled task doesn't exist.")
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "The request body wasn't valid.")
		return
	}
	// Re-enabling recomputes the next occurrence from "now" so a task that was
	// paused across its scheduled time picks up at the next future slot.
	next := existing.NextRunAt
	if body.Enabled {
		first, _ := parseTime(existing.FirstRunAt)
		next = initialNext(first, existing.Repeat)
	}
	if err := db.SetScheduledTaskEnabled(id, body.Enabled, next); err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't update the scheduled task.")
		return
	}
	writeJSON(w, http.StatusOK, viewOf(*db.GetScheduledTask(id)))
}

func (h *Handlers) runNow(w http.ResponseWriter, r *http.Request) {
	t := db.GetScheduledTask(r.PathValue("id"))
	if t == nil {
		writeError(w, http.StatusNotFound, "That scheduled task doesn't exist.")
		return
	}
	label := labelFor(*t)
	go runTask(t.ID, t.Prompt, t.Workdir, label, t.Model, "manual", now())
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (h *Handlers) resolvePending(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t := db.GetScheduledTask(id)
	if t == nil {
		writeError(w, http.StatusNotFound, "That scheduled task doesn't exist.")
		return
	}
	var body struct {
		Action string `json:"action"` // "run" | "skip"
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "The request body wasn't valid.")
		return
	}
	if t.PendingCatchupAt == "" {
		writeError(w, http.StatusConflict, "This task has no missed run to resolve.")
		return
	}
	if strings.TrimSpace(body.Action) == "run" {
		scheduledFor, _ := parseTime(t.PendingCatchupAt)
		go runTask(t.ID, t.Prompt, t.Workdir, labelFor(*t), t.Model, "catch-up", scheduledFor)
	}
	// Both run and skip clear the pending marker.
	if err := db.SetScheduledTaskPendingCatchup(id, ""); err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't update the scheduled task.")
		return
	}
	writeJSON(w, http.StatusOK, viewOf(*db.GetScheduledTask(id)))
}

func (h *Handlers) listRuns(w http.ResponseWriter, r *http.Request) {
	runs := db.ListTaskRuns(r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (h *Handlers) markOpened(w http.ResponseWriter, r *http.Request) {
	if err := db.SetTaskRunOpened(r.PathValue("runId"), true); err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't update the run.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// initialNext returns the RFC3339 next occurrence at or after the first slot,
// coalescing to the next future slot if the first has already passed.
func initialNext(first time.Time, repeat string) string {
	occ := firstOccurrence(first, repeat)
	if occ.After(now()) {
		return formatTime(occ)
	}
	if future, ok := NextRun(first, repeat, now()); ok {
		return formatTime(future)
	}
	return ""
}

func labelFor(t db.ScheduledTask) string {
	if s := strings.TrimSpace(t.Name); s != "" {
		return s
	}
	return DeriveLabel(t.Prompt)
}

// provisionTaskWorkdir creates a dedicated shared directory for a task under
// <Scheduled Tasks>/<slug>-<shortID>. All of the task's runs reuse this
// directory.
func provisionTaskWorkdir(label, taskID string) (string, error) {
	root := layout.ScheduledTasks()
	base := slugifyTask(label)
	// Append a short task-ID suffix so each task gets a distinct folder even
	// when labels collide (dedupe below still guards against any residual clash).
	if suffix := shortID(taskID); suffix != "" {
		base = base + "-" + suffix
	}
	dir := uniqueTaskDir(filepath.Join(root, base))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// shortID returns the first hex segment of a UUID (up to 8 chars), safe for use
// in a filesystem path.
func shortID(id string) string {
	id = strings.TrimSpace(id)
	if i := strings.IndexByte(id, '-'); i > 0 {
		id = id[:i]
	}
	if len(id) > 8 {
		id = id[:8]
	}
	return id
}

func slugifyTask(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	prevDash := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == '-' || r == '_' || r == ' ' || r == '.':
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = "task"
	}
	return s
}

func uniqueTaskDir(base string) string {
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return base
	}
	for i := 2; i < 1000; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
	return fmt.Sprintf("%s-%d", base, os.Getpid())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
