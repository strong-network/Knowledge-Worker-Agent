// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package providers is the single source of truth for which model providers a
// workspace offers, who authenticates them, and which models back the Default
// and Thinking presets.
//
// Before this registry, provider support was hard-coded in five independent places:
// which providers are queried for models, which the user can authenticate,
// which provider backs the server default, what the presets mean, and whether
// the assistant counts as connected at all. Adding a provider meant editing all
// five and shipping a release. This package replaces those decisions with one
// list that either comes from code (the built-ins) or from the central config
// repo, depending on the workspace's central-management switch.
//
// Which world the workspace is in is decided by config.CentralConfigEnabled:
//
// Off (the default, and every workspace that exists today): the built-in
// providers — GitHub Copilot and Google Vertex AI — exactly as before. A
// provider artifact assigned by the config repo is still materialized into
// opencode.json, but Knowledge Worker Agent does not surface it.
//
// On: the config repo is authoritative and the built-ins are not offered.
//
// The two modes are deliberately exclusive. Offering both would multiply the
// preset, cost-reporting and fallback cases without anyone asking for it, and
// leaves no sane rule for which provider's Default nomination wins.
package providers

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// AuthType describes who supplies a provider's credential and therefore what
// the Accounts row can offer the user.
type AuthType string

const (
	// AuthOAuthDevice is the GitHub Copilot device flow (built-in).
	AuthOAuthDevice AuthType = "oauth-device"
	// AuthGcloudADC is Google Vertex Application Default Credentials (built-in).
	AuthGcloudADC AuthType = "gcloud-adc"
	// AuthUserKey means the user pastes their own API key, which is stored in
	// opencode's credential store.
	AuthUserKey AuthType = "user-key"
	// AuthManaged means the platform provisions the key out of band. The row is
	// read-only: there is nothing for the user to do but wait.
	AuthManaged AuthType = "managed"
)

// Unauthenticated reason codes. These exist so the UI can tell the user
// something they can act on: "sign in" is useless advice for a managed provider
// whose secret an administrator has not provisioned yet.
const (
	// ReasonNotSignedIn — the user has not completed the provider's login flow.
	ReasonNotSignedIn = "not-signed-in"
	// ReasonNoKey — a user-key provider with no key stored yet.
	ReasonNoKey = "no-key"
	// ReasonSecretNotProvisioned — a managed provider whose secretRef does not
	// resolve. The administrator has not finished setting it up.
	ReasonSecretNotProvisioned = "secret-not-provisioned"
)

// Provider is one entry in the registry, as served to the browser.
type Provider struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	AuthType      AuthType `json:"authType"`
	Authenticated bool     `json:"authenticated"`
	// Reason is set only when Authenticated is false.
	Reason string `json:"reason,omitempty"`
	// Builtin distinguishes the code-registered providers from config-declared
	// ones, so the UI can keep the existing bespoke login flows for the former.
	Builtin bool `json:"builtin"`
	// Help is admin-authored text shown on the Accounts row.
	Help string `json:"help,omitempty"`
	// HelpURL is an optional admin-authored link rendered alongside Help, for
	// the vendor's own "how to get a key" page. Sanitized to https:// — it comes
	// from the config repo and ends up in an href.
	HelpURL string `json:"helpUrl,omitempty"`
	// Reachability reports whether the provider actually answered when last
	// probed. Independent of Authenticated: valid credentials and a blocked
	// network path look identical to an auth check.
	Reachability Reach `json:"reachability,omitempty"`
	// ReachabilityNote carries the provider's own failure message, which is the
	// only part of this a user can act on.
	ReachabilityNote string `json:"reachabilityNote,omitempty"`
	// ReachabilityCheckedAt is RFC 3339, empty when never probed.
	ReachabilityCheckedAt string `json:"reachabilityCheckedAt,omitempty"`
	// CanSignOut reports whether this workspace can discard the provider's
	// credentials itself.
	CanSignOut bool `json:"canSignOut,omitempty"`
	// Presets nominates the models backing Default/Thinking, by preset name.
	Presets map[string]string `json:"presets,omitempty"`
}

// withReach stamps the cached probe verdict onto a provider record.
func withReach(p Provider) Provider {
	r := reachFor(p.ID)
	p.Reachability, p.ReachabilityNote = r.state, r.note
	if !r.checkedAt.IsZero() {
		p.ReachabilityCheckedAt = r.checkedAt.UTC().Format(time.RFC3339)
	}
	p.CanSignOut = CanSignOut(p)
	return p
}

// Mode reports which world the registry is describing.
type Mode string

const (
	// ModeBuiltin — the built-in providers are offered.
	ModeBuiltin Mode = "builtin"
	// ModeCentral — config-declared providers are offered.
	ModeCentral Mode = "central"
	// ModeFallback — central models were requested but nothing usable
	// resolved, so the built-ins are offered to keep the workspace working.
	// Distinct from ModeBuiltin so the UI can say why.
	ModeFallback Mode = "fallback"
)

// Registry is the resolved provider set for the workspace.
type Registry struct {
	Mode      Mode       `json:"mode"`
	Providers []Provider `json:"providers"`
	// FallbackReason explains a ModeFallback registry.
	FallbackReason string `json:"fallbackReason,omitempty"`
}

