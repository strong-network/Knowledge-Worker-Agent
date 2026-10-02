// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

const atlassianArtifact = `{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "atlassian": {
      "type": "remote",
      "url": "https://mcp.atlassian.com/v1/mcp/authv2"
    }
  }
}`

const atlassianMeta = `{
  "servers": [
    {
      "name": "atlassian",
      "auth": {
        "type": "oauth",
        "label": "Atlassian",
        "help": "Sign in to reach Jira and Confluence.",
        "helpUrl": "https://support.atlassian.com/rovo"
      }
    }
  ]
}`

// A server from an assigned MCP artifact appears in the modal, marked as
// organization-provided, rather than being live in the model's tool
// set and invisible here.
func TestProvisionedServerIsListedAndMarked(t *testing.T) {
	withGlobalConfig(t, "")
	withPlatformConfig(t, atlassianArtifact, atlassianMeta)

	sv := findServer(t, "atlassian")
	if !sv.Provisioned {
		t.Error("not marked provisioned")
	}
	if sv.URL != "https://mcp.atlassian.com/v1/mcp/authv2" {
		t.Errorf("url = %q, want the platform's", sv.URL)
	}
	if sv.Label != "Atlassian" || sv.Help == "" {
		t.Errorf("artifact UI text not surfaced: %+v", sv)
	}
	if sv.HelpURL != "https://support.atlassian.com/rovo" {
		t.Errorf("helpUrl = %q", sv.HelpURL)
	}
}

// The same server reaches the connector bar, which builds on
// GlobalServers(). It inherits the merge rather than needing its own.
func TestProvisionedServerReachesGlobalServers(t *testing.T) {
	withGlobalConfig(t, "")
	withPlatformConfig(t, atlassianArtifact, atlassianMeta)

	servers, err := GlobalServers()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range servers {
		if s.Name == "atlassian" {
			return
		}
	}
	t.Fatalf("atlassian missing from GlobalServers: %+v", servers)
}

// It arrives switched OFF. A server with no "enabled" key in
// either file is connected by opencode, so provisioning would otherwise mean
// enabling — tools in the schema, and cost, from the moment of assignment.
func TestProvisionedServerArrivesSwitchedOff(t *testing.T) {
	path := withGlobalConfig(t, "")
	withPlatformConfig(t, atlassianArtifact, atlassianMeta)

	EnsureProvisionedStubs(io.Discard)

	if sv := findServer(t, "atlassian"); sv.Enabled {
		t.Error("provisioned server is on before the user asked for it")
	}
	// The stub carries only the switch: the definition stays the platform's, so
	// a later change to the artifact's URL is picked up rather than shadowed.
	entry, ok := readGlobal(t, path)["atlassian"].(map[string]any)
	if !ok {
		t.Fatalf("no stub written to the Global")
	}
	if entry["enabled"] != false {
		t.Errorf("stub = %v, want enabled:false", entry)
	}
	if _, hasURL := entry["url"]; hasURL {
		t.Errorf("stub copied the definition into the Global: %v", entry)
	}
}

// Enabling and disabling work for a server that has never had an
// entry in the user's Global. This was once a silent no-op — setEnabled
// returned ok=false and wrote nothing, so the toggle moved and nothing happened.
func TestSetEnabledWorksForProvisionedServerWithNoGlobalEntry(t *testing.T) {
	path := withGlobalConfig(t, "")
	withPlatformConfig(t, atlassianArtifact, atlassianMeta)

	ok, err := setEnabled("atlassian", true)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("setEnabled reported the server as unknown")
	}
	if entry, _ := readGlobal(t, path)["atlassian"].(map[string]any); entry["enabled"] != true {
		t.Fatalf("nothing written to the Global: %v", readGlobal(t, path))
	}
	if sv := findServer(t, "atlassian"); !sv.Enabled {
		t.Error("still reads as off after enabling")
	}

	if ok, err := setEnabled("atlassian", false); err != nil || !ok {
		t.Fatalf("disable: ok=%v err=%v", ok, err)
	}
	if sv := findServer(t, "atlassian"); sv.Enabled {
		t.Error("still reads as on after disabling")
	}
}

