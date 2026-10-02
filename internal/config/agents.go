// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type AgentInfo struct {
	// ID is the OpenCode-resolvable agent identifier: the file stem of the
	// agent's *.md file (e.g. "product-manager"). This is the value passed
	// to opencode's --agent flag / server "agent" param, NOT the display Name.
	ID string `json:"id"`
	// Name is the human-friendly label shown in the UI. It comes from the
	// agent frontmatter "name:" field, falling back to a title-cased ID.
	Name        string `json:"name"`
	Description string `json:"description"`
	Filename    string `json:"filename"`
	// Source names the tier the agent was found in: AgentSourceAssigned for
	// the platform-owned dir the materializer owns, AgentSourcePersonal
	// for the user's own Global. The picker groups on it, because the two tiers
	// answer different questions — "what did IT give me" versus "what did I
	// build" — and a flat list cannot distinguish them. Without it an agent an
	// administrator removed from the assignment is indistinguishable from one
	// the user created, which is exactly the confusion this field exists to end.
	Source string `json:"source"`
}

// The tiers an agent can come from. Values are part of the /api/agents
// response contract and are matched by the frontend, so they are stable
// identifiers rather than display labels — the UI owns the wording.
const (
	// AgentSourceAssigned is the platform-owned dir: the set the config repo
	// assigns to this workspace's project. The materializer wipes and rewrites
	// that dir on every boot, so it is always exactly the current assignment.
	AgentSourceAssigned = "assigned"
	// AgentSourcePersonal is the user's own Global dir, where the Agent
	// Builder writes. Nothing reconciles it against the assignment, by design:
	// it holds work the user owns and central config must not delete.
	AgentSourcePersonal = "personal"
)

// EnvOpencodeConfigDir names the platform-owned OpenCode config dir that the
// materializer writes the project's assigned artifacts into and exports
// once it has run. It must match materializer.EnvConfigDir; a test guards the
// two against drift. Declared here rather than imported so that config, which
// nearly every package depends on, stays a leaf.
const EnvOpencodeConfigDir = "OPENCODE_CONFIG_DIR"

// agentDir is one tier agents are read from, paired with the Source it stamps
// on everything found there.
type agentDir struct {
	path   string
	source string
}

// agentDirs returns the directories agents are read from, in OpenCode's own
// precedence order (lowest first).
//
// The user's Global is the baseline; the platform-owned dir sits above it, and
// is read from the environment on every call rather than resolved once at
// Init() because the materializer runs during bootstrap and exports the var
// only after it has successfully materialized. Resolving it early would bake in
// the value from before the materializer ran.
//
// When the var is unset — no central config provisioned — this is exactly
// today's single-directory behaviour, and every agent is personal: with no
// assignment to compare against, nothing can honestly be labelled as assigned.
func agentDirs() []agentDir {
	dirs := []agentDir{{path: OpencodeAgentsDir, source: AgentSourcePersonal}}
	if platform := strings.TrimSpace(os.Getenv(EnvOpencodeConfigDir)); platform != "" {
		dirs = append(dirs, agentDir{path: filepath.Join(platform, "agent"), source: AgentSourceAssigned})
	}
	return dirs
}

// ListAgents reports the OpenCode agents (*.md) available to this workspace,
// reading every tier OpenCode itself resolves from so that the picker offers
// what a turn would actually run.
//
// Two directories can supply agents: the user's Global (~/.config/opencode/
// agent) and, when central configuration has provisioned one, the platform-owned dir the
// materializer owns. Reading only the Global was invisible while Knowledge Worker Agent
// installed its bundled defaults there — the picker was listing those copies,
// not the centrally assigned set — and left the picker empty once
// KWA_CENTRAL_CONFIG stopped installing them.
//
// OpenCode resolves an agent by its file stem, so the stem is reported as ID
// (and passed to --agent) and the frontmatter "name:" is only a display label.
// Because the stem is the identity, two tiers can define the same agent: the
// platform copy wins, mirroring OpenCode's precedence so the UI cannot offer a
// description of one agent while the run uses another.
//
// Each agent carries the Source of the tier it was resolved from, so the picker
// can group by provenance. An override therefore reports "assigned": that is
// the copy a turn would actually run, and claiming otherwise would recreate the
// mismatch the precedence rule exists to prevent.
func ListAgents() []AgentInfo {
	byID := map[string]AgentInfo{}

	for _, dir := range agentDirs() {
		entries, err := os.ReadDir(dir.path)
		if err != nil {
			// A tier that isn't present contributes nothing. Missing dirs are
			// normal: no central config, or a workspace with no agents of its own.
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".md") {
				continue
			}
			info := parseAgentFile(filepath.Join(dir.path, name))
			if info.ID == "" {
				continue
			}
			info.Source = dir.source
			// Later dirs are higher precedence, so a plain overwrite is the
			// override.
			byID[info.ID] = info
		}
	}

	agents := make([]AgentInfo, 0, len(byID))
	for _, info := range byID {
		agents = append(agents, info)
	}
	// Map iteration is random; sort by ID so the picker's order is stable
	// across requests. Matches the previous single-directory order, which came
	// from os.ReadDir returning entries sorted by filename.
	sort.Slice(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })

	return agents
}

// parseAgentFile reads an OpenCode agent markdown file and extracts its
// display name and description from YAML frontmatter. The ID is always the
// file stem (what OpenCode uses to resolve --agent).
func parseAgentFile(path string) AgentInfo {
	base := filepath.Base(path)
	id := strings.TrimSuffix(base, ".md")

	info := AgentInfo{
		ID:       id,
		Filename: base,
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return AgentInfo{}
	}
	content := string(data)

	// Parse YAML frontmatter between --- delimiters, if present.
	if strings.HasPrefix(content, "---") {
		if endIdx := strings.Index(content[3:], "---"); endIdx != -1 {
			frontmatter := content[3 : endIdx+3]
			for _, line := range strings.Split(frontmatter, "\n") {
				line = strings.TrimSpace(line)
				switch {
				case strings.HasPrefix(line, "name:"):
					n := strings.TrimSpace(strings.TrimPrefix(line, "name:"))
					info.Name = strings.Trim(n, "\"'")
				case strings.HasPrefix(line, "description:"):
					desc := strings.TrimSpace(strings.TrimPrefix(line, "description:"))
					desc = strings.Trim(desc, "\"'")
					// Take only the first sentence/paragraph for display.
					if idx := strings.Index(desc, "\\n"); idx > 0 {
						desc = desc[:idx]
					}
					info.Description = desc
				}
			}
		}
	}

	if info.Name == "" {
		info.Name = titleizeAgentID(id)
	}

	return info
}

// titleizeAgentID converts an agent file stem like "product-manager" into a
// human-friendly fallback label like "Product Manager".
func titleizeAgentID(id string) string {
	words := strings.FieldsFunc(id, func(r rune) bool { return r == '-' || r == '_' })
	for i, w := range words {
		if w == "" {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	if len(words) == 0 {
		return id
	}
	return strings.Join(words, " ")
}
