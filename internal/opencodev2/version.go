// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package opencodev2 drives opencode 2.x.
//
// KWA keeps one `opencode serve` running for the workspace and sends every
// opencode command to it. A private server per command connects the
// connectors while the turn runs; opencode then adds a reminder about their
// tools to the turn, and the model answers it in its reply. A server that has
// already connected them gives no such reminder.
package opencodev2

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"time"
)

var versionRe = regexp.MustCompile(`(\d+)\.\d+\.\d+`)

type binInfo struct {
	path  string
	size  int64
	mtime time.Time
}

var (
	majorMu   sync.Mutex
	majorOf   binInfo
	majorSeen int
)

func statBin(bin string) binInfo {
	info := binInfo{path: bin}
	if st, err := os.Stat(bin); err == nil {
		info.size, info.mtime = st.Size(), st.ModTime()
	} else if p, err := exec.LookPath(bin); err == nil {
		if st, err := os.Stat(p); err == nil {
			info.size, info.mtime = st.Size(), st.ModTime()
		}
	}
	return info
}

// Detect returns the major version bin reports, remembered until the binary
// changes, so that checking it again doesn't run it.
func Detect(bin string) int {
	info := statBin(bin)
	majorMu.Lock()
	defer majorMu.Unlock()
	if majorSeen != 0 && majorOf == info {
		return majorSeen
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	majorOf, majorSeen = info, parseMajor(string(out))
	return majorSeen
}

func parseMajor(version string) int {
	m := versionRe.FindStringSubmatch(version)
	if m == nil {
		return 1
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 {
		return 1
	}
	return n
}
