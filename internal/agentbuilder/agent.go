// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package agentbuilder models an OpenCode agent file (for the Agent Builder) and
// provides deterministic serialization to / parsing from the single markdown
// file OpenCode consumes (YAML frontmatter + prompt body).
//
// The whole point of the Agent Builder is that the app owns the file's location and format:
// typed fields are serialized to well-formed frontmatter (never hand-typed), so
// OpenCode's silent-skip-on-malformed-YAML failure mode cannot occur for files
// the builder writes. The serializer here is intentionally hand-rolled (the repo
// is standard-library-only, plus modernc.org/sqlite) and emits a small, fixed,
// known schema with deterministic key ordering and controlled quoting.
package agentbuilder

// Mode is the agent's OpenCode "mode" (subagent | primary | all).
type Mode string

const (
	ModeSubagent Mode = "subagent"
	ModePrimary  Mode = "primary"
	ModeAll      Mode = "all"
)

// Action is a permission action (allow | ask | deny), matching OpenCode's
// PermissionActionConfig = Literals(["ask","allow","deny"]).
type Action string

const (
	ActionAllow Action = "allow"
	ActionAsk   Action = "ask"
	ActionDeny  Action = "deny"
)

// PermissionCapabilities is the fixed set of 15 capability keys OpenCode's
// permission schema defines (v1/config/permission.ts). The builder must not emit
// a key outside this set. Order here is the canonical serialization order.
var PermissionCapabilities = []string{
	"read", "edit", "glob", "grep", "list", "bash", "task",
	"external_directory", "todowrite", "question", "webfetch",
	"websearch", "lsp", "doom_loop", "skill",
}

// permissionCapabilitySet is the lookup form of PermissionCapabilities.
var permissionCapabilitySet = func() map[string]bool {
	m := make(map[string]bool, len(PermissionCapabilities))
	for _, c := range PermissionCapabilities {
		m[c] = true
	}
	return m
}()

// Permission is the builder's v1 (blanket-only) view of an OpenCode permission
// block. Global is the top-level "*" wildcard entry (the "global default"); an
// empty Global means no "*" key is written (absence = inherit defaults). Rules
// holds per-capability overrides keyed by one of PermissionCapabilities.
//
// v1 deliberately exposes only blanket allow/ask/deny (a plain string action per
// capability); OpenCode's per-pattern map form is not surfaced.
type Permission struct {
	Global Action            `json:"global,omitempty"`
	Rules  map[string]Action `json:"rules,omitempty"`
}

// IsZero reports whether the permission block is entirely unset, in which case
// the serializer omits the `permission:` key altogether (inherit defaults).
func (p *Permission) IsZero() bool {
	return p == nil || (p.Global == "" && len(p.Rules) == 0)
}

// Agent is the full, typed representation of an OpenCode agent file. Name is not
// a frontmatter key — it is the file stem (OpenCode derives the agent name from
// the path). Prompt is the markdown body, not a YAML key. Every other field maps
// to a frontmatter key and is omitted from the file when left at its zero value
// (absence = OpenCode default), per the spec's "omit the key, never write a
// placeholder" rule.
type Agent struct {
	// Name is the display/identity name; it is slugified to the filename.
	Name string `json:"name"`
	// Description is the routing signal ("when to use this agent").
	Description string `json:"description"`
	// Prompt is the system prompt = the markdown body.
	Prompt string `json:"prompt"`

	// Advanced (all optional; omitted when zero).
	Model       string   `json:"model,omitempty"`
	Mode        Mode     `json:"mode,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	Steps       *int     `json:"steps,omitempty"`
	Color       string   `json:"color,omitempty"`
	Hidden      bool     `json:"hidden,omitempty"`
	Disable     bool     `json:"disable,omitempty"`

	Permission *Permission `json:"permission,omitempty"`
}

// ValidCapability reports whether key is one of the 15 permission capabilities.
func ValidCapability(key string) bool { return permissionCapabilitySet[key] }

// ValidAction reports whether a is one of allow|ask|deny.
func ValidAction(a Action) bool {
	return a == ActionAllow || a == ActionAsk || a == ActionDeny
}

// ValidMode reports whether m is one of subagent|primary|all.
func ValidMode(m Mode) bool {
	return m == ModeSubagent || m == ModePrimary || m == ModeAll
}
