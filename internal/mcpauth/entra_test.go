// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcpauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/entra"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// No test reaches a connector's metadata over the network unless it says so.
func TestMain(m *testing.M) {
	detectEntra = func(context.Context, string) (entra.Target, bool, error) { return entra.Target{}, false, nil }
	os.Exit(m.Run())
}

// entraConnector makes "workiq" a connector that signs in with Microsoft.
func entraConnector(t *testing.T) {
	t.Helper()
	isolateMCPConfig(t, true, "workiq")
	t.Setenv("KWA_HOME", t.TempDir())
	t.Setenv("KWA_ENTRA_CLIENT_ID", "client-1")
	t.Setenv("KWA_ENTRA_TENANT_ID", "tenant-1")
	t.Setenv("KWA_ENTRA_AUTHORITY", "http://127.0.0.1:9")
	oldDetect, oldEnabled := detectEntra, entraEnabled
	detectEntra = func(_ context.Context, u string) (entra.Target, bool, error) {
		return entra.Target{ServerURL: u, Scopes: []string{"api-1/Ask", "offline_access"}}, true, nil
	}
	entraEnabled = func() bool { return true }
	t.Cleanup(func() { detectEntra, entraEnabled = oldDetect, oldEnabled })
	// opencode's own sign-in must not run: it would fail on Entra.
	marker := filepath.Join(t.TempDir(), "opencode-ran")
	old := OpencodeBin
	OpencodeBin = writeFakeOpencode(t, "#!/bin/sh\ntouch "+marker+"\nexit 1\n")
	t.Cleanup(func() {
		OpencodeBin = old
		if _, err := os.Stat(marker); err == nil {
			t.Error("opencode's sign-in ran for a Microsoft connector")
		}
	})
	auth.mu.Lock()
	auth.resetLocked("")
	auth.mu.Unlock()
}

func TestEntraConnectorSignsInWithKWA(t *testing.T) {
	entraConnector(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/servers/workiq/auth/start", nil)
	req.SetPathValue("name", "workiq")
	HandleAuthStart(rec, req)
	var info AuthInfo
	json.Unmarshal(rec.Body.Bytes(), &info)
	if rec.Code != http.StatusOK || !info.Running || info.Method != "entra" ||
		!strings.HasPrefix(info.VerifyURL, "http://127.0.0.1:9/tenant-1/oauth2/v2.0/authorize?") {
		t.Fatalf("start: %d %+v", rec.Code, info)
	}

	// An address that isn't the sign-in's leaves it waiting.
	rec = httptest.NewRecorder()
	HandleAuthCallback(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/servers/workiq/auth/callback",
		strings.NewReader(`{"url":"http://127.0.0.1:19876/oauth/callback?code=c"}`)))
	var res struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusBadRequest || res.OK || !strings.Contains(res.Error, "http://localhost/?code=") {
		t.Errorf("a wrong address: %d %+v", rec.Code, res)
	}
	if info := snapshot(); !info.Running || info.Done {
		t.Errorf("after a wrong address: %+v", info)
	}

	rec = httptest.NewRecorder()
	HandleAuthCancel(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/servers/workiq/auth/cancel", nil))
	if info := snapshot(); !info.Done || info.Success || info.Error != "cancelled by user" {
		t.Errorf("after cancel: %+v", info)
	}
}

func TestEntraConnectorNeedsAnAppAnd2x(t *testing.T) {
	entraConnector(t)
	t.Setenv("KWA_ENTRA_CLIENT_ID", "")
	var unsupported ErrOAuthUnsupported
	if err := startAuth("workiq"); !errors.As(err, &unsupported) || !strings.Contains(err.Error(), "KWA_ENTRA_CLIENT_ID") {
		t.Errorf("without an app: %v", err)
	}
	t.Setenv("KWA_ENTRA_CLIENT_ID", "client-1")
	entraEnabled = func() bool { return false }
	if err := startAuth("workiq"); !errors.As(err, &unsupported) || !strings.Contains(err.Error(), "opencode 2.x") {
		t.Errorf("on 1.x: %v", err)
	}
}

func TestLogoutForgetsTheMicrosoftSignIn(t *testing.T) {
	entraConnector(t)
	OpencodeBin = writeFakeOpencode(t, "#!/bin/sh\n[ \"$2\" = logout ] && exit 0\nexit 1\n")
	dir := filepath.Join(layout.System(), "entra")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workiq.json"), []byte(`{"server":"workiq"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !entra.Has("workiq") {
		t.Fatal("no sign-in to forget")
	}
	if _, err := Logout("workiq"); err != nil {
		t.Fatal(err)
	}
	if entra.Has("workiq") {
		t.Error("the Microsoft sign-in outlived the sign-out")
	}
}

// An artifact's app wins over the settings, part by part.
func TestEntraAppFromTheArtifact(t *testing.T) {
	entraConnector(t)
	platform := t.TempDir()
	if err := os.WriteFile(filepath.Join(platform, "opencode.json"),
		[]byte(`{"mcp":{"workiq":{"type":"remote","url":"https://workiq.example.com/mcp"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(platform, "mcp-meta.json"),
		[]byte(`{"servers":[{"name":"workiq","auth":{"type":"entra","clientId":"client-9"}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_CONFIG_DIR", platform)
	if app, ok := entraApp("workiq"); !ok || app.ClientID != "client-9" || app.Tenant != "tenant-1" {
		t.Errorf("app = %+v, %v", app, ok)
	}
	t.Setenv("KWA_ENTRA_TENANT_ID", "")
	if _, ok := entraApp("workiq"); ok {
		t.Error("an app without a tenant")
	}
}

// The sign-in asks for the permission the connector entry names, when it names one.
func TestEntraConnectorAsksForTheEntrysScope(t *testing.T) {
	for _, tc := range []struct{ set, want string }{
		{"", "api-1/Ask offline_access"},
		{"res-9/.default", "res-9/.default offline_access"},
	} {
		entraConnector(t)
		old := oauthScope
		oauthScope = func(string) string { return tc.set }
		if err := startAuth("workiq"); err != nil {
			t.Fatal(err)
		}
		oauthScope = old
		u, err := url.Parse(snapshot().VerifyURL)
		if err != nil {
			t.Fatal(err)
		}
		if got := u.Query().Get("scope"); got != tc.want {
			t.Errorf("with oauth.scope %q: scope = %q, want %q", tc.set, got, tc.want)
		}
		auth.mu.Lock()
		auth.resetLocked("")
		auth.mu.Unlock()
	}
}
