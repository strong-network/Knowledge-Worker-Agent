// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package materializer

import (
	"fmt"
	"sort"
	"strings"
)

// MCPMetaFile is the sidecar the materializer writes next to opencode.json
// carrying Knowledge Worker Agent's own half of each assigned MCP artifact.
//
// An MCP artifact holds two blocks: a native "mcp" block, written verbatim into
// opencode.json, and a "kwa" block that is meaningful only to Knowledge Worker Agent (how
// the server is authenticated, and the UI text that tells a user how). The
// latter is not part of opencode's schema, so it is stripped out here rather
// than merged — the same split provider-meta.json uses.
//
// This sidecar is what lets a provisioned server stand alone: without it, auth
// classification could only come from the built-in catalogue, and a workspace
// with the catalogue switched off would render every provisioned server with no
// sign-in affordance at all.
const MCPMetaFile = "mcp-meta.json"

// MCPMeta is Knowledge Worker Agent's own metadata for one config-declared MCP server.
type MCPMeta struct {
	// Name is the server name, taken from the key of the artifact's "mcp"
	// block. It is the same name used in opencode.json and by
	// `opencode mcp auth`, so the two cannot drift.
	Name string `json:"name"`
	// Auth describes how the server is signed in to, and the UI text shown
	// alongside it.
	Auth MCPAuthMeta `json:"auth"`
}

// MCPAuthMeta is the kwa.auth block of an MCP artifact.
type MCPAuthMeta struct {
	// Type is one of the AuthKind values internal/mcp understands: "none",
	// "oauth", "setup" or "local". Empty means unclassified, and the reader
	// falls back to the built-in catalogue.
	Type string `json:"type,omitempty"`
	// Label and Help are UI strings for the MCP Servers modal row.
	Label string `json:"label,omitempty"`
	Help  string `json:"help,omitempty"`
	// HelpURL is an optional vendor documentation link. Sanitized to https://
	// by the reader — it comes from the config repo and ends up in an href.
	HelpURL string `json:"helpUrl,omitempty"`
	// Header and Scheme shape an "api-key" server's header: by default
	// "Authorization: Bearer <key>". An explicitly empty scheme sends the bare key.
	Header string  `json:"header,omitempty"`
	Scheme *string `json:"scheme,omitempty"`
}

// mcpKeyFile returns the file an "api-key" server's key is read from, created
// empty when missing. The key store is Knowledge Worker Agent's, so the caller supplies it.
var mcpKeyFile func(name string) (string, error)

// SetMCPKeyFile installs the key-file function used for "api-key" servers.
func SetMCPKeyFile(fn func(name string) (string, error)) { mcpKeyFile = fn }

// mcpMetaFile is the on-disk shape of the sidecar. It is an ordered array
// rather than a map keyed by name to match provider-meta.json, so the two
// sidecars read the same way.
type mcpMetaFile struct {
	Servers []MCPMeta `json:"servers"`
}

// takeMCPMeta removes the "kwa" block from an MCP fragment (mutating it, so the
// caller merges a schema-pure fragment into opencode.json) and returns one
// MCPMeta per server name the fragment declares.
//
// It also strips any "enabled" key from each server entry, returning a warning
// naming each one. That key must not reach the platform config: opencode's
// merge is per-key and the platform file wins, so an artifact declaring
// "enabled": true would override the user's own setting and turn the modal's
// toggle into a silent no-op, while "enabled": false would mean the user could
// never turn the server on. Either way the on/off switch stops belonging to the
// user, and the failure is invisible — the toggle appears to work.
//
// The key is stripped rather than failing the whole run. Materialization is
// all-or-nothing across every artifact kind, so rejecting the run would take
// down the project's agents, skills and context because of a metadata typo in
// one MCP fragment — a strictly worse outcome than the one being prevented, and
// a new way for the config repo to brick a workspace. Stripping removes the
// invisible failure, which is the actual requirement, and the warning tells the
// administrator their key was ignored.
func takeMCPMeta(artifactID string, frag map[string]any) ([]MCPMeta, []string) {
	raw := takeMetaBlock(frag)

	names := mcpNames(frag)
	if len(names) == 0 {
		return nil, nil
	}

	var warnings []string
	block, _ := frag["mcp"].(map[string]any)
	for _, name := range names {
		entry, ok := block[name].(map[string]any)
		if !ok {
			continue
		}
		if _, declared := entry["enabled"]; declared {
			delete(entry, "enabled")
			warnings = append(warnings, fmt.Sprintf(
				"mcp artifact %s declares \"enabled\" for server %q; ignoring it — "+
					"the platform config wins per key, so declaring it would take the "+
					"on/off switch away from the user",
				artifactID, name))
		}
	}

	auth := parseMCPMetaBlock(raw)
	if strings.EqualFold(strings.TrimSpace(auth.Type), "api-key") {
		for _, name := range names {
			entry, _ := block[name].(map[string]any)
			warnings = append(warnings, wireAPIKey(artifactID, name, entry, auth)...)
		}
	}
	out := make([]MCPMeta, 0, len(names))
	for _, name := range names {
		out = append(out, MCPMeta{Name: name, Auth: auth})
	}
	return out, warnings
}

