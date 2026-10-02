// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"io"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

// With the switch on, the built-in catalogue is absent from a
// fresh workspace.
func TestCentralMCPKeepsTheCatalogueOutOfAFreshWorkspace(t *testing.T) {
	t.Setenv(config.EnvCentralConfig, "true")
	path := withGlobalConfig(t, "")

	EnsureDefaults(io.Discard)

	if block := readGlobal(t, path); len(block) != 0 {
		t.Errorf("installed %d catalogue server(s) in central mode: %v", len(block), names2(block))
	}
}

// The conservative half of the same switch: a default the user had enabled before
// the switch is still present. Removing a server somebody is signed in to, to
// satisfy a provisioning rule, is a worse outcome than an extra disabled row.
func TestCentralMCPPreservesDefaultsTheUserInvestedIn(t *testing.T) {
	// Install the catalogue as an earlier release did.
	path := withGlobalConfig(t, "")
	installEarlierCatalogue()

	before := readGlobal(t, path)
	if len(before) == 0 {
		t.Fatal("catalogue did not install; the rest of this test proves nothing")
	}
	// The user enables one and hand-edits another.
	if _, err := setEnabled("atlassian", true); err != nil {
		t.Fatal(err)
	}
	raw, err := readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	edited, _ := raw["mcp"].(map[string]any)["sentry"].(map[string]any)
	edited["url"] = "https://sentry.acme.internal/mcp"
	if err := writeConfig(path, raw); err != nil {
		t.Fatal(err)
	}

	// Now the workspace moves to the administrator's catalogue.
	t.Setenv(config.EnvCentralConfig, "true")
	EnsureDefaults(io.Discard)

	after := readGlobal(t, path)
	if _, ok := after["atlassian"]; !ok {
		t.Error("removed a server the user had enabled")
	}
	if _, ok := after["sentry"]; !ok {
		t.Error("removed a server the user had edited")
	}
	for _, gone := range []string{"context7", "deepwiki", "figma", "pendo", "slack", "microsoft-learn"} {
		if _, ok := after[gone]; ok {
			t.Errorf("kept untouched built-in %q", gone)
		}
	}
}

// github and obsidian are registered outside DefaultServers, so the catalogue
// switch must not touch them: github carries a runtime-injected Copilot bearer
// token and obsidian is installed by Knowledge Worker Agent itself.
func TestCentralMCPLeavesNonCatalogueServersAlone(t *testing.T) {
	path := withGlobalConfig(t, `{"mcp":{
		"github":{"type":"remote","url":"https://api.githubcopilot.com/mcp","enabled":false},
		"obsidian":{"type":"local","command":["obsidian-mcp"],"enabled":false},
		"mine":{"type":"remote","url":"https://mine.example.com/mcp","enabled":false}
	}}`)

	t.Setenv(config.EnvCentralConfig, "true")
	EnsureDefaults(io.Discard)

	after := readGlobal(t, path)
	for _, keep := range []string{"github", "obsidian", "mine"} {
		if _, ok := after[keep]; !ok {
			t.Errorf("removed %q, which is not a catalogue entry", keep)
		}
	}
}

// With the switch off and nothing assigned, behaviour is exactly
// what it was before central connectors existed.
func TestSwitchOffAndNothingAssignedBehavesAsBefore(t *testing.T) {
	path := withGlobalConfig(t, "")
	// No platform config at all (TestMain points OPENCODE_CONFIG_DIR at an
	// empty dir, which is the situation before central connectors).

	EnsureDefaults(io.Discard)
	EnsureProvisionedStubs(io.Discard)

	block := readGlobal(t, path)
	if len(block) != len(DefaultServers) {
		t.Fatalf("got %d servers, want the %d catalogue entries", len(block), len(DefaultServers))
	}
	views, err := listServers()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.Provisioned {
			t.Errorf("%s marked provisioned with nothing assigned", v.Name)
		}
		if v.Enabled {
			t.Errorf("%s installed enabled; the catalogue is opt-in", v.Name)
		}
	}
}

func names2(block map[string]any) []string {
	out := make([]string, 0, len(block))
	for k := range block {
		out = append(out, k)
	}
	return out
}

// Several catalogue names — atlassian, figma, pendo — are also names an
// administrator provisions, so a provisioned server whose name collides with a
// built-in is the common case rather than an edge one.
//
// Two things must hold when the catalogue is retired underneath one. The stub
// must survive: it is the user's on/off choice, and the definition it belongs
// to comes from the platform dir, not the catalogue. And it must not be
// reported as a built-in "you enabled or edited", which would be a plain
// falsehood about something the user has never touched.
func TestCentralMCPLeavesProvisionedStubsAloneAndUnreported(t *testing.T) {
	withPlatformConfig(t, `{"mcp":{"atlassian":{"type":"remote","url":"https://mcp.atlassian.com/v1/mcp/authv2"}}}`,
		`{"servers":[{"name":"atlassian","auth":{"type":"oauth"}}]}`)

	// Install the catalogue, then seed the provisioned stub, then flip the
	// switch — the migration order a real workspace goes through.
	path := withGlobalConfig(t, "")
	installEarlierCatalogue()
	EnsureProvisionedStubs(io.Discard)

	t.Setenv(config.EnvCentralConfig, "true")
	var log strings.Builder
	EnsureDefaults(&log)

	block := readGlobal(t, path)
	if _, ok := block["atlassian"]; !ok {
		t.Error("removed the provisioned stub; the user's on/off choice is gone")
	}
	if strings.Contains(log.String(), "atlassian") {
		t.Errorf("reported a provisioned server as a built-in the user invested in:\n%s", log.String())
	}
	// The rest of the catalogue should still have been cleared out.
	for name := range block {
		if !strings.EqualFold(name, "atlassian") {
			t.Errorf("catalogue entry %q survived central mode", name)
		}
	}
}
