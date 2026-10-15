// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package diagnostics

import "syscall"

func diskSpace(path string) (free, total uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	bsize := uint64(st.Bsize)
	return uint64(st.Bavail) * bsize, uint64(st.Blocks) * bsize
}
