// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package providers

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// configDirFn resolves the platform-owned config dir. Overridable for tests.
var configDirFn = platformConfigDir

// credentialFn reports whether opencode holds a stored credential for a
// provider id. Injected at startup (see SetCredentialChecker) so this package
// does not import the auth packages.
var credentialFn func(string) bool

// SetCredentialChecker installs the function used to decide whether a user-key
// provider has a key stored. Called once at startup.
func SetCredentialChecker(fn func(string) bool) { credentialFn = fn }

// platformConfigDir returns the dir the materializer writes to.
//
// This is OPENCODE_CONFIG_DIR — NOT the user's Global (~/.config/opencode).
// The materializer owns the platform dir and explicitly refuses to write to the
// user's Global, so the platform dir is the only place a config-declared
// provider exists. (internal/mcp reads the user's Global instead; that is a
// known inconsistency on the MCP side, not a pattern to copy here.)
func platformConfigDir() string { return layout.PlatformConfig() }

// providerMetaEntry mirrors materializer.ProviderMeta. It is redeclared rather
// than imported to keep this package free of a dependency on the materializer:
// the sidecar is a file format, and reading it should not couple the reader to
// the writer's package.
type providerMetaEntry struct {
	ID   string `json:"id"`
	Auth struct {
		Type      string `json:"type"`
		SecretRef string `json:"secretRef"`
		Label     string `json:"label"`
		Help      string `json:"help"`
		HelpURL   string `json:"helpUrl"`
	} `json:"auth"`
	Presets map[string]string `json:"presets"`
}

type providerMetaDoc struct {
	Providers []providerMetaEntry `json:"providers"`
}

// Resolve returns the provider set for the workspace.
//
// With central management off it is the built-ins, unchanged. With it on it is
// whatever the config repo assigned — unless nothing usable resolved, in which
// case it falls back to the built-ins and says so.
func Resolve() Registry {
	return stampReach(resolveRegistry())
}

// stampReach adds the cached probe verdict to every provider, so a caller never
// has to remember to join the two. Kept out of the builders because both paths
// need it and a missed one silently reports every provider as unprobed.
func stampReach(reg Registry) Registry {
	for i, p := range reg.Providers {
		reg.Providers[i] = withReach(p)
	}
	return reg
}

func resolveRegistry() Registry {
	if !config.CentralConfigEnabled() {
		return builtinRegistry(ModeBuiltin, "")
	}

	reg, err := centralRegistry()
	if err != nil {
		return builtinRegistry(ModeFallback, err.Error())
	}
	if len(reg.Providers) == 0 {
		return builtinRegistry(ModeFallback, "no provider is assigned to this workspace")
	}
	return reg
}

// centralRegistry builds the registry from the materialized config.
func centralRegistry() (Registry, error) {
	dir := configDirFn()
	if dir == "" {
		return Registry{}, fmt.Errorf("cannot resolve the platform config dir")
	}

	declared, err := declaredProviderIDs(filepath.Join(dir, "opencode.json"))
	if err != nil {
		return Registry{}, err
	}

	meta := readProviderMeta(filepath.Join(dir, "provider-meta.json"))

	// Order comes from the sidecar (assignment order); any provider declared in
	// opencode.json without sidecar metadata is appended afterwards so it is
	// still offered rather than silently dropped.
	out := make([]Provider, 0, len(declared))
	used := map[string]bool{}
	for _, m := range meta {
		if !declared[m.ID] {
			// Metadata for a provider that is not in the config: the artifact
			// was denied or removed. Nothing to offer.
			continue
		}
		used[m.ID] = true
		out = append(out, providerFromMeta(m))
	}
	for _, id := range sortedKeys(declared) {
		if used[id] {
			continue
		}
		out = append(out, providerFromMeta(providerMetaEntry{ID: id}))
	}

	return Registry{Mode: ModeCentral, Providers: out}, nil
}

