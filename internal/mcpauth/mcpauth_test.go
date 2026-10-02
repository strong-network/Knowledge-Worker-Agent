// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcpauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/mcp"
	"testing"
	"time"
)

func writeFakeOpencode(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "opencode")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	return bin
}

// isolateMCPConfig points the mcp package at a throwaway opencode.json holding
// the named servers in the given enabled state. Mandatory for any test that
// reaches startAuth or Logout: both consult (and Logout writes) the MCP config,
// which would otherwise be the developer's real ~/.config/opencode/opencode.json.
func isolateMCPConfig(t *testing.T, enabled bool, servers ...string) {
	t.Helper()
	block := map[string]any{}
	for _, n := range servers {
		block[n] = map[string]any{
			"type":    "remote",
			"url":     "https://example.test/mcp",
			"enabled": enabled,
		}
	}
	b, err := json.Marshal(map[string]any{"mcp": block})
	if err != nil {
		t.Fatalf("marshal mcp config: %v", err)
	}
	path := filepath.Join(t.TempDir(), "opencode.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write mcp config: %v", err)
	}
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", path)
	// mcp.IsEnabled merges the platform-owned config dir, which defaults
	// to a path under the real user's home. Point it at an empty dir so these
	// tests never pick up a materialized config from the machine they run on.
	t.Setenv("OPENCODE_CONFIG_DIR", t.TempDir())
}

func TestParseServerConnected(t *testing.T) {
	// Realistic `opencode mcp list` output: one needs-auth, one connected.
	out := "" +
		"┌  MCP Servers\n" +
		"●  ⚠ atlassian needs authentication\n" +
		"      https://mcp.atlassian.com/v1/mcp/authv2\n" +
		"●  ✓ obsidian connected\n" +
		"└  2 server(s)\n"

	if parseServerConnected(out, "atlassian") {
		t.Error("atlassian should be reported NOT connected (needs authentication)")
	}
	if !parseServerConnected(out, "obsidian") {
		t.Error("obsidian should be reported connected")
	}
	// Whole-word matching: a differently-named server must not match.
	if parseServerConnected(out, "atlas") {
		t.Error("'atlas' should not match 'atlassian'")
	}
	if parseServerConnected(out, "missing") {
		t.Error("absent server should be NOT connected")
	}
}

func TestServerAuthenticatedAndHandler(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub not supported on windows")
	}
	bin := writeFakeOpencode(t, `#!/bin/sh
if [ "$1" = "mcp" ] && [ "$2" = "list" ]; then
  printf '%s\n' 'atlassian connected'
  printf '%s\n' 'obsidian connected'
  exit 0
fi
exit 1
`)
	old := OpencodeBin
	OpencodeBin = bin
	defer func() { OpencodeBin = old }()

	if !ServerAuthenticated("atlassian") {
		t.Error("expected atlassian authenticated")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/mcp/servers/atlassian/status", nil)
	req.SetPathValue("name", "atlassian")
	HandleStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status handler code %d", rec.Code)
	}
	var got struct {
		Server        string `json:"server"`
		Authenticated bool   `json:"authenticated"`
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Server != "atlassian" || !got.Authenticated {
		t.Errorf("unexpected status response: %+v", got)
	}
}

func TestLogout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub not supported on windows")
	}
	isolateMCPConfig(t, true, "atlassian")
	bin := writeFakeOpencode(t, `#!/bin/sh
if [ "$1" = "mcp" ] && [ "$2" = "logout" ]; then
  printf '%s\n' "removed credentials for $3"
  exit 0
fi
exit 1
`)
	old := OpencodeBin
	OpencodeBin = bin
	defer func() { OpencodeBin = old }()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/servers/atlassian/logout", nil)
	req.SetPathValue("name", "atlassian")
	HandleLogout(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout handler code %d", rec.Code)
	}
	var got struct {
		OK     bool   `json:"ok"`
		Output string `json:"output"`
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.OK {
		t.Errorf("expected ok=true, got %+v", got)
	}
}

