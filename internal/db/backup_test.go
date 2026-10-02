// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

func TestRetentionWindowDefaults(t *testing.T) {
	cases := []struct {
		tier RetentionTier
		want time.Duration
	}{
		{TierLoose, RetentionLoose},
		{TierPersonal, RetentionPersonal},
		{TierShared, RetentionShared},
		{TierTemporary, RetentionTemporary},
		{RetentionTier("bogus"), RetentionLoose}, // unknown → shortest
	}
	for _, c := range cases {
		if got := RetentionWindow(c.tier); got != c.want {
			t.Errorf("RetentionWindow(%q) = %v, want %v", c.tier, got, c.want)
		}
	}
	// Ordering sanity: loose < personal < shared, and temporary is far shorter.
	if !(RetentionLoose < RetentionPersonal && RetentionPersonal < RetentionShared) {
		t.Errorf("retention tiers not strictly increasing: %v %v %v",
			RetentionLoose, RetentionPersonal, RetentionShared)
	}
	if RetentionTemporary >= RetentionLoose {
		t.Errorf("temporary window (%v) should be far shorter than loose (%v)",
			RetentionTemporary, RetentionLoose)
	}
}

func TestSessionBackupState(t *testing.T) {
	setupTestDB(t)
	if err := CreateSession("s-backup", config.SessionConfig{Label: "b"}); err != nil {
		t.Fatal(err)
	}

	// Fresh session: zero-value backup state.
	st := GetSessionBackupState("s-backup")
	if st.Remote != "" || st.Status != "" || st.LastSyncedAt != "" {
		t.Fatalf("expected empty backup state, got %+v", st)
	}

	// Set a remote + syncing status: last_synced_at stays empty (not synced).
	if err := SetSessionBackupState("s-backup", "git@example.com:me/durable.git", BackupStatusSyncing); err != nil {
		t.Fatal(err)
	}
	st = GetSessionBackupState("s-backup")
	if st.Remote != "git@example.com:me/durable.git" {
		t.Errorf("remote not persisted, got %q", st.Remote)
	}
	if st.Status != BackupStatusSyncing {
		t.Errorf("status = %q, want %q", st.Status, BackupStatusSyncing)
	}
	if st.LastSyncedAt != "" {
		t.Errorf("last_synced_at should be empty before a successful sync, got %q", st.LastSyncedAt)
	}

	// A successful sync bumps last_synced_at and preserves the remote (empty
	// remote arg means "leave unchanged").
	if err := SetSessionBackupState("s-backup", "", BackupStatusSynced); err != nil {
		t.Fatal(err)
	}
	st = GetSessionBackupState("s-backup")
	if st.Status != BackupStatusSynced {
		t.Errorf("status = %q, want %q", st.Status, BackupStatusSynced)
	}
	if st.LastSyncedAt == "" {
		t.Error("last_synced_at should be set after a successful sync")
	}
	if st.Remote != "git@example.com:me/durable.git" {
		t.Errorf("remote should be preserved when passing empty remote, got %q", st.Remote)
	}
}

func TestProjectKeepAndBackupState(t *testing.T) {
	setupTestDB(t)
	if err := CreateProject("p-keep", "Keeper", "", "/tmp/keeper", ""); err != nil {
		t.Fatal(err)
	}

	// Default: not kept.
	if GetProjectKeep("p-keep") {
		t.Error("new project should not be Keep by default")
	}
	if err := SetProjectKeep("p-keep", true); err != nil {
		t.Fatal(err)
	}
	if !GetProjectKeep("p-keep") {
		t.Error("project should be Keep after SetProjectKeep(true)")
	}
	if err := SetProjectKeep("p-keep", false); err != nil {
		t.Fatal(err)
	}
	if GetProjectKeep("p-keep") {
		t.Error("project should not be Keep after SetProjectKeep(false)")
	}

	// Project backup status + last_synced_at on success.
	if err := SetProjectBackupState("p-keep", BackupStatusSynced); err != nil {
		t.Fatal(err)
	}
	var status string
	var lastSynced *string
	if err := DB.QueryRow(
		`SELECT backup_status, last_synced_at FROM projects WHERE id = ?`, "p-keep",
	).Scan(&status, &lastSynced); err != nil {
		t.Fatal(err)
	}
	if status != BackupStatusSynced {
		t.Errorf("project backup_status = %q, want %q", status, BackupStatusSynced)
	}
	if lastSynced == nil || *lastSynced == "" {
		t.Error("project last_synced_at should be set after a successful sync")
	}
}

// TestBackupColumnsExist verifies the idempotent migrations added the durable
// storage backup columns to both sessions and projects.
func TestBackupColumnsExist(t *testing.T) {
	setupTestDB(t)
	wantSession := []string{"backup_remote", "last_synced_at", "backup_status"}
	for _, col := range wantSession {
		if !columnExists(t, "sessions", col) {
			t.Errorf("sessions.%s column missing after migration", col)
		}
	}
	wantProject := []string{"keep", "last_synced_at", "backup_status"}
	for _, col := range wantProject {
		if !columnExists(t, "projects", col) {
			t.Errorf("projects.%s column missing after migration", col)
		}
	}
}

func columnExists(t *testing.T, table, col string) bool {
	t.Helper()
	// #nosec G202 — table/col are test constants.
	rows, err := DB.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == col {
			return true
		}
	}
	return false
}
