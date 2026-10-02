// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package projects

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// setup points HOME at a temp dir (so provisioning writes under a throwaway
// ~/Projects) and opens a temp SQLite DB.
func setup(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	config.Workspace = filepath.Join(home, "workspace")

	f, err := os.CreateTemp("", "projtest-*.db")
	if err != nil {
		t.Fatal(err)
	}
	dbPath := f.Name()
	f.Close()
	t.Cleanup(func() { db.Close(); os.Remove(dbPath) })
	if err := db.Init(dbPath); err != nil {
		t.Fatal(err)
	}
	return home
}

func createProjectViaHandler(t *testing.T, body string) *db.Project {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/projects", strings.NewReader(body))
	w := httptest.NewRecorder()
	HandleCreate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HandleCreate: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var p db.Project
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	return &p
}

func TestHandleCreate_ProvisionsFolder(t *testing.T) {
	home := setup(t)

	p := createProjectViaHandler(t, `{"name":"Acme Deal","description":"the acme engagement"}`)

	if p.ID == "" || p.Name != "Acme Deal" || p.Description != "the acme engagement" {
		t.Errorf("unexpected project: %+v", p)
	}
	// Workspace must be a real directory under ~/Projects.
	wantPrefix := filepath.Join(home, "Projects")
	if !strings.HasPrefix(p.WorkspacePath, wantPrefix) {
		t.Errorf("workspace %q not under %q", p.WorkspacePath, wantPrefix)
	}
	if info, err := os.Stat(p.WorkspacePath); err != nil || !info.IsDir() {
		t.Errorf("expected provisioned workspace dir at %q: %v", p.WorkspacePath, err)
	}
	// Folder name is the slug of the project name.
	if filepath.Base(p.WorkspacePath) != "acme-deal" {
		t.Errorf("expected slug folder 'acme-deal', got %q", filepath.Base(p.WorkspacePath))
	}
	if p.RepoURL != "" {
		t.Errorf("expected no repo url, got %q", p.RepoURL)
	}
}

func TestHandleCreate_RequiresName(t *testing.T) {
	setup(t)
	req := httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"  "}`))
	w := httptest.NewRecorder()
	HandleCreate(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty name, got %d", w.Code)
	}
}

func TestHandleCreate_RejectsBadRepoURL(t *testing.T) {
	setup(t)
	req := httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"X","repo_url":"not-a-url"}`))
	w := httptest.NewRecorder()
	HandleCreate(w, req)
	if w.Code == http.StatusOK {
		t.Fatalf("expected failure for bad repo url, got 200: %s", w.Body.String())
	}
	// And no project should have been recorded.
	if len(db.ListProjects()) != 0 {
		t.Error("no project should be created when provisioning fails")
	}
}

func TestHandleListAndGet(t *testing.T) {
	setup(t)
	p := createProjectViaHandler(t, `{"name":"P1"}`)

	// List
	req := httptest.NewRequest("GET", "/api/projects", nil)
	w := httptest.NewRecorder()
	HandleList(w, req)
	var listResp struct {
		Projects []db.Project `json:"projects"`
	}
	json.Unmarshal(w.Body.Bytes(), &listResp)
	if len(listResp.Projects) != 1 || listResp.Projects[0].ID != p.ID {
		t.Errorf("list mismatch: %+v", listResp.Projects)
	}

	// Get
	req = httptest.NewRequest("GET", "/api/projects/"+p.ID, nil)
	req.SetPathValue("id", p.ID)
	w = httptest.NewRecorder()
	HandleGet(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HandleGet: %d", w.Code)
	}

	// Get missing → 404
	req = httptest.NewRequest("GET", "/api/projects/nope", nil)
	req.SetPathValue("id", "nope")
	w = httptest.NewRecorder()
	HandleGet(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing project, got %d", w.Code)
	}
}

