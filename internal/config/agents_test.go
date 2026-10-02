// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeAgentFile creates an OpenCode agent markdown file in dir.
func writeAgentFile(t *testing.T, dir, stem, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, stem+".md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", stem, err)
	}
}

func findAgent(agents []AgentInfo, id string) (AgentInfo, bool) {
	for _, a := range agents {
		if a.ID == id {
			return a, true
		}
	}
	return AgentInfo{}, false
}

// TestListAgents_ReportsFileStemAsID is the core regression test for the
// agent-pickup bug: the ID reported to the frontend (and passed to opencode's
// --agent) must be the file stem OpenCode resolves, not the display name.
func TestListAgents_ReportsFileStemAsID(t *testing.T) {
	dir := t.TempDir()
	OpencodeAgentsDir = dir
	// ListAgents also reads the platform tier from the environment; keep it out
	// of this test so an ambient value in the dev workspace can't leak in.
	t.Setenv(EnvOpencodeConfigDir, "")

	writeAgentFile(t, dir, "product-manager", "---\nname: \"Product Manager - Example\"\ndescription: \"Shapes product requirements.\"\nmode: all\n---\nBody.\n")

	agents := ListAgents()
	a, ok := findAgent(agents, "product-manager")
	if !ok {
		t.Fatalf("expected agent id 'product-manager', got %+v", agents)
	}
	if a.ID != "product-manager" {
		t.Errorf("ID = %q, want 'product-manager' (the opencode-resolvable stem)", a.ID)
	}
	if a.Name != "Product Manager - Example" {
		t.Errorf("Name = %q, want display label from frontmatter", a.Name)
	}
	if a.Description != "Shapes product requirements." {
		t.Errorf("Description = %q", a.Description)
	}
}

func TestParseAgentFile_FallsBackToTitleizedID(t *testing.T) {
	dir := t.TempDir()
	// No name: field — display name should derive from the stem.
	writeAgentFile(t, dir, "demo-creator", "---\ndescription: \"Builds demos.\"\nmode: all\n---\nBody.\n")

	info := parseAgentFile(filepath.Join(dir, "demo-creator.md"))
	if info.ID != "demo-creator" {
		t.Errorf("ID = %q, want 'demo-creator'", info.ID)
	}
	if info.Name != "Demo Creator" {
		t.Errorf("Name = %q, want 'Demo Creator' (titleized fallback)", info.Name)
	}
}

func TestListAgents_MissingDirReturnsEmptySlice(t *testing.T) {
	OpencodeAgentsDir = filepath.Join(t.TempDir(), "does-not-exist")
	t.Setenv(EnvOpencodeConfigDir, "")
	agents := ListAgents()
	if agents == nil {
		t.Fatal("expected non-nil empty slice, got nil")
	}
	if len(agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(agents))
	}
}

func TestTitleizeAgentID(t *testing.T) {
	cases := map[string]string{
		"product-manager":      "Product Manager",
		"second_brain":         "Second Brain",
		"account-intelligence": "Account Intelligence",
		"solo":                 "Solo",
	}
	for in, want := range cases {
		if got := titleizeAgentID(in); got != want {
			t.Errorf("titleizeAgentID(%q) = %q, want %q", in, got, want)
		}
	}
}

// setupTiers wires a user Global dir and a platform-owned config dir, and
// returns the two agent directories to write into.
func setupTiers(t *testing.T) (globalAgents, platformAgents string) {
	t.Helper()
	root := t.TempDir()

	globalAgents = filepath.Join(root, "opencode", "agent")
	if err := os.MkdirAll(globalAgents, 0o755); err != nil {
		t.Fatal(err)
	}
	OpencodeAgentsDir = globalAgents

	platformDir := filepath.Join(root, "opencode-platform")
	platformAgents = filepath.Join(platformDir, "agent")
	if err := os.MkdirAll(platformAgents, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvOpencodeConfigDir, platformDir)

	return globalAgents, platformAgents
}

