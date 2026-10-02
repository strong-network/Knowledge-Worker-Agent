// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package agents

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/agentbuilder"
)

func mustAgent(name, desc, prompt string) *agentbuilder.Agent {
	return &agentbuilder.Agent{Name: name, Description: desc, Prompt: prompt}
}

func TestExportRoundTrip(t *testing.T) {
	s := newStore(t)
	if _, err := s.Save(mustAgent("Bug Triage", "Triages bugs.", "Do triage."), ""); err != nil {
		t.Fatalf("Save: %v", err)
	}
	filename, content, err := s.Export("bug-triage")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if filename != "bug-triage.md" {
		t.Errorf("filename = %q", filename)
	}
	if !strings.Contains(string(content), "Triages bugs.") {
		t.Errorf("exported content missing description:\n%s", content)
	}
}

func TestExportProtectsShipped(t *testing.T) {
	s := newStore(t, "core.md")
	write(t, s, "core.md", "---\ndescription: core\n---\nbody")
	if _, _, err := s.Export("core"); err != ErrProtected {
		t.Errorf("Export shipped: err = %v, want ErrProtected", err)
	}
	if _, _, err := s.Export("ghost"); err != ErrNotFound {
		t.Errorf("Export missing: err = %v, want ErrNotFound", err)
	}
}

func TestExists(t *testing.T) {
	s := newStore(t, "shipped.md")
	if _, err := s.Save(mustAgent("Mine", "d", "p"), ""); err != nil {
		t.Fatal(err)
	}
	if !s.Exists("mine") {
		t.Error("Exists(mine) = false, want true (user file)")
	}
	if !s.Exists("shipped") {
		t.Error("Exists(shipped) = false, want true (shipped default)")
	}
	if s.Exists("nope") {
		t.Error("Exists(nope) = true, want false")
	}
}

const validUpload = "---\ndescription: \"Reviews PRs.\\nRequires: the gh MCP server\"\nmodel: openai/gpt-5\nmode: subagent\npermission:\n  \"*\": ask\n  bash: deny\n---\nYou review pull requests."

func TestPreviewImportValid(t *testing.T) {
	s := newStore(t)
	p := s.PreviewImport([]byte(validUpload), "pr-reviewer.md", "", []string{"anthropic/claude"})
	if !p.Valid {
		t.Fatalf("expected valid, got errors: %+v", p.Errors)
	}
	if p.Slug != "pr-reviewer" || p.Filename != "pr-reviewer.md" {
		t.Errorf("slug/filename = %q / %q", p.Slug, p.Filename)
	}
	if p.Name != "Pr Reviewer" {
		t.Errorf("name = %q, want derived from filename", p.Name)
	}
	if p.Requires != "the gh MCP server" {
		t.Errorf("requires = %q", p.Requires)
	}
	if p.ModelAvailable {
		t.Error("expected ModelAvailable=false (openai/gpt-5 not in the adopter's set)")
	}
	if p.Collision {
		t.Error("unexpected collision on a fresh store")
	}
	if p.Permission == nil || p.Permission.Global != "ask" || p.Permission.Rules["bash"] != "deny" {
		t.Errorf("permission not parsed: %+v", p.Permission)
	}
}

func TestPreviewImportCollision(t *testing.T) {
	s := newStore(t)
	if _, err := s.Save(mustAgent("PR Reviewer", "existing", "p"), ""); err != nil {
		t.Fatal(err)
	}
	p := s.PreviewImport([]byte(validUpload), "pr-reviewer.md", "", nil)
	if !p.Collision {
		t.Error("expected collision with existing pr-reviewer")
	}
	// A rename resolves it.
	p2 := s.PreviewImport([]byte(validUpload), "pr-reviewer.md", "PR Reviewer 2", nil)
	if p2.Collision {
		t.Error("rename should clear the collision")
	}
	if p2.Slug != "pr-reviewer-2" {
		t.Errorf("renamed slug = %q", p2.Slug)
	}
}

func TestPreviewImportMalformed(t *testing.T) {
	s := newStore(t)
	// Empty name (no filename, no frontmatter name source) → invalid.
	p := s.PreviewImport([]byte("just a body, no frontmatter"), "", "", nil)
	if p.Valid {
		t.Error("expected invalid for a nameless upload")
	}
	if len(p.Errors) == 0 {
		t.Error("expected validation errors")
	}
}

func TestImportPlacesFile(t *testing.T) {
	s := newStore(t)
	item, err := s.Import([]byte(validUpload), "pr-reviewer.md", "")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if item.ID != "pr-reviewer" {
		t.Errorf("item id = %q", item.ID)
	}
	got, err := s.Get("pr-reviewer")
	if err != nil {
		t.Fatalf("Get after import: %v", err)
	}
	if got.Model != "openai/gpt-5" {
		t.Errorf("imported model = %q", got.Model)
	}
}

func TestImportCollisionReturnsErr(t *testing.T) {
	s := newStore(t)
	if _, err := s.Import([]byte(validUpload), "pr-reviewer.md", ""); err != nil {
		t.Fatal(err)
	}
	_, err := s.Import([]byte(validUpload), "pr-reviewer.md", "")
	var ce ErrCollision
	if !errors.As(err, &ce) {
		t.Errorf("second import: err = %v, want ErrCollision", err)
	}
}

func TestHandlerImportPreviewAndCommit(t *testing.T) {
	s := newStore(t)
	h := &Handlers{Store: s, ModelsFn: func() []string { return []string{"anthropic/claude"} }}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Preview.
	body, _ := json.Marshal(map[string]string{"filename": "pr-reviewer.md", "content": validUpload})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/agent-builder/import/preview", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("preview status = %d", rr.Code)
	}
	var prev ImportPreview
	if err := json.Unmarshal(rr.Body.Bytes(), &prev); err != nil {
		t.Fatal(err)
	}
	if !prev.Valid || prev.ModelAvailable {
		t.Errorf("preview = %+v", prev)
	}

	// Commit.
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/agent-builder/import", bytes.NewReader(body)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("commit status = %d, body = %s", rr.Code, rr.Body.String())
	}

	// Second commit collides → 409.
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/agent-builder/import", bytes.NewReader(body)))
	if rr.Code != http.StatusConflict {
		t.Fatalf("duplicate commit status = %d, want 409", rr.Code)
	}
}

func TestHandlerExport(t *testing.T) {
	s := newStore(t)
	if _, err := s.Save(mustAgent("Docs Helper", "Helps.", "Help."), ""); err != nil {
		t.Fatal(err)
	}
	h := &Handlers{Store: s}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/agent-builder/agents/docs-helper/export", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("export status = %d", rr.Code)
	}
	if cd := rr.Header().Get("Content-Disposition"); !strings.Contains(cd, "docs-helper.md") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if !strings.Contains(rr.Body.String(), "Helps.") {
		t.Errorf("body missing description:\n%s", rr.Body.String())
	}
}
