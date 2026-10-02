// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"database/sql"
	"time"
)

// Durable file storage: backup / retention state.
//
// This file holds the queryable persistence for durable-storage backup state
// and the inactivity-retention model. The semantic per-chat flags (Temporary,
// Keep) live in config.SessionConfig; the state that tidy-up eligibility is
// computed from (last successful sync, the durable remote, sync status, and
// project-level Keep) lives in real columns so it can be queried with SQL.

// Backup status values surfaced to the UI in plain language (never raw git).
const (
	BackupStatusUnset   = ""          // never attempted
	BackupStatusSynced  = "synced"    // durable copy is up to date
	BackupStatusSyncing = "syncing"   // a sync is in flight
	BackupStatusOffline = "offline"   // remote unreachable
	BackupStatusError   = "error"     // last sync failed
	BackupStatusSkipped = "not_backed_up" // Temporary chats: intentionally no durable store
)

// RetentionTier classifies how long a container's durable copy is retained
// after inactivity. Defaults differ by container type.
type RetentionTier string

const (
	// TierLoose is an everyday, non-project chat. Shortest keep.
	TierLoose RetentionTier = "loose"
	// TierPersonal is a personal project. Longer keep.
	TierPersonal RetentionTier = "personal"
	// TierShared is a shared project. Longest keep; the clock
	// resets on any collaborator's activity.
	TierShared RetentionTier = "shared"
	// TierTemporary is a throwaway chat: not backed up, purged after a short
	// inactivity window.
	TierTemporary RetentionTier = "temporary"
)

// Default inactivity-retention windows per tier. These are the
// product defaults; admin-governed overrides are a later slice. Expressed as
// durations so callers can compute eligibility from a last-activity timestamp.
const (
	RetentionLoose     = 6 * 30 * 24 * time.Hour  // ~6 months
	RetentionPersonal  = 12 * 30 * 24 * time.Hour // ~12 months
	RetentionShared    = 24 * 30 * 24 * time.Hour // ~24 months
	RetentionTemporary = 72 * time.Hour           // 72 hours
)

// RetentionWindow returns the default inactivity window for a tier. An unknown
// tier falls back to the (shortest) loose window so nothing is retained longer
// than intended by accident.
func RetentionWindow(tier RetentionTier) time.Duration {
	switch tier {
	case TierPersonal:
		return RetentionPersonal
	case TierShared:
		return RetentionShared
	case TierTemporary:
		return RetentionTemporary
	default:
		return RetentionLoose
	}
}

// SetSessionBackupState records the durable remote and/or sync status for a
// chat. Empty remote leaves the existing value; status is always written.
// On a successful sync (BackupStatusSynced) last_synced_at is bumped to now.
func SetSessionBackupState(sessionID, remote, status string) error {
	if remote != "" {
		if _, err := DB.Exec(
			`UPDATE sessions SET backup_remote = ? WHERE id = ?`, remote, sessionID,
		); err != nil {
			return err
		}
	}
	if status == BackupStatusSynced {
		_, err := DB.Exec(
			`UPDATE sessions SET backup_status = ?, last_synced_at = ? WHERE id = ?`,
			status, time.Now().UTC().Format(time.RFC3339), sessionID,
		)
		return err
	}
	_, err := DB.Exec(`UPDATE sessions SET backup_status = ? WHERE id = ?`, status, sessionID)
	return err
}

// SetProjectBackupState records the sync status for a project. On a successful
// sync last_synced_at is bumped to now.
func SetProjectBackupState(projectID, status string) error {
	if status == BackupStatusSynced {
		_, err := DB.Exec(
			`UPDATE projects SET backup_status = ?, last_synced_at = ? WHERE id = ?`,
			status, time.Now().UTC().Format(time.RFC3339), projectID,
		)
		return err
	}
	_, err := DB.Exec(`UPDATE projects SET backup_status = ? WHERE id = ?`, status, projectID)
	return err
}

// BackupState is the durable-storage state for a container, surfaced to the
// backup/sync status affordance.
type BackupState struct {
	Remote       string `json:"remote"`
	Status       string `json:"status"`
	LastSyncedAt string `json:"last_synced_at"`
}

// GetSessionBackupState returns the durable-storage state for a chat. A
// missing session yields a zero-value state (not an error).
func GetSessionBackupState(sessionID string) BackupState {
	var remote, status string
	var lastSynced sql.NullString
	_ = DB.QueryRow(
		`SELECT backup_remote, backup_status, last_synced_at FROM sessions WHERE id = ?`,
		sessionID,
	).Scan(&remote, &status, &lastSynced)
	return BackupState{Remote: remote, Status: status, LastSyncedAt: lastSynced.String}
}

// GetProjectBackupState returns the durable-storage state for a project. The
// remote is carried on the project's repo_url (not duplicated here). A missing
// project yields a zero-value state.
func GetProjectBackupState(projectID string) BackupState {
	var repoURL, status string
	var lastSynced sql.NullString
	_ = DB.QueryRow(
		`SELECT repo_url, backup_status, last_synced_at FROM projects WHERE id = ?`,
		projectID,
	).Scan(&repoURL, &status, &lastSynced)
	return BackupState{Remote: repoURL, Status: status, LastSyncedAt: lastSynced.String}
}

// SetProjectKeep marks (or unmarks) a project as exempt from inactivity
// cleanup, retained indefinitely ("Keep").
func SetProjectKeep(projectID string, keep bool) error {
	v := 0
	if keep {
		v = 1
	}
	_, err := DB.Exec(`UPDATE projects SET keep = ? WHERE id = ?`, v, projectID)
	return err
}

// GetProjectKeep reports whether a project is marked Keep.
func GetProjectKeep(projectID string) bool {
	var keep int
	_ = DB.QueryRow(`SELECT keep FROM projects WHERE id = ?`, projectID).Scan(&keep)
	return keep == 1
}
