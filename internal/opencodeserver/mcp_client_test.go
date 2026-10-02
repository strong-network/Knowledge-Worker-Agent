// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMcpStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/mcp" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"github":{"status":"connected"},"obsidian":{"status":"disconnected"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	got, err := c.McpStatus(context.Background())
	if err != nil {
		t.Fatalf("McpStatus: %v", err)
	}
	if got["github"] != "connected" {
		t.Errorf("github = %q, want connected", got["github"])
	}
	if got["obsidian"] != "disconnected" {
		t.Errorf("obsidian = %q, want disconnected", got["obsidian"])
	}
}

func TestMcpConnectDisconnect(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	if err := c.McpConnect(context.Background(), "github"); err != nil {
		t.Fatalf("McpConnect: %v", err)
	}
	if gotPath != "POST /mcp/github/connect" {
		t.Errorf("connect path = %q", gotPath)
	}
	if err := c.McpDisconnect(context.Background(), "github"); err != nil {
		t.Fatalf("McpDisconnect: %v", err)
	}
	if gotPath != "POST /mcp/github/disconnect" {
		t.Errorf("disconnect path = %q", gotPath)
	}
}
