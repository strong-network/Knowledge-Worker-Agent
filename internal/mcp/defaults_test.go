// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtlassianDefault(t *testing.T) {
	if AtlassianDefault.Name != "atlassian" {
		t.Errorf("expected name 'atlassian', got %q", AtlassianDefault.Name)
	}
	if AtlassianDefault.Transport != "http" {
		t.Errorf("expected http transport, got %q", AtlassianDefault.Transport)
	}
	if AtlassianDefault.URL != "https://mcp.atlassian.com/v1/mcp/authv2" {
		t.Errorf("expected current Rovo authv2 endpoint, got %q", AtlassianDefault.URL)
	}
	if AtlassianURL != AtlassianDefault.URL {
		t.Errorf("AtlassianURL (%q) and AtlassianDefault.URL (%q) must match", AtlassianURL, AtlassianDefault.URL)
	}
}

func TestDefaultServersIncludesAtlassian(t *testing.T) {
	found := false
	for _, s := range DefaultServers {
		if s.Name == "atlassian" {
			found = true
		}
	}
	if !found {
		t.Error("atlassian not in DefaultServers")
	}
}

// TestClickUpDefault pins the catalogue entry for ClickUp, retired but still
// classifying the entries users kept. The endpoint and
// its auth kind were verified against the live server: it returns a 401 with
// RFC 9728 protected-resource metadata, and its authorization server exposes a
// working dynamic client registration endpoint with PKCE (S256) — which is
// what the generic browser OAuth flow needs. ClickUp's own docs also state
// that OAuth is the only supported method (API keys are refused), so this must
// not be reclassified as AuthSetup.
func TestClickUpDefault(t *testing.T) {
	found, ok := CatalogueEntry("clickup")
	if !ok {
		t.Fatal("clickup has no catalogue entry")
	}
	if found.URL != "https://mcp.clickup.com/mcp" {
		t.Errorf("url = %q, want the documented Streamable-HTTP endpoint", found.URL)
	}
	if found.Transport != "http" {
		t.Errorf("transport = %q, want http", found.Transport)
	}
	if found.Auth != AuthOAuth {
		t.Errorf("auth = %q, want %q (its DCR endpoint works, so the browser flow drives it)", found.Auth, AuthOAuth)
	}
	// Nothing may ship pre-authenticated or pre-enabled.
	if len(found.Headers) != 0 {
		t.Errorf("clickup must not carry hard-coded headers, got %v", found.Headers)
	}
}

func TestGitHubDefault(t *testing.T) {
	if GitHubDefault.Name != "github" {
		t.Errorf("expected name 'github', got %q", GitHubDefault.Name)
	}
	if GitHubDefault.Transport != "http" {
		t.Errorf("expected http transport, got %q", GitHubDefault.Transport)
	}
	if GitHubDefault.URL != "https://api.githubcopilot.com/mcp" {
		t.Errorf("expected hosted GitHub MCP endpoint, got %q", GitHubDefault.URL)
	}
	if GitHubURL != GitHubDefault.URL {
		t.Errorf("GitHubURL (%q) and GitHubDefault.URL (%q) must match", GitHubURL, GitHubDefault.URL)
	}
}

// TestDefaultServersExcludesGitHub verifies GitHub is NOT in the OAuth
// add-if-absent DefaultServers path: it is registered separately via
// EnsureGitHub with a bearer token, because its auth server does not support
// OAuth dynamic client registration.
func TestDefaultServersExcludesGitHub(t *testing.T) {
	for _, s := range DefaultServers {
		if s.Name == "github" {
			t.Error("github must NOT be in DefaultServers (it uses EnsureGitHub bearer-token auth)")
		}
	}
}

func TestEnsureGitHub_ConfiguresBearerHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", path)

	EnsureGitHub(io.Discard, "tok-123")

	sv, ok, err := getServer("github")
	if err != nil || !ok {
		t.Fatalf("github should be configured; ok=%v err=%v", ok, err)
	}
	if sv.URL != GitHubURL {
		t.Errorf("url = %q, want %q", sv.URL, GitHubURL)
	}
	if got := sv.Headers["Authorization"]; got != "Bearer tok-123" {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer tok-123")
	}
}

