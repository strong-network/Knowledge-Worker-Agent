// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"unicode"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/diagnostics"
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
		Free:  bytesText(s.Free),
		Total: bytesText(s.Total),
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

// bytesText is a size for people, in decimal units: "12.3 GB".
func bytesText(b uint64) string {
	const unit = 1000
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	v, suffix := float64(b), "kMGTP"
	i := -1
	for v >= unit && i < len(suffix)-1 {
		v /= unit
		i++
	}
	return fmt.Sprintf("%.1f %cB", v, suffix[i])
}
