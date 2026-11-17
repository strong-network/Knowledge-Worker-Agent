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
