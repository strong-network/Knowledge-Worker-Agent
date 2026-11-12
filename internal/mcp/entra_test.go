// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// An artifact can declare a Microsoft connector and the app it signs in with.
func TestArtifactDeclaresAMicrosoftConnector(t *testing.T) {
	withGlobalConfig(t, "")
	withPlatformConfig(t, `{"mcp":{"workiq":{"type":"remote","url":"https://workiq.example.com/mcp"},"acme":{"type":"remote","url":"https://mcp.acme.example.com/mcp"}}}`,
		`{"servers":[{"name":"workiq","auth":{"type":"entra","clientId":"client-9","tenant":"contoso.example.com"}},
		{"name":"acme","auth":{"type":"oauth","clientId":"ignored","tenant":"ignored"}}]}`)

	if sv := findServer(t, "workiq"); sv.Auth != AuthEntra {
		t.Errorf("auth = %q, want entra", sv.Auth)
	}
	if id, tenant := EntraApp("WorkIQ"); id != "client-9" || tenant != "contoso.example.com" {
		t.Errorf("EntraApp = %q, %q", id, tenant)
	}
	if id, tenant := EntraApp("acme"); id != "" || tenant != "" {
		t.Errorf("an oauth artifact named an app: %q, %q", id, tenant)
	}
	if id, _ := EntraApp("missing"); id != "" {
		t.Errorf("an unknown connector named an app: %q", id)
	}
}

// The list says who a Microsoft sign-in is for, and why it stopped working.
func TestListShowsTheMicrosoftSignIn(t *testing.T) {
	withGlobalConfig(t, `{"mcp":{"workiq":{"type":"remote","url":"https://workiq.example.com/mcp","enabled":true},"other":{"type":"remote","url":"https://other.example.com/mcp","enabled":true}}}`)
	t.Setenv("KWA_HOME", t.TempDir())
	dir := filepath.Join(layout.System(), "entra")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	record := `{"server":"workiq","account":"alex@example.com","failed":"Your Microsoft sign-in has expired. Sign in again."}`
	if err := os.WriteFile(filepath.Join(dir, "workiq.json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	HandleList(rec, httptest.NewRequest(http.MethodGet, "/api/mcp/servers", nil))
	var body struct {
		Servers []serverView `json:"servers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, sv := range body.Servers {
		switch sv.Name {
		case "workiq":
			if sv.Microsoft == nil || sv.Microsoft.Account != "alex@example.com" || sv.Microsoft.Failed == "" {
				t.Errorf("workiq: %+v", sv.Microsoft)
			}
		case "other":
			if sv.Microsoft != nil {
				t.Errorf("other has a Microsoft sign-in: %+v", sv.Microsoft)
			}
		}
	}
}

// A connector's oauth.scope comes from its central entry, or else the user's.
func TestOAuthScopeFromTheEntry(t *testing.T) {
	withGlobalConfig(t, `{"mcp":{"mine":{"type":"remote","url":"https://mine.example.com/mcp","oauth":{"scope":" res-1/.default "}},
		"plain":{"type":"remote","url":"https://plain.example.com/mcp"},"copilot":{"enabled":true}}}`)
	withPlatformConfig(t, `{"mcp":{"copilot":{"type":"remote","url":"https://copilot.example.com/mcp","oauth":{"scope":"res-2/.default"}}}}`,
		`{"servers":[{"name":"copilot","auth":{"type":"entra"}}]}`)

	for name, want := range map[string]string{"Mine": "res-1/.default", "copilot": "res-2/.default", "plain": "", "missing": ""} {
		if got := OAuthScope(name); got != want {
			t.Errorf("OAuthScope(%q) = %q, want %q", name, got, want)
		}
	}
}
