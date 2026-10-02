// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
)

// held keeps the server's lock open until the process exits: closing any
// descriptor of the database file would drop SQLite's own locks on it too.
var held *os.File

// HoldDatabase takes a shared lock on the database file for as long as the
// server runs, so the migration, which needs it to itself, can't start under
// it. SQLite's own locks are of another kind, so this doesn't touch them.
func HoldDatabase(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		f.Close()
		return fmt.Errorf("the one-folder migration is using %s; start again once it has finished", path)
	}
	held = f
	return nil
}

// lockDatabase takes the database file to itself for the migration.
func lockDatabase(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another Knowledge Worker Agent process is using %s", path)
	}
	return f, nil
}

// portFree checks that no server, of this release or an older one, listens on
// the configured port.
func portFree(host string, port int) error {
	l, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("port %d is in use: is Knowledge Worker Agent running?", port)
	}
	return l.Close()
}
