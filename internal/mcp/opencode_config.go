// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
)

// opencode's global config file is opencode.json under its config dir. MCP
// servers are declared under the top-level "mcp" object (see
// https://opencode.ai/docs/mcp-servers). We read/write only that key and
// preserve every other field in the file so the user's other opencode config
// is never disturbed.

var opencodeConfigMu sync.Mutex

// opencodeConfigPath returns the path to opencode's global opencode.json.
// Honours KWA_OPENCODE_MCP_CONFIG_PATH for tests, then $XDG_CONFIG_HOME/opencode,
// otherwise ~/.config/opencode/opencode.json. This mirrors opencode's own
// global-config resolution.
func opencodeConfigPath() string {
	if v := env.Get("KWA_OPENCODE_MCP_CONFIG_PATH"); v != "" {
		return v
	}
	var base string
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		base = filepath.Join(xdg, "opencode")
	} else {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			home = "."
		}
		base = filepath.Join(home, ".config", "opencode")
	}
	return filepath.Join(base, "opencode.json")
}

// mcpEntry is the opencode MCP server schema (a subset we manage).
//   - Local (stdio) servers: type="local", command=[cmd, args...], environment.
//   - Remote (http/sse) servers: type="remote", url, headers.
//
// enabled defaults to true when omitted. We always write it explicitly.
type mcpEntry struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Enabled     *bool             `json:"enabled,omitempty"`
	Timeout     int               `json:"timeout,omitempty"`
}

// managedEntryKeys are the mcpEntry fields upsertServer writes. Any other key
// in an entry was put there by someone else and survives a rewrite.
var managedEntryKeys = map[string]bool{
	"type": true, "command": true, "environment": true, "url": true,
	"headers": true, "enabled": true, "timeout": true,
}