// providerFromMeta turns one sidecar entry into a registry entry, resolving its
// authentication state.
func providerFromMeta(m providerMetaEntry) Provider {
	// A provider that names a built-in id is that built-in, offered because the
	// config repo assigned it rather than because code hard-coded it. Its
	// sign-in flow lives in this binary, so use it.
	if b, ok := builtinByID(m.ID); ok {
		return builtinBackedProvider(b, m)
	}

	label := strings.TrimSpace(m.Auth.Label)
	if label == "" {
		label = m.ID
	}
	p := Provider{
		ID:       m.ID,
		Label:    label,
		AuthType: AuthType(strings.TrimSpace(m.Auth.Type)),
		Help:     strings.TrimSpace(m.Auth.Help),
		HelpURL:  safeHelpURL(m.Auth.HelpURL),
		Presets:  m.Presets,
	}
	// An artifact that omits kwa.auth.type gets the type the user can act on.
	if p.AuthType != AuthManaged {
		p.AuthType = AuthUserKey
	}

	if p.AuthType == AuthManaged {
		// A managed provider is NOT authenticated by definition. The artifact
		// can name a secret path before the platform has placed the file there
		// — artifact and secret are provisioned by different systems and will
		// drift. Treating it as ready would turn that into an opaque failure on
		// the user's first message.
		if secretReady(m.Auth.SecretRef) {
			p.Authenticated = true
		} else {
			p.Reason = ReasonSecretNotProvisioned
		}
		return p
	}

	if credentialFn != nil && credentialFn(m.ID) {
		p.Authenticated = true
	} else {
		p.Reason = ReasonNoKey
	}
	return p
}

// builtinBackedProvider resolves a config-declared provider whose id matches a
// registered built-in, using the built-in's own auth type and probe.
//
// The division is: an artifact declares that a provider is OFFERED and which
// models back the presets; code owns HOW it is authenticated. So the artifact's
// kwa.auth.type is deliberately ignored here, and cannot be used to reclassify
// a built-in. That is not a limitation, it is the point — GitHub Copilot needs
// an OAuth token and Vertex needs Google ADC, and honouring an artifact that
// called either "user-key" would render a key field, accept whatever was pasted
// into it, store it, and fail on the user's first message with no clue why.
//
// Marking the result Builtin also does two things for free: the Accounts modal
// renders the provider's real Connect button (it keys the login modals off the
// flag plus the id), and the key endpoints refuse to write a credential for it
// (see guardUserKey). Without this, a workspace in central-models mode could
// see its providers but had no way to sign in to any of them.
//
// The artifact's label and help are still honoured — they are UI text an admin
// legitimately owns, and neither can break authentication.
func builtinBackedProvider(b Builtin, m providerMetaEntry) Provider {
	label := strings.TrimSpace(m.Auth.Label)
	if label == "" {
		label = b.Label
	}
	ok := b.CheckAuth != nil && b.CheckAuth()
	p := Provider{
		ID:            b.ID,
		Label:         label,
		AuthType:      b.AuthType,
		Authenticated: ok,
		Builtin:       true,
		Help:          strings.TrimSpace(m.Auth.Help),
		HelpURL:       safeHelpURL(m.Auth.HelpURL),
		Presets:       m.Presets,
	}
	if !ok {
		p.Reason = ReasonNotSignedIn
	}
	return p
}

// safeHelpURL accepts an admin-authored documentation link only if it is an
// absolute https:// URL with a host.
//
// This value is written into an href in the browser. The config repo is a
// trusted source, but "trusted" is an assumption about intent, not about
// review: a mistyped or copy-pasted `javascript:` or `data:` URL would become
// script execution in the user's session. Restricting the scheme here costs
// nothing and removes the question entirely.
func safeHelpURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	return u.String()
}

// secretReady reports whether a managed provider's secret has been provisioned.
//
// Presence only: the contents are never read, logged or returned. A bare name
// is treated as an environment variable, anything path-like as a file.
func secretReady(ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	if !strings.ContainsAny(ref, "/\\") {
		return strings.TrimSpace(os.Getenv(ref)) != ""
	}
	info, err := os.Stat(ref)
	return err == nil && !info.IsDir() && info.Size() > 0
}

// declaredProviderIDs returns the ids in the materialized opencode.json's
// "provider" block.
func declaredProviderIDs(path string) (map[string]bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no materialized config at %s", filepath.Dir(path))
		}
		return nil, fmt.Errorf("read materialized config: %w", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse materialized config: %w", err)
	}
	block, _ := doc["provider"].(map[string]any)
	out := make(map[string]bool, len(block))
	for _, id := range sortedIDs(block) {
		if strings.TrimSpace(id) != "" {
			out[id] = true
		}
	}
	return out, nil
}

// readProviderMeta reads the sidecar, preserving order. A missing or malformed
// sidecar is not an error: the providers in opencode.json are still offered,
// just without admin-supplied labels or preset nominations.
func readProviderMeta(path string) []providerMetaEntry {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return nil
	}
	var doc providerMetaDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	out := make([]providerMetaEntry, 0, len(doc.Providers))
	for _, e := range doc.Providers {
		if strings.TrimSpace(e.ID) != "" {
			out = append(out, e)
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}
