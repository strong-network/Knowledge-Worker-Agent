// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package durable

import (
	"fmt"
	"log"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/git"
)

// Enable-backup orchestration.
//
// "Enable backup" binds a container's workspace to a durable Git remote and
// performs the first sync. It composes the write-side pieces in the required
// order: scaffold the folder convention, install secret exclusions (so nothing
// sensitive is ever pushed), attach the remote, then commit + push. The
// resulting remote and sync status are persisted so the status affordance and
// later automatic syncs (SyncChat/SyncProject) can find them.

// ErrTemporaryChat is returned when backup is requested for a Temporary chat.
// Temporary chats are never provisioned a durable store by contract.
var ErrTemporaryChat = fmt.Errorf("temporary chats are not backed up")

// EnableBackupForChat provisions durable backup for a loose chat: scaffolds the
// share, writes secret exclusions, attaches remoteURL, and performs the first
// sync. It records the remote and status on the session. Temporary chats are
// rejected. The chat's workspace is cfg.Workdir. Returns the git transcript
// from provisioning for surfacing/logging.
func EnableBackupForChat(sessionID, chatID, projectID string, cfg config.SessionConfig, remoteURL string) (string, error) {
	if cfg.Temporary {
		return "", ErrTemporaryChat
	}
	transcript, err := enableBackup(cfg.Workdir, remoteURL, func() error {
		return WriteChatMetadata(cfg.Workdir, chatID, projectID, identityFromEnv())
	}, func(status string) {
		_ = db.SetSessionBackupState(sessionID, remoteURL, status)
	})
	return transcript, err
}

// EnableBackupForProject provisions durable backup for a project shared
// workspace. Same steps as EnableBackupForChat, keyed by project.
func EnableBackupForProject(projectID, workspacePath, remoteURL string) (string, error) {
	transcript, err := enableBackup(workspacePath, remoteURL, func() error {
		return WriteProjectMetadata(workspacePath, projectID, identityFromEnv())
	}, func(status string) {
		// Persist the project's status; the remote is recorded separately on the
		// projects.repo_url field by the caller when appropriate.
		_ = db.SetProjectBackupState(projectID, status)
	})
	return transcript, err
}

// enableBackup runs the shared provisioning sequence against workdir. writeMeta
// writes the appropriate .system/metadata; setStatus records status/remote
// transitions. On the happy path it ends in BackupStatusSynced; on failure it
// records BackupStatusError and returns the error with the transcript so far.
func enableBackup(workdir, remoteURL string, writeMeta func() error, setStatus func(status string)) (string, error) {
	// 1. Scaffold the folder convention (inputs/, working/, .system/).
	if err := ScaffoldShare(workdir); err != nil {
		setStatus(db.BackupStatusError)
		return "", fmt.Errorf("scaffold share: %w", err)
	}
	// 2. Install secret exclusions BEFORE any commit so secrets never enter
	//    durable history.
	if err := EnsureSecretExclusions(workdir); err != nil {
		setStatus(db.BackupStatusError)
		return "", fmt.Errorf("secret exclusions: %w", err)
	}
	// 3. Write the platform-populated metadata descriptor.
	if err := writeMeta(); err != nil {
		// Non-fatal to backup itself, but log — the descriptor is best-effort.
		log.Printf("[durable] metadata write failed for %s: %v", workdir, err)
	}
	// 4. Attach the remote (init if needed, remote add/set-url, first push).
	setStatus(db.BackupStatusSyncing)
	transcript, err := git.AttachRemote(workdir, remoteURL)
	if err != nil {
		setStatus(db.BackupStatusError)
		return transcript, fmt.Errorf("attach remote: %w", err)
	}
	// 5. Final commit+push to capture the scaffold/metadata/gitignore that were
	//    written after AttachRemote's initial push, and mark synced.
	if out, err := git.CommitAndPush(workdir, syncCommitMessage); err != nil {
		setStatus(db.BackupStatusError)
		return transcript + out, fmt.Errorf("initial sync: %w", err)
	}
	setStatus(db.BackupStatusSynced)
	return transcript, nil
}
