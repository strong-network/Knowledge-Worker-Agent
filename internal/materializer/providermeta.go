// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package materializer

import "sort"

// ProviderMetaFile is the sidecar the materializer writes next to opencode.json
// carrying Knowledge Worker Agent's own half of each assigned provider artifact.
//
// A provider artifact holds two blocks: a native "provider" block, written
// verbatim into opencode.json, and a "kwa" block that is meaningful only to
// Knowledge Worker Agent (who supplies the key, which models back the Default/Thinking
// presets). The latter is not part of opencode's schema, so it is stripped out
// here rather than merged — the same split already used for
// mcp-default-on.json.
const ProviderMetaFile = "provider-meta.json"

// ProviderMeta is Knowledge Worker Agent's own metadata for one config-declared provider.
type ProviderMeta struct {
	// ID is the provider id, taken from the key of the artifact's "provider"
	// block. It is the same id used in opencode.json and in the credential
	// store, so the two cannot drift.
	ID string `json:"id"`
	// Auth describes who supplies the key: type ("user-key" | "managed"), an
	// optional secretRef for managed providers, and UI label/help text.
	Auth ProviderAuthMeta `json:"auth"`
	// Presets nominates the models backing the Default and Thinking presets.
	Presets map[string]string `json:"presets,omitempty"`
}

// ProviderAuthMeta is the kwa.auth block of a provider artifact.
type ProviderAuthMeta struct {
	// Type is "user-key" (the user enters their own key) or "managed" (the
	// platform provisions it). Empty means unspecified; readers default to
	// "user-key" since that is the only type a user can act on.
	Type string `json:"type,omitempty"`
	// SecretRef is the file path or env var a managed key is provisioned to.
	// It duplicates what is inside options.apiKey so readiness can be checked
	// without parsing opencode's {file:}/{env:} placeholder syntax. Its
	// contents are never read — only its presence.
	SecretRef string `json:"secretRef,omitempty"`
	// Label and Help are UI strings for the Accounts row.
	Label string `json:"label,omitempty"`
	Help  string `json:"help,omitempty"`
}

// providerMetaFile is the on-disk shape of the sidecar. It is an ordered array
// rather than a map keyed by id because preset conflicts between several
// assigned providers break on assignment order, and Go maps do not preserve it.
type providerMetaFile struct {
	Providers []ProviderMeta `json:"providers"`
}

// takeProviderMeta removes the "kwa" block from a provider fragment (mutating
// it, so the caller merges a schema-pure fragment into opencode.json) and
// returns one ProviderMeta per provider id the fragment declares.
//
// The kwa block is per-artifact while the provider block is a map, so an
// artifact declaring several providers applies the same kwa block to each. That
// is a degenerate case — the documented shape is one provider per artifact —
// but it must be deterministic, so ids are emitted in sorted order.
func takeProviderMeta(frag map[string]any) []ProviderMeta {
	raw := takeMetaBlock(frag)

	ids := providerIDs(frag)
	if len(ids) == 0 {
		return nil
	}

	auth, presets := parseMetaBlock(raw)
	out := make([]ProviderMeta, 0, len(ids))
	for _, id := range ids {
		out = append(out, ProviderMeta{ID: id, Auth: auth, Presets: presets})
	}
	return out
}

// takeMetaBlock removes an artifact's block for Knowledge Worker Agent and
// returns it: "kwa", or else "sds", its name before the rename, which existing
// configuration repositories use. Both are stripped unconditionally: neither is
// part of opencode's schema, and they must never reach opencode.json.
func takeMetaBlock(frag map[string]any) any {
	kwa, hasKWA := frag["kwa"]
	sds := frag["sds"]
	delete(frag, "kwa")
	delete(frag, "sds")
	if hasKWA {
		return kwa
	}
	return sds
}

// providerIDs returns the sorted keys of a fragment's "provider" block.
func providerIDs(frag map[string]any) []string {
	block, _ := frag["provider"].(map[string]any)
	if len(block) == 0 {
		return nil
	}
	ids := make([]string, 0, len(block))
	for id := range block {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// parseMetaBlock extracts the auth and presets sub-blocks, tolerating anything
// malformed by returning zero values. A provider whose artifact carries a
// broken kwa block still works — it just falls back to the default auth type
// and the built-in preset heuristic, which is strictly better than failing the
// whole materialization for a metadata typo.
func parseMetaBlock(raw any) (ProviderAuthMeta, map[string]string) {
	block, _ := raw.(map[string]any)
	if block == nil {
		return ProviderAuthMeta{}, nil
	}

	var auth ProviderAuthMeta
	if a, ok := block["auth"].(map[string]any); ok {
		auth.Type, _ = a["type"].(string)
		auth.SecretRef, _ = a["secretRef"].(string)
		auth.Label, _ = a["label"].(string)
		auth.Help, _ = a["help"].(string)
	}

	var presets map[string]string
	if p, ok := block["presets"].(map[string]any); ok {
		for k, v := range p {
			if s, ok := v.(string); ok && s != "" {
				if presets == nil {
					presets = map[string]string{}
				}
				presets[k] = s
			}
		}
	}
	return auth, presets
}
