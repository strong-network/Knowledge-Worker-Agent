// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// Central connectors: reading the platform-owned config dir.
//
// opencode reads configuration from two directories and merges them per key:
// the user's Global (~/.config/opencode) and the platform dir the
// materializer owns (OPENCODE_CONFIG_DIR). Reading only the Global, as this
// package once did, left a centrally provisioned MCP server
// invisible in the MCP modal and in the connector bar while being live in
// the model's tool set — a server the user could not see, disable, or sign in
// to, but was paying for on every turn.
//
// This file adds the missing read. The merge here mirrors opencode's own so the
// modal shows the same set the model sees.

// platformDirFn resolves the platform-owned config dir. Overridable for tests.
var platformDirFn = platformConfigDir

// platformConfigDir returns the dir the materializer writes to.
//
// This is OPENCODE_CONFIG_DIR — NOT the user's Global. The materializer wipes
// and recreates it on every run and explicitly refuses to write to the Global,
// so the platform dir is the only place a provisioned server exists, and the
// Global is the only place a user's own choices survive.
func platformConfigDir() string { return layout.PlatformConfig() }

// mcpMetaEntry mirrors materializer.MCPMeta. It is redeclared rather than
// imported to keep this package free of a dependency on the materializer: the
// sidecar is a file format, and reading it should not couple the reader to the
// writer's package. (internal/providers does the same for provider-meta.json.)
type mcpMetaEntry struct {
	Name string `json:"name"`
	Auth struct {
		Type    string `json:"type"`
		Label   string `json:"label"`
		Help    string `json:"help"`
		HelpURL string `json:"helpUrl"`
	} `json:"auth"`
}

type mcpMetaDoc struct {
	Servers []mcpMetaEntry `json:"servers"`
}

// provisionedServer is one MCP server the administrator provisioned: its
// definition from the platform opencode.json, plus whatever the artifact's kwa
// block said about how to sign in to it.
type provisionedServer struct {
	// Name is the platform config's own spelling of the key.
	Name string
	// Entry is the raw opencode mcp entry (type, url, headers, command…).
	Entry map[string]any
	// Meta is the sidecar metadata; zero when the artifact carried no kwa block.
	Meta mcpMetaEntry
}

// provisionedServers returns the MCP servers declared in the platform config,
// keyed by lower-cased name.
//
// A missing platform dir, a missing opencode.json, or a parse failure all yield
// an empty map rather than an error: a workspace with no central config is the
// normal case, and a broken platform config must not take the user's own MCP
// servers off the screen with it.
func provisionedServers() map[string]provisionedServer {
	dir := platformDirFn()
	if dir == "" {
		return nil
	}

	raw, err := os.ReadFile(filepath.Join(dir, "opencode.json"))
	if err != nil || len(raw) == 0 {
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	block, _ := doc["mcp"].(map[string]any)
	if len(block) == 0 {
		return nil
	}

	meta := readMCPMeta(filepath.Join(dir, mcpMetaFileName))

	out := make(map[string]provisionedServer, len(block))
	for name, v := range block {
		if strings.TrimSpace(name) == "" {
			continue
		}
		entry, _ := v.(map[string]any)
		if entry == nil {
			entry = map[string]any{}
		}
		out[strings.ToLower(name)] = provisionedServer{
			Name:  name,
			Entry: entry,
			Meta:  meta[strings.ToLower(name)],
		}
	}
	return out
}

// mcpMetaFileName is the sidecar the materializer writes (materializer.MCPMetaFile).
const mcpMetaFileName = "mcp-meta.json"

// readMCPMeta reads the sidecar, keyed by lower-cased name. A missing or
// malformed sidecar is not an error: the servers in the platform opencode.json
// are still shown, they just fall back to the built-in catalogue for
// classification.
func readMCPMeta(path string) map[string]mcpMetaEntry {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return nil
	}
	var doc mcpMetaDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	out := make(map[string]mcpMetaEntry, len(doc.Servers))
	for _, e := range doc.Servers {
		if strings.TrimSpace(e.Name) == "" {
			continue
		}
		out[strings.ToLower(e.Name)] = e
	}
	return out
}

// mergeEntry layers a platform entry over a Global one, per key, the way
// opencode itself merges the two files (verified against the binary). The
// platform wins on every key it declares, so the
// definition (type, url, headers) is the administrator's and cannot be tampered
// with from the Global.
//
// The materializer strips "enabled" from every provisioned entry, so that key
// only ever comes from the Global — which is exactly the split this design
// wants: the platform owns what a server IS, the user owns whether it RUNS.
func mergeEntry(global, platform map[string]any) map[string]any {
	out := make(map[string]any, len(global)+len(platform))
	for k, v := range global {
		out[k] = v
	}
	for k, v := range platform {
		out[k] = v
	}
	return out
}

