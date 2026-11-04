// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodev2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/agentbuilder"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// The platform tier on 2.x.
//
// 1.x layers the platform folder (OPENCODE_CONFIG_DIR) over the user's global
// configuration; 2.x reads that folder instead of the global one, so a user's
// own connectors, toggles and agents would vanish. On 2.x the folder becomes
// one document instead, OPENCODE_CONFIG, which 2.x layers over the global
// configuration: the folder's opencode.json with its skills folder named and
// its agents written in, plus what KWA itself needs from opencode.

// overlayName is the document KWA writes for 2.x. In the platform folder, the
// relative paths in its opencode.json still resolve.
const overlayName = "opencode.kwa-v2.json"

// NoteAgent is the agent KWA's one-off writing tasks run as on 2.x, whose title
// agent only writes titles.
const NoteAgent = "kwa-text"

// overlayPath returns where the document goes: the platform folder, or KWA's
// own folder when there is no platform folder.
func overlayPath(platformDir string) string {
	if platformDir != "" {
		return filepath.Join(platformDir, overlayName)
	}
	return filepath.Join(layout.System(), overlayName)
}

// writeOverlay writes the 2.x document for platformDir ("" for none) and
// returns its path.
func writeOverlay(platformDir string) (string, error) {
	doc := map[string]any{}
	if platformDir != "" {
		b, err := os.ReadFile(filepath.Join(platformDir, "opencode.json"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if len(b) > 0 {
			if err := json.Unmarshal(b, &doc); err != nil {
				return "", fmt.Errorf("read %s: %w", filepath.Join(platformDir, "opencode.json"), err)
			}
		}
		if skills := filepath.Join(platformDir, "skills"); isDir(skills) {
			doc["skills"] = append([]any{skills}, asList(doc["skills"])...)
		}
		agents := asMap(doc["agent"])
		for name, a := range platformAgents(platformDir) {
			if _, set := agents[name]; !set {
				agents[name] = a
			}
		}
		doc["agent"] = agents
		applyUserToggles(doc)
	}
	addKWASettings(doc)

	path := overlayPath(platformDir)
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}

// platformAgents reads the folder's agent files into config entries, in the
// 1.x shape that 2.x accepts. Keys the agent builder doesn't know are dropped.
func platformAgents(dir string) map[string]any {
	out := map[string]any{}
	for _, sub := range []string{"agent", "agents"} {
		files, _ := filepath.Glob(filepath.Join(dir, sub, "*.md"))
		sort.Strings(files)
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			name := strings.TrimSuffix(filepath.Base(f), ".md")
			a, _ := agentbuilder.Parse(name, data)
			e := map[string]any{"prompt": a.Prompt}
			if a.Description != "" {
				e["description"] = a.Description
			}
			if a.Mode != "" {
				e["mode"] = string(a.Mode)
			}
			if a.Model != "" {
				e["model"] = a.Model
			}
			if a.Temperature != nil {
				e["temperature"] = *a.Temperature
			}
			if a.TopP != nil {
				e["top_p"] = *a.TopP
			}
			if a.Steps != nil {
				e["steps"] = *a.Steps
			}
			if a.Color != "" {
				e["color"] = a.Color
			}
			if a.Hidden {
				e["hidden"] = true
			}
			if a.Disable {
				e["disable"] = true
			}
			if !a.Permission.IsZero() {
				perm := map[string]any{}
				if a.Permission.Global != "" {
					perm["*"] = string(a.Permission.Global)
				}
				for k, v := range a.Permission.Rules {
					perm[k] = string(v)
				}
				e["permission"] = perm
			}
			if _, seen := out[name]; !seen {
				out[name] = e
			}
		}
	}
	return out
}

// applyUserToggles carries the user's on/off switches into the platform's
// connectors. KWA keeps them in the global configuration as entries with only
// `enabled`, which 2.x ignores rather than merging into the platform's
// definition, so a connector the user switched off would connect again.
func applyUserToggles(doc map[string]any) {
	servers := asMap(doc["mcp"])
	if len(servers) == 0 {
		return
	}
	b, err := os.ReadFile(globalConfigPath())
	if err != nil {
		return
	}
	var global struct {
		MCP map[string]struct {
			Enabled *bool `json:"enabled"`
		} `json:"mcp"`
	}
	if json.Unmarshal(b, &global) != nil {
		return
	}
	for name, s := range servers {
		entry, ok := s.(map[string]any)
		if !ok {
			continue
		}
		if g, ok := global.MCP[name]; ok && g.Enabled != nil {
			entry["enabled"] = *g.Enabled
		}
	}
	doc["mcp"] = servers
}

// addKWASettings adds what KWA needs whatever the platform says: the plan
// display reads opencode's todo tool, which 2.x offers only on request;
// folders outside the chat are asked about, as 1.x does, so a turn without
// Yolo refuses them; and an agent for KWA's one-off writing tasks.
func addKWASettings(doc map[string]any) {
	exp := asMap(doc["experimental"])
	exp["todo"] = true
	doc["experimental"] = exp

	switch p := doc["permission"].(type) {
	case map[string]any:
		if _, set := p["external_directory"]; !set {
			p["external_directory"] = "ask"
		}
	case nil:
		doc["permission"] = map[string]any{"external_directory": "ask"}
	}

	agents := asMap(doc["agent"])
	agents[NoteAgent] = map[string]any{
		"description": "Writes the text a message asks for. Used by Knowledge Worker Agent for notes.",
		"mode":        "primary",
		"hidden":      true,
		"prompt":      "Write exactly the text the message asks for, and nothing else. Use no tools.",
		"permission":  map[string]any{"*": "deny"},
	}
	doc["agent"] = agents
}

// configEnv returns env with the platform folder handed to 2.x as the
// document it layers over the global configuration. It is left as it is when
// the document can't be written.
func configEnv(env []string) []string {
	var dir string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "OPENCODE_CONFIG_DIR="); ok {
			dir = v
		}
	}
	path, err := writeOverlay(dir)
	if err != nil {
		log.Printf("[opencode-v2] could not write %s: %v", overlayPath(dir), err)
		return env
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, "OPENCODE_CONFIG_DIR=") || strings.HasPrefix(kv, "OPENCODE_CONFIG=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "OPENCODE_CONFIG="+path)
}

