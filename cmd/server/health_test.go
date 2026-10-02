// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunHealthCheck_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	code := runHealthCheck([]string{"--url=" + srv.URL + "/healthz", "--timeout=2s"})
	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}
}

func TestRunHealthCheck_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	code := runHealthCheck([]string{"--url=" + srv.URL + "/healthz", "--timeout=2s"})
	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
}

func TestRunHealthCheck_ConnectionRefused(t *testing.T) {
	// Port 1 is reserved/unused; connect should fail fast.
	code := runHealthCheck([]string{"--url=http://127.0.0.1:1/healthz", "--timeout=500ms"})
	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
}

func TestRunHealthCheck_HelpReturnsZero(t *testing.T) {
	code := runHealthCheck([]string{"--help"})
	if code != 0 {
		t.Errorf("expected --help to exit 0, got %d", code)
	}
}

func TestRunHealthCheck_UsesHostPortFlags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	// Parse host:port out of the test server URL (form http://127.0.0.1:NNNN).
	addr := strings.TrimPrefix(srv.URL, "http://")
	host, port, ok := strings.Cut(addr, ":")
	if !ok {
		t.Fatalf("could not parse %q", srv.URL)
	}

	code := runHealthCheck([]string{"--host=" + host, "--port=" + port, "--timeout=2s"})
	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}
}
