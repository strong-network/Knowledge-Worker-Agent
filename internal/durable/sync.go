// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package durable

import (
	"log"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/git"
)

// Automatic sync orchestration.
//
// The workspace is the live working copy; the durable remote is the copy of
// record. Sync happens *around* file operations at sensible checkpoints — for
// now, at the end of each chat turn — so durable state does not materially lag
// the workspace, without the user managing commits. This is the Git-backend
// implementation of the sync model; the pluggable-backend seam is a later slice.

// syncCommitMessage is the checkpoint commit message. It is deliberately
// generic and non-sensitive (no prompt text) since it lands in durable history.
const syncCommitMessage = "Knowledge Worker Agent: sync workspace"

// SyncChat backs up a chat's workspace to its durable remote at a checkpoint.
// It is a no-op (returns false) when:
//   - the chat is Temporary (never backed up), or
//   - the workspace is not a git repo with a configured remote (backup not yet
//     provisioned — a later slice adds remote provisioning on "enable backup").
//
// On an attempted sync it records backup status transitions (syncing → synced
// or error) so the status affordance can reflect them. Runs synchronously;
// callers on a hot path should invoke it in a goroutine. Returns true if a sync
// was attempted.
func SyncChat(sessionID string, cfg config.SessionConfig) bool {
	if cfg.Temporary {
		return false
	}
	return sync(cfg.Workdir, func(status string) {
		_ = db.SetSessionBackupState(sessionID, "", status)
	})
}

// SyncProject backs up a project's shared workspace to its durable remote.
// Same semantics as SyncChat but keyed by project. Projects are never
// Temporary. Returns true if a sync was attempted.
func SyncProject(projectID, workspacePath string) bool {
	return sync(workspacePath, func(status string) {
		_ = db.SetProjectBackupState(projectID, status)
	})
}

// sync performs the shared commit/push checkpoint against workdir, invoking
// setStatus at each state transition. Returns false without touching status
// when there is no durable remote to sync to.
func sync(workdir string, setStatus func(status string)) bool {
	isRepo, hasRemote := git.RepoState(workdir)
	if !isRepo || !hasRemote {
		return false
	}
	setStatus(db.BackupStatusSyncing)
	if out, err := git.CommitAndPush(workdir, syncCommitMessage); err != nil {
		log.Printf("[durable] sync failed for %s: %v\n%s", workdir, err, out)
		setStatus(db.BackupStatusError)
		return true
	}
	setStatus(db.BackupStatusSynced)
	return true
}
