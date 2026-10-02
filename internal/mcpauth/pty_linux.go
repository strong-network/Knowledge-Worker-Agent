// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package mcpauth

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// openPTY allocates a new pseudo-terminal pair using only the Linux kernel ABI
// (no cgo, no third-party deps). It returns the master (controlling) and slave
// ends; callers attach the slave to a child process's stdin/stdout/stderr and
// read prompts through the master.
//
// `opencode mcp auth <name>` is interactive — it renders a spinner and prints
// the authorization URL to a TTY, then blocks waiting for the OAuth callback.
// Without a real TTY it won't emit the URL the way we scrape it.
func openPTY() (master, slave *os.File, err error) {
	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}
	// Unlock the slave (TIOCSPTLCK with 0).
	var zero uint32
	if _, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL, ptmx.Fd(),
		uintptr(0x40045431), // TIOCSPTLCK
		uintptr(unsafe.Pointer(&zero)),
	); errno != 0 {
		ptmx.Close()
		return nil, nil, fmt.Errorf("unlockpt: %w", errno)
	}
	// Get the slave terminal number (TIOCGPTN).
	var n uint32
	if _, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL, ptmx.Fd(),
		uintptr(0x80045430), // TIOCGPTN
		uintptr(unsafe.Pointer(&n)),
	); errno != 0 {
		ptmx.Close()
		return nil, nil, fmt.Errorf("ptsname: %w", errno)
	}
	slavePath := fmt.Sprintf("/dev/pts/%d", n)
	pts, err := os.OpenFile(slavePath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		ptmx.Close()
		return nil, nil, fmt.Errorf("open %s: %w", slavePath, err)
	}
	return ptmx, pts, nil
}