// TestListAgents_IncludesPlatformDir is the regression test for the empty agent
// picker: with KWA_CENTRAL_CONFIG on, every agent lives in the platform-owned
// dir and the Global is empty, so reading only the Global returned nothing.
func TestListAgents_IncludesPlatformDir(t *testing.T) {
	_, platform := setupTiers(t)

	writeAgentFile(t, platform, "competitive-analyst", "---\nname: \"Competitive Analyst\"\ndescription: \"Sizes up rivals.\"\nmode: all\n---\nBody.\n")
	writeAgentFile(t, platform, "product-manager", "---\ndescription: \"Shapes requirements.\"\nmode: all\n---\nBody.\n")

	agents := ListAgents()
	if len(agents) != 2 {
		t.Fatalf("expected 2 materialized agents, got %d: %+v", len(agents), agents)
	}
	a, ok := findAgent(agents, "competitive-analyst")
	if !ok {
		t.Fatalf("expected the materialized agent to be listed, got %+v", agents)
	}
	if a.Name != "Competitive Analyst" {
		t.Errorf("Name = %q, want the frontmatter label", a.Name)
	}
}

func TestListAgents_MergesBothTiers(t *testing.T) {
	global, platform := setupTiers(t)

	writeAgentFile(t, global, "my-own-agent", "---\ndescription: \"Mine.\"\nmode: all\n---\nBody.\n")
	writeAgentFile(t, platform, "product-manager", "---\ndescription: \"Assigned.\"\nmode: all\n---\nBody.\n")

	agents := ListAgents()
	if len(agents) != 2 {
		t.Fatalf("expected both tiers to contribute, got %d: %+v", len(agents), agents)
	}
	for _, id := range []string{"my-own-agent", "product-manager"} {
		if _, ok := findAgent(agents, id); !ok {
			t.Errorf("expected %q in the merged list, got %+v", id, agents)
		}
	}
}

// Merging the tiers is only half the job: the picker has to be able to tell
// them apart. A flat list is what let an agent the administrator never assigned
// sit beside the assigned ones looking identical.
func TestListAgents_StampsSourcePerTier(t *testing.T) {
	global, platform := setupTiers(t)

	writeAgentFile(t, global, "my-own-agent", "---\ndescription: \"Mine.\"\nmode: all\n---\nBody.\n")
	writeAgentFile(t, platform, "product-manager", "---\ndescription: \"Assigned.\"\nmode: all\n---\nBody.\n")

	agents := ListAgents()
	if len(agents) != 2 {
		t.Fatalf("expected both tiers to contribute, got %d: %+v", len(agents), agents)
	}
	mine, ok := findAgent(agents, "my-own-agent")
	if !ok {
		t.Fatalf("expected the Global agent to be listed, got %+v", agents)
	}
	if mine.Source != AgentSourcePersonal {
		t.Errorf("Global agent Source = %q, want %q", mine.Source, AgentSourcePersonal)
	}
	assigned, ok := findAgent(agents, "product-manager")
	if !ok {
		t.Fatalf("expected the platform agent to be listed, got %+v", agents)
	}
	if assigned.Source != AgentSourceAssigned {
		t.Errorf("platform agent Source = %q, want %q", assigned.Source, AgentSourceAssigned)
	}
}

// With no central config there is no assignment to compare against, so nothing
// can honestly be called assigned. Labelling a Global agent "provided by IT"
// in an unmanaged workspace would invent an authority that does not exist.
func TestListAgents_UnsetPlatformDirMakesEverythingPersonal(t *testing.T) {
	dir := t.TempDir()
	OpencodeAgentsDir = dir
	t.Setenv(EnvOpencodeConfigDir, "")

	writeAgentFile(t, dir, "my-own-agent", "---\ndescription: \"Mine.\"\nmode: all\n---\nBody.\n")

	agents := ListAgents()
	if len(agents) != 1 {
		t.Fatalf("expected one agent, got %+v", agents)
	}
	if agents[0].Source != AgentSourcePersonal {
		t.Errorf("Source = %q, want %q", agents[0].Source, AgentSourcePersonal)
	}
}

