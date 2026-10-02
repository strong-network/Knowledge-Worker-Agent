// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package projects

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	gitops "github.com/strong-network/Knowledge-Worker-Agent/internal/git"
)

// withSyncStubs replaces the injected git/streaming functions for a test and
// restores them afterward. `streaming` is keyed by project id.
func withSyncStubs(
	t *testing.T,
	status func(string) gitops.Status,
	fetch func(string) (string, error),
	pull func(string) (string, error),
	streaming func(projectID string) bool,
) {
	t.Helper()
	pStatus, pFetch, pPull, pStream := syncStatus, syncFetch, syncPullFF, projectStreaming
	syncStatus, syncFetch, syncPullFF, projectStreaming = status, fetch, pull, streaming
	t.Cleanup(func() { syncStatus, syncFetch, syncPullFF, projectStreaming = pStatus, pFetch, pPull, pStream })
}

func repoProject() db.Project {
	return db.Project{ID: "p1", Name: "Proj", WorkspacePath: "/tmp/p1", RepoURL: "https://x/y.git"}
}

func TestSyncProject_FastForwardsWhenSafe(t *testing.T) {
	pulled := false
	withSyncStubs(t,
		func(string) gitops.Status { return gitops.Status{IsRepo: true, HasRemote: true, Behind: 3, Ahead: 0, Dirty: false} },
		func(string) (string, error) { return "", nil },
		func(string) (string, error) { pulled = true; return "Updating…", nil },
		func(string) bool { return false },
	)
	if !syncProject(repoProject()) || !pulled {
		t.Error("expected a fast-forward pull when clean+behind+not-ahead+not-streaming")
	}
}

func TestSyncProject_SkipsWhenDirty(t *testing.T) {
	withSyncStubs(t,
		func(string) gitops.Status { return gitops.Status{IsRepo: true, HasRemote: true, Behind: 2, Dirty: true} },
		func(string) (string, error) { return "", nil },
		func(string) (string, error) { t.Fatal("must not pull a dirty workspace"); return "", nil },
		func(string) bool { return false },
	)
	if syncProject(repoProject()) {
		t.Error("expected skip when dirty")
	}
}

func TestSyncProject_SkipsWhenAhead(t *testing.T) {
	withSyncStubs(t,
		func(string) gitops.Status { return gitops.Status{IsRepo: true, HasRemote: true, Behind: 2, Ahead: 1} },
		func(string) (string, error) { return "", nil },
		func(string) (string, error) { t.Fatal("must not pull a diverged workspace"); return "", nil },
		func(string) bool { return false },
	)
	if syncProject(repoProject()) {
		t.Error("expected skip when ahead (diverged)")
	}
}

func TestSyncProject_SkipsWhenNotBehind(t *testing.T) {
	withSyncStubs(t,
		func(string) gitops.Status { return gitops.Status{IsRepo: true, HasRemote: true, Behind: 0} },
		func(string) (string, error) { return "", nil },
		func(string) (string, error) { t.Fatal("nothing to pull"); return "", nil },
		func(string) bool { return false },
	)
	if syncProject(repoProject()) {
		t.Error("expected skip when up to date")
	}
}

func TestSyncProject_SkipsWhenStreaming(t *testing.T) {
	withSyncStubs(t,
		func(string) gitops.Status { return gitops.Status{IsRepo: true, HasRemote: true, Behind: 2, Ahead: 0} },
		func(string) (string, error) { return "", nil },
		func(string) (string, error) { t.Fatal("must not pull while a chat streams"); return "", nil },
		func(projectID string) bool { return projectID == "p1" },
	)
	if syncProject(repoProject()) {
		t.Error("expected deferral while a project chat is streaming")
	}
}

func TestSyncProject_SkipsWhenFetchFails(t *testing.T) {
	withSyncStubs(t,
		func(string) gitops.Status { return gitops.Status{IsRepo: true, HasRemote: true, Behind: 2} },
		func(string) (string, error) { return "auth failed", os.ErrPermission },
		func(string) (string, error) { t.Fatal("must not pull if fetch failed"); return "", nil },
		func(string) bool { return false },
	)
	if syncProject(repoProject()) {
		t.Error("expected skip when fetch fails")
	}
}

func TestSyncProject_SkipsNonRepoOrNoRemote(t *testing.T) {
	// Not a repo.
	withSyncStubs(t,
		func(string) gitops.Status { return gitops.Status{IsRepo: false} },
		func(string) (string, error) { t.Fatal("must not fetch a non-repo"); return "", nil },
		func(string) (string, error) { return "", nil },
		func(string) bool { return false },
	)
	if syncProject(repoProject()) {
		t.Error("expected skip when not a repo")
	}
}

// TestSyncProject_RealFastForward exercises the actual git helpers end-to-end:
// a clone that is behind its upstream must be fast-forwarded by a sync pass.
func TestSyncProject_RealFastForward(t *testing.T) {
	upstream := t.TempDir()
	gitInitRepo(t, upstream, "a.txt", "v1")

	clone := t.TempDir()
	if out, err := exec.Command("git", "clone", "-q", upstream, clone).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	// New commit lands upstream after the clone.
	gitCommitRepo(t, upstream, "a.txt", "v2", "v2")

	// No stubs — use the real syncStatus/Fetch/PullFF; only stub streaming=false.
	withSyncStubs(t, gitStatusReal, gitFetchReal, gitPullReal, func(string) bool { return false })

	p := db.Project{ID: "px", Name: "Real", WorkspacePath: clone, RepoURL: upstream}
	if !syncProject(p) {
		t.Fatal("expected a real fast-forward pull")
	}
	got, _ := os.ReadFile(filepath.Join(clone, "a.txt"))
	if string(got) != "v2" {
		t.Errorf("expected file fast-forwarded to v2, got %q", got)
	}
}

// Real git funcs (so the stubs helper signature is satisfied).
func gitStatusReal(dir string) gitops.Status         { return gitops.StatusAt(dir) }
func gitFetchReal(dir string) (string, error)         { return gitops.FetchAt(dir) }
func gitPullReal(dir string) (string, error)          { return gitops.PullFastForwardAt(dir) }

func gitInitRepo(t *testing.T, dir, file, content string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
}

func gitCommitRepo(t *testing.T, dir, file, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", msg}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}
