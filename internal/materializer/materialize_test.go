// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package materializer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMaterializeExampleTeam(t *testing.T) {
	repo := exampleRepoDir(t)
	a, err := ParseAssignment(readExampleAssignment(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Resolve("proj_team")
	if err != nil {
		t.Fatal(err)
	}
	// The example leaves mcp_default_on out; set one to check it is carried.
	res.MCPDefaultOn = []string{"mcp.atlassian.v1"}

	cfg := filepath.Join(t.TempDir(), "platform-opencode")
	if _, err := Materialize(cfg, repo, res, ""); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	// Agents written as markdown files.
	mustExist(t, filepath.Join(cfg, "agent", "writer.md"))

	// Skill written as a folder with SKILL.md, under the plural dir name that
	// internal/defaults also installs into.
	mustExist(t, filepath.Join(cfg, "skills", "meeting-notes", "SKILL.md"))

	// Context files copied and referenced via instructions.
	mustExist(t, filepath.Join(cfg, "context", "style.md"))

	// Vocabulary copied beside, not into, the OpenCode config.
	mustExist(t, filepath.Join(cfg, VocabularyDir, "001-org.md"))

	// opencode.json: merged mcp, instructions list, no enabled key.
	oc := readJSON(t, filepath.Join(cfg, "opencode.json"))
	mcp, ok := oc["mcp"].(map[string]any)
	if !ok || mcp["microsoft-learn"] == nil || mcp["atlassian"] == nil || mcp["acme-docs"] == nil {
		t.Fatalf("mcp not merged correctly: %v", oc["mcp"])
	}
	if g, _ := mcp["atlassian"].(map[string]any); g["enabled"] != nil {
		t.Fatalf("assigned mcp must not carry an enabled key: %v", g)
	}
	instr, ok := oc["instructions"].([]any)
	if !ok || len(instr) != 1 {
		t.Fatalf("instructions = %v, want 1 entry", oc["instructions"])
	}

	// mcp_default_on carried as sidecar metadata, NOT in opencode.json.
	if oc["mcp_default_on"] != nil {
		t.Fatalf("mcp_default_on must not appear in opencode.json")
	}
	meta := readJSON(t, filepath.Join(cfg, "mcp-default-on.json"))
	if on, _ := meta["mcp_default_on"].([]any); len(on) != 1 || on[0] != "mcp.atlassian.v1" {
		t.Fatalf("mcp-default-on.json = %v", meta)
	}
}

func TestMaterializeProviderAndIdempotentRemoval(t *testing.T) {
	repo := exampleRepoDir(t)
	a, err := ParseAssignment(readExampleAssignment(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "platform-opencode")

	// First materialize the team project (has a skill + mcp).
	team, _ := a.Resolve("proj_team")
	if _, err := Materialize(cfg, repo, team, ""); err != nil {
		t.Fatal(err)
	}
	mustExist(t, filepath.Join(cfg, "skills", "meeting-notes", "SKILL.md"))

	// Re-materialize as the on-prem project: provider provisioned, and the
	// team-only skill/mcp must be REMOVED (owned dir rewritten idempotently).
	onprem, _ := a.Resolve("proj_onprem")
	if _, err := Materialize(cfg, repo, onprem, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg, "skills", "meeting-notes")); !os.IsNotExist(err) {
		t.Fatalf("stale skill was not removed on re-materialize")
	}
	if _, err := os.Stat(filepath.Join(cfg, VocabularyDir)); !os.IsNotExist(err) {
		t.Fatalf("stale vocabulary was not removed on re-materialize")
	}
	oc := readJSON(t, filepath.Join(cfg, "opencode.json"))
	prov, ok := oc["provider"].(map[string]any)
	if !ok || prov["onprem"] == nil {
		t.Fatalf("provider not merged: %v", oc["provider"])
	}
	if mcp, _ := oc["mcp"].(map[string]any); len(mcp) != 0 {
		t.Fatalf("onprem lists no mcp, got %v", mcp)
	}
}

// Skills go to the plural `skills/`, the same name internal/defaults installs
// into the user's Global. OpenCode loads either name from a config dir, so a
// regression here is silent at runtime — hence pinning it.
//
// Also covers the upgrade path: an earlier build wrote the singular `skill/`,
// and because the materializer owns and rewrites configDir wholesale, the
// leftover must be gone rather than lingering as a second copy that OpenCode
// would still pick up.
func TestMaterializeSkillsDirIsPluralAndClearsLegacySingular(t *testing.T) {
	repo := exampleRepoDir(t)
	a, err := ParseAssignment(readExampleAssignment(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Resolve("proj_team")
	if err != nil {
		t.Fatal(err)
	}

	cfg := filepath.Join(t.TempDir(), "platform-opencode")

	// Simulate a config dir materialized by the previous (singular) build.
	legacy := filepath.Join(cfg, "skill", "meeting-notes")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "SKILL.md"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Materialize(cfg, repo, res, ""); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	mustExist(t, filepath.Join(cfg, "skills", "meeting-notes", "SKILL.md"))
	if _, err := os.Stat(filepath.Join(cfg, "skill")); !os.IsNotExist(err) {
		t.Fatalf("legacy singular skill/ dir survived materialize (err=%v)", err)
	}
}

func TestMaterializeRefusesUserGlobal(t *testing.T) {
	dir := t.TempDir()
	res := &Resolved{ProjectID: "p", ByKind: map[string][]ResolvedArtifact{}, MCPDefaultOn: []string{}}
	if _, err := Materialize(dir, dir, res, dir); err == nil {
		t.Fatal("expected refusal to write to user Global dir")
	}
}

// Dictation merges vocabularies in assignment order and reads them sorted by
// name, so the prefix must follow assignment order, not the file names.
func TestMaterializeVocabularyKeepsAssignmentOrder(t *testing.T) {
	repo := t.TempDir()
	for _, name := range []string{"zeta.md", "alpha.md"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("| Term |\n|---|\n| "+name+" |\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res := &Resolved{ProjectID: "p", MCPDefaultOn: []string{}, ByKind: map[string][]ResolvedArtifact{
		KindVocabulary: {{ID: "v.company", Path: "zeta.md"}, {ID: "v.team", Path: "alpha.md"}},
	}}
	cfg := filepath.Join(t.TempDir(), "platform-opencode")
	if _, err := Materialize(cfg, repo, res, ""); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(cfg, VocabularyDir))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if len(got) != 2 || got[0] != "001-zeta.md" || got[1] != "002-alpha.md" {
		t.Fatalf("vocabulary files = %v, want [001-zeta.md 002-alpha.md]", got)
	}
	oc := readJSON(t, filepath.Join(cfg, "opencode.json"))
	if _, ok := oc["vocabulary"]; ok {
		t.Fatalf("vocabulary must not leak into opencode.json: %v", oc)
	}
}

// --- helpers ---

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return m
}
