// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package durable

import (
	"os"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
)

// identityFromEnv builds a best-effort Identity from the platform
// environment. The platform injects workspace/owner identity; where a
// value is not present we leave it empty (the metadata descriptor is
// non-normative and tolerates missing fields). Callers that already know the
// owner (e.g. from a session record) should prefer that over this fallback.
//
// Currently mapped:
//   - OwnerUserID    ← STRONG_NETWORK_WORKSPACE_ID (the per-workspace owner id)
//   - OrganizationID ← KWA_ORGANIZATION_ID (reserved; not yet injected)
//   - Region         ← KWA_REGION (reserved; not yet injected)
func identityFromEnv() Identity {
	return Identity{
		OwnerUserID:    firstNonEmpty(os.Getenv("STRONG_NETWORK_WORKSPACE_ID")),
		OrganizationID: firstNonEmpty(env.Get("KWA_ORGANIZATION_ID")),
		Region:         firstNonEmpty(env.Get("KWA_REGION")),
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// InitChatShare scaffolds the folder convention and writes .system/metadata for
// a loose-chat share, sourcing identity from the environment. Best-effort:
// returns any filesystem error so the caller can log it, but callers should not
// treat a metadata failure as fatal to chat creation.
func InitChatShare(shareRoot, chatID, projectID string) error {
	if err := ScaffoldShare(shareRoot); err != nil {
		return err
	}
	return WriteChatMetadata(shareRoot, chatID, projectID, identityFromEnv())
}

// InitProjectShare scaffolds the folder convention and writes .system/metadata
// for a project shared workspace.
func InitProjectShare(shareRoot, projectID string) error {
	if err := ScaffoldShare(shareRoot); err != nil {
		return err
	}
	return WriteProjectMetadata(shareRoot, projectID, identityFromEnv())
}
