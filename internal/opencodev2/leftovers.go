// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodev2

import (
	"log"
	"os"
	"path/filepath"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// leftovers are files earlier releases kept in case a workspace went back to
// opencode 1.x: a copy of opencode's database from before 2.x first opened it,
// which can take gigabytes, the copy's temporary file, and the record that
// 1.x's connector sign-ins were copied.
func leftovers() []string {
	sys := layout.System()
	return []string{
		filepath.Join(sys, "opencode-db-before-2.x.db"),
		filepath.Join(sys, "opencode-db-before-2.x.db.tmp"),
		filepath.Join(sys, "opencode-v2-connectors-imported"),
	}
}

// RemoveLeftovers deletes them, since nothing can go back to 1.x any more. A
// failure is logged and doesn't stop the server.
func RemoveLeftovers() {
	for _, p := range leftovers() {
		info, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if err := os.Remove(p); err != nil {
			log.Printf("[opencode-v2] could not remove %s, kept for going back to opencode 1.x: %v", p, err)
			continue
		}
		log.Printf("[opencode-v2] removed %s (%d MB), kept for going back to opencode 1.x", p, info.Size()>>20)
	}
}