func TestHandleNewChat_UsesProjectWorkspace(t *testing.T) {
	setup(t)
	p := createProjectViaHandler(t, `{"name":"Proj"}`)
	// Give the project some instructions to verify they flow into the chat.
	if err := db.UpdateProjectMeta(p.ID, "", "always be concise", nil); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/api/projects/"+p.ID+"/chats", strings.NewReader(`{}`))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	HandleNewChat(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HandleNewChat: %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		SessionID string              `json:"session_id"`
		ProjectID string              `json:"project_id"`
		Config    config.SessionConfig `json:"config"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.ProjectID != p.ID {
		t.Errorf("expected project_id %q, got %q", p.ID, resp.ProjectID)
	}
	// Working directory must be the shared project workspace (no auto per-chat
	// folder).
	if resp.Config.Workdir != p.WorkspacePath {
		t.Errorf("expected workdir %q, got %q", p.WorkspacePath, resp.Config.Workdir)
	}
	// Project instructions inherited.
	if resp.Config.ProjectInstructions != "always be concise" {
		t.Errorf("expected inherited instructions, got %q", resp.Config.ProjectInstructions)
	}
	// The session must be linked to the project.
	if db.GetSessionProject(resp.SessionID) != p.ID {
		t.Error("session not linked to project")
	}
	// And appear in the project's chat list.
	if len(db.ListProjectSessions(p.ID)) != 1 {
		t.Error("expected 1 chat in project")
	}
}

func TestHandleStarChat(t *testing.T) {
	setup(t)
	p := createProjectViaHandler(t, `{"name":"Proj"}`)
	if err := db.CreateSession("c1", config.DefaultSessionConfig()); err != nil {
		t.Fatal(err)
	}
	db.SetSessionProject("c1", p.ID)

	req := httptest.NewRequest("POST", "/api/sessions/c1/project-star", strings.NewReader(`{"starred":true}`))
	req.SetPathValue("session_id", "c1")
	w := httptest.NewRecorder()
	HandleStarChat(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HandleStarChat: %d", w.Code)
	}
	list := db.ListProjectSessions(p.ID)
	if len(list) != 1 || !list[0].ProjectStarred {
		t.Errorf("expected c1 starred, got %+v", list)
	}
}

func TestHandleUpdate_RenameAndMeta(t *testing.T) {
	setup(t)
	p := createProjectViaHandler(t, `{"name":"Old"}`)

	req := httptest.NewRequest("PATCH", "/api/projects/"+p.ID,
		strings.NewReader(`{"name":"New","instructions":"be terse","data_sources":["jira","slack"]}`))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	HandleUpdate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HandleUpdate: %d: %s", w.Code, w.Body.String())
	}
	got := db.GetProject(p.ID)
	if got.Name != "New" || got.Instructions != "be terse" {
		t.Errorf("update not applied: %+v", got)
	}
	if len(got.DataSources) != 2 {
		t.Errorf("data sources: %v", got.DataSources)
	}
}

func TestHandleDelete_UnlinksChatsByDefault(t *testing.T) {
	setup(t)
	p := createProjectViaHandler(t, `{"name":"Proj"}`)
	db.CreateSession("c1", config.DefaultSessionConfig())
	db.SetSessionProject("c1", p.ID)

	req := httptest.NewRequest("DELETE", "/api/projects/"+p.ID, nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	HandleDelete(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HandleDelete: %d", w.Code)
	}
	if db.ProjectExists(p.ID) {
		t.Error("project should be deleted")
	}
	if !db.SessionExists("c1") {
		t.Error("chat should survive by default (unlinked, not deleted)")
	}
	if db.GetSessionProject("c1") != "" {
		t.Error("chat should be unlinked")
	}
}

func TestHandleDelete_WithDeleteChats(t *testing.T) {
	setup(t)
	p := createProjectViaHandler(t, `{"name":"Proj"}`)
	db.CreateSession("c1", config.DefaultSessionConfig())
	db.SetSessionProject("c1", p.ID)

	req := httptest.NewRequest("DELETE", "/api/projects/"+p.ID+"?delete_chats=true", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	HandleDelete(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HandleDelete: %d", w.Code)
	}
	if db.SessionExists("c1") {
		t.Error("chat should be deleted with ?delete_chats=true")
	}
}

// newChatViaHandler starts a chat in a project via the handler and returns the
// session id and its resolved config.
func newChatViaHandler(t *testing.T, projectID string) (string, config.SessionConfig) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/projects/"+projectID+"/chats", strings.NewReader(`{}`))
	req.SetPathValue("id", projectID)
	w := httptest.NewRecorder()
	HandleNewChat(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HandleNewChat: %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		SessionID string               `json:"session_id"`
		Config    config.SessionConfig `json:"config"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode new chat: %v", err)
	}
	return resp.SessionID, resp.Config
}

// TestSharedWorkspaceAcrossChats verifies the core promise of projects: all chats in
// a project share ONE workspace, so a file produced in one project chat is
// visible to every other chat in the project (and to the user directly).
func TestSharedWorkspaceAcrossChats(t *testing.T) {
	setup(t)
	p := createProjectViaHandler(t, `{"name":"Shared Proj"}`)

	// Two chats started in the same project.
	c1, cfg1 := newChatViaHandler(t, p.ID)
	c2, cfg2 := newChatViaHandler(t, p.ID)

	// Both chats must resolve to the SAME workspace — the project's shared
	// workspace — and NOT get their own per-chat auto-folders.
	if cfg1.Workdir != p.WorkspacePath {
		t.Errorf("chat1 workdir %q != project workspace %q", cfg1.Workdir, p.WorkspacePath)
	}
	if cfg1.Workdir != cfg2.Workdir {
		t.Errorf("project chats do not share a workspace: %q vs %q", cfg1.Workdir, cfg2.Workdir)
	}

	// Read each chat's persisted config back to confirm the workdir is durable
	// (not just in the create response) and both point at the shared dir.
	for _, sid := range []string{c1, c2} {
		got, err := db.GetSessionConfig(sid)
		if err != nil {
			t.Fatalf("GetSessionConfig(%s): %v", sid, err)
		}
		if got.Workdir != p.WorkspacePath {
			t.Errorf("session %s persisted workdir %q != %q", sid, got.Workdir, p.WorkspacePath)
		}
		if db.GetSessionProject(sid) != p.ID {
			t.Errorf("session %s not linked to project", sid)
		}
	}

	// Simulate an agent (in chat 1) writing an artifact into the shared
	// workspace. It must be visible via the SAME directory chat 2 uses — proof
	// that work produced in one project chat is shared across the project.
	artifact := filepath.Join(cfg1.Workdir, "brief.md")
	if err := os.WriteFile(artifact, []byte("# Shared brief\nproduced in chat 1\n"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	fromChat2 := filepath.Join(cfg2.Workdir, "brief.md")
	data, err := os.ReadFile(fromChat2)
	if err != nil {
		t.Fatalf("artifact not visible from chat 2's workspace: %v", err)
	}
	if !strings.Contains(string(data), "Shared brief") {
		t.Errorf("unexpected shared artifact contents: %q", data)
	}
}
