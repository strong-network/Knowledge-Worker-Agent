// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"math"
	"net/http"
	"os"
	"strings"
	"unicode"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/diagnostics"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/migration"
)

// lowDisk is what /api/status says while the workspace's disk is almost full,
// so the UI can warn and say where to get more space.
type lowDisk struct {
	Free        string  `json:"free"`
	Total       string  `json:"total"`
	PercentFree float64 `json:"percent_free"`
	// Workspace is the workspace's name in the SecurSpaces console, when the
	// platform's proxy passed it on.
	Workspace  string `json:"workspace,omitempty"`
	ConsoleURL string `json:"console_url,omitempty"`
}

var measureDisk = diagnostics.DiskSpace

func lowDiskView(r *http.Request) *lowDisk {
	s := measureDisk()
	if !s.Low() {
		return nil
	}
	return &lowDisk{
		Free:  migration.Bytes(s.Free),
		Total: migration.Bytes(s.Total),
		// Rounded down, so a warning never reads as 5%.
		PercentFree: math.Floor(1000*float64(s.Free)/float64(s.Total)) / 10,
		Workspace:   workspaceName(r),
		ConsoleURL:  consoleURL(),
	}
}

// workspaceName is the name the platform's proxy sends with each request.
func workspaceName(r *http.Request) string {
	name := strings.TrimSpace(r.Header.Get("x-strong-ws-name"))
	if len([]rune(name)) > 100 || strings.IndexFunc(name, func(c rune) bool { return !unicode.IsPrint(c) }) >= 0 {
		return ""
	}
	return name
}

// consoleURL is the SecurSpaces console, or "" when the platform domain isn't known.
func consoleURL() string {
	d := strings.TrimSpace(os.Getenv("STRONG_NETWORK_DOMAIN"))
	if d == "" || strings.ContainsAny(d, "/?#@\\ ") {
		return ""
	}
	return "https://" + d + "/"
}