// The platform tier is higher precedence in OpenCode, so it must win here too:
// offering the Global copy's description while the run uses the platform copy
// would be worse than not listing it at all.
func TestListAgents_PlatformOverridesGlobalOnSameID(t *testing.T) {
	global, platform := setupTiers(t)

	writeAgentFile(t, global, "product-manager", "---\nname: \"Mine\"\ndescription: \"User copy.\"\nmode: all\n---\nBody.\n")
	writeAgentFile(t, platform, "product-manager", "---\nname: \"Assigned\"\ndescription: \"Platform copy.\"\nmode: all\n---\nBody.\n")

	agents := ListAgents()
	if len(agents) != 1 {
		t.Fatalf("expected the same stem to collapse to one entry, got %d: %+v", len(agents), agents)
	}
	if agents[0].Name != "Assigned" || agents[0].Description != "Platform copy." {
		t.Errorf("expected the platform copy to win, got %+v", agents[0])
	}
	// The winner reports where the *running* copy came from. Reporting
	// "personal" here would file an agent the workspace actually runs from the
	// assignment under the user's own work.
	if agents[0].Source != AgentSourceAssigned {
		t.Errorf("Source = %q, want %q for an overridden agent", agents[0].Source, AgentSourceAssigned)
	}
}

func TestListAgents_UnsetPlatformDirReadsGlobalOnly(t *testing.T) {
	dir := t.TempDir()
	OpencodeAgentsDir = dir
	t.Setenv(EnvOpencodeConfigDir, "")

	writeAgentFile(t, dir, "my-own-agent", "---\ndescription: \"Mine.\"\nmode: all\n---\nBody.\n")

	agents := ListAgents()
	if len(agents) != 1 || agents[0].ID != "my-own-agent" {
		t.Errorf("expected the Global-only list to be unchanged, got %+v", agents)
	}
}

// A configured-but-absent platform dir is normal: the var can be exported by
// the workspace before anything has been materialized into it.
func TestListAgents_MissingPlatformDirIsHarmless(t *testing.T) {
	dir := t.TempDir()
	OpencodeAgentsDir = dir
	t.Setenv(EnvOpencodeConfigDir, filepath.Join(t.TempDir(), "not-created"))

	writeAgentFile(t, dir, "my-own-agent", "---\ndescription: \"Mine.\"\nmode: all\n---\nBody.\n")

	agents := ListAgents()
	if len(agents) != 1 || agents[0].ID != "my-own-agent" {
		t.Errorf("expected the Global agent to still be listed, got %+v", agents)
	}
}

// The platform dir is read per call, not resolved once: the materializer
// exports it partway through bootstrap, after config.Init() has already run.
func TestListAgents_PicksUpPlatformDirSetAfterInit(t *testing.T) {
	dir := t.TempDir()
	OpencodeAgentsDir = dir
	t.Setenv(EnvOpencodeConfigDir, "")

	if got := ListAgents(); len(got) != 0 {
		t.Fatalf("expected no agents before materialization, got %+v", got)
	}

	platformDir := t.TempDir()
	platformAgents := filepath.Join(platformDir, "agent")
	if err := os.MkdirAll(platformAgents, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAgentFile(t, platformAgents, "product-manager", "---\ndescription: \"Assigned.\"\nmode: all\n---\nBody.\n")
	t.Setenv(EnvOpencodeConfigDir, platformDir)

	agents := ListAgents()
	if len(agents) != 1 || agents[0].ID != "product-manager" {
		t.Errorf("expected the agent list to reflect a late-exported platform dir, got %+v", agents)
	}
}

func TestListAgents_SortedByID(t *testing.T) {
	global, platform := setupTiers(t)

	writeAgentFile(t, global, "zulu", "---\ndescription: \"z\"\n---\nBody.\n")
	writeAgentFile(t, platform, "alpha", "---\ndescription: \"a\"\n---\nBody.\n")
	writeAgentFile(t, global, "mike", "---\ndescription: \"m\"\n---\nBody.\n")

	agents := ListAgents()
	var ids []string
	for _, a := range agents {
		ids = append(ids, a.ID)
	}
	want := []string{"alpha", "mike", "zulu"}
	if len(ids) != len(want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("order = %v, want %v (stable order across requests)", ids, want)
		}
	}
}
