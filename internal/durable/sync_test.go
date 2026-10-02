// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package durable

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// setupSyncDB initializes a temp SQLite DB for tests that touch backup state.
func setupSyncDB(t *testing.T) {
	t.Helper()
	f, err := os.CreateTemp("", "durable-*.db")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	t.Cleanup(func() {
		db.Close()
		os.Remove(path)
	})
	if err := db.Init(path); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

// newClonedRepo creates a bare "remote" and a working clone with one commit
// pushed, returning the working clone path. The clone has an origin remote and
// an upstream set, so CommitAndPush can push to it.
func newClonedRepo(t *testing.T) string {
	t.Helper()
	remote := t.TempDir()
	runGit(t, remote, "init", "--bare", "-q")

	clone := t.TempDir()
	cmd := exec.Command("git", "clone", "-q", remote, clone)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone failed: %v\n%s", err, out)
	}
	runGit(t, clone, "config", "user.email", "test@example.com")
	runGit(t, clone, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(clone, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, clone, "add", "-A")
	runGit(t, clone, "commit", "-q", "-m", "seed")
	// Push and set upstream so a later plain `git push` works.
	runGit(t, clone, "push", "-q", "-u", "origin", "HEAD")
	return clone
}

func TestSyncChatSkipsTemporary(t *testing.T) {
	setupSyncDB(t)
	if err := db.CreateSession("temp-chat", config.SessionConfig{Temporary: true}); err != nil {
		t.Fatal(err)
	}
	// Even with a valid repo workdir, a Temporary chat is never synced.
	repo := newClonedRepo(t)
	cfg := config.SessionConfig{Temporary: true, Workdir: repo}
	if SyncChat("temp-chat", cfg) {
		t.Error("SyncChat should be a no-op for Temporary chats")
	}
	if st := db.GetSessionBackupState("temp-chat"); st.Status != "" {
		t.Errorf("Temporary chat should have no backup status, got %q", st.Status)
	}
}

func TestSyncChatSkipsNonRepo(t *testing.T) {
	setupSyncDB(t)
	if err := db.CreateSession("plain-chat", config.SessionConfig{}); err != nil {
		t.Fatal(err)
	}
	cfg := config.SessionConfig{Workdir: t.TempDir()} // a dir, not a git repo
	if SyncChat("plain-chat", cfg) {
		t.Error("SyncChat should be a no-op when the workdir has no durable remote")
	}
	if st := db.GetSessionBackupState("plain-chat"); st.Status != "" {
		t.Errorf("non-repo chat should have no backup status, got %q", st.Status)
	}
}

func TestSyncChatCommitsAndRecordsSynced(t *testing.T) {
	setupSyncDB(t)
	repo := newClonedRepo(t)
	if err := db.CreateSession("real-chat", config.SessionConfig{Workdir: repo}); err != nil {
		t.Fatal(err)
	}

	// Produce a new deliverable in the workspace to be backed up.
	if err := os.WriteFile(filepath.Join(repo, "report.md"), []byte("# Report\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.SessionConfig{Workdir: repo}
	if !SyncChat("real-chat", cfg) {
		t.Fatal("SyncChat should attempt a sync for a repo-backed chat")
	}

	st := db.GetSessionBackupState("real-chat")
	if st.Status != db.BackupStatusSynced {
		t.Errorf("status = %q, want %q", st.Status, db.BackupStatusSynced)
	}
	if st.LastSyncedAt == "" {
		t.Error("last_synced_at should be set after a successful sync")
	}

	// The new file must be committed locally (nothing left dirty).
	out, err := exec.Command("git", "-C", repo, "status", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("workspace should be clean after sync, got dirty:\n%s", out)
	}
}

func TestSyncProjectCommitsAndRecordsSynced(t *testing.T) {
	setupSyncDB(t)
	repo := newClonedRepo(t)
	if err := db.CreateProject("proj-sync", "P", "", repo, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "out.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !SyncProject("proj-sync", repo) {
		t.Fatal("SyncProject should attempt a sync for a repo-backed project")
	}
	var status string
	if err := db.DB.QueryRow(
		`SELECT backup_status FROM projects WHERE id = ?`, "proj-sync",
	).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != db.BackupStatusSynced {
		t.Errorf("project status = %q, want %q", status, db.BackupStatusSynced)
	}
}

// newBareRemote creates a bare repo to serve as a durable push target.
func newBareRemote(t *testing.T) string {
	t.Helper()
	remote := t.TempDir()
	cmd := exec.Command("git", "init", "--bare", "-q", remote)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}
	return remote
}

func TestEnableBackupForChatProvisionsAndSyncs(t *testing.T) {
	setupSyncDB(t)
	remote := newBareRemote(t)

	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "deliverable.md"), []byte("# out\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession("chat-enable", config.SessionConfig{Workdir: work}); err != nil {
		t.Fatal(err)
	}

	cfg := config.SessionConfig{Workdir: work}
	out, err := EnableBackupForChat("chat-enable", "chat-enable", "", cfg, remote)
	if err != nil {
		t.Fatalf("EnableBackupForChat failed: %v\n%s", err, out)
	}

	// State recorded: remote + synced.
	st := db.GetSessionBackupState("chat-enable")
	if st.Remote != remote {
		t.Errorf("remote = %q, want %q", st.Remote, remote)
	}
	if st.Status != db.BackupStatusSynced {
		t.Errorf("status = %q, want %q", st.Status, db.BackupStatusSynced)
	}
	if st.LastSyncedAt == "" {
		t.Error("last_synced_at should be set")
	}

	// Folder convention + secret exclusions + metadata were created.
	for _, p := range []string{"inputs", "working", ".system", ".gitignore", filepath.Join(".system", "metadata")} {
		if _, err := os.Stat(filepath.Join(work, p)); err != nil {
			t.Errorf("expected %q to exist after enable: %v", p, err)
		}
	}
	gi, _ := os.ReadFile(filepath.Join(work, ".gitignore"))
	if !strings.Contains(string(gi), "*.pem") {
		t.Error("secret exclusions should be present in .gitignore")
	}
}

func TestEnableBackupForChatRejectsTemporary(t *testing.T) {
	setupSyncDB(t)
	remote := newBareRemote(t)
	work := t.TempDir()
	if err := db.CreateSession("chat-temp", config.SessionConfig{Temporary: true, Workdir: work}); err != nil {
		t.Fatal(err)
	}
	cfg := config.SessionConfig{Temporary: true, Workdir: work}
	if _, err := EnableBackupForChat("chat-temp", "chat-temp", "", cfg, remote); err != ErrTemporaryChat {
		t.Fatalf("expected ErrTemporaryChat, got %v", err)
	}
}

func TestEnableBackupForProjectProvisionsAndSyncs(t *testing.T) {
	setupSyncDB(t)
	remote := newBareRemote(t)
	work := t.TempDir()
	if err := db.CreateProject("proj-enable", "P", "", work, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := EnableBackupForProject("proj-enable", work, remote); err != nil {
		t.Fatalf("EnableBackupForProject failed: %v", err)
	}
	var status string
	if err := db.DB.QueryRow(`SELECT backup_status FROM projects WHERE id = ?`, "proj-enable").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != db.BackupStatusSynced {
		t.Errorf("project status = %q, want %q", status, db.BackupStatusSynced)
	}
}