// applyProvisionedMeta stamps provenance and the artifact's UI metadata onto a
// server view, and resolves its auth classification.
//
// Precedence is: the artifact wins, and the catalogue supplies a default for
// names the artifact does not classify. That is the reverse of the rule for model
// providers, deliberately. The built-in MCP catalogue is transitional and is
// going away; if the catalogue outranked the artifact, deleting it would
// silently strip every provisioned atlassian of its "oauth" classification and
// with it the Connect button. A centrally managed workspace with every
// artifact classified therefore behaves exactly like a future with no catalogue
// at all, which is what makes that end state testable now rather than
// discovered later.
func applyProvisionedMeta(sv serverView, p provisionedServer) serverView {
	sv.Provisioned = true
	sv.Label = strings.TrimSpace(p.Meta.Auth.Label)
	// The artifact's text wins, but silence is not an instruction to erase: a
	// name that is also a catalogue entry keeps the catalogue's help rather
	// than losing its only explanation. Same rule as the auth kind below.
	if h := strings.TrimSpace(p.Meta.Auth.Help); h != "" {
		sv.Help = h
	}
	if u := safeHelpURL(p.Meta.Auth.HelpURL); u != "" {
		sv.HelpURL = u
	}

	declared := AuthKind(strings.ToLower(strings.TrimSpace(p.Meta.Auth.Type)))
	if declared == AuthUnknown {
		return sv // unclassified: keep the catalogue's answer
	}

	// github is the one genuine exception, and it is not a catalogue member in
	// the DefaultServers sense. Its bearer token is injected at runtime from a
	// GitHub PAT (EnsureGitHub) and no artifact can restore that, so an artifact
	// calling it "oauth" would render a sign-in that cannot work ("does not
	// support dynamic client registration"). Its classification stays with the
	// code that owns the token.
	if strings.EqualFold(sv.Name, GitHubDefault.Name) {
		return sv
	}

	// A local (stdio) server has no account to sign in to whatever the artifact
	// says, so the transport keeps the last word here too.
	if sv.Auth == AuthLocal {
		return sv
	}

	switch declared {
	case AuthNone, AuthOAuth, AuthSetup, AuthLocal:
		sv.Auth = declared
	case AuthAPIKey:
		sv.Auth = declared
		sv.KeySaved = KeySaved(p.Name)
	}
	// Anything else (a typo, or a kind this build doesn't know) leaves the
	// catalogue's answer in place rather than blanking the row's affordance.
	return sv
}

// safeHelpURL accepts an admin-authored documentation link only if it is an
// absolute https:// URL with a host.
//
// This value is written into an href in the browser. The config repo is a
// trusted source, but "trusted" is an assumption about intent, not about
// review: a mistyped or copy-pasted `javascript:` or `data:` URL would become
// script execution in the user's session. Restricting the scheme here costs
// nothing and removes the question entirely. (Mirrors providers.safeHelpURL.)
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

// IsProvisioned reports whether a server of this name comes from an assigned
// artifact. Provisioned servers may be switched on and off (that lands in the
// Global, which the materializer never touches) but may not be edited or
// removed: the platform dir is wiped and recreated on every boot, so either
// action would appear to work and then silently undo itself at the next restart.
func IsProvisioned(name string) bool {
	_, ok := provisionedServers()[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

// EnsureProvisionedStubs seeds a disabled stub in the user's Global for every
// provisioned server that has no entry there yet.
//
// Provisioning and enabling are separate acts: an administrator assigning an
// artifact is saying "this workspace may use Jira", not "every chat now loads
// Jira's tools". But left alone, provisioning DOES enable — a server with no
// "enabled" key in either file is connected by opencode, its tools enter the
// schema on every turn, and it costs money from the moment the artifact is
// assigned.
//
// The artifact cannot fix that itself: opencode's merge is per key and the
// platform file wins, so "enabled": false in the artifact would override the
// user and they could never turn the server ON. So the stub goes in the Global
// instead. opencode's deep merge then takes the definition from the platform
// file and "enabled" from the Global, the existing toggle flips the stub, and
// the materializer's wipe never touches it — so the choice survives a restart
// and a re-materialization.
//
// Seeding is skipped whenever the name is already present, so a user who
// enabled a server is never quietly switched off again on the next boot.
func EnsureProvisionedStubs(logw io.Writer) {
	if logw == nil {
		logw = io.Discard
	}
	prov := provisionedServers()
	if len(prov) == 0 {
		return
	}

	opencodeConfigMu.Lock()
	defer opencodeConfigMu.Unlock()

	path := opencodeConfigPath()
	raw, err := readConfig(path)
	if err != nil {
		fmt.Fprintf(logw, "  ⚠ MCP provisioning: cannot read opencode config: %v\n", err)
		return
	}
	block := mcpBlock(raw)

	// Index the Global case-insensitively: names round-trip through the UI and
	// through the config repo, and a case difference must not produce a second
	// entry that shadows the user's real one.
	existing := make(map[string]bool, len(block))
	for k := range block {
		existing[strings.ToLower(k)] = true
	}

	var seeded []string
	for key, p := range prov {
		if existing[key] {
			continue // the user already has a say for this server; leave it alone
		}
		block[p.Name] = map[string]any{"enabled": false}
		seeded = append(seeded, p.Name)
	}
	if len(seeded) == 0 {
		return
	}
	if err := writeConfig(path, raw); err != nil {
		fmt.Fprintf(logw, "  ⚠ MCP provisioning: cannot seed provisioned servers: %v\n", err)
		return
	}
	fmt.Fprintf(logw, "  ✓ MCP provisioning: %d provisioned server(s) available, switched off: %s\n",
		len(seeded), strings.Join(seeded, ", "))
}
