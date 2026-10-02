// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"fmt"
	"io"
	"strings"
)

// Central MCP management rides the single central-management switch,
// config.CentralConfigEnabled — see config.EnvCentralConfig.
//
// With it on, the built-in catalogue in DefaultServers is not installed, and
// entries it previously wrote are conservatively removed. The built-in
// catalogue is a starter set, not part of the design: the intended end state is
// a workspace whose MCP catalogue is entirely the administrator's.
//
// Reading the platform directory is NOT gated on it: that is a bug fix, not a
// migration. A provisioned server is already connected and already spending
// tokens, so showing it in the UI is how the user gets the ability to reason
// about it, and withholding that behind a switch would leave the worst of the
// three states reachable by default.

// RemoveCatalogueDefaults removes built-in catalogue entries, current and
// retired, from the user's Global, for a workspace running on the
// administrator's catalogue.
func RemoveCatalogueDefaults(logw io.Writer) {
	removeUntouched(logw, append(append([]DefaultServer{}, DefaultServers...), RetiredServers...))
}

// removeUntouched removes the given catalogue servers' entries from the user's
// Global.
//
// Removal is conservative: an entry is only dropped when it is still disabled
// AND still matches the catalogue's own definition. A default the user enabled,
// signed in to, or edited represents real investment, and removing a server
// somebody is signed in to in order to satisfy a provisioning rule would be a
// worse outcome than an extra disabled row. This is the same rule
// internal/defaults already applies to bundled content.
//
// github and obsidian are unaffected: they are registered outside DefaultServers
// (github carries a runtime-injected Copilot bearer token, obsidian is installed
// by Knowledge Worker Agent itself), so this never touches them.
func removeUntouched(logw io.Writer, servers []DefaultServer) {
	if logw == nil {
		logw = io.Discard
	}

	opencodeConfigMu.Lock()
	defer opencodeConfigMu.Unlock()

	path := opencodeConfigPath()
	raw, err := readConfig(path)
	if err != nil {
		fmt.Fprintf(logw, "  ⚠ MCP catalogue: cannot read opencode config: %v\n", err)
		return
	}
	block, _ := raw["mcp"].(map[string]any)
	if len(block) == 0 {
		return
	}

	var removed, kept []string
	for _, d := range servers {
		key := ""
		for k := range block {
			if strings.EqualFold(k, d.Name) {
				key = k
				break
			}
		}
		if key == "" {
			continue
		}
		entry, _ := block[key].(map[string]any)
		if entry == nil {
			continue
		}
		// A provisioned name is not a catalogue entry at all: the Global holds
		// only the enable/disable stub, and the definition comes from the
		// platform dir. Skip it silently rather than reporting it as a built-in
		// the user invested in — several catalogue names (atlassian, figma,
		// pendo) are also things an administrator provisions, so this is the
		// common case, not an edge one. Deleting the stub would also throw away
		// the user's on/off choice.
		if IsProvisioned(key) {
			continue
		}
		if !catalogueUntouched(entry, d) {
			kept = append(kept, key)
			continue
		}
		delete(block, key)
		removed = append(removed, key)
	}

	if len(kept) > 0 {
		fmt.Fprintf(logw, "  ℹ MCP catalogue: keeping %d built-in server(s) you enabled or edited: %s\n",
			len(kept), strings.Join(kept, ", "))
	}
	if len(removed) == 0 {
		return
	}
	if err := writeConfig(path, raw); err != nil {
		fmt.Fprintf(logw, "  ⚠ MCP catalogue: cannot remove built-in servers: %v\n", err)
		return
	}
	fmt.Fprintf(logw, "  ✓ MCP catalogue: removed %d untouched built-in server(s): %s\n",
		len(removed), strings.Join(removed, ", "))
}

// catalogueUntouched reports whether a Global entry is still exactly what
// EnsureServer wrote for this catalogue member: switched off, and with the
// definition unchanged.
//
// "Enabled" counts as touched even without an edit, because enabling is the one
// deliberate act the UI offers for a default, and it is usually accompanied by
// a sign-in whose credential lives in opencode's store rather than here.
func catalogueUntouched(entry map[string]any, d DefaultServer) bool {
	if enabled, ok := entry["enabled"].(bool); !ok || enabled {
		return false
	}
	if url, _ := entry["url"].(string); url != d.URL {
		return false
	}
	// EnsureServer writes no headers or environment for a catalogue entry, so
	// anything there is the user's (e.g. a hand-added API token).
	if h, ok := entry["headers"].(map[string]any); ok && len(h) > 0 {
		return false
	}
	if e, ok := entry["environment"].(map[string]any); ok && len(e) > 0 {
		return false
	}
	return true
}
