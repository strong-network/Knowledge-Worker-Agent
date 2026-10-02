// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessions

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
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

func TestHandleBackupState_Temporary(t *testing.T) {
	setupTestDB(t)
	if err := db.CreateSession("temp", config.SessionConfig{Temporary: true}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/sessions/temp/backup", nil)
	req.SetPathValue("session_id", "temp")
	w := httptest.NewRecorder()
	HandleBackupState(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp backupStateResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Temporary || resp.Status != db.BackupStatusSkipped {
		t.Errorf("temporary chat should report not-backed-up, got %+v", resp)
	}
}

func TestHandleEnableBackup_ChatHappyPath(t *testing.T) {
	setupTestDB(t)
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "note.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession("c1", config.SessionConfig{Workdir: work}); err != nil {
		t.Fatal(err)
	}
	remote := bareRemote(t)
	req := httptest.NewRequest("POST", "/api/sessions/c1/backup/enable",
		strings.NewReader(`{"remote_url":"`+remote+`"}`))
	req.SetPathValue("session_id", "c1")
	w := httptest.NewRecorder()
	HandleEnableBackup(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp backupStateResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Status != db.BackupStatusSynced || resp.Remote != remote {
		t.Errorf("unexpected state after enable: %+v", resp)
	}
}

func TestHandleEnableBackup_RejectsTemporary(t *testing.T) {
	setupTestDB(t)
	if err := db.CreateSession("ct", config.SessionConfig{Temporary: true, Workdir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	remote := bareRemote(t)
	req := httptest.NewRequest("POST", "/api/sessions/ct/backup/enable",
		strings.NewReader(`{"remote_url":"`+remote+`"}`))
	req.SetPathValue("session_id", "ct")
	w := httptest.NewRecorder()
	HandleEnableBackup(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for Temporary chat, got %d", w.Code)
	}
}

func TestHandleEnableBackup_RequiresRemote(t *testing.T) {
	setupTestDB(t)
	if err := db.CreateSession("cn", config.SessionConfig{Workdir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/sessions/cn/backup/enable", strings.NewReader(`{}`))
	req.SetPathValue("session_id", "cn")
	w := httptest.NewRecorder()
	HandleEnableBackup(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without remote_url, got %d", w.Code)
	}
}

// --- config-update invariants ---

func patchConfig(t *testing.T, sid, body string) config.SessionConfig {
	t.Helper()
	req := httptest.NewRequest("PATCH", "/api/sessions/"+sid+"/config", strings.NewReader(body))
	req.SetPathValue("session_id", sid)
	w := httptest.NewRecorder()
	HandleUpdateSessionConfig(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH config: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var cfg config.SessionConfig
	_ = json.Unmarshal(w.Body.Bytes(), &cfg)
	return cfg
}

func TestConfigUpdate_TemporaryIsCreationOnly(t *testing.T) {
	setupTestDB(t)
	// Chat created NOT temporary; a later attempt to set temporary is ignored.
	if err := db.CreateSession("s1", config.SessionConfig{Temporary: false}); err != nil {
		t.Fatal(err)
	}
	cfg := patchConfig(t, "s1", `{"temporary":true}`)
	if cfg.Temporary {
		t.Error("Temporary must not be settable via mid-chat config update")
	}

	// Chat created temporary; a later attempt to clear it is ignored.
	if err := db.CreateSession("s2", config.SessionConfig{Temporary: true}); err != nil {
		t.Fatal(err)
	}
	cfg = patchConfig(t, "s2", `{"temporary":false}`)
	if !cfg.Temporary {
		t.Error("Temporary must not be clearable via mid-chat config update")
	}
}

func TestConfigUpdate_KeepMutuallyExclusiveWithTemporary(t *testing.T) {
	setupTestDB(t)
	if err := db.CreateSession("s3", config.SessionConfig{Temporary: true}); err != nil {
		t.Fatal(err)
	}
	cfg := patchConfig(t, "s3", `{"keep":true}`)
	if cfg.Keep {
		t.Error("a Temporary chat must not be able to set Keep")
	}
}

func TestConfigUpdate_ProjectChatKeepMapsToProject(t *testing.T) {
	setupTestDB(t)
	if err := db.CreateProject("p1", "P", "", "/tmp/p1", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession("s4", config.SessionConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionProject("s4", "p1"); err != nil {
		t.Fatal(err)
	}
	cfg := patchConfig(t, "s4", `{"keep":true}`)
	// Chat-level keep is not stored; it maps to the project.
	if cfg.Keep {
		t.Error("project-chat Keep should not be stored on the chat config")
	}
	if !db.GetProjectKeep("p1") {
		t.Error("project-chat Keep should map to project-level Keep")
	}
}

func TestConfigUpdate_LooseChatKeepStoredOnChat(t *testing.T) {
	setupTestDB(t)
	if err := db.CreateSession("s5", config.SessionConfig{}); err != nil {
		t.Fatal(err)
	}
	cfg := patchConfig(t, "s5", `{"keep":true}`)
	if !cfg.Keep {
		t.Error("a loose chat's Keep should be stored on the chat config")
	}
}
