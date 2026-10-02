// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package materializer implements the workspace configuration
// materializer: at workspace startup it reads a central, admin-controlled
// config repo (artifacts + a JSON assignment file), resolves which artifacts
// apply to the workspace's project, and writes them into one platform-owned
// OpenCode config dir (OPENCODE_CONFIG_DIR) above the user's untouched Global.
//
// This file covers parsing the assignment control file and resolving a
// project's artifact set (the union of its assigned artifacts minus deny).
package materializer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Artifact kinds. The first five are the building-block types of central
// configuration; vocabulary is Knowledge Worker Agent's own content for dictation. The keys match
// the assignment file's registry sub-objects.
const (
	KindMCP        = "mcp"
	KindAgents     = "agents"
	KindContext    = "context"
	KindSkills     = "skills"
	KindProvider   = "provider"
	KindVocabulary = "vocabulary"
)

// artifactKinds is the fixed set of artifact types, in a stable order.
var artifactKinds = []string{KindMCP, KindAgents, KindContext, KindSkills, KindProvider, KindVocabulary}

// Assignment is the parsed form of assignment.jsonc.
type Assignment struct {
	SchemaVersion int `json:"schema_version"`
	// Artifacts is the registry: kind -> id -> {path}. Every reusable building
	// block is registered once under an immutable id that points at its path in
	// the repo.
	Artifacts map[string]map[string]ArtifactRef `json:"artifacts"`
	// Assignments carries per-project artifact assignments.
	Assignments struct {
		Projects map[string]ProjectAssignment `json:"projects"`
	} `json:"assignments"`
}

// ArtifactRef registers an artifact id against a repo-relative path.
type ArtifactRef struct {
	Path string `json:"path"`
}

// ProjectAssignment lists which artifact ids a project receives, plus optional
// deny removals and mcp_default_on selection metadata.
type ProjectAssignment struct {
	MCP          []string            `json:"mcp"`
	Agents       []string            `json:"agents"`
	Context      []string            `json:"context"`
	Skills       []string            `json:"skills"`
	Provider     []string            `json:"provider"`
	Vocabulary   []string            `json:"vocabulary"`
	MCPDefaultOn []string            `json:"mcp_default_on"`
	Deny         map[string][]string `json:"deny"`
}

// idsForKind returns the assigned artifact ids for a given kind.
func (p ProjectAssignment) idsForKind(kind string) []string {
	switch kind {
	case KindMCP:
		return p.MCP
	case KindAgents:
		return p.Agents
	case KindContext:
		return p.Context
	case KindSkills:
		return p.Skills
	case KindProvider:
		return p.Provider
	case KindVocabulary:
		return p.Vocabulary
	default:
		return nil
	}
}

// ResolvedArtifact is a single artifact selected for a project, with its
// repo-relative path resolved from the registry.
type ResolvedArtifact struct {
	ID   string
	Path string
}

// Resolved is the effective, ordered artifact set for one project: the union of
// its assigned artifacts minus its deny block, grouped by kind. MCPDefaultOn is
// carried through as selection metadata; it is not materialized into
// OpenCode config.
type Resolved struct {
	ProjectID    string
	ByKind       map[string][]ResolvedArtifact
	MCPDefaultOn []string
}

// ParseAssignment parses assignment.jsonc bytes (JSON with // and /* */
// comments and tolerant trailing commas) into an Assignment.
func ParseAssignment(data []byte) (*Assignment, error) {
	clean := stripJSONComments(data)
	clean = stripTrailingCommas(clean)
	var a Assignment
	if err := json.Unmarshal(clean, &a); err != nil {
		return nil, fmt.Errorf("parse assignment: %w", err)
	}
	return &a, nil
}

// Resolve computes the effective artifact set for projectID. A missing project
// yields an error; the caller decides whether that is fatal. Unknown artifact
// ids (referenced but not registered) are reported as an error so a broken
// assignment file is surfaced rather than silently dropping config.
func (a *Assignment) Resolve(projectID string) (*Resolved, error) {
	proj, ok := a.Assignments.Projects[projectID]
	if !ok {
		return nil, fmt.Errorf("project %q not found in assignment config", projectID)
	}

	res := &Resolved{
		ProjectID:    projectID,
		ByKind:       map[string][]ResolvedArtifact{},
		MCPDefaultOn: dedupe(proj.MCPDefaultOn),
	}

	var missing []string
	for _, kind := range artifactKinds {
		denied := sliceToSet(proj.Deny[kind])
		seen := map[string]bool{}
		for _, id := range proj.idsForKind(kind) {
			if denied[id] || seen[id] {
				continue
			}
			seen[id] = true
			ref, ok := a.Artifacts[kind][id]
			if !ok || strings.TrimSpace(ref.Path) == "" {
				missing = append(missing, kind+"/"+id)
				continue
			}
			res.ByKind[kind] = append(res.ByKind[kind], ResolvedArtifact{ID: id, Path: ref.Path})
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("assignment references unregistered artifacts: %s", strings.Join(missing, ", "))
	}
	return res, nil
}

// sliceToSet builds a lookup set from a string slice.
func sliceToSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}

// dedupe returns ss with duplicates removed, preserving first-seen order, and
// never returns nil (so it JSON-encodes as []).
func dedupe(ss []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range ss {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
