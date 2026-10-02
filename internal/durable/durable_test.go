// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package durable

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScaffoldShareCreatesReservedDirs(t *testing.T) {
	root := t.TempDir()
	if err := ScaffoldShare(root); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{DirInputs, DirWorking, DirSystem, filepath.Join(DirSystem, memoryDir)} {
		p := filepath.Join(root, sub)
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("expected %s to exist: %v", sub, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s should be a directory", sub)
		}
	}
	// Idempotent: a second call must not error.
	if err := ScaffoldShare(root); err != nil {
		t.Errorf("ScaffoldShare not idempotent: %v", err)
	}
}

func TestWriteChatMetadata(t *testing.T) {
	root := t.TempDir()
	id := Identity{OwnerUserID: "user_1", OrganizationID: "org_9", Region: "eu"}
	if err := WriteChatMetadata(root, "chat_abc", "", id); err != nil {
		t.Fatal(err)
	}

	m, err := ReadMetadata(root)
	if err != nil {
		t.Fatal(err)
	}
	if m.SchemaVersion != currentSchemaVersion {
		t.Errorf("schema_version = %d, want %d", m.SchemaVersion, currentSchemaVersion)
	}
	if m.ShareType != ShareChat {
		t.Errorf("share_type = %q, want %q", m.ShareType, ShareChat)
	}
	if m.ChatID != "chat_abc" {
		t.Errorf("chat_id = %q, want chat_abc", m.ChatID)
	}
	if m.OwnerUserID != "user_1" {
		t.Errorf("owner_user_id = %q, want user_1", m.OwnerUserID)
	}
	// Owner is always a contributor.
	if len(m.ContributingUserIDs) != 1 || m.ContributingUserIDs[0] != "user_1" {
		t.Errorf("contributing_user_ids = %v, want [user_1]", m.ContributingUserIDs)
	}
	if m.OrganizationID != "org_9" || m.Region != "eu" {
		t.Errorf("org/region = %q/%q, want org_9/eu", m.OrganizationID, m.Region)
	}
	// Consent defaults to not vault-eligible (opt-in, never implicit).
	if m.Consent.VaultEligible {
		t.Error("consent.vault_eligible must default to false")
	}
	if m.CreatedAt == "" || m.UpdatedAt == "" {
		t.Error("created_at and updated_at must be populated")
	}
}

func TestWriteMetadataPreservesCreatedAt(t *testing.T) {
	root := t.TempDir()

	// Pin time to two distinct values across the two writes.
	orig := nowRFC3339
	t.Cleanup(func() { nowRFC3339 = orig })

	nowRFC3339 = func() string { return "2026-01-01T00:00:00Z" }
	if err := WriteChatMetadata(root, "c1", "", Identity{OwnerUserID: "u1"}); err != nil {
		t.Fatal(err)
	}

	nowRFC3339 = func() string { return "2026-02-02T00:00:00Z" }
	if err := WriteChatMetadata(root, "c1", "", Identity{OwnerUserID: "u1"}); err != nil {
		t.Fatal(err)
	}

	m, err := ReadMetadata(root)
	if err != nil {
		t.Fatal(err)
	}
	if m.CreatedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("created_at should be preserved from first write, got %q", m.CreatedAt)
	}
	if m.UpdatedAt != "2026-02-02T00:00:00Z" {
		t.Errorf("updated_at should reflect the latest write, got %q", m.UpdatedAt)
	}
}

func TestWriteProjectMetadata(t *testing.T) {
	root := t.TempDir()
	if err := WriteProjectMetadata(root, "proj_1", Identity{OwnerUserID: "u2"}); err != nil {
		t.Fatal(err)
	}
	m, err := ReadMetadata(root)
	if err != nil {
		t.Fatal(err)
	}
	if m.ShareType != ShareProject {
		t.Errorf("share_type = %q, want %q", m.ShareType, ShareProject)
	}
	if m.ProjectID != "proj_1" {
		t.Errorf("project_id = %q, want proj_1", m.ProjectID)
	}
	if m.ChatID != "" {
		t.Errorf("project share should have empty chat_id, got %q", m.ChatID)
	}
}

func TestReservedNames(t *testing.T) {
	for _, n := range []string{DirInputs, DirWorking, DirSystem} {
		if !ReservedNames[n] {
			t.Errorf("%q should be reserved", n)
		}
	}
	if ReservedNames["deliverable.md"] {
		t.Error("ordinary names must not be reserved")
	}
}
