// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

func TestProject_CreateGetList(t *testing.T) {
	setupTestDB(t)

	if err := CreateProject("p1", "Acme Deal", "customer engagement", "/tmp/proj/p1", ""); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := CreateProject("p2", "Repo Project", "", "/tmp/proj/p2", "https://github.com/x/y.git"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	got := GetProject("p1")
	if got == nil {
		t.Fatal("expected project p1")
	}
	if got.Name != "Acme Deal" || got.Description != "customer engagement" || got.WorkspacePath != "/tmp/proj/p1" {
		t.Errorf("unexpected project fields: %+v", got)
	}
	if got.RepoURL != "" {
		t.Errorf("expected empty repo url, got %q", got.RepoURL)
	}
	if got.DataSources == nil || len(got.DataSources) != 0 {
		t.Errorf("expected empty non-nil data sources, got %v", got.DataSources)
	}

	repoProj := GetProject("p2")
	if repoProj == nil || repoProj.RepoURL != "https://github.com/x/y.git" {
		t.Errorf("expected repo url preserved, got %+v", repoProj)
	}

	if !ProjectExists("p1") || ProjectExists("nope") {
		t.Error("ProjectExists wrong")
	}

	all := ListProjects()
	if len(all) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(all))
	}
}

func TestProject_RenameAndUpdateMeta(t *testing.T) {
	setupTestDB(t)
	if err := CreateProject("p1", "Old", "", "/tmp/p1", ""); err != nil {
		t.Fatal(err)
	}

	if err := RenameProject("p1", "New Name"); err != nil {
		t.Fatalf("RenameProject: %v", err)
	}
	if GetProject("p1").Name != "New Name" {
		t.Error("rename did not persist")
	}

	if err := UpdateProjectMeta("p1", "desc updated", "always use Acme terms", []string{"salesforce", "jira"}); err != nil {
		t.Fatalf("UpdateProjectMeta: %v", err)
	}
	p := GetProject("p1")
	if p.Description != "desc updated" || p.Instructions != "always use Acme terms" {
		t.Errorf("meta not updated: %+v", p)
	}
	if len(p.DataSources) != 2 || p.DataSources[0] != "salesforce" || p.DataSources[1] != "jira" {
		t.Errorf("data sources not updated: %v", p.DataSources)
	}
}

func TestProject_McpSelection(t *testing.T) {
	setupTestDB(t)
	if err := CreateProject("p1", "Proj", "", "/tmp/p1", ""); err != nil {
		t.Fatal(err)
	}

	// Default: empty, non-nil map (marshals as {}).
	p := GetProject("p1")
	if p.McpSelection == nil || len(p.McpSelection) != 0 {
		t.Errorf("expected empty non-nil selection, got %v", p.McpSelection)
	}

	sel := map[string]bool{"github": true, "atlassian": false}
	if err := UpdateProjectMcpSelection("p1", sel); err != nil {
		t.Fatalf("UpdateProjectMcpSelection: %v", err)
	}
	p = GetProject("p1")
	if len(p.McpSelection) != 2 || !p.McpSelection["github"] || p.McpSelection["atlassian"] {
		t.Errorf("selection not persisted: %v", p.McpSelection)
	}

	// nil clears to empty map, not null.
	if err := UpdateProjectMcpSelection("p1", nil); err != nil {
		t.Fatalf("UpdateProjectMcpSelection(nil): %v", err)
	}
	if p = GetProject("p1"); p.McpSelection == nil || len(p.McpSelection) != 0 {
		t.Errorf("expected cleared selection, got %v", p.McpSelection)
	}
}

func TestProject_SessionLinkingAndScopedList(t *testing.T) {
	setupTestDB(t)
	if err := CreateProject("p1", "Proj", "", "/tmp/p1", ""); err != nil {
		t.Fatal(err)
	}

	// Two project chats + one loose chat.
	for _, id := range []string{"c1", "c2", "loose"} {
		if err := CreateSession(id, config.DefaultSessionConfig()); err != nil {
			t.Fatalf("CreateSession %s: %v", id, err)
		}
	}
	if err := SetSessionProject("c1", "p1"); err != nil {
		t.Fatal(err)
	}
	if err := SetSessionProject("c2", "p1"); err != nil {
		t.Fatal(err)
	}

	if GetSessionProject("c1") != "p1" {
		t.Error("c1 should belong to p1")
	}
	if GetSessionProject("loose") != "" {
		t.Error("loose chat should have no project")
	}

	// Chat count reflected on the project.
	if GetProject("p1").ChatCount != 2 {
		t.Errorf("expected chat_count=2, got %d", GetProject("p1").ChatCount)
	}

	// Star c2 → it must sort first in the project-scoped list.
	if err := SetProjectStarred("c2", true); err != nil {
		t.Fatal(err)
	}
	list := ListProjectSessions("p1")
	if len(list) != 2 {
		t.Fatalf("expected 2 project sessions, got %d", len(list))
	}
	if list[0].SessionID != "c2" || !list[0].ProjectStarred {
		t.Errorf("expected starred c2 first, got %+v", list[0])
	}
	if list[1].SessionID != "c1" {
		t.Errorf("expected c1 second, got %q", list[1].SessionID)
	}

	// The loose chat must NOT appear in the project list.
	for _, s := range list {
		if s.SessionID == "loose" {
			t.Error("loose chat leaked into project list")
		}
	}

	// ListSessions (global) must surface project_id + project_starred.
	for _, s := range ListSessions() {
		switch s.SessionID {
		case "c1":
			if s.ProjectID != "p1" {
				t.Errorf("c1 ProjectID = %q", s.ProjectID)
			}
		case "c2":
			if s.ProjectID != "p1" || !s.ProjectStarred {
				t.Errorf("c2 wrong: %+v", s)
			}
		case "loose":
			if s.ProjectID != "" {
				t.Errorf("loose ProjectID = %q", s.ProjectID)
			}
		}
	}
}

func TestProject_DeleteUnlinksSessions(t *testing.T) {
	setupTestDB(t)
	if err := CreateProject("p1", "Proj", "", "/tmp/p1", ""); err != nil {
		t.Fatal(err)
	}
	if err := CreateSession("c1", config.DefaultSessionConfig()); err != nil {
		t.Fatal(err)
	}
	if err := SetSessionProject("c1", "p1"); err != nil {
		t.Fatal(err)
	}

	if err := DeleteProject("p1"); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if ProjectExists("p1") {
		t.Error("project should be gone")
	}
	// The session survives but is unlinked (delete-project unlinks; the
	// handler layer decides whether to also delete the chats).
	if !SessionExists("c1") {
		t.Error("session should still exist after project delete")
	}
	if GetSessionProject("c1") != "" {
		t.Error("session should be unlinked from deleted project")
	}
}
