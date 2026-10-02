// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcpauth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/mcp"
)

// A centrally provisioned server arrives switched off, so
// "enable it first" is the FIRST thing a user meets when they try to sign in to
// one — not an edge case. It has to read as the next step, and it has to work
// for a server declared only in the platform dir, which has no entry in the
// user's Global at all.
func TestSignInToDisabledProvisionedServerNamesTheFix(t *testing.T) {
	// A Global with no MCP servers whatsoever…
	global := filepath.Join(t.TempDir(), "opencode.json")
	if err := os.WriteFile(global, []byte(`{"mcp":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", global)

	// …and a server that exists only because an artifact was assigned.
	platform := t.TempDir()
	if err := os.WriteFile(filepath.Join(platform, "opencode.json"),
		[]byte(`{"mcp":{"atlassian":{"type":"remote","url":"https://mcp.atlassian.com/v1/mcp/authv2"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_CONFIG_DIR", platform)

	// Bootstrap seeds the disabled stub that makes provisioning != enabling.
	mcp.EnsureProvisionedStubs(nil)

	enabled, found, err := mcp.IsEnabled("atlassian")
	if err != nil {
		t.Fatalf("IsEnabled: %v", err)
	}
	if !found {
		t.Fatal("a provisioned server must be visible to the auth guard")
	}
	if enabled {
		t.Fatal("a provisioned server must arrive switched off")
	}

	auth.mu.Lock()
	auth.resetLocked("")
	auth.mu.Unlock()

	// Fail loudly if the guard lets the flow through.
	old := OpencodeBin
	OpencodeBin = writeFakeOpencode(t, "#!/bin/sh\nexit 0\n")
	defer func() { OpencodeBin = old }()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/servers/atlassian/auth/start", nil)
	req.SetPathValue("name", "atlassian")
	HandleAuthStart(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "atlassian") || !strings.Contains(body, "on first") {
		t.Errorf("message must name the server and the fix, got %s", body)
	}
	// It must not read as a plain failure — the user has done nothing wrong.
	if strings.Contains(strings.ToLower(body), "failed") {
		t.Errorf("message reads as a failure: %s", body)
	}

	auth.mu.Lock()
	started := auth.running || auth.cmd != nil
	auth.mu.Unlock()
	if started {
		t.Error("an auth flow was started for a disabled server")
	}
}

// Once the user turns it on, the same call goes through. `opencode mcp auth`
// works on a server declared only in the platform dir (verified against the
// binary), so the existing flow reaches provisioned servers unchanged.
func TestSignInIsAllowedOnceProvisionedServerIsEnabled(t *testing.T) {
	global := filepath.Join(t.TempDir(), "opencode.json")
	if err := os.WriteFile(global, []byte(`{"mcp":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", global)

	platform := t.TempDir()
	if err := os.WriteFile(filepath.Join(platform, "opencode.json"),
		[]byte(`{"mcp":{"atlassian":{"type":"remote","url":"https://mcp.atlassian.com/v1/mcp/authv2"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_CONFIG_DIR", platform)

	mcp.EnsureProvisionedStubs(nil)
	if ok, err := mcp.SetEnabled("atlassian", true); err != nil || !ok {
		t.Fatalf("SetEnabled: ok=%v err=%v", ok, err)
	}

	auth.mu.Lock()
	auth.resetLocked("")
	auth.mu.Unlock()

	old := OpencodeBin
	OpencodeBin = writeFakeOpencode(t, "#!/bin/sh\nexit 0\n")
	defer func() { OpencodeBin = old }()

	if err := startAuth("atlassian"); err != nil {
		if _, blocked := err.(ErrNotEnabled); blocked {
			t.Fatal("an enabled provisioned server must not be refused")
		}
	}
	HandleAuthCancel(httptest.NewRecorder(), httptest.NewRequest(
		http.MethodPost, "/api/mcp/servers/atlassian/auth/cancel", nil))
}
