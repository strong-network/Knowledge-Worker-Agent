// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/diagnostics"
)

func fakeDisk(t *testing.T, free, total uint64) {
	t.Helper()
	prev := measureDisk
	t.Cleanup(func() { measureDisk = prev })
	measureDisk = func() diagnostics.Space { return diagnostics.Space{Free: free, Total: total} }
}

func statusDisk(t *testing.T, header string) (int, *lowDisk) {
	t.Helper()
	setupServerTestDB(t)
	mux := http.NewServeMux()
	registerRoutes(mux)
	req := httptest.NewRequest("GET", "/api/status", nil)
	if header != "" {
		req.Header.Set("x-strong-ws-name", header)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var resp struct {
		Disk *lowDisk `json:"disk"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	return w.Code, resp.Disk
}

// Below 5% free, the status says how much is left, which workspace it is and
// where its disk can be increased.
func TestStatusWarnsWhenTheDiskIsAlmostFull(t *testing.T) {
	t.Setenv("STRONG_NETWORK_DOMAIN", "console.example.com")
	fakeDisk(t, 2_490_000_000, 50_000_000_000)
	code, d := statusDisk(t, "  Q3 analysis ")
	if code != http.StatusOK || d == nil {
		t.Fatalf("status = %d, disk = %+v", code, d)
	}
	want := lowDisk{Free: "2.5 GB", Total: "50.0 GB", PercentFree: 4.9, Workspace: "Q3 analysis", ConsoleURL: "https://console.example.com/"}
	if *d != want {
		t.Errorf("disk = %+v, want %+v", *d, want)
	}
}

func TestStatusSaysNothingWhileThereIsSpace(t *testing.T) {
	fakeDisk(t, 2_500_000_000, 50_000_000_000)
	if _, d := statusDisk(t, ""); d != nil {
		t.Errorf("5%% free was reported as low: %+v", d)
	}
	fakeDisk(t, 0, 0)
	if _, d := statusDisk(t, ""); d != nil {
		t.Errorf("an unknown disk was reported as low: %+v", d)
	}
}

// The name and the domain come from outside; anything odd is left out.
func TestLowDiskLeavesOutOddNamesAndDomains(t *testing.T) {
	fakeDisk(t, 1, 100)
	t.Setenv("STRONG_NETWORK_DOMAIN", "evil.example/phish?x=")
	for _, name := range []string{"bad\x00name", "line\nbreak", strings.Repeat("n", 101)} {
		_, d := statusDisk(t, name)
		if d == nil || d.Workspace != "" || d.ConsoleURL != "" {
			t.Errorf("name %q: %+v", name, d)
		}
	}
}
