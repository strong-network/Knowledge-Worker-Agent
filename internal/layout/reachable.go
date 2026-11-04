// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package layout

import (
	"path/filepath"
	"strings"
)

// UnreachableMessage answers a request for a path Reachable refuses.
const UnreachableMessage = "Knowledge Worker Agent only opens what's in your home folder, apart from the hidden folders and files directly in it."

// Reachable reports whether Knowledge Worker Agent may open path for the user,
// and returns it absolute and clean. That is the home folder, apart from the
// hidden items directly in it (~/.ssh, ~/.config, ~/.local: keys and tokens),
// and Knowledge Worker Agent's own folders where they're elsewhere, apart from
// the root's .system folder (its database and connector keys). Symbolic links
// aren't followed: the check is on the path as given.
func Reachable(path string) (string, bool) {
	if strings.TrimSpace(path) == "" {
		return "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	if within(abs, System()) {
		return abs, false
	}
	home := homeOr("")
	for _, dir := range []string{Root(), Workspace(), Projects(), ScheduledTasks()} {
		// One that holds the home folder, as the old layout's base folder did,
		// would let in its hidden items too: the home folder's rule decides.
		if dir != "" && !within(home, dir) && within(abs, dir) {
			return abs, true
		}
	}
	if home == "" || !within(abs, home) {
		return abs, false
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(abs, home), "/")
	first, _, _ := strings.Cut(rel, "/")
	return abs, !strings.HasPrefix(first, ".")
}

// within reports whether path is dir or inside it, both absolute and clean.
func within(path, dir string) bool {
	if dir == "" {
		return false
	}
	dir = filepath.Clean(dir)
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/")
}
