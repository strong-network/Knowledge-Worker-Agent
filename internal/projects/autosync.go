// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package projects

import (
	"log"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
	gitops "github.com/strong-network/Knowledge-Worker-Agent/internal/git"
)

// Auto-sync (background): for each repo-backed project, periodically fetch the
// remote and fast-forward pull when it's SAFE to do so — i.e. the workspace is
// clean, has no local commits, is behind, and no project chat is currently
// streaming (the agent could be writing files). Never merges, never touches a
// dirty or diverged workspace, so it can't clobber in-progress work.

const defaultAutoSyncInterval = 5 * time.Minute

// Injected for tests. Production wiring uses the real git + streaming checks.
var (
	syncStatus = func(dir string) gitops.Status { return gitops.StatusAt(dir) }
	syncFetch  = func(dir string) (string, error) { return gitops.FetchAt(dir) }
	syncPullFF = func(dir string) (string, error) { return gitops.PullFastForwardAt(dir) }
	// projectStreaming reports whether any chat in the project has a live
	// stream (so we don't pull while the agent may be editing files).
	projectStreaming = func(projectID string) bool {
		for _, s := range db.ListProjectSessions(projectID) {
			if chat.IsStreaming(s.SessionID) {
				return true
			}
		}
		return false
	}
)

// autoSyncInterval resolves the tick cadence from KWA_PROJECT_AUTO_SYNC_INTERVAL
// (a Go duration or bare seconds), defaulting to 5m. A value <= 0 disables it.
func autoSyncInterval() time.Duration {
	raw := strings.TrimSpace(env.Get("KWA_PROJECT_AUTO_SYNC_INTERVAL"))
	if raw == "" {
		return defaultAutoSyncInterval
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return d
	}
	if secs, err := time.ParseDuration(raw + "s"); err == nil {
		return secs
	}
	return defaultAutoSyncInterval
}

func autoSyncDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(env.Get("KWA_PROJECT_AUTO_SYNC"))) {
	case "0", "false", "no", "off":
		return true
	default:
		return false
	}
}

// StartAutoSync launches the background project auto-sync ticker. Non-blocking;
// safe to call once at startup. Honors KWA_PROJECT_AUTO_SYNC (off) and
// KWA_PROJECT_AUTO_SYNC_INTERVAL. Runs an initial pass shortly after start.
func StartAutoSync() {
	if autoSyncDisabled() {
		log.Printf("[project-sync] disabled via KWA_PROJECT_AUTO_SYNC")
		return
	}
	interval := autoSyncInterval()
	if interval <= 0 {
		log.Printf("[project-sync] disabled (interval <= 0)")
		return
	}
	log.Printf("[project-sync] auto-sync every %s", interval)
	go func() {
		// First pass after a short delay so it doesn't race startup bootstrap.
		time.Sleep(30 * time.Second)
		SyncAllProjects()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			SyncAllProjects()
		}
	}()
}

// SyncAllProjects runs one auto-sync pass over every repo-backed project.
// Exported so it can be triggered on demand and tested directly.
func SyncAllProjects() {
	for _, p := range db.ListProjects() {
		if strings.TrimSpace(p.RepoURL) == "" {
			continue // plain-folder projects are not git-backed
		}
		syncProject(p)
	}
}

// syncProject fetches and, when safe, fast-forward-pulls a single project's
// shared workspace. Returns true if a pull was performed (for tests).
func syncProject(p db.Project) bool {
	dir := strings.TrimSpace(p.WorkspacePath)
	if dir == "" {
		return false
	}
	st := syncStatus(dir)
	if !st.IsRepo || !st.HasRemote {
		return false
	}

	if _, err := syncFetch(dir); err != nil {
		// Best-effort: private repos with no valid credentials, offline, etc.
		log.Printf("[project-sync] %s: fetch failed (skipping): %v", p.Name, err)
		return false
	}

	// Re-read status after fetch so behind/ahead reflect the live remote.
	st = syncStatus(dir)
	safe := st.Behind > 0 && st.Ahead == 0 && !st.Dirty
	if !safe {
		return false
	}
	if projectStreaming(p.ID) {
		log.Printf("[project-sync] %s: %d behind but a chat is streaming — deferring pull", p.Name, st.Behind)
		return false
	}

	out, err := syncPullFF(dir)
	if err != nil {
		log.Printf("[project-sync] %s: ff-pull failed: %v\n%s", p.Name, err, out)
		return false
	}
	log.Printf("[project-sync] %s: fast-forwarded %d commit(s)", p.Name, st.Behind)
	return true
}