// Authenticated returns the ids of providers we hold credentials for, in
// registry order. Holding a credential is not the same as being able to reach
// the provider — see UsableProviders for the set that may contribute models.
func (r Registry) Authenticated() []string {
	out := make([]string, 0, len(r.Providers))
	for _, p := range r.Providers {
		if p.Authenticated {
			out = append(out, p.ID)
		}
	}
	return out
}

// AnyAuthenticated reports whether at least one provider is usable. It backs
// the sign-in banner, which previously tested the two built-in auth stores
// directly and so showed "not connected" for a workspace with a perfectly
// healthy config-declared provider.
func (r Registry) AnyAuthenticated() bool {
	return len(r.Authenticated()) > 0
}

// Preset returns the model nominated for a preset ("default", "thinking") by
// the first authenticated provider that nominates one, together with whether
// any nomination was found.
//
// First-wins on registry order, which for config-declared providers is the
// order of the project's `provider` array in assignment.jsonc — something an
// admin can see and reorder in the file they are already editing. Resolution is
// independent per preset, so a provider nominating only "default" does not
// suppress another's "thinking".
//
// isAvailable filters nominations against the models actually discovered: a
// nomination naming a model that is not there is skipped, not fatal, so a typo
// or a retired model degrades to the next provider (and ultimately to the
// built-in heuristic) instead of leaving the preset resolving to nothing.
func (r Registry) Preset(name string, isAvailable func(string) bool) (string, bool) {
	for _, p := range r.Providers {
		if !p.Authenticated {
			continue
		}
		m := strings.TrimSpace(p.Presets[name])
		if m == "" {
			continue
		}
		if isAvailable != nil && !isAvailable(m) {
			continue
		}
		return m, true
	}
	return "", false
}

// Builtin describes a provider registered in code, with its bespoke auth flow.
type Builtin struct {
	ID       string
	Label    string
	AuthType AuthType
	// CheckAuth reports whether the user is signed in. It is a func so this
	// package does not import the auth packages (which would be a cycle) and so
	// tests can substitute it.
	CheckAuth func() bool
	// CurateModels optionally narrows the provider's discovered catalogue.
	//
	// `opencode models <provider>` returns the vendor's full upstream list,
	// which for a built-in can be wider than what we actually serve. Attaching
	// the filter to the provider keeps it out of the shared discovery loop,
	// which otherwise needs an `if provider == …` per provider — the exact
	// hard-coding this package exists to remove.
	//
	// Config-declared providers do not need this: opencode's ProviderConfig has
	// native `whitelist`/`blacklist` arrays, and the materializer already merges
	// a provider artifact's block verbatim, so an admin curates in the artifact
	// with no code involved.
	CurateModels func([]string) []string
}

var (
	builtinsMu sync.RWMutex
	builtins   []Builtin
)

// RegisterBuiltin adds a code-registered provider. Called once per built-in at
// startup, in the order they should appear. Re-registering the same id replaces
// the entry rather than duplicating it, so bootstrap is idempotent.
func RegisterBuiltin(b Builtin) {
	builtinsMu.Lock()
	defer builtinsMu.Unlock()
	for i, existing := range builtins {
		if strings.EqualFold(existing.ID, b.ID) {
			builtins[i] = b
			return
		}
	}
	builtins = append(builtins, b)
}

// ResetBuiltins clears the registered built-ins. For tests.
func ResetBuiltins() {
	builtinsMu.Lock()
	builtins = nil
	builtinsMu.Unlock()
}

// CurateModels applies the provider's catalogue filter, if it declared one.
// Providers without a filter — which is every config-declared one — get their
// list back untouched.
func CurateModels(providerID string, list []string) []string {
	if b, ok := builtinByID(providerID); ok && b.CurateModels != nil {
		return b.CurateModels(list)
	}
	return list
}

// builtinByID returns the built-in registered under an id, if any.
//
// Registration is independent of which providers are OFFERED: the built-ins are
// registered at startup in both modes, so this answers "does code own an auth
// flow for this id" even in central-models mode, where the built-ins are not
// offered in their own right. That is what lets a config-declared provider
// naming a built-in id reuse its sign-in flow (see providerFromMeta).
func builtinByID(id string) (Builtin, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Builtin{}, false
	}
	builtinsMu.RLock()
	defer builtinsMu.RUnlock()
	for _, b := range builtins {
		if strings.EqualFold(b.ID, id) {
			return b, true
		}
	}
	return Builtin{}, false
}

// builtinRegistry resolves the code-registered providers, probing each one's
// auth state.
func builtinRegistry(mode Mode, fallbackReason string) Registry {
	builtinsMu.RLock()
	list := make([]Builtin, len(builtins))
	copy(list, builtins)
	builtinsMu.RUnlock()

	out := make([]Provider, 0, len(list))
	for _, b := range list {
		ok := b.CheckAuth != nil && b.CheckAuth()
		p := Provider{
			ID:            b.ID,
			Label:         b.Label,
			AuthType:      b.AuthType,
			Authenticated: ok,
			Builtin:       true,
		}
		if !ok {
			p.Reason = ReasonNotSignedIn
		}
		out = append(out, p)
	}
	return Registry{Mode: mode, Providers: out, FallbackReason: fallbackReason}
}

// sortedIDs returns map keys in a deterministic order.
func sortedIDs(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortStrings sorts in place. Wrapped so central.go does not need the import.
func sortStrings(s []string) { sort.Strings(s) }
