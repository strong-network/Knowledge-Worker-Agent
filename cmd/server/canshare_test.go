// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

func TestChildProcessesAreToldWhereTheServerIs(t *testing.T) {
	t.Setenv("KWA_URL", "http://stale.example")
	prev := config.Port
	t.Cleanup(func() { config.Port = prev })
	config.Port = 9123
	exportURL()
	if got := os.Getenv("KWA_URL"); got != "http://127.0.0.1:9123" {
		t.Errorf("KWA_URL = %q", got)
	}
}

func TestTheCanShareRouteIsServed(t *testing.T) {
	setupServerTestDB(t)
	mux := http.NewServeMux()
	registerRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/artifacts/can-share?folder=/no/chat/here", nil))
	var resp struct {
		CanShare *bool  `json:"can_share"`
		Reason   string `json:"reason"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &resp) != nil || resp.CanShare == nil || *resp.CanShare || resp.Reason == "" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
