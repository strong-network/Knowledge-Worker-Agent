// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package providers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/env/envtest"
)

// centralDir points the registry at a temp platform config dir holding the
// given opencode.json and provider-meta.json contents. Either may be "" to
// leave that file absent.
func centralDir(t *testing.T, opencodeJSON, providerMeta string) string {
	t.Helper()
	dir := t.TempDir()
	if opencodeJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(opencodeJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if providerMeta != "" {
		if err := os.WriteFile(filepath.Join(dir, "provider-meta.json"), []byte(providerMeta), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prev := configDirFn
	configDirFn = func() string { return dir }
	t.Cleanup(func() { configDirFn = prev })
	return dir
}

// withCredentials scripts which provider ids have a stored key.
func withCredentials(t *testing.T, have ...string) {
	t.Helper()
	set := map[string]bool{}
	for _, id := range have {
		set[id] = true
	}
	prev := credentialFn
	credentialFn = func(id string) bool { return set[id] }
	t.Cleanup(func() { credentialFn = prev })
}

// withBuiltins installs stub built-ins for the duration of a test.
func withBuiltins(t *testing.T, ids ...string) {
	t.Helper()
	ResetBuiltins()
	t.Cleanup(ResetBuiltins)
	for _, id := range ids {
		RegisterBuiltin(stub(id, true))
	}
}

// TestResolveDefaultsToBuiltins is the regression guard that matters most: with
// the switch off, a workspace behaves exactly as it did before configurable providers — even
// when a provider artifact has been assigned and materialized.
func TestResolveDefaultsToBuiltins(t *testing.T) {
	envtest.Clear(t, config.EnvCentralConfig)
	withBuiltins(t, "github-copilot", "google-vertex")
	centralDir(t, `{"provider":{"mistral":{}}}`, `{"providers":[{"id":"mistral"}]}`)

	reg := Resolve()
	if reg.Mode != ModeBuiltin {
		t.Fatalf("mode = %q, want %q", reg.Mode, ModeBuiltin)
	}
	if len(reg.Providers) != 2 {
		t.Fatalf("got %d providers, want the 2 built-ins: %+v", len(reg.Providers), reg.Providers)
	}
	for _, p := range reg.Providers {
		if p.ID == "mistral" {
			t.Error("config-declared provider leaked into built-in mode")
		}
	}
}

func TestResolveCentralMode(t *testing.T) {
	t.Setenv(config.EnvCentralConfig, "1")
	withBuiltins(t, "github-copilot")
	withCredentials(t, "mistral")
	centralDir(t,
		`{"provider":{"mistral":{},"acme":{}}}`,
		`{"providers":[
			{"id":"mistral","auth":{"type":"user-key","label":"Mistral","help":"Paste your key"},"presets":{"default":"mistral/small"}},
			{"id":"acme","auth":{"type":"user-key"}}
		]}`)

	reg := Resolve()
	if reg.Mode != ModeCentral {
		t.Fatalf("mode = %q, want %q", reg.Mode, ModeCentral)
	}
	if len(reg.Providers) != 2 {
		t.Fatalf("got %d providers, want 2: %+v", len(reg.Providers), reg.Providers)
	}
	// Sidecar order is assignment order and decides preset conflicts.
	if reg.Providers[0].ID != "mistral" || reg.Providers[1].ID != "acme" {
		t.Errorf("sidecar order not preserved: %+v", reg.Providers)
	}
	m := reg.Providers[0]
	if !m.Authenticated || m.Reason != "" {
		t.Errorf("mistral: authenticated=%v reason=%q, want true/\"\"", m.Authenticated, m.Reason)
	}
	if m.Label != "Mistral" || m.Help != "Paste your key" {
		t.Errorf("admin label/help not surfaced: %+v", m)
	}
	if m.Builtin {
		t.Error("config-declared provider marked as built-in")
	}
	a := reg.Providers[1]
	if a.Authenticated || a.Reason != ReasonNoKey {
		t.Errorf("acme: authenticated=%v reason=%q, want false/%q", a.Authenticated, a.Reason, ReasonNoKey)
	}
	// No kwa.auth.label: fall back to the id so the row is never blank.
	if a.Label != "acme" {
		t.Errorf("acme label = %q, want the id", a.Label)
	}
}

func TestResolveFallsBackWhenNothingResolves(t *testing.T) {
	// Central models requested but nothing usable: falling back keeps the
	// workspace chattable, and ModeFallback is what lets the server say so.
	cases := []struct {
		name         string
		opencodeJSON string
		meta         string
	}{
		{"no materialized config", "", ""},
		{"no provider block", `{"agent":{}}`, ""},
		{"empty provider block", `{"provider":{}}`, ""},
		{"unparseable config", `{ not json`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(config.EnvCentralConfig, "on")
			withBuiltins(t, "github-copilot")
			centralDir(t, tc.opencodeJSON, tc.meta)

			reg := Resolve()
			if reg.Mode != ModeFallback {
				t.Fatalf("mode = %q, want %q", reg.Mode, ModeFallback)
			}
			if reg.FallbackReason == "" {
				t.Error("no fallback reason given — the operator cannot tell why")
			}
			if len(reg.Providers) != 1 || reg.Providers[0].ID != "github-copilot" {
				t.Errorf("built-ins not restored: %+v", reg.Providers)
			}
		})
	}
}

// The sidecar is metadata. Without it the providers in opencode.json are still
// offered — just unlabelled and with no preset nominations — rather than the
// workspace losing every provider it was assigned.
func TestCentralRegistryToleratesMissingOrBrokenSidecar(t *testing.T) {
	for _, meta := range []string{"", `{ not json`, `{"providers":[{"id":"  "}]}`} {
		t.Setenv(config.EnvCentralConfig, "true")
		withCredentials(t)
		centralDir(t, `{"provider":{"zeta":{},"alpha":{}}}`, meta)

		reg := Resolve()
		if reg.Mode != ModeCentral {
			t.Fatalf("meta %q: mode = %q, want %q", meta, reg.Mode, ModeCentral)
		}
		if len(reg.Providers) != 2 {
			t.Fatalf("meta %q: got %+v, want both providers", meta, reg.Providers)
		}
		// Without sidecar order there is none to honour, so fall back to a
		// stable one rather than Go's randomised map iteration.
		if reg.Providers[0].ID != "alpha" || reg.Providers[1].ID != "zeta" {
			t.Errorf("meta %q: unlisted providers not in stable order: %+v", meta, reg.Providers)
		}
		// Unspecified auth type must be the one the user can act on.
		if reg.Providers[0].AuthType != AuthUserKey {
			t.Errorf("meta %q: authType = %q, want %q", meta, reg.Providers[0].AuthType, AuthUserKey)
		}
	}
}

func TestCentralRegistryIgnoresMetaForUndeclaredProvider(t *testing.T) {
	// Metadata for a provider that is not in opencode.json means the artifact
	// was denied or removed. Offering it would show a sign-in row for something
	// no model could ever come from.
	t.Setenv(config.EnvCentralConfig, "1")
	withCredentials(t, "ghost")
	centralDir(t, `{"provider":{"real":{}}}`,
		`{"providers":[{"id":"ghost"},{"id":"real"}]}`)

	reg := Resolve()
	if len(reg.Providers) != 1 || reg.Providers[0].ID != "real" {
		t.Fatalf("got %+v, want only real", reg.Providers)
	}
}

func TestCentralRegistryAppendsDeclaredProviderWithoutMeta(t *testing.T) {
	t.Setenv(config.EnvCentralConfig, "1")
	withCredentials(t)
	centralDir(t, `{"provider":{"listed":{},"unlisted":{}}}`,
		`{"providers":[{"id":"listed"}]}`)

	reg := Resolve()
	if len(reg.Providers) != 2 {
		t.Fatalf("got %+v, want both", reg.Providers)
	}
	if reg.Providers[0].ID != "listed" || reg.Providers[1].ID != "unlisted" {
		t.Errorf("got %+v, want listed then unlisted", reg.Providers)
	}
}

func TestManagedProviderReadiness(t *testing.T) {
	t.Setenv(config.EnvCentralConfig, "1")
	withCredentials(t) // no user keys anywhere

	secretDir := t.TempDir()
	present := filepath.Join(secretDir, "key")
	if err := os.WriteFile(present, []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(secretDir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	meta := providerMetaDoc{Providers: []providerMetaEntry{
		metaEntry("ready", "managed", present),
		metaEntry("pending", "managed", filepath.Join(secretDir, "absent")),
		metaEntry("husk", "managed", empty),
		metaEntry("noref", "managed", ""),
	}}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	centralDir(t, `{"provider":{"ready":{},"pending":{},"husk":{},"noref":{}}}`, string(raw))

	reg := Resolve()
	want := map[string]bool{"ready": true, "pending": false, "husk": false, "noref": false}
	for _, p := range reg.Providers {
		if p.AuthType != AuthManaged {
			t.Errorf("%s: authType = %q, want %q", p.ID, p.AuthType, AuthManaged)
		}
		if p.Authenticated != want[p.ID] {
			t.Errorf("%s: authenticated = %v, want %v", p.ID, p.Authenticated, want[p.ID])
		}
		if !p.Authenticated && p.Reason != ReasonSecretNotProvisioned {
			t.Errorf("%s: reason = %q, want %q", p.ID, p.Reason, ReasonSecretNotProvisioned)
		}
	}
}

func metaEntry(id, authType, secretRef string) providerMetaEntry {
	var e providerMetaEntry
	e.ID = id
	e.Auth.Type = authType
	e.Auth.SecretRef = secretRef
	return e
}

func TestSecretReadyEnvVar(t *testing.T) {
	// A bare name is an env var; anything path-like is a file. Only presence is
	// ever checked — the value must not be read, logged or returned.
	t.Setenv("SDS_TEST_SECRET", "value")
	t.Setenv("SDS_TEST_BLANK", "   ")
	if !secretReady("SDS_TEST_SECRET") {
		t.Error("populated env var reported not ready")
	}
	if secretReady("SDS_TEST_BLANK") {
		t.Error("blank env var reported ready")
	}
	if secretReady("SDS_TEST_UNSET_XYZ") {
		t.Error("unset env var reported ready")
	}
	if secretReady("") || secretReady("   ") {
		t.Error("empty secretRef reported ready")
	}
	if secretReady(t.TempDir()) {
		t.Error("a directory reported ready")
	}
}

func TestPlatformConfigDirPrefersEnv(t *testing.T) {
	// Must be OPENCODE_CONFIG_DIR (the materializer's target), never the user's
	// Global — a config-declared provider only exists in the platform dir.
	t.Setenv("OPENCODE_CONFIG_DIR", "/tmp/platform-cfg")
	if got := platformConfigDir(); got != "/tmp/platform-cfg" {
		t.Fatalf("got %q, want /tmp/platform-cfg", got)
	}
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	if got := platformConfigDir(); got != filepath.Join("/tmp/xdg", "opencode-platform") {
		t.Fatalf("got %q, want the opencode-platform sibling", got)
	}
}

// A config-declared provider whose id matches a built-in must be offered with
// the built-in's own sign-in flow. Without this a workspace in central-models
// mode could see GitHub Copilot and Vertex in Accounts but had no way to sign
// in to either: the Connect button is gated on the builtin flag, so the row
// offered an API-key field instead — and a pasted string is not the OAuth token
// or the Google credential those providers actually need.
func TestCentralModeUsesTheBuiltinsOwnAuthFlow(t *testing.T) {
	t.Setenv(config.EnvCentralConfig, "1")
	ResetBuiltins()
	t.Cleanup(ResetBuiltins)
	RegisterBuiltin(Builtin{
		ID: "github-copilot", Label: "GitHub Copilot",
		AuthType: AuthOAuthDevice, CheckAuth: func() bool { return true },
	})
	// No stored key for anyone: the built-in's own probe is what decides, not
	// the credential store the user-key path would have consulted.
	withCredentials(t)
	centralDir(t,
		`{"provider":{"github-copilot":{}}}`,
		`{"providers":[{"id":"github-copilot","auth":{"label":"GitHub Copilot"},`+
			`"presets":{"default":"github-copilot/claude-sonnet-5"}}]}`)

	reg := Resolve()
	if reg.Mode != ModeCentral {
		t.Fatalf("mode = %s, want central", reg.Mode)
	}
	p := reg.Providers[0]
	if !p.Builtin {
		t.Error("not marked builtin — the Accounts row will render no Connect button")
	}
	if p.AuthType != AuthOAuthDevice {
		t.Errorf("authType = %q, want the built-in's own flow", p.AuthType)
	}
	if !p.Authenticated {
		t.Error("not authenticated — the built-in's probe said it was")
	}
	if p.Presets["default"] == "" {
		t.Error("preset nomination lost: the artifact still owns presets")
	}
}

// The artifact declares that a provider is offered and what backs the presets.
// Code owns how it is authenticated. An artifact cannot reclassify a built-in
// into a paste-a-key provider — doing so would render a key field, accept
// whatever was typed, store it, and fail on the first message with no clue why.
func TestCentralModeIgnoresAnArtifactsAuthTypeForABuiltin(t *testing.T) {
	t.Setenv(config.EnvCentralConfig, "1")
	ResetBuiltins()
	t.Cleanup(ResetBuiltins)
	RegisterBuiltin(Builtin{
		ID: "google-vertex", Label: "Google Cloud (Vertex AI)",
		AuthType: AuthGcloudADC, CheckAuth: func() bool { return false },
	})
	withCredentials(t, "google-vertex")
	centralDir(t,
		`{"provider":{"google-vertex":{}}}`,
		`{"providers":[{"id":"google-vertex","auth":{"type":"user-key","help":"h"}}]}`)

	p := Resolve().Providers[0]
	if p.AuthType != AuthGcloudADC {
		t.Errorf("authType = %q — the artifact reclassified a built-in", p.AuthType)
	}
	// A stored credential must not stand in for the built-in's probe either:
	// Vertex is not signed in, and saying otherwise would move the failure to
	// the user's first message.
	if p.Authenticated {
		t.Error("authenticated via the credential store, bypassing the built-in probe")
	}
	if p.Reason != ReasonNotSignedIn {
		t.Errorf("reason = %q, want %q", p.Reason, ReasonNotSignedIn)
	}
	// Admin-authored UI text is still honoured — it cannot break sign-in.
	if p.Help != "h" {
		t.Errorf("help = %q, want the artifact's text", p.Help)
	}
	// The built-in's label stands in when the artifact names none.
	if p.Label != "Google Cloud (Vertex AI)" {
		t.Errorf("label = %q, want the built-in's", p.Label)
	}
}

// A built-in-backed provider must not accept a pasted key, so the key
// endpoints reject it. This falls out of the builtin flag, which is exactly
// why the flag is the thing being set rather than a bespoke bypass.
func TestBuiltinBackedProviderRejectsKeyWrites(t *testing.T) {
	p := Provider{ID: "github-copilot", AuthType: AuthOAuthDevice, Builtin: true}
	if guardUserKey(p) == "" {
		t.Error("the key endpoints would write a credential for an OAuth provider")
	}
}

// A genuinely config-declared provider — one code knows nothing about — still
// takes the user-key path. This is the case the built-in shortcut must not
// swallow.
func TestCentralModeKeepsUserKeyForUnknownProviders(t *testing.T) {
	t.Setenv(config.EnvCentralConfig, "1")
	withBuiltins(t, "github-copilot")
	withCredentials(t, "mistral-onprem")
	centralDir(t,
		`{"provider":{"mistral-onprem":{}}}`,
		`{"providers":[{"id":"mistral-onprem","auth":{"type":"user-key"}}]}`)

	p := Resolve().Providers[0]
	if p.Builtin {
		t.Error("a config-declared provider was marked builtin")
	}
	if p.AuthType != AuthUserKey || !p.Authenticated {
		t.Errorf("got authType=%q authenticated=%v, want user-key/true", p.AuthType, p.Authenticated)
	}
}

// An admin-authored help link is trusted enough to display, but not trusted
// enough to be an arbitrary URL scheme: the value lands in an href, so anything
// but https is dropped rather than rendered.
func TestHelpURLKeepsHTTPSAndDropsEverythingElse(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"https is kept", "https://docs.mistral.ai/admin/identity-access/api-keys", "https://docs.mistral.ai/admin/identity-access/api-keys"},
		{"whitespace is trimmed", "  https://example.com/keys  ", "https://example.com/keys"},
		{"javascript is dropped", "javascript:alert(1)", ""},
		{"data is dropped", "data:text/html,<script>alert(1)</script>", ""},
		{"plain http is dropped", "http://example.com/keys", ""},
		{"a bare path is dropped", "/admin/keys", ""},
		{"empty stays empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeHelpURL(tc.in); got != tc.want {
				t.Errorf("safeHelpURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The link travels the same path as the help text, for both kinds of provider:
// one code knows nothing about, and one backed by a built-in.
func TestCentralModeSurfacesTheHelpURL(t *testing.T) {
	t.Setenv(config.EnvCentralConfig, "1")
	withBuiltins(t, "github-copilot")
	withCredentials(t, "mistral")
	centralDir(t,
		`{"provider":{"mistral":{},"github-copilot":{}}}`,
		`{"providers":[
			{"id":"mistral","auth":{"type":"user-key","helpUrl":"https://docs.mistral.ai/admin/identity-access/api-keys"}},
			{"id":"github-copilot","auth":{"helpUrl":"https://example.com/copilot","label":"Copilot"}}
		]}`)

	byID := map[string]Provider{}
	for _, p := range Resolve().Providers {
		byID[p.ID] = p
	}
	if got := byID["mistral"].HelpURL; got != "https://docs.mistral.ai/admin/identity-access/api-keys" {
		t.Errorf("config-declared provider helpUrl = %q", got)
	}
	// A built-in-backed provider ignores the artifact's auth *type* but still
	// honours its UI text — the link is UI text.
	if got := byID["github-copilot"].HelpURL; got != "https://example.com/copilot" {
		t.Errorf("builtin-backed provider helpUrl = %q", got)
	}
}
