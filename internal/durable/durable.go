// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package durable implements durable file storage: the workspace folder
// convention, the platform-populated .system/metadata descriptor, and (later
// slices) the pluggable backend abstraction and automatic sync orchestration.
//
// This package owns the "write side" of the durable store — the clean,
// self-describing, admin-partitionable layout the spec requires so a durable
// copy is legible for users and for a future knowledge-vault distiller. It does
// NOT analyze content; it only captures it in a predictable structure.
package durable

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Reserved directory names at a share root. Deliverables may not shadow
// these. inputs/ holds user-provided source material; working/ is discardable
// scratch (the pilot's "WIP" dir); .system/ is the hidden platform/agent-managed
// area holding metadata and conversation memory.
const (
	DirInputs  = "inputs"
	DirWorking = "working"
	DirSystem  = ".system"

	// metadataFile is the single self-describing descriptor tying a share to
	// its chat/project, org/region and contributors. Platform-populated.
	metadataFile = "metadata"
	// memoryDir holds generated conversation summaries (reserved; mechanics
	// are experimental and not implemented in this slice).
	memoryDir = "memory"
)

// ReservedNames is the set of directory names reserved at a share root. Used by
// the file layer to block a deliverable from shadowing them.
var ReservedNames = map[string]bool{
	DirInputs:  true,
	DirWorking: true,
	DirSystem:  true,
}

// ShareType discriminates a loose-chat store from a project shared workspace.
type ShareType string

const (
	ShareChat    ShareType = "chat"
	ShareProject ShareType = "project"
)

// Consent carries the opt-in flags for the (reserved) knowledge-vault pipeline.
// vault_eligible defaults to false — opt-in, never implicit.
type Consent struct {
	VaultEligible bool `json:"vault_eligible"`
}

// Metadata is the .system/metadata descriptor (a starter schema, non-
// normative). All identity fields are platform IDs, not display names, so they
// survive renames. It is platform-populated — the agent never authors it — so
// ownership, consent and identity cannot be spoofed from inside a chat.
type Metadata struct {
	SchemaVersion        int       `json:"schema_version"`
	ShareType            ShareType `json:"share_type"`
	ChatID               string    `json:"chat_id,omitempty"`
	ProjectID            string    `json:"project_id,omitempty"`
	OwnerUserID          string    `json:"owner_user_id"`
	ContributingUserIDs  []string  `json:"contributing_user_ids"`
	OrganizationID       string    `json:"organization_id"`
	Region               string    `json:"region"`
	Consent              Consent   `json:"consent"`
	CreatedAt            string    `json:"created_at"`
	UpdatedAt            string    `json:"updated_at"`
}

// currentSchemaVersion lets the format evolve without breaking older shares.
const currentSchemaVersion = 1

// Identity carries the platform-known facts used to populate metadata. Callers
// source these from the platform environment / session records; empty fields are
// tolerated (the descriptor is non-normative and best-effort).
type Identity struct {
	OwnerUserID    string
	OrganizationID string
	Region         string
}

// systemDir returns the .system directory path for a share root.
func systemDir(shareRoot string) string {
	return filepath.Join(shareRoot, DirSystem)
}

// nowRFC3339 is a package var so tests can pin time.
var nowRFC3339 = func() string { return time.Now().UTC().Format(time.RFC3339) }

// ScaffoldShare creates the base folder convention under shareRoot: the
// supporting inputs/ and working/ directories and the hidden .system/ area
// (with .system/memory/). The share root itself is assumed to already exist
// (a new chat creates its own dir; a project provisions its shared workspace).
// It is idempotent — existing directories are left as-is. The share root
// remains the deliverables directory (nothing is created there).
func ScaffoldShare(shareRoot string) error {
	if shareRoot == "" {
		return os.ErrInvalid
	}
	dirs := []string{
		filepath.Join(shareRoot, DirInputs),
		filepath.Join(shareRoot, DirWorking),
		filepath.Join(systemDir(shareRoot), memoryDir),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// WriteChatMetadata writes (or overwrites) .system/metadata for a loose-chat
// share. It is platform-populated. created_at is preserved across rewrites when
// an existing descriptor is present; updated_at is always bumped.
func WriteChatMetadata(shareRoot, chatID, projectID string, id Identity) error {
	m := Metadata{
		SchemaVersion:       currentSchemaVersion,
		ShareType:           ShareChat,
		ChatID:              chatID,
		ProjectID:           projectID,
		OwnerUserID:         id.OwnerUserID,
		ContributingUserIDs: contributors(id.OwnerUserID),
		OrganizationID:      id.OrganizationID,
		Region:              id.Region,
		Consent:             Consent{VaultEligible: false},
	}
	return writeMetadata(shareRoot, m)
}

// WriteProjectMetadata writes (or overwrites) .system/metadata for a project
// shared workspace.
func WriteProjectMetadata(shareRoot, projectID string, id Identity) error {
	m := Metadata{
		SchemaVersion:       currentSchemaVersion,
		ShareType:           ShareProject,
		ProjectID:           projectID,
		OwnerUserID:         id.OwnerUserID,
		ContributingUserIDs: contributors(id.OwnerUserID),
		OrganizationID:      id.OrganizationID,
		Region:              id.Region,
		Consent:             Consent{VaultEligible: false},
	}
	return writeMetadata(shareRoot, m)
}

// contributors returns a non-nil slice containing the owner (owner is always a
// contributor). An empty owner yields an empty slice.
func contributors(owner string) []string {
	if owner == "" {
		return []string{}
	}
	return []string{owner}
}

// writeMetadata serializes m into .system/metadata, preserving created_at from
// any existing descriptor and stamping updated_at (and created_at if new).
func writeMetadata(shareRoot string, m Metadata) error {
	if err := ScaffoldShare(shareRoot); err != nil {
		return err
	}
	path := filepath.Join(systemDir(shareRoot), metadataFile)

	now := nowRFC3339()
	m.UpdatedAt = now
	m.CreatedAt = now
	if existing, err := ReadMetadata(shareRoot); err == nil && existing.CreatedAt != "" {
		m.CreatedAt = existing.CreatedAt
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

// ReadMetadata reads and parses .system/metadata for a share. Returns an error
// if the descriptor is missing or unreadable.
func ReadMetadata(shareRoot string) (Metadata, error) {
	var m Metadata
	data, err := os.ReadFile(filepath.Join(systemDir(shareRoot), metadataFile))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	return m, nil
}

// IsShareRoot reports whether dir looks like a durable share root — i.e. it
// contains the platform-managed .system/ directory that ScaffoldShare creates.
// This is how the file layer recognizes a share root without carrying its own
// notion of one.
func IsShareRoot(dir string) bool {
	info, err := os.Stat(systemDir(dir))
	return err == nil && info.IsDir()
}

// ReservedRootCollision reports whether creating (or moving/renaming to) the
// given absolute path would shadow one of the reserved directory names
// (inputs/, working/, .system/) AT A SHARE ROOT. Returns the offending name
// when it would. Paths whose parent is not a share root never collide — the
// reservation only applies at the root of a durable share, so users keep full
// freedom to create these names inside subfolders or in non-share directories.
func ReservedRootCollision(absPath string) (string, bool) {
	base := filepath.Base(absPath)
	if !ReservedNames[base] {
		return "", false
	}
	parent := filepath.Dir(absPath)
	if !IsShareRoot(parent) {
		return "", false
	}
	return base, true
}