// With no token, github is still REGISTERED — visible, disabled, header-less —
// so a token-less workspace shows a row that can explain itself. Previously it
// was skipped entirely and the user had nothing to discover.
func TestEnsureGitHub_EmptyTokenStillRegistersVisibly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", path)

	EnsureGitHub(io.Discard, "   ")

	sv, ok, _ := getServer("github")
	if !ok {
		t.Fatal("github should be registered even with no token, so the user can find it")
	}
	if sv.Enabled {
		t.Error("a header-less github cannot connect (400), so it must arrive disabled")
	}
	if sv.Headers["Authorization"] != "" {
		t.Errorf("no token means no bearer header, got %q", sv.Headers["Authorization"])
	}
	if sv.URL != GitHubURL {
		t.Errorf("url = %q, want %q", sv.URL, GitHubURL)
	}
}

// Losing the token must not strip a working header: EnsureGitHub is re-run on
// every bootstrap, and a transient empty read would otherwise silently break a
// server that was fine.
func TestEnsureGitHub_EmptyTokenPreservesAnExistingHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", path)

	EnsureGitHub(io.Discard, "tok-good")
	EnsureGitHub(io.Discard, "")

	sv, _, _ := getServer("github")
	if sv.Headers["Authorization"] != "Bearer tok-good" {
		t.Errorf("existing header should survive a token-less re-run, got %q", sv.Headers["Authorization"])
	}
}

// A token refresh must not silently switch off a server the user turned on.
func TestEnsureGitHub_PreservesTheUsersEnabledChoice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", path)

	EnsureGitHub(io.Discard, "tok-A")
	if _, err := setEnabled("github", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	EnsureGitHub(io.Discard, "tok-B")

	sv, _, _ := getServer("github")
	if !sv.Enabled {
		t.Error("a token refresh must not switch the server back off")
	}
	if sv.Headers["Authorization"] != "Bearer tok-B" {
		t.Errorf("header should update, got %q", sv.Headers["Authorization"])
	}
}