// mcpNames returns the sorted keys of a fragment's "mcp" block.
func mcpNames(frag map[string]any) []string {
	block, _ := frag["mcp"].(map[string]any)
	if len(block) == 0 {
		return nil
	}
	names := make([]string, 0, len(block))
	for name := range block {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// parseMCPMetaBlock extracts the auth sub-block, tolerating anything malformed
// by returning a zero value. A server whose artifact carries a broken kwa block
// still works — it just falls back to the built-in catalogue's classification,
// which is strictly better than failing the whole materialization for a
// metadata typo.
func parseMCPMetaBlock(raw any) MCPAuthMeta {
	block, _ := raw.(map[string]any)
	if block == nil {
		return MCPAuthMeta{}
	}
	var auth MCPAuthMeta
	if a, ok := block["auth"].(map[string]any); ok {
		auth.Type, _ = a["type"].(string)
		auth.Label, _ = a["label"].(string)
		auth.Help, _ = a["help"].(string)
		auth.HelpURL, _ = a["helpUrl"].(string)
		auth.Header, _ = a["header"].(string)
		if s, ok := a["scheme"].(string); ok {
			auth.Scheme = &s
		}
	}
	return auth
}

// wireAPIKey points an "api-key" server's header at the user's key file, so
// the artifact defines the server and each user brings their own key. opencode
// resolves {file:} when it loads the config; the file exists (empty) before
// this config does, because a missing one invalidates the whole config.
func wireAPIKey(artifactID, name string, entry map[string]any, auth MCPAuthMeta) []string {
	if entry == nil {
		return nil
	}
	if t, _ := entry["type"].(string); t != "remote" {
		return []string{fmt.Sprintf("mcp artifact %s declares an API key for %q, which is not a remote server; ignoring it", artifactID, name)}
	}
	if mcpKeyFile == nil {
		return []string{fmt.Sprintf("mcp artifact %s: no key store for %q, so it will not authenticate", artifactID, name)}
	}
	path, err := mcpKeyFile(name)
	if err != nil {
		return []string{fmt.Sprintf("mcp artifact %s: %v", artifactID, err)}
	}
	header := strings.TrimSpace(auth.Header)
	if header == "" {
		header = "Authorization"
	}
	scheme := "Bearer"
	if auth.Scheme != nil {
		scheme = strings.TrimSpace(*auth.Scheme)
	}
	value := "{file:" + path + "}"
	if scheme != "" {
		value = scheme + " " + value
	}

	headers, _ := entry["headers"].(map[string]any)
	if headers == nil {
		headers = map[string]any{}
		entry["headers"] = headers
	}
	var warnings []string
	for k := range headers {
		if strings.EqualFold(k, header) {
			delete(headers, k)
			warnings = append(warnings, fmt.Sprintf(
				"mcp artifact %s declares the %s header for %q; ignoring it — Knowledge Worker Agent fills it with each user's own key",
				artifactID, header, name))
		}
	}
	headers[header] = value
	return warnings
}