// Durability: the user's choice lives in the Global, which the
// materializer never touches, so it survives the platform dir being wiped and
// rewritten.
func TestEnabledChoiceSurvivesRematerialization(t *testing.T) {
	withGlobalConfig(t, "")
	withPlatformConfig(t, atlassianArtifact, atlassianMeta)

	EnsureProvisionedStubs(io.Discard)
	if _, err := setEnabled("atlassian", true); err != nil {
		t.Fatal(err)
	}

	// Re-materialize: the platform dir is wiped and rewritten from scratch.
	withPlatformConfig(t, atlassianArtifact, atlassianMeta)
	EnsureProvisionedStubs(io.Discard)

	if sv := findServer(t, "atlassian"); !sv.Enabled {
		t.Error("re-materialization switched the user's server back off")
	}
}

// Seeding is skipped when the name is already present, so a boot
// never quietly undoes a user's decision.
func TestSeedingIsIdempotentAndNeverOverwritesTheUsersChoice(t *testing.T) {
	path := withGlobalConfig(t, "")
	withPlatformConfig(t, atlassianArtifact, atlassianMeta)

	EnsureProvisionedStubs(io.Discard)
	if _, err := setEnabled("atlassian", true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		EnsureProvisionedStubs(io.Discard)
	}

	if entry, _ := readGlobal(t, path)["atlassian"].(map[string]any); entry["enabled"] != true {
		t.Fatalf("repeated seeding reset the user's choice: %v", entry)
	}
}

// A case difference between the config repo and the Global must not produce a
// second, shadowing entry.
func TestSeedingMatchesExistingNamesCaseInsensitively(t *testing.T) {
	path := withGlobalConfig(t, `{"mcp":{"Atlassian":{"enabled":true}}}`)
	withPlatformConfig(t, atlassianArtifact, atlassianMeta)

	EnsureProvisionedStubs(io.Discard)

	block := readGlobal(t, path)
	if len(block) != 1 {
		t.Fatalf("seeding created a duplicate entry: %v", block)
	}
	if entry, _ := block["Atlassian"].(map[string]any); entry["enabled"] != true {
		t.Errorf("existing entry disturbed: %v", block)
	}
}

