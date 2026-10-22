// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package diagnostics

import (
	"path/filepath"
	"syscall"
)

// diskSpace measures the disk holding path, or its nearest existing parent:
// the Knowledge Worker Agent folder doesn't exist until a workspace has moved into it.
func diskSpace(path string) (free, total uint64) {
	var st syscall.Statfs_t
	for syscall.Statfs(path, &st) != nil {
		parent := filepath.Dir(path)
		if parent == path {
			return 0, 0
		}
		path = parent
	}
	bsize := uint64(st.Bsize)
	return uint64(st.Bavail) * bsize, uint64(st.Blocks) * bsize
}
