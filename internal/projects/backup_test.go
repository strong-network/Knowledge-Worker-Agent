// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package projects

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

func bareRemote(t *testing.T) string {
	t.Helper()
	remote := t.TempDir()
	cmd := exec.Command("git", "init", "--bare", "-q", remote)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}
	return remote
}

func TestHandleSetKeep(t *testing.T) {
	setup(t)
	p := createProjectViaHandler(t, `{"name":"Keeper"}`)

	req := httptest.NewRequest("POST", "/api/projects/"+p.ID+"/keep", strings.NewReader(`{"keep":true}`))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	HandleSetKeep(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !db.GetProjectKeep(p.ID) {
		t.Error("project should be Keep after HandleSetKeep(true)")
	}

	// Unknown project → 404.
	req = httptest.NewRequest("POST", "/api/projects/nope/keep", strings.NewReader(`{"keep":true}`))
	req.SetPathValue("id", "nope")
	w = httptest.NewRecorder()
	HandleSetKeep(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown project, got %d", w.Code)
	}
}

func TestHandleEnableBackupAndState(t *testing.T) {
	setup(t)
	p := createProjectViaHandler(t, `{"name":"Backed"}`)
	// Drop a deliverable into the provisioned workspace.
	if err := os.WriteFile(filepath.Join(p.WorkspacePath, "out.md"), []byte("# out\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	remote := bareRemote(t)
	body := `{"remote_url":"` + remote + `"}`
	req := httptest.NewRequest("POST", "/api/projects/"+p.ID+"/backup/enable", strings.NewReader(body))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	HandleEnableBackup(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("enable backup: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp projectBackupResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != db.BackupStatusSynced {
		t.Errorf("status = %q, want %q", resp.Status, db.BackupStatusSynced)
	}
	if resp.RepoURL != remote {
		t.Errorf("repo_url = %q, want %q (should be recorded on enable)", resp.RepoURL, remote)
	}

	// GET status reflects the same.
	req = httptest.NewRequest("GET", "/api/projects/"+p.ID+"/backup", nil)
	req.SetPathValue("id", p.ID)
	w = httptest.NewRecorder()
	HandleBackupState(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get backup state: expected 200, got %d", w.Code)
	}
	var got projectBackupResponse
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.Status != db.BackupStatusSynced || got.RepoURL != remote {
		t.Errorf("state mismatch: %+v", got)
	}
}

func TestHandleEnableBackupRequiresRemote(t *testing.T) {
	setup(t)
	p := createProjectViaHandler(t, `{"name":"NoRemote"}`)
	req := httptest.NewRequest("POST", "/api/projects/"+p.ID+"/backup/enable", strings.NewReader(`{}`))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	HandleEnableBackup(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when no remote_url and no repo_url, got %d", w.Code)
	}
}