func TestEnsureGitHub_RepairsBrokenOAuthEntryAndUpdatesToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", path)

	// Simulate the broken state: github registered as a bare OAuth remote with
	// no Authorization header.
	if _, err := upsertServer(serverView{
		Name:      "github",
		Transport: "http",
		URL:       GitHubURL,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	EnsureGitHub(io.Discard, "tok-A")
	sv, _, _ := getServer("github")
	if sv.Headers["Authorization"] != "Bearer tok-A" {
		t.Fatalf("broken OAuth entry should be repaired with a bearer header, got %q", sv.Headers["Authorization"])
	}

	// A new token updates the header.
	EnsureGitHub(io.Discard, "tok-B")
	sv, _, _ = getServer("github")
	if sv.Headers["Authorization"] != "Bearer tok-B" {
		t.Errorf("header should update to the new token, got %q", sv.Headers["Authorization"])
	}
}

// serverURLInConfig reads a stored server's URL by name, or "" if absent.
func serverURLInConfig(t *testing.T, name string) string {
	t.Helper()
	sv, ok, err := getServer(name)
	if err != nil {
		t.Fatalf("getServer(%q): %v", name, err)
	}
	if !ok {
		return ""
	}
	return sv.URL
}

// atlassianURLInConfig reads the stored atlassian server URL, or "" if absent.
func atlassianURLInConfig(t *testing.T) string {
	t.Helper()
	sv, ok, err := getServer("atlassian")
	if err != nil {
		t.Fatalf("getServer: %v", err)
	}
	if !ok {
		return ""
	}
	return sv.URL
}

func TestMigrateAtlassianURL_UpgradesLegacyMcp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", path)

	// Seed a legacy-registered atlassian server (old /v1/mcp endpoint).
	if _, err := upsertServer(serverView{
		Name:      "atlassian",
		Transport: "http",
		URL:       "https://mcp.atlassian.com/v1/mcp",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	MigrateAtlassianURL(io.Discard)

	if got := atlassianURLInConfig(t); got != AtlassianURL {
		t.Errorf("legacy /v1/mcp should migrate to %q, got %q", AtlassianURL, got)
	}
}

func TestMigrateAtlassianURL_UpgradesLegacySSE(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", path)

	if _, err := upsertServer(serverView{
		Name:      "atlassian",
		Transport: "sse",
		URL:       "https://mcp.atlassian.com/v1/sse",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	MigrateAtlassianURL(io.Discard)

	if got := atlassianURLInConfig(t); got != AtlassianURL {
		t.Errorf("legacy /v1/sse should migrate to %q, got %q", AtlassianURL, got)
	}
}

func TestMigrateAtlassianURL_LeavesCurrentUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", path)

	if _, err := upsertServer(serverView{
		Name:      "atlassian",
		Transport: "http",
		URL:       AtlassianURL,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	MigrateAtlassianURL(io.Discard)

	if got := atlassianURLInConfig(t); got != AtlassianURL {
		t.Errorf("current URL should be unchanged, got %q", got)
	}
}

func TestMigrateAtlassianURL_LeavesCustomURLUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", path)

	// A user-supplied non-legacy URL (e.g. an enterprise/proxy endpoint) must
	// not be clobbered by the migration.
	custom := "https://mcp.example.internal/atlassian"
	if _, err := upsertServer(serverView{
		Name:      "atlassian",
		Transport: "http",
		URL:       custom,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	MigrateAtlassianURL(io.Discard)

	if got := atlassianURLInConfig(t); got != custom {
		t.Errorf("custom URL should be preserved, got %q", got)
	}
}

func TestMigrateAtlassianURL_NoopWhenAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", path)

	// No atlassian server configured: migration must not create one.
	MigrateAtlassianURL(io.Discard)

	if got := atlassianURLInConfig(t); got != "" {
		t.Errorf("migration should not add atlassian when absent, got %q", got)
	}
}

func TestEnsureDefaults_AddsAndMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", path)

	// Fresh config: EnsureDefaults should add atlassian on the current URL.
	EnsureDefaults(io.Discard)
	if got := atlassianURLInConfig(t); got != AtlassianURL {
		t.Fatalf("EnsureDefaults should add atlassian on %q, got %q", AtlassianURL, got)
	}
	// EnsureDefaults must NOT add github — it is configured separately by
	// EnsureGitHub with a bearer token.
	if got := serverURLInConfig(t, "github"); got != "" {
		t.Fatalf("EnsureDefaults should not add github (got %q); it uses EnsureGitHub", got)
	}

	// Simulate a legacy entry, then re-run: EnsureDefaults must migrate it.
	if _, err := upsertServer(serverView{
		Name:      "atlassian",
		Transport: "http",
		URL:       "https://mcp.atlassian.com/v1/mcp",
	}); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	EnsureDefaults(io.Discard)
	if got := atlassianURLInConfig(t); got != AtlassianURL {
		t.Errorf("EnsureDefaults should migrate legacy atlassian to %q, got %q", AtlassianURL, got)
	}
}

// ── Approved-catalogue defaults (enabled gating) ─────────────────────────────

// Every catalogue entry, current or retired, must be well formed: a unique
// name and a URL, since they are all remote servers.
func TestDefaultServers_WellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range append(append([]DefaultServer{}, DefaultServers...), RetiredServers...) {
		if s.Name == "" {
			t.Fatalf("catalogue entry with empty name: %+v", s)
		}
		if seen[strings.ToLower(s.Name)] {
			t.Errorf("duplicate catalogue entry %q", s.Name)
		}
		seen[strings.ToLower(s.Name)] = true
		if s.URL == "" && s.Command == "" {
			t.Errorf("%q has neither URL nor command", s.Name)
		}
	}
}

// The built-in catalogue is a starter set: one server for each way of
// connecting that needs no setup. GitHub, local servers and API keys are
// covered outside it.
func TestDefaultServers_StarterSet(t *testing.T) {
	byAuth := map[AuthKind][]string{}
	for _, s := range DefaultServers {
		byAuth[s.Auth] = append(byAuth[s.Auth], s.Name)
	}
	if len(DefaultServers) != 2 || len(byAuth[AuthNone]) != 1 || len(byAuth[AuthOAuth]) != 1 {
		t.Errorf("want one anonymous and one sign-in server, got %v", byAuth)
	}
}

// Nothing ships enabled: a new workspace must have no active MCP server, so
// the model starts with no third-party tools at all. Auth classification is
// metadata for the UI badge and must be set for every catalogue entry,
// including the retired ones users kept.
func TestDefaultServers_Classification(t *testing.T) {
	want := map[string]AuthKind{
		"atlassian": AuthOAuth, "clickup": AuthOAuth, "figma": AuthOAuth,
		"gainsight": AuthOAuth, "pendo": AuthOAuth, "sentry": AuthOAuth,
		"aws": AuthNone, "context7": AuthNone, "deepwiki": AuthNone,
		"microsoft-learn": AuthNone,
		"azure-devops":    AuthSetup, "microsoft-foundry": AuthSetup,
		"pagerduty": AuthSetup, "slack": AuthSetup, "zephyr": AuthSetup,
	}
	for _, s := range append(append([]DefaultServer{}, DefaultServers...), RetiredServers...) {
		if s.Auth == AuthUnknown {
			t.Errorf("%q has no auth classification", s.Name)
		}
		if w, ok := want[s.Name]; ok && s.Auth != w {
			t.Errorf("%q auth = %q, want %q", s.Name, s.Auth, w)
		}
	}
	if GitHubDefault.Auth != AuthSetup {
		t.Errorf("github auth = %q, want %q", GitHubDefault.Auth, AuthSetup)
	}
}

// A setup-kind server has no sign-in flow, so its help text is the only route
// forward. A row the user cannot act on and cannot understand is a dead end.
func TestEverySetupServerCarriesHelpText(t *testing.T) {
	all := append(append([]DefaultServer{GitHubDefault}, DefaultServers...), RetiredServers...)
	for _, s := range all {
		if s.Auth != AuthSetup {
			continue
		}
		if strings.TrimSpace(s.Help) == "" {
			t.Errorf("%q is setup-kind but has no help text", s.Name)
		}
	}
}

// The catalogue's help must reach the UI for built-ins, not just for
// provisioned artifacts — that was the gap that left github unexplained.
func TestCatalogueHelpReachesTheServerView(t *testing.T) {
	withGlobalConfig(t, `{"mcp":{"github":{"type":"remote","url":"https://api.githubcopilot.com/mcp","enabled":false}}}`)

	sv := findServer(t, "github")
	if sv.Auth != AuthSetup {
		t.Errorf("auth = %q, want %q", sv.Auth, AuthSetup)
	}
	if !strings.Contains(sv.Help, "repo scope") {
		t.Errorf("help = %q, want it to mention the required scope", sv.Help)
	}
	if sv.HelpURL == "" {
		t.Error("want a help URL pointing at token creation")
	}
}

func TestAuthKindFor(t *testing.T) {
	cases := []struct {
		name, transport string
		want            AuthKind
	}{
		{"context7", "http", AuthNone}, // retired, still classified
		{"microsoft-learn", "http", AuthNone},
		{"atlassian", "http", AuthOAuth},
		{"SENTRY", "http", AuthOAuth}, // case-insensitive
		{"slack", "http", AuthSetup},
		{"github", "http", AuthSetup},
		{"obsidian", "stdio", AuthLocal}, // local wins, not in the catalogue
		{"context7", "stdio", AuthLocal}, // transport wins over catalogue
		{"something-custom", "http", AuthUnknown},
	}
	for _, c := range cases {
		if got := AuthKindFor(c.name, c.transport); got != c.want {
			t.Errorf("AuthKindFor(%q, %q) = %q, want %q", c.name, c.transport, got, c.want)
		}
	}
}

// EnsureDefaults writes the whole catalogue disabled, and never touches a
// server the user already configured.
func TestEnsureDefaults_RegistersCatalogueDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", path)

	EnsureDefaults(io.Discard)

	for _, want := range DefaultServers {
		sv, ok, err := getServer(want.Name)
		if err != nil || !ok {
			t.Fatalf("%q not registered (err=%v)", want.Name, err)
		}
		if sv.Enabled {
			t.Errorf("%q must be registered disabled", want.Name)
		}
		if sv.URL != want.URL {
			t.Errorf("%q url = %q, want %q", want.Name, sv.URL, want.URL)
		}
		if sv.Auth != want.Auth {
			t.Errorf("%q auth = %q, want %q", want.Name, sv.Auth, want.Auth)
		}
	}

	// Re-running must not flip a server the user enabled by hand.
	if ok, err := setEnabled("microsoft-learn", true); err != nil || !ok {
		t.Fatalf("setEnabled: ok=%v err=%v", ok, err)
	}
	EnsureDefaults(io.Discard)
	if sv, _, _ := getServer("microsoft-learn"); !sv.Enabled {
		t.Error("EnsureDefaults must not disable a server the user turned on")
	}
}

// A workspace from an earlier release has the servers since retired. The ones
// the user never touched go; one they switched on or edited stays, and keeps
// its badge and help text.
func TestEnsureDefaults_RemovesUntouchedRetiredServers(t *testing.T) {
	path := withGlobalConfig(t, "")
	installEarlierCatalogue()

	if ok, err := setEnabled("context7", true); err != nil || !ok {
		t.Fatalf("setEnabled: ok=%v err=%v", ok, err)
	}
	raw, err := readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	slack, _ := raw["mcp"].(map[string]any)["slack"].(map[string]any)
	slack["headers"] = map[string]any{"Authorization": "Bearer {env:SLACK_TOKEN}"}
	if err := writeConfig(path, raw); err != nil {
		t.Fatal(err)
	}

	EnsureDefaults(io.Discard)

	after := readGlobal(t, path)
	for _, s := range RetiredServers {
		_, present := after[s.Name]
		if keep := s.Name == "context7" || s.Name == "slack"; present != keep {
			t.Errorf("%q present = %v, want %v", s.Name, present, keep)
		}
	}
	for _, s := range DefaultServers {
		if _, ok := after[s.Name]; !ok {
			t.Errorf("starter server %q removed", s.Name)
		}
	}
	if sv := findServer(t, "context7"); sv.Auth != AuthNone {
		t.Errorf("context7 auth = %q, want %q: it would offer a sign-in that cannot work", sv.Auth, AuthNone)
	}
	if sv := findServer(t, "slack"); sv.Auth != AuthSetup || sv.Help == "" {
		t.Errorf("slack auth = %q, help = %q: want setup, with its help text", sv.Auth, sv.Help)
	}
}

// installEarlierCatalogue writes the catalogue as earlier releases did, with
// the servers since retired.
func installEarlierCatalogue() {
	EnsureDefaults(io.Discard)
	for _, s := range RetiredServers {
		EnsureServer(io.Discard, s)
	}
}

func TestSetEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", path)

	if _, err := upsertServer(serverView{
		Name: "sentry", Transport: "http", URL: "https://mcp.sentry.dev/mcp",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if sv, _, _ := getServer("sentry"); sv.Enabled {
		t.Fatal("seeded server should start disabled")
	}

	ok, err := SetEnabled("sentry", true)
	if err != nil || !ok {
		t.Fatalf("SetEnabled: ok=%v err=%v", ok, err)
	}
	if sv, _, _ := getServer("sentry"); !sv.Enabled {
		t.Error("expected sentry enabled")
	}

	// Only the flag changes; the rest of the entry survives.
	if sv, _, _ := getServer("sentry"); sv.URL != "https://mcp.sentry.dev/mcp" {
		t.Errorf("url clobbered: %q", sv.URL)
	}

	// Case-insensitive lookup, matching how names round-trip through the UI.
	if ok, err := SetEnabled("SENTRY", false); err != nil || !ok {
		t.Fatalf("case-insensitive SetEnabled: ok=%v err=%v", ok, err)
	}
	if sv, _, _ := getServer("sentry"); sv.Enabled {
		t.Error("expected sentry disabled")
	}

	// Unknown servers report ok=false rather than creating an entry.
	if ok, err := SetEnabled("nope", true); err != nil || ok {
		t.Errorf("unknown server: ok=%v err=%v", ok, err)
	}
}

// ── Requirement: a user's enable/disable choice is never silently reverted ──

// Simulates what happens on every server start: bootstrap re-runs the whole
// ensure/migrate path. Whatever the user switched on or off must survive it,
// including across a GitHub token refresh.
func TestUserToggleSurvivesBootstrap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", path)

	bootstrap := func(token string) {
		EnsureDefaults(io.Discard)
		EnsureGitHub(io.Discard, token)
	}

	bootstrap("tok-A")

	// Fresh install: nothing is on.
	for _, sv := range mustList(t) {
		if sv.Enabled {
			t.Fatalf("fresh install has %q enabled", sv.Name)
		}
	}

	// The user turns some on and explicitly leaves others off.
	for _, n := range []string{"microsoft-learn", "github", "atlassian"} {
		if ok, err := SetEnabled(n, true); err != nil || !ok {
			t.Fatalf("enable %q: ok=%v err=%v", n, ok, err)
		}
	}
	// ...then changes their mind about one of them.
	if ok, err := SetEnabled("atlassian", false); err != nil || !ok {
		t.Fatalf("disable atlassian: ok=%v err=%v", ok, err)
	}

	// Restart twice, with a rotated GitHub token in between.
	bootstrap("tok-A")
	bootstrap("tok-B")

	want := map[string]bool{
		"microsoft-learn": true, "github": true, "atlassian": false,
	}
	for name, expect := range want {
		sv, ok, err := getServer(name)
		if err != nil || !ok {
			t.Fatalf("%q missing after bootstrap (err=%v)", name, err)
		}
		if sv.Enabled != expect {
			t.Errorf("%q enabled = %v after bootstrap, want %v", name, sv.Enabled, expect)
		}
	}

	// The rotated token must still have been applied to github.
	if sv, _, _ := getServer("github"); sv.Headers["Authorization"] != "Bearer tok-B" {
		t.Errorf("github token not refreshed: %q", sv.Headers["Authorization"])
	}
}

// A legacy-URL migration rewrites the atlassian entry; that rewrite must not
// change whether the user had it switched on.
func TestMigrateAtlassianPreservesEnabled(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		path := filepath.Join(t.TempDir(), "opencode.json")
		t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", path)

		if _, err := upsertServer(serverView{
			Name: "atlassian", Transport: "http",
			URL: LegacyAtlassianURLs[0], Enabled: enabled,
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		MigrateAtlassianURL(io.Discard)

		sv, _, _ := getServer("atlassian")
		if sv.URL != AtlassianURL {
			t.Errorf("not migrated: %q", sv.URL)
		}
		if sv.Enabled != enabled {
			t.Errorf("enabled changed by migration: got %v, want %v", sv.Enabled, enabled)
		}
	}
}

func mustList(t *testing.T) []serverView {
	t.Helper()
	svs, err := listServers()
	if err != nil {
		t.Fatalf("listServers: %v", err)
	}
	return svs
}

// End-to-end over the HTTP surface the modal actually consumes: a fresh
// workspace must hand the UI a fully-classified catalogue with nothing on.
func TestHandleList_FreshInstallIsAllDisabledAndClassified(t *testing.T) {
	t.Setenv("KWA_OPENCODE_MCP_CONFIG_PATH", filepath.Join(t.TempDir(), "opencode.json"))
	EnsureDefaults(io.Discard)
	EnsureGitHub(io.Discard, "tok")
	EnsureServer(io.Discard, DefaultServer{
		Name: "obsidian", Transport: "stdio", Command: "/bin/obs", Auth: AuthLocal,
	})

	rr := httptest.NewRecorder()
	HandleList(rr, httptest.NewRequest(http.MethodGet, "/api/mcp/servers", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	var body struct {
		Servers []struct {
			Name    string `json:"name"`
			Auth    string `json:"auth"`
			Enabled bool   `json:"enabled"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rr.Body.String(), err)
	}
	if len(body.Servers) < len(DefaultServers) {
		t.Fatalf("got %d servers, want >= %d", len(body.Servers), len(DefaultServers))
	}

	seen := map[string]string{}
	for _, s := range body.Servers {
		if s.Enabled {
			t.Errorf("%q is enabled on a fresh install", s.Name)
		}
		if s.Auth == "" {
			t.Errorf("%q reached the UI with no auth classification", s.Name)
		}
		seen[s.Name] = s.Auth
	}
	// The two classifications the badges depend on must round-trip.
	if seen["obsidian"] != string(AuthLocal) {
		t.Errorf("obsidian auth = %q, want %q", seen["obsidian"], AuthLocal)
	}
	if seen["microsoft-learn"] != string(AuthNone) {
		t.Errorf("microsoft-learn auth = %q, want %q", seen["microsoft-learn"], AuthNone)
	}
}
