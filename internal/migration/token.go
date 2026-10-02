// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"os"
	"path/filepath"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// MoveGitHubToken moves the file holding the GitHub token saved in the app
// into the root, where an earlier release's migration left it at its old
// place. It returns that place when it moved the file. An undo moves it back
// as it does any location created in the root since the migration.
func MoveGitHubToken() (string, error) {
	if !layout.Migrated() {
		return "", nil
	}
	for _, l := range layout.Locations() {
		if l.Name != layout.NameGitHubToken || l.Custom || exists(l.Moved) || !exists(l.Current) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(l.Moved), 0o700); err != nil {
			return "", err
		}
		if err := os.Rename(l.Current, l.Moved); err != nil {
			return "", err
		}
		return l.Current, nil
	}
	return "", nil
}