// Remove and Edit are refused. Not access control — the platform
// dir is wiped every boot, so either action would appear to work and then undo
// itself at the next restart.
func TestAPIRefusesEditAndRemoveForProvisionedServers(t *testing.T) {
	withGlobalConfig(t, "")
	withPlatformConfig(t, atlassianArtifact, atlassianMeta)

	t.Run("remove", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/api/mcp/servers/atlassian", nil)
		req.SetPathValue("name", "atlassian")
		rec := httptest.NewRecorder()
		HandleRemove(rec, req)

		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409", rec.Code)
		}
		if msg := errorOf(t, rec.Body.Bytes()); !strings.Contains(msg, "organization") {
			t.Errorf("message does not explain why: %q", msg)
		}
		// Still listed afterwards.
		findServer(t, "atlassian")
	})

	t.Run("add over the same name", func(t *testing.T) {
		body := strings.NewReader(`{"name":"atlassian","transport":"http","url":"https://evil.example.com/mcp"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/mcp/servers", body)
		rec := httptest.NewRecorder()
		HandleAdd(rec, req)

		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409", rec.Code)
		}
		if sv := findServer(t, "atlassian"); sv.URL != "https://mcp.atlassian.com/v1/mcp/authv2" {
			t.Errorf("definition was overwritten: %q", sv.URL)
		}
	})
}

// Enabling is NOT refused: it writes to the Global, which survives.
func TestAPIAllowsEnablingAProvisionedServer(t *testing.T) {
	withGlobalConfig(t, "")
	withPlatformConfig(t, atlassianArtifact, atlassianMeta)

	body := strings.NewReader(`{"enabled":true}`)
	req := httptest.NewRequest(http.MethodPut, "/api/mcp/servers/atlassian/enabled", body)
	req.SetPathValue("name", "atlassian")
	rec := httptest.NewRecorder()
	HandleSetEnabled(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if sv := findServer(t, "atlassian"); !sv.Enabled {
		t.Error("not enabled")
	}
}

// The artifact's classification is what puts a Connect button on the row, for
// a server declared ONLY in the platform dir. This covers the server's side.
func TestArtifactSuppliesAuthClassification(t *testing.T) {
	withGlobalConfig(t, "")
	withPlatformConfig(t, `{"mcp":{"acme":{"type":"remote","url":"https://mcp.acme.example.com/mcp"}}}`,
		`{"servers":[{"name":"acme","auth":{"type":"oauth","label":"Acme"}}]}`)

	if sv := findServer(t, "acme"); sv.Auth != AuthOAuth {
		t.Errorf("auth = %q, want oauth — a server with no catalogue entry would otherwise get no sign-in affordance", sv.Auth)
	}
}

// The standalone case: with the built-in catalogue switched off,
// a provisioned atlassian still classifies as oauth from its artifact alone.
// This is what proves there is no hidden dependency on the built-in entry, and
// makes the "no built-in MCP servers" end state testable now.
func TestArtifactClassificationStandsWithoutTheCatalogue(t *testing.T) {
	t.Setenv(config.EnvCentralConfig, "true")
	withGlobalConfig(t, "")
	withPlatformConfig(t, atlassianArtifact, atlassianMeta)

	EnsureDefaults(io.Discard) // installs nothing in central mode
	EnsureProvisionedStubs(io.Discard)

	sv := findServer(t, "atlassian")
	if sv.Auth != AuthOAuth {
		t.Errorf("auth = %q, want oauth from the artifact alone", sv.Auth)
	}
	if !sv.Provisioned || sv.Enabled {
		t.Errorf("want provisioned and off, got %+v", sv)
	}
}

// The artifact outranks the catalogue — the reverse of the rule for model
// providers, and deliberate: the catalogue is going away, so a rule where it
// wins would guarantee a silent regression on the day it is deleted.
func TestArtifactOutranksTheCatalogue(t *testing.T) {
	withGlobalConfig(t, "")
	// "figma" is AuthOAuth in the catalogue; the artifact says it needs setup.
	withPlatformConfig(t, `{"mcp":{"figma":{"type":"remote","url":"https://mcp.figma.com/mcp"}}}`,
		`{"servers":[{"name":"figma","auth":{"type":"setup"}}]}`)

	if sv := findServer(t, "figma"); sv.Auth != AuthSetup {
		t.Errorf("auth = %q, want setup from the artifact", sv.Auth)
	}
}

// …but the catalogue still supplies a default for names the artifact leaves
// unclassified, so an artifact that says nothing is not a downgrade.
func TestCatalogueSuppliesTheDefaultForUnclassifiedNames(t *testing.T) {
	withGlobalConfig(t, "")
	withPlatformConfig(t, `{"mcp":{"figma":{"type":"remote","url":"https://mcp.figma.com/mcp"}}}`, "")

	if sv := findServer(t, "figma"); sv.Auth != AuthOAuth {
		t.Errorf("auth = %q, want the catalogue's oauth", sv.Auth)
	}
}

// The github server keeps its own auth classification whatever an artifact
// declares. Its bearer comes from a GitHub PAT injected at runtime and no
// artifact can restore that, so honouring "oauth" here would render a sign-in
// that fails with "does not support dynamic client registration".
func TestGithubKeepsItsOwnAuthRegardlessOfTheArtifact(t *testing.T) {
	withGlobalConfig(t, "")
	withPlatformConfig(t, `{"mcp":{"github":{"type":"remote","url":"https://ghe.acme.internal/api/mcp"}}}`,
		`{"servers":[{"name":"github","auth":{"type":"oauth"}}]}`)

	sv := findServer(t, "github")
	if sv.Auth != AuthSetup {
		t.Errorf("auth = %q, want %q", sv.Auth, AuthSetup)
	}
	// The artifact still owns the definition — only the classification is fixed.
	if sv.URL != "https://ghe.acme.internal/api/mcp" {
		t.Errorf("url = %q, want the artifact's", sv.URL)
	}
}

// A local stdio server has no account to sign in to, whatever the artifact says.
func TestLocalTransportOutranksTheArtifactsClassification(t *testing.T) {
	withGlobalConfig(t, "")
	withPlatformConfig(t, `{"mcp":{"notes":{"type":"local","command":["notes-mcp"]}}}`,
		`{"servers":[{"name":"notes","auth":{"type":"oauth"}}]}`)

	if sv := findServer(t, "notes"); sv.Auth != AuthLocal {
		t.Errorf("auth = %q, want local", sv.Auth)
	}
}

// A setup-kind server has no sign-in flow, so its help text is
// the only route forward. It must reach the UI.
func TestSetupServerCarriesItsHelpForward(t *testing.T) {
	withGlobalConfig(t, "")
	withPlatformConfig(t, `{"mcp":{"zephyr":{"type":"remote","url":"https://api.zephyrscale.smartbear.com/v2"}}}`,
		`{"servers":[{"name":"zephyr","auth":{"type":"setup","label":"Zephyr Scale","help":"Ask IT for a SmartBear API token.","helpUrl":"https://smartbear.example.com/tokens"}}]}`)

	sv := findServer(t, "zephyr")
	if sv.Auth != AuthSetup {
		t.Fatalf("auth = %q, want setup", sv.Auth)
	}
	if sv.Help == "" || sv.HelpURL == "" {
		t.Errorf("a setup row with no help is a dead end: %+v", sv)
	}
}

// helpUrl lands in an href. The config repo is trusted as to intent, not as to
// review: a copy-pasted javascript: URL would be script execution in the user's
// session.
func TestHelpURLIsRestrictedToHTTPS(t *testing.T) {
	cases := map[string]string{
		"https://example.com/docs": "https://example.com/docs",
		"javascript:alert(1)":      "",
		"http://example.com":       "",
		"data:text/html,<script>":  "",
		"":                         "",
		"not a url":                "",
	}
	for in, want := range cases {
		if got := safeHelpURL(in); got != want {
			t.Errorf("safeHelpURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// The platform owns what a server IS; the user owns whether it RUNS. opencode
// merges the two files per key, and this mirrors it.
func TestPlatformOwnsTheDefinitionAndTheGlobalOwnsTheSwitch(t *testing.T) {
	withGlobalConfig(t, `{"mcp":{"atlassian":{"type":"remote","url":"https://stale.example.com/mcp","enabled":true}}}`)
	withPlatformConfig(t, atlassianArtifact, atlassianMeta)

	sv := findServer(t, "atlassian")
	if sv.URL != "https://mcp.atlassian.com/v1/mcp/authv2" {
		t.Errorf("url = %q; the Global must not be able to override the platform's definition", sv.URL)
	}
	if !sv.Enabled {
		t.Error("the user's enabled=true was discarded; the switch must stay theirs")
	}
}

// A user-added server alongside a provisioned one keeps its own provenance and
// stays fully editable.
func TestUserAddedServersAreUnaffectedByProvisioning(t *testing.T) {
	withGlobalConfig(t, `{"mcp":{"mine":{"type":"remote","url":"https://mine.example.com/mcp","enabled":true}}}`)
	withPlatformConfig(t, atlassianArtifact, atlassianMeta)

	sv := findServer(t, "mine")
	if sv.Provisioned {
		t.Error("a user's own server was marked organization-provided")
	}
	if !sv.Enabled {
		t.Error("user's server switched off")
	}
	if IsProvisioned("mine") {
		t.Error("IsProvisioned true for a user-added server")
	}
}

// A broken or absent platform config must not take the user's own servers off
// the screen. A workspace with no central config is the normal case.
func TestBrokenOrAbsentPlatformConfigDegradesToTheGlobal(t *testing.T) {
	for _, platform := range []string{"", "{ not json", `{"mcp":"not an object"}`} {
		func() {
			withGlobalConfig(t, `{"mcp":{"mine":{"type":"remote","url":"https://mine.example.com/mcp"}}}`)
			withPlatformConfig(t, platform, "")

			views, err := listServers()
			if err != nil {
				t.Fatalf("platform=%q: %v", platform, err)
			}
			if len(views) != 1 || views[0].Name != "mine" {
				t.Errorf("platform=%q: got %v, want just the user's server", platform, names(views))
			}
		}()
	}
}

// A malformed sidecar is not an error either: the servers are still shown, they
// just fall back to the catalogue for classification.
func TestMalformedSidecarStillListsTheServers(t *testing.T) {
	withGlobalConfig(t, "")
	withPlatformConfig(t, atlassianArtifact, "{ not json")

	sv := findServer(t, "atlassian")
	if !sv.Provisioned {
		t.Error("not marked provisioned")
	}
	if sv.Auth != AuthOAuth {
		t.Errorf("auth = %q, want the catalogue's oauth as the fallback", sv.Auth)
	}
}

func errorOf(t *testing.T, body []byte) string {
	t.Helper()
	var doc struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("parse error body %q: %v", body, err)
	}
	return doc.Error
}
