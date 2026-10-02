// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package mcpauth

import (
	"fmt"
	"os"
)

// openPTY is unsupported off Linux. startAuth falls back to pipe-based stdio,
// which is enough to read the URL opencode prints before it blocks.
func openPTY() (*os.File, *os.File, error) {
	return nil, nil, fmt.Errorf("PTY not supported on this platform")
}
