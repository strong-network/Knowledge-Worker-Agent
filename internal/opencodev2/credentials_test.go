// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodev2

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startFake(t *testing.T) (context.Context, string) {
	t.Helper()
	useFake(t, "ok")
	store := filepath.Join(t.TempDir(), "creds.json")
	t.Setenv("KWA_TEST_FAKE_CREDS", store)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	if _, _, err := Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	return ctx, store
}

func storedCredentials(t *testing.T, path string) []map[string]any {
	t.Helper()
	var out []map[string]any
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func TestSetCredentialReplacesTheOldOne(t *testing.T) {
	ctx, store := startFake(t)
	if has, err := HasCredential(ctx, "github-copilot"); err != nil || has {
		t.Fatalf("before: %v, %v", has, err)
	}
	for _, token := range []string{"first", "second"} {
		if err := SetCredential(ctx, "github-copilot", "OAuth", map[string]any{"type": "oauth", "access": token}); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetCredential(ctx, "acme", "", map[string]any{"type": "key", "key": "k"}); err != nil {
		t.Fatal(err)
	}
	creds := storedCredentials(t, store)
	if len(creds) != 2 {
		t.Fatalf("stored %d credentials, want the second sign-in and the key: %v", len(creds), creds)
	}
	if v := creds[0]["value"].(map[string]any); v["access"] != "second" || creds[0]["label"] != "OAuth" {
		t.Errorf("kept %v", creds[0])
	}
	if has, _ := HasCredential(ctx, "GitHub-Copilot"); !has {
		t.Error("HasCredential is false after a sign-in")
	}
	if err := RemoveCredentials(ctx, "github-copilot"); err != nil {
		t.Fatal(err)
	}
	if creds := storedCredentials(t, store); len(creds) != 1 || creds[0]["integrationID"] != "acme" {
		t.Errorf("after sign-out: %v", creds)
	}
}

func TestConnectorStatusesAndSignOut(t *testing.T) {
	ctx, store := startFake(t)
	got, err := ConnectorStatuses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Ensure waited until "a" connected; a switched-off one gives no verdict.
	if len(got) != 2 || !got["a"] || got["b"] {
		t.Errorf("statuses = %v", got)
	}
	if err := SetCredential(ctx, "mcp_b", "", map[string]any{"type": "oauth"}); err != nil {
		t.Fatal(err)
	}
	if in, _ := ConnectorSignedIn(ctx, "B"); !in {
		t.Error("ConnectorSignedIn is false with a credential")
	}
	if err := SignOutConnector(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if creds := storedCredentials(t, store); len(creds) != 0 {
		t.Errorf("after sign-out: %v", creds)
	}
	if err := SignOutConnector(ctx, "nope"); err == nil {
		t.Error("signed out of a connector that doesn't exist")
	}
}

func TestImportConnectorSignInsOnceAndOnlyToTheSameAddress(t *testing.T) {
	ctx, store := startFake(t)
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	if err := os.MkdirAll(filepath.Join(cfgHome, "opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	global := `{"mcp":{"a":{"type":"remote","url":"https://a.example.com/mcp"},"b":{"type":"remote","url":"https://b.example.com/mcp"}}}`
	if err := os.WriteFile(filepath.Join(cfgHome, "opencode", "opencode.json"), []byte(global), 0o644); err != nil {
		t.Fatal(err)
	}
	v1 := `{
	  "a": {"tokens": {"accessToken": "A", "refreshToken": "RA", "expiresAt": 1900000000.5, "scope": "read"},
	        "clientInfo": {"clientId": "cid", "clientSecret": "cs", "clientIdIssuedAt": 1700000000, "clientSecretExpiresAt": 0},
	        "serverUrl": "https://a.example.com/mcp/"},
	  "b": {"tokens": {"accessToken": "B"}, "serverUrl": "https://elsewhere.example.com/mcp"},
	  "off": {"clientInfo": {"clientId": "x"}, "serverUrl": "https://off.example.com/mcp"},
	  "gone": {"tokens": {"accessToken": "G"}, "serverUrl": "https://gone.example.com/mcp"}
	}`
	path := filepath.Join(t.TempDir(), "mcp-auth.json")
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := ImportConnectorSignIns(ctx, path)
	if err != nil || n != 1 {
		t.Fatalf("imported %d, %v; want 1 (b's address changed, off never signed in, gone isn't configured)", n, err)
	}
	creds := storedCredentials(t, store)
	if len(creds) != 1 || creds[0]["integrationID"] != "mcp_a" {
		t.Fatalf("stored %v", creds)
	}
	v := creds[0]["value"].(map[string]any)
	meta := v["metadata"].(map[string]any)
	client := meta["client"].(map[string]any)
	if v["type"] != "oauth" || v["methodID"] != "mcp_a" || v["access"] != "A" || v["refresh"] != "RA" ||
		v["expires"] != float64(1900000000500) || meta["scope"] != "read" || meta["serverUrl"] != "https://a.example.com/mcp/" ||
		client["client_id"] != "cid" || client["client_secret"] != "cs" || client["client_secret_expires_at"] != float64(0) {
		t.Errorf("value = %v", v)
	}

	// A sign-out on 2.x sticks: the import doesn't run again.
	if err := SignOutConnector(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if n, err := ImportConnectorSignIns(ctx, path); err != nil || n != 0 {
		t.Fatalf("second import: %d, %v", n, err)
	}
	if creds := storedCredentials(t, store); len(creds) != 0 {
		t.Errorf("imported again: %v", creds)
	}
}

// 2.x fails a turn whose model lacks the reasoning level asked for, so KWA
// asks first. Only a known model without the level says no.
func TestHasVariant(t *testing.T) {
	ctx, _ := startFake(t)
	for _, tc := range []struct {
		model, variant string
		want           bool
	}{
		{"github-copilot/claude-haiku-4.5", "high", true},
		{"github-copilot/claude-haiku-4.5", "medium", false},
		{"github-copilot/unknown", "medium", true},
		{"no-provider", "medium", true},
	} {
		if got := HasVariant(ctx, tc.model, tc.variant); got != tc.want {
			t.Errorf("HasVariant(%s, %s) = %v", tc.model, tc.variant, got)
		}
	}
}

// Right after a reload 2.x lists no connectors for a moment. An import then
// must wait for the list, not record that it ran with nothing to import.
func TestImportConnectorSignInsWaitsForTheList(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty-once")
	t.Setenv("KWA_TEST_FAKE_EMPTY_ONCE", empty)
	ctx, store := startFake(t)
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	if err := os.MkdirAll(filepath.Join(cfgHome, "opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgHome, "opencode", "opencode.json"), []byte(`{"mcp":{"b":{"type":"remote","url":"https://b.example.com/mcp"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "mcp-auth.json")
	if err := os.WriteFile(path, []byte(`{"b":{"tokens":{"accessToken":"B"},"serverUrl":"https://b.example.com/mcp"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if n, err := ImportConnectorSignIns(ctx, path); err != nil || n != 1 {
		t.Fatalf("imported %d, %v; want 1 once the list is there", n, err)
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Fatal("the fake never answered an empty list")
	}
	if creds := storedCredentials(t, store); len(creds) != 1 || creds[0]["integrationID"] != "mcp_b" {
		t.Errorf("stored %v", creds)
	}
}

// On a large configuration 2.x lists no connectors for longer than an empty
// list's grace after it starts. The import after a reload must still wait for
// the list rather than take the empty one as the answer.
func TestImportConnectorSignInsAfterASlowStart(t *testing.T) {
	t.Setenv("KWA_TEST_FAKE_SLOW_LIST", "2500ms")
	empty := filepath.Join(t.TempDir(), "empty-once")
	t.Setenv("KWA_TEST_FAKE_EMPTY_ONCE", empty)
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	if err := os.MkdirAll(filepath.Join(cfgHome, "opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	global := `{"mcp":{"b":{"type":"remote","url":"https://b.example.com/mcp"},"toggle":{"enabled":false}}}`
	if err := os.WriteFile(filepath.Join(cfgHome, "opencode", "opencode.json"), []byte(global), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, store := startFake(t)
	if n := connectorCount.Load(); n != 3 {
		t.Fatalf("the server listed %d connectors at start, want 3 once it had loaded them", n)
	}
	path := filepath.Join(t.TempDir(), "mcp-auth.json")
	if err := os.WriteFile(path, []byte(`{"b":{"tokens":{"accessToken":"B"},"serverUrl":"https://b.example.com/mcp"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if n, err := ImportConnectorSignIns(ctx, path); err != nil || n != 1 {
		t.Fatalf("imported %d, %v; want 1", n, err)
	}
	if creds := storedCredentials(t, store); len(creds) != 1 || creds[0]["integrationID"] != "mcp_b" {
		t.Errorf("stored %v", creds)
	}
}

func TestDeclaredConnectors(t *testing.T) {
	dir := t.TempDir()
	global, overlay := filepath.Join(dir, "global.json"), filepath.Join(dir, "overlay.json")
	for p, doc := range map[string]string{
		global:  `{"mcp":{"a":{"type":"remote","url":"https://a.example.com"},"toggle":{"enabled":false},"shared":{"enabled":true}}}`,
		overlay: `{"mcp":{"local":{"type":"local","command":["x"]},"shared":{"type":"remote","url":"https://s.example.com"}}}`,
	} {
		if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if n := declaredConnectors(global, overlay, filepath.Join(dir, "missing.json"), ""); n != 3 {
		t.Errorf("declaredConnectors = %d, want 3 (a, local, shared)", n)
	}
	if n := declaredConnectors(); n != 0 {
		t.Errorf("declaredConnectors() = %d, want 0", n)
	}
}

// A refused credential request's error leaves out the server's reply, which
// can quote the secret it was sent.
func TestCredentialErrorsLeaveOutTheReply(t *testing.T) {
	err := withoutReply(&apiError{method: "POST", path: "/api/credential", status: "400 Bad Request", body: `Expected string, got "sk-secret"`})
	if strings.Contains(err.Error(), "sk-secret") || !strings.Contains(err.Error(), "400") {
		t.Fatalf("error = %v", err)
	}
}

func TestImportConnectorSignInsWithoutA1xFile(t *testing.T) {
	ctx, _ := startFake(t)
	if n, err := ImportConnectorSignIns(ctx, filepath.Join(t.TempDir(), "missing.json")); err != nil || n != 0 {
		t.Fatalf("%d, %v", n, err)
	}
	if _, err := os.Stat(importMarker()); err != nil {
		t.Error("no marker after a run with nothing to import")
	}
}