// TestAuthFlow_CompletesSuccessfully drives startAuth against a fake opencode
// that prints an authorization URL then exits; `mcp list` reports the server
// connected, so the flow must finish Done+Success and expose the URL.
func TestAuthFlow_CompletesSuccessfully(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub not supported on windows")
	}
	// mcp auth: print a URL, then exit 0. mcp list: server is connected.
	bin := writeFakeOpencode(t, `#!/bin/sh
if [ "$1" = "mcp" ] && [ "$2" = "auth" ]; then
  printf 'Authorize in your browser:\n'
  printf 'https://auth.example.com/authorize?client_id=abc&state=xyz\n'
  exit 0
fi
if [ "$1" = "mcp" ] && [ "$2" = "list" ]; then
  printf '%s\n' 'atlassian connected'
  exit 0
fi
exit 1
`)
	old := OpencodeBin
	OpencodeBin = bin
	defer func() { OpencodeBin = old }()

	// Reset shared state.
	isolateMCPConfig(t, true, "atlassian")
	auth.mu.Lock()
	auth.resetLocked("")
	auth.mu.Unlock()

	if err := startAuth("atlassian"); err != nil {
		t.Fatalf("startAuth: %v", err)
	}

	// Poll snapshot until Done.
	deadline := time.Now().Add(6 * time.Second)
	var final AuthInfo
	for time.Now().Before(deadline) {
		final = snapshot()
		if final.Done {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !final.Done {
		t.Fatalf("auth flow did not finish: %+v", final)
	}
	if !final.Success {
		t.Errorf("expected success, got %+v", final)
	}
	if final.VerifyURL != "https://auth.example.com/authorize?client_id=abc&state=xyz" {
		t.Errorf("expected scraped auth URL, got %q", final.VerifyURL)
	}
	if final.Server != "atlassian" {
		t.Errorf("expected server=atlassian, got %q", final.Server)
	}
}

// TestAuthFlow_ExposesURLViaStartHandler verifies the /auth/start handler
// blocks briefly and returns the URL in its first response.
func TestAuthFlow_ExposesURLViaStartHandler(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub not supported on windows")
	}
	// mcp auth: print URL then block a bit (so the URL is scraped while running).
	bin := writeFakeOpencode(t, `#!/bin/sh
if [ "$1" = "mcp" ] && [ "$2" = "auth" ]; then
  printf 'https://auth.example.com/authorize?x=1\n'
  sleep 2
  exit 0
fi
if [ "$1" = "mcp" ] && [ "$2" = "list" ]; then
  printf '%s\n' 'atlassian connected'
  exit 0
fi
exit 1
`)
	old := OpencodeBin
	OpencodeBin = bin
	defer func() { OpencodeBin = old }()

	isolateMCPConfig(t, true, "atlassian")
	auth.mu.Lock()
	auth.resetLocked("")
	auth.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/servers/atlassian/auth/start", nil)
	req.SetPathValue("name", "atlassian")
	HandleAuthStart(rec, req)

	var info AuthInfo
	json.Unmarshal(rec.Body.Bytes(), &info)
	if info.VerifyURL != "https://auth.example.com/authorize?x=1" {
		t.Errorf("start handler should surface the URL, got %+v", info)
	}

	// Cancel to tidy up the still-running fake process.
	crec := httptest.NewRecorder()
	creq := httptest.NewRequest(http.MethodPost, "/api/mcp/servers/atlassian/auth/cancel", nil)
	creq.SetPathValue("name", "atlassian")
	HandleAuthCancel(crec, creq)
}

