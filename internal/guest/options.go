// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package guest

import "github.com/strong-network/Knowledge-Worker-Agent/internal/db"

// optionsFrame is what a guest page is told the owner allows.
func optionsFrame(sid string) map[string]any {
	o := db.SessionShareOptions(sid)
	return map[string]any{"type": "share_options", "session_id": sid, "allow_permissions": o.AllowPermissions, "allow_files": o.AllowFiles}
}
