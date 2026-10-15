// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package materializer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaterializeReplacesTheDirWhole(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "platform")
	if err := os.MkdirAll(filepath.Join(cfg, "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "agent", "retired.md"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := exampleRepoDir(t)
	res := &Resolved{ProjectID: "p", MCPDefaultOn: []string{}, ByKind: map[string][]ResolvedArtifact{
		KindAgents: {{ID: "agent.writer.v1", Path: "artifacts/agents/writer.md"}},
	}}
	if _, err := Materialize(cfg, repo, res, ""); err != nil {
		t.Fatal(err)
	}
	mustExist(t, filepath.Join(cfg, "agent", "writer.md"))
	if _, err := os.Stat(filepath.Join(cfg, "agent", "retired.md")); !os.IsNotExist(err) {
		t.Fatalf("retired.md survived: %v", err)
	}
	for _, leftover := range []string{cfg + ".new", cfg + ".old"} {
		if _, err := os.Stat(leftover); !os.IsNotExist(err) {
			t.Fatalf("%s left behind", leftover)
		}
	}
}

func TestMaterializeFailureKeepsTheCurrentDir(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "platform")
	if err := os.MkdirAll(filepath.Join(cfg, "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "agent", "current.md"), []byte("in use"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A leftover from an interrupted run must not end up in the result.
	if err := os.MkdirAll(cfg+".new", 0o755); err != nil {
		t.Fatal(err)
	}
	res := &Resolved{ProjectID: "p", MCPDefaultOn: []string{}, ByKind: map[string][]ResolvedArtifact{
		KindAgents: {{ID: "agent.writer.v1", Path: "artifacts/agents/writer.md"}},
		KindSkills: {{ID: "skill.missing.v1", Path: "artifacts/skills/does-not-exist/"}},
	}}
	if _, err := Materialize(cfg, exampleRepoDir(t), res, ""); err == nil {
		t.Fatal("want an error for the missing skill")
	}
	if b, err := os.ReadFile(filepath.Join(cfg, "agent", "current.md")); err != nil || string(b) != "in use" {
		t.Fatalf("current config changed: %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(cfg, "agent", "writer.md")); !os.IsNotExist(err) {
		t.Fatal("a partial build leaked into the current config")
	}
	if _, err := os.Stat(cfg + ".new"); !os.IsNotExist(err) {
		t.Fatal("staging dir left behind")
	}
}
