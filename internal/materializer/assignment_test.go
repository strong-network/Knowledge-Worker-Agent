// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package materializer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStripJSONComments(t *testing.T) {
	in := []byte(`{
  // line comment
  "a": "keep // not a comment",
  /* block
     comment */
  "b": "with /* not */ comment"
}`)
	got := string(stripJSONComments(in))
	if want := `"keep // not a comment"`; !contains(got, want) {
		t.Fatalf("string with // was altered: %s", got)
	}
	if want := `"with /* not */ comment"`; !contains(got, want) {
		t.Fatalf("string with /* */ was altered: %s", got)
	}
	if contains(got, "line comment") || contains(got, "block") {
		t.Fatalf("comments not stripped: %s", got)
	}
}

func TestStripTrailingCommas(t *testing.T) {
	in := []byte(`{"a":[1,2,],"b":{"c":1,},"s":"x,]"}`)
	got := string(stripTrailingCommas(in))
	want := `{"a":[1,2],"b":{"c":1},"s":"x,]"}`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestParseAndResolveExampleRepo(t *testing.T) {
	data := readExampleAssignment(t)
	a, err := ParseAssignment(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if a.SchemaVersion != 2 {
		t.Fatalf("schema_version = %d, want 2", a.SchemaVersion)
	}

	// proj_team: union of its listed artifacts.
	team, err := a.Resolve("proj_team")
	if err != nil {
		t.Fatalf("resolve team: %v", err)
	}
	assertIDs(t, team, KindMCP, "mcp.microsoft-learn.v1", "mcp.atlassian.v1", "mcp.acme-docs.v1")
	assertIDs(t, team, KindAgents, "agent.writer.v1")
	assertIDs(t, team, KindContext, "context.style.v1")
	assertIDs(t, team, KindSkills, "skill.meeting-notes.v1")
	assertIDs(t, team, KindVocabulary, "vocabulary.org")
	if len(team.MCPDefaultOn) != 0 {
		t.Fatalf("mcp_default_on = %v", team.MCPDefaultOn)
	}

	// proj_onprem: a provider, and no mcp.
	onprem, err := a.Resolve("proj_onprem")
	if err != nil {
		t.Fatalf("resolve onprem: %v", err)
	}
	assertIDs(t, onprem, KindProvider, "provider.onprem.v1")
	if len(onprem.ByKind[KindMCP]) != 0 {
		t.Fatalf("onprem should have no mcp, got %v", onprem.ByKind[KindMCP])
	}

	// Every registered path must exist in the example repo.
	base := exampleRepoDir(t)
	for kind, arts := range a.Artifacts {
		for id, art := range arts {
			if _, err := os.Stat(filepath.Join(base, art.Path)); err != nil {
				t.Errorf("%s %s: path missing: %s (%v)", kind, id, art.Path, err)
			}
		}
	}
}

func TestResolveUnknownProject(t *testing.T) {
	a, err := ParseAssignment(readExampleAssignment(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Resolve("proj_does_not_exist"); err == nil {
		t.Fatal("expected error for unknown project")
	}
}

func TestResolveDenyRemoves(t *testing.T) {
	raw := `{
  "schema_version": 2,
  "artifacts": { "mcp": { "m.a": {"path":"a.json"}, "m.b": {"path":"b.json"} } },
  "assignments": { "projects": { "p": {
    "mcp": ["m.a", "m.b"],
    "deny": { "mcp": ["m.b"] }
  } } }
}`
	a, err := ParseAssignment([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.Resolve("p")
	if err != nil {
		t.Fatal(err)
	}
	assertIDs(t, r, KindMCP, "m.a")
}

func TestResolveUnregisteredArtifactErrors(t *testing.T) {
	raw := `{
  "schema_version": 2,
  "artifacts": { "mcp": {} },
  "assignments": { "projects": { "p": { "mcp": ["m.ghost"] } } }
}`
	a, err := ParseAssignment([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Resolve("p"); err == nil {
		t.Fatal("expected error for unregistered artifact reference")
	}
}

// --- helpers ---

func exampleRepoDir(t *testing.T) string {
	t.Helper()
	// test file lives in internal/materializer; example repo is at examples/config-repo.
	dir := filepath.Join("..", "..", "examples", "config-repo")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("example repo not found: %v", err)
	}
	return dir
}

func readExampleAssignment(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(exampleRepoDir(t), "assignment.jsonc"))
	if err != nil {
		t.Fatalf("read example assignment: %v", err)
	}
	return data
}

func assertIDs(t *testing.T, r *Resolved, kind string, want ...string) {
	t.Helper()
	got := r.ByKind[kind]
	if len(got) != len(want) {
		t.Fatalf("%s: got %d artifacts %v, want %v", kind, len(got), ids(got), want)
	}
	for i, w := range want {
		if got[i].ID != w {
			t.Fatalf("%s[%d] = %s, want %s", kind, i, got[i].ID, w)
		}
	}
}

func ids(arts []ResolvedArtifact) []string {
	out := []string{}
	for _, a := range arts {
		out = append(out, a.ID)
	}
	return out
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