// serverView is the transport-oriented shape the HTTP API exposes, translated
// from an opencode mcpEntry. It keeps the frontend contract (transport +
// command/args + url) stable even though opencode stores servers differently.
type serverView struct {
	Name        string            `json:"name"`
	Transport   string            `json:"transport"` // stdio|http
	Type        string            `json:"type"`      // opencode-native: local|remote
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	URL         string            `json:"url,omitempty"`
	Environment map[string]string `json:"env,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Enabled     bool              `json:"enabled"`
	Timeout     int               `json:"timeout,omitempty"`
	// Auth is derived metadata (not stored in opencode.json) telling the UI
	// what this server needs before it can work.
	Auth AuthKind `json:"auth,omitempty"`
	// Provisioned marks a server that comes from an assigned central configuration artifact
	// rather than the user's own config. It may be switched on and off but not
	// edited or removed — see IsProvisioned.
	Provisioned bool `json:"provisioned,omitempty"`
	// Owned marks a server Knowledge Worker Agent rewrites at every start —
	// see IsOwned. Like a provisioned one, it can be switched but not edited.
	Owned bool `json:"owned,omitempty"`
	// Label, Help and HelpURL are admin-authored UI text from the artifact's
	// kwa block. They are what lets a user who has never heard of a provisioned
	// server work out what it is and how to connect it, which matters most for
	// "setup" servers where there is no sign-in flow to fall back on.
	Label   string `json:"label,omitempty"`
	Help    string `json:"help,omitempty"`
	HelpURL string `json:"helpUrl,omitempty"`
	// KeySaved says whether an "api-key" server has the user's key stored. The
	// key itself is never returned.
	KeySaved bool `json:"key_saved,omitempty"`
}

// readConfig loads the full opencode.json as a generic map (so unknown keys are
// preserved on write). A missing file yields an empty config.
func readConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	raw := map[string]any{}
	if len(strings.TrimSpace(string(data))) == 0 {
		return raw, nil
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return raw, nil
}

// writeConfig atomically writes the full opencode.json, creating the parent
// directory if needed. A "$schema" key is added when absent so editors get
// completion.
func writeConfig(path string, raw map[string]any) error {
	if _, ok := raw["$schema"]; !ok {
		raw["$schema"] = "https://opencode.ai/config.json"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// mcpBlock returns the "mcp" object from a parsed config, creating it if it
// doesn't exist. The returned map is the live reference stored in raw.
func mcpBlock(raw map[string]any) map[string]any {
	block, _ := raw["mcp"].(map[string]any)
	if block == nil {
		block = map[string]any{}
		raw["mcp"] = block
	}
	return block
}

// entryToView converts an opencode MCP entry (as an arbitrary map from JSON)
// into the transport-oriented serverView the API exposes.
func entryToView(name string, v any) serverView {
	obj, _ := v.(map[string]any)
	if obj == nil {
		obj = map[string]any{}
	}
	sv := serverView{Name: name, Enabled: true}

	sv.Type, _ = obj["type"].(string)
	switch sv.Type {
	case "remote":
		sv.Transport = "http"
		sv.URL, _ = obj["url"].(string)
		sv.Headers = toStringMap(obj["headers"])
	default: // "local" or unknown → treat as stdio
		sv.Type = "local"
		sv.Transport = "stdio"
		cmd := toStringSlice(obj["command"])
		if len(cmd) > 0 {
			sv.Command = cmd[0]
			sv.Args = cmd[1:]
		}
		sv.Environment = toStringMap(obj["environment"])
	}
	if en, ok := obj["enabled"].(bool); ok {
		sv.Enabled = en
	}
	if t, ok := obj["timeout"].(float64); ok {
		sv.Timeout = int(t)
	}
	sv.Auth = AuthKindFor(sv.Name, sv.Transport)
	// Catalogue-supplied UI text, for built-ins. A provisioned artifact
	// overrides this in applyProvisionedMeta; a user-added server has none.
	if d, ok := CatalogueEntry(sv.Name); ok && sv.Auth != AuthLocal {
		sv.Help = d.Help
		sv.HelpURL = d.HelpURL
	}
	return sv
}

// listServers returns all configured MCP servers, sorted by name.
//
// The list is the union of the user's Global and the platform-owned config dir
// the materializer writes, merged per key with the platform winning —
// the same merge opencode itself performs. That is what makes the modal and
// the connector bar show the same set the model actually sees. Otherwise a
// provisioned server would be live in the model's tool set but invisible
// here, so the user could neither disable it nor sign in to it.
func listServers() ([]serverView, error) {
	prov := provisionedServers()

	opencodeConfigMu.Lock()
	raw, err := readConfig(opencodeConfigPath())
	opencodeConfigMu.Unlock()
	if err != nil {
		return nil, err
	}
	block, _ := raw["mcp"].(map[string]any)

	out := make([]serverView, 0, len(block)+len(prov))
	seen := make(map[string]bool, len(block))
	for name, v := range block {
		key := strings.ToLower(name)
		seen[key] = true
		globalEntry, _ := v.(map[string]any)
		p, isProv := prov[key]
		if !isProv {
			sv := entryToView(name, v)
			sv.Owned = IsOwned(name)
			out = append(out, sv)
			continue
		}
		// Provisioned and present in the Global: the definition comes from the
		// platform, "enabled" from the Global (the materializer strips that key
		// from every artifact, so it can only come from the user).
		sv := entryToView(p.Name, mergeEntry(globalEntry, p.Entry))
		out = append(out, applyProvisionedMeta(sv, p))
	}
	// Provisioned servers with no Global entry yet. EnsureProvisionedStubs
	// normally seeds one at boot; listing them regardless means a server the
	// model can already use is never missing from this list, whatever happened
	// during bootstrap.
	for key, p := range prov {
		if seen[key] {
			continue
		}
		out = append(out, applyProvisionedMeta(entryToView(p.Name, p.Entry), p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// getServer returns a single server's GLOBAL entry by name, or ok=false when
// the user's own config has none.
//
// Deliberately not merged with the platform dir: the callers (EnsureGitHub,
// MigrateAtlassianURL) use it to decide whether to WRITE to the Global, and a
// merged read would have them react to a definition they do not own and cannot
// change. Use mergedServer for anything user-facing.
func getServer(name string) (serverView, bool, error) {
	opencodeConfigMu.Lock()
	defer opencodeConfigMu.Unlock()

	raw, err := readConfig(opencodeConfigPath())
	if err != nil {
		return serverView{}, false, err
	}
	block, _ := raw["mcp"].(map[string]any)
	v, ok := block[name]
	if !ok {
		return serverView{}, false, nil
	}
	return entryToView(name, v), true, nil
}

// mergedServer returns the user-facing view of a single server — the same
// merged, provenance-stamped shape listServers produces.
func mergedServer(name string) (serverView, bool, error) {
	views, err := listServers()
	if err != nil {
		return serverView{}, false, err
	}
	for _, v := range views {
		if strings.EqualFold(v.Name, name) {
			return v, true, nil
		}
	}
	return serverView{}, false, nil
}

// upsertServer adds or replaces a server entry. transport is "stdio" (local) or
// "http"/"sse" (remote). It returns the stored view.
func upsertServer(sv serverView) (serverView, error) {
	opencodeConfigMu.Lock()
	defer opencodeConfigMu.Unlock()

	path := opencodeConfigPath()
	raw, err := readConfig(path)
	if err != nil {
		return serverView{}, err
	}
	block := mcpBlock(raw)

	enabled := sv.Enabled
	entry := mcpEntry{Enabled: &enabled, Timeout: sv.Timeout}
	switch strings.ToLower(sv.Transport) {
	case "http", "sse", "remote":
		entry.Type = "remote"
		entry.URL = sv.URL
		entry.Headers = nonEmptyMap(sv.Headers)
		sv.Type = "remote"
		sv.Transport = "http"
	default:
		entry.Type = "local"
		cmd := []string{sv.Command}
		cmd = append(cmd, sv.Args...)
		entry.Command = cmd
		entry.Environment = nonEmptyMap(sv.Environment)
		sv.Type = "local"
		sv.Transport = "stdio"
	}

	// Marshal the typed entry to a generic map so it merges cleanly.
	b, err := json.Marshal(entry)
	if err != nil {
		return serverView{}, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return serverView{}, err
	}
	// The UI never shows keys like opencode's "oauth" block, so an edit must
	// not be what deletes them.
	if prev, ok := block[sv.Name].(map[string]any); ok {
		for k, v := range prev {
			if !managedEntryKeys[k] {
				m[k] = v
			}
		}
	}
	block[sv.Name] = m

	if err := writeConfig(path, raw); err != nil {
		return serverView{}, err
	}
	sv.Auth = AuthKindFor(sv.Name, sv.Transport)
	return sv, nil
}

// setEnabled flips only the "enabled" flag of an existing server, leaving the
// rest of its entry untouched. opencode skips servers with enabled=false: it
// never connects them, so none of their tools reach the model. Returns
// ok=false when the server isn't configured.
//
// The write always lands in the user's GLOBAL, never the platform dir. That is
// what makes the choice durable: the materializer wipes and recreates the
// platform dir on every boot, but never touches the Global.
//
// When the name has no Global entry but IS provisioned, an entry is created
// carrying only "enabled". Without this, disabling a provisioned server was a
// silent no-op — the toggle moved and nothing was written, because the server
// was declared solely in the platform file. opencode's per-key merge then takes
// the definition from the platform file and this flag from the Global, which is
// exactly the split the design wants.
func setEnabled(name string, enabled bool) (bool, error) {
	provisioned := provisionedServers()

	opencodeConfigMu.Lock()
	defer opencodeConfigMu.Unlock()

	path := opencodeConfigPath()
	raw, err := readConfig(path)
	if err != nil {
		return false, err
	}
	block, _ := raw["mcp"].(map[string]any)
	key := name
	if _, exact := block[key]; !exact {
		// Names round-trip through the UI, so tolerate a case mismatch.
		for k := range block {
			if strings.EqualFold(k, name) {
				key = k
				break
			}
		}
	}
	entry, ok := block[key].(map[string]any)
	if !ok {
		p, isProvisioned := provisioned[strings.ToLower(strings.TrimSpace(name))]
		if !isProvisioned {
			return false, nil
		}
		// Seed the stub the user's choice lives in, using the platform's own
		// spelling of the name so the two files key on the same string.
		block = mcpBlock(raw)
		entry = map[string]any{}
		block[p.Name] = entry
	}
	if cur, isBool := entry["enabled"].(bool); isBool && cur == enabled {
		return true, nil // already in the desired state; avoid a pointless write
	}
	entry["enabled"] = enabled
	if err := writeConfig(path, raw); err != nil {
		return false, err
	}
	return true, nil
}

// SetEnabled enables or disables a configured MCP server by name (case
// sensitive, matching the opencode.json key). It is the gate that decides
// whether the server's tools are exposed to the model at all.
func SetEnabled(name string, enabled bool) (bool, error) { return setEnabled(name, enabled) }

// IsEnabled reports whether a configured server is switched on. found is false
// when no server of that name is configured. The name is matched
// case-insensitively, as it round-trips through the UI.
func IsEnabled(name string) (enabled, found bool, err error) {
	views, err := listServers()
	if err != nil {
		return false, false, err
	}
	for _, v := range views {
		if strings.EqualFold(v.Name, name) {
			return v.Enabled, true, nil
		}
	}
	return false, false, nil
}

// removeServer deletes a server entry by name. Missing entries are a no-op.
func removeServer(name string) error {
	opencodeConfigMu.Lock()
	defer opencodeConfigMu.Unlock()

	path := opencodeConfigPath()
	raw, err := readConfig(path)
	if err != nil {
		return err
	}
	block, _ := raw["mcp"].(map[string]any)
	if block == nil {
		return nil
	}
	if _, ok := block[name]; !ok {
		return nil
	}
	delete(block, name)
	return writeConfig(path, raw)
}

// GlobalServer is a minimal view of a globally-configured MCP server, exposed
// for per-session (per-chat) selection: the name and whether it is enabled in
// the global opencode.json. Enabled servers are the default "on" set seeded
// into a new chat's connector selection.
type GlobalServer struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// GlobalServers returns all globally-configured MCP servers (name + enabled),
// sorted by name. Never returns nil.
func GlobalServers() ([]GlobalServer, error) {
	views, err := listServers()
	if err != nil {
		return nil, err
	}
	out := make([]GlobalServer, 0, len(views))
	for _, v := range views {
		out = append(out, GlobalServer{Name: v.Name, Enabled: v.Enabled})
	}
	return out, nil
}

// serverNames returns the names of all configured MCP servers.
func serverNames() ([]string, error) {
	views, err := listServers()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(views))
	for _, v := range views {
		names = append(names, v.Name)
	}
	return names, nil
}

func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func toStringMap(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for k, val := range m {
		if s, ok := val.(string); ok {
			out[k] = s
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func nonEmptyMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return m
}