// Reload makes the running server read its configuration again, after KWA or
// the user changed it. With no server running there is nothing to do: the
// next one reads it when it starts. A server started before the platform
// folder was known reads its document from elsewhere, and `opencode reload`
// can't move it, so that one is restarted.
func Reload(ctx context.Context) error {
	url, password, ok := Running()
	if !ok {
		return nil
	}
	path, err := writeOverlay(os.Getenv("OPENCODE_CONFIG_DIR"))
	if err != nil {
		return err
	}
	srv.mu.Lock()
	startedWith := srv.overlay
	srv.mu.Unlock()
	if startedWith != path {
		log.Printf("[opencode-v2] the configuration moved to %s; restarting the server", path)
		return Restart(ctx)
	}
	cmd := exec.CommandContext(ctx, config.OpencodeBin, "reload", "--server", url)
	cmd.Env = append(childEnv(os.Environ()), "OPENCODE_PASSWORD="+password, "OPENCODE_DISABLE_AUTOUPDATE=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func globalConfigPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode", "opencode.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "opencode", "opencode.json")
}

// declaredConnectors counts the connectors the documents at paths define. An
// entry with only `enabled` defines none: 2.x drops it.
func declaredConnectors(paths ...string) int {
	names := map[string]bool{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var doc struct {
			MCP map[string]struct {
				URL     string `json:"url"`
				Command any    `json:"command"`
			} `json:"mcp"`
		}
		if json.Unmarshal(b, &doc) != nil {
			continue
		}
		for name, s := range doc.MCP {
			if s.URL != "" || s.Command != nil {
				names[name] = true
			}
		}
	}
	return len(names)
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func asList(v any) []any {
	switch l := v.(type) {
	case []any:
		return l
	case string:
		return []any{l}
	}
	return nil
}