func TestStartAuth_GitHubRejected(t *testing.T) {
	auth.mu.Lock()
	auth.resetLocked("")
	auth.mu.Unlock()

	// Direct call: github must be refused with the typed error, never spawning
	// an OAuth flow (it is bearer-token auth via GitHub Copilot login).
	if err := startAuth("github"); err == nil {
		t.Fatal("startAuth(github) should be rejected")
	} else if _, ok := err.(ErrOAuthUnsupported); !ok {
		t.Fatalf("startAuth(github) error = %T (%v), want ErrOAuthUnsupported", err, err)
	}
	// Case-insensitive.
	if err := startAuth("GitHub"); err == nil {
		t.Fatal("startAuth(GitHub) should be rejected (case-insensitive)")
	}

	// No auth flow should have started.
	auth.mu.Lock()
	running := auth.running || auth.cmd != nil
	auth.mu.Unlock()
	if running {
		t.Error("startAuth(github) must not start an auth flow")
	}

	// HTTP handler surfaces it as 400 Bad Request.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/servers/github/auth/start", nil)
	req.SetPathValue("name", "github")
	HandleAuthStart(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("HandleAuthStart(github) status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestDeliverCallback_Validation(t *testing.T) {
	cases := []struct{ name, url string }{
		{"empty", ""},
		{"not-loopback", "http://evil.example.com/mcp/oauth/callback?code=x&state=y"},
		{"wrong-path", "http://127.0.0.1:19876/something-else?code=x"},
		{"missing-code", "http://127.0.0.1:19876/mcp/oauth/callback?state=y"},
		{"bad-scheme", "ftp://127.0.0.1:19876/mcp/oauth/callback?code=x"},
	}
	for _, c := range cases {
		if _, _, err := DeliverCallback(c.url); err == nil {
			t.Errorf("%s: expected rejection for %q", c.name, c.url)
		}
	}
}

func TestDeliverCallback_ForwardsToLoopback(t *testing.T) {
	// Stand up a fake local callback listener (stands in for opencode's).
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// httptest binds 127.0.0.1; build a callback URL on the OAuth path with a code.
	callback := srv.URL + "/mcp/oauth/callback?code=abc123&state=xyz"

	code, body, err := DeliverCallback(callback)
	if err != nil {
		t.Fatalf("DeliverCallback: %v", err)
	}
	if code != http.StatusOK {
		t.Errorf("expected 200 from listener, got %d", code)
	}
	if body != "ok" {
		t.Errorf("expected body 'ok', got %q", body)
	}
	if gotQuery.Get("code") != "abc123" || gotQuery.Get("state") != "xyz" {
		t.Errorf("listener did not receive code/state, got %v", gotQuery)
	}
}

func TestHandleAuthCallback_RejectsBadURL(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/servers/atlassian/auth/callback",
		strings.NewReader(`{"url":"http://evil.example.com/mcp/oauth/callback?code=x"}`))
	req.SetPathValue("name", "atlassian")
	HandleAuthCallback(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-loopback callback, got %d", rec.Code)
	}
	var resp struct {
		OK bool `json:"ok"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.OK {
		t.Error("expected ok=false")
	}
}

func TestParseAllServers(t *testing.T) {
	// Realistic `opencode mcp list` output (ANSI already implied stripped by
	// the parser via ansiRe; include the box glyphs opencode uses).
	raw := "" +
		"\n┌  MCP Servers\n│\n" +
		"●  ✓ atlassian connected\n" +
		"│      https://mcp.atlassian.com/v1/mcp/authv2\n│\n" +
		"●  ✗ github needs authentication\n" +
		"│      https://api.githubcopilot.com/mcp\n│\n" +
		"●  ✓ obsidian connected\n" +
		"│      /home/developer/.local/bin/obsidian-mcp\n" +
		"└  3 server(s)\n"

	got := parseAllServers(raw)
	if got["atlassian"] != true {
		t.Errorf("atlassian should be connected, got %v", got["atlassian"])
	}
	if got["obsidian"] != true {
		t.Errorf("obsidian should be connected, got %v", got["obsidian"])
	}
	if got["github"] != false {
		t.Errorf("github should be needs-auth (false), got %v", got["github"])
	}
	// Header/footer lines must not produce entries.
	if _, ok := got["servers"]; ok {
		t.Error("header line leaked into results")
	}
	if len(got) != 3 {
		t.Errorf("expected exactly 3 servers, got %d: %v", len(got), got)
	}
}

func TestServerNameFromLine(t *testing.T) {
	cases := map[string]string{
		"●  ✓ atlassian connected":           "atlassian",
		"●  ✗ github needs authentication":   "github",
		"●  ✓ obsidian connected":            "obsidian",
		"┌  MCP Servers":                     "", // no status word
		"│      https://mcp.atlassian.com/…": "", // detail line, no status word
		"└  2 server(s)":                     "", // footer
	}
	for line, want := range cases {
		if got, _, _ := serverRow(line); got != want {
			t.Errorf("serverRow(%q) name = %q, want %q", line, got, want)
		}
	}
}

// A user who clicks "Sign in" without switching the server on first would
// complete the whole OAuth flow and still find the agent unable to use it,
// because opencode never connects a disabled server. Refuse up front.
func TestStartAuth_RefusesDisabledServer(t *testing.T) {
	isolateMCPConfig(t, false, "sentry")

	auth.mu.Lock()
	auth.resetLocked("")
	auth.mu.Unlock()

	// Fail loudly if the guard lets the flow through: this binary would be
	// spawned only if we got past the enable check.
	old := OpencodeBin
	OpencodeBin = writeFakeOpencode(t, "#!/bin/sh\nexit 0\n")
	defer func() { OpencodeBin = old }()

	err := startAuth("sentry")
	if err == nil {
		t.Fatal("startAuth on a disabled server should be refused")
	}
	if _, ok := err.(ErrNotEnabled); !ok {
		t.Fatalf("error = %T (%v), want ErrNotEnabled", err, err)
	}

	auth.mu.Lock()
	started := auth.running || auth.cmd != nil
	auth.mu.Unlock()
	if started {
		t.Error("no auth flow should have been started")
	}

	// The handler surfaces it as 409 with a message naming the fix.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/servers/sentry/auth/start", nil)
	req.SetPathValue("name", "sentry")
	HandleAuthStart(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "on first") {
		t.Errorf("response should tell the user to enable first, got %s", body)
	}

	// Once enabled, the same call is allowed through (guard is the only thing
	// that was stopping it).
	isolateMCPConfig(t, true, "sentry")
	if _, _, err := mcp.IsEnabled("sentry"); err != nil {
		t.Fatalf("IsEnabled: %v", err)
	}
	if err := startAuth("sentry"); err != nil {
		if _, blocked := err.(ErrNotEnabled); blocked {
			t.Fatal("enabled server must not be refused")
		}
	}
	HandleAuthCancel(httptest.NewRecorder(), httptest.NewRequest(
		http.MethodPost, "/api/mcp/servers/sentry/auth/cancel", nil))
}

// opencode prints one of four statuses. Only "connected" is working; a failed
// server must stop reading as connected, and a disabled one says nothing about
// its credentials, so it leaves the cache alone.
func TestParseAllServersReadsEveryStatus(t *testing.T) {
	raw := "" +
		"┌  MCP Servers\n│\n" +
		"●  ✓ recall connected\n" +
		"│      /usr/bin/chat mcp-recall\n│\n" +
		"●  ✗ kfp failed\n" +
		"│      http://127.0.0.1:8977/mcp\n│\n" +
		"●  ○ github disabled\n" +
		"●  ⚠ atlassian needs authentication\n" +
		"●  ✓ connected-docs connected\n" +
		"└  5 server(s)\n"
	got := parseAllServers(raw)
	want := map[string]bool{"recall": true, "kfp": false, "atlassian": false, "connected-docs": true}
	for name, w := range want {
		if v, ok := got[name]; !ok || v != w {
			t.Errorf("%s = %v (present %v), want %v", name, v, ok, w)
		}
	}
	if _, ok := got["github"]; ok || len(got) != len(want) {
		t.Errorf("got %v: a disabled server must be left out", got)
	}
	if parseServerConnected(raw, "kfp") || !parseServerConnected(raw, "recall") || parseServerConnected(raw, "connected") {
		t.Error("parseServerConnected disagrees with parseAllServers")
	}
}
