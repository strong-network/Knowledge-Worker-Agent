// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package materializer

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSyncRepoLocalPath(t *testing.T) {
	repo := exampleRepoDir(t)
	dir, err := SyncRepo(nil, repo, t.TempDir())
	if err != nil {
		t.Fatalf("sync local: %v", err)
	}
	if dir != repo {
		t.Fatalf("local path source should be used in place, got %s", dir)
	}
}

func TestSyncRepoCloneAndPull(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// Build a throwaway upstream git repo from the example.
	upstream := filepath.Join(t.TempDir(), "upstream")
	if err := copyDir(exampleRepoDir(t), upstream); err != nil {
		t.Fatal(err)
	}
	gitInit(t, upstream)
	upstreamURL := "file://" + upstream

	cache := filepath.Join(t.TempDir(), "cache", "repo")
	// First sync clones.
	dir, err := SyncRepo(nil, upstreamURL, cache)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if dir != cache {
		t.Fatalf("expected clone into cache dir, got %s", dir)
	}
	mustExist(t, filepath.Join(cache, "assignment.jsonc"))

	// Second sync pulls into the existing cache (idempotent).
	if _, err := SyncRepo(nil, upstreamURL, cache); err != nil {
		t.Fatalf("pull: %v", err)
	}
}

func TestSyncRepoUnreachableUsesCache(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	upstream := filepath.Join(t.TempDir(), "upstream")
	if err := copyDir(exampleRepoDir(t), upstream); err != nil {
		t.Fatal(err)
	}
	gitInit(t, upstream)
	upstreamURL := "file://" + upstream
	cache := filepath.Join(t.TempDir(), "cache", "repo")
	if _, err := SyncRepo(nil, upstreamURL, cache); err != nil {
		t.Fatal(err)
	}
	// Remove the upstream so a pull can't reach it; cached clone must still work.
	os.RemoveAll(upstream)
	dir, err := SyncRepo(nil, upstreamURL, cache)
	if err != nil {
		t.Fatalf("unreachable should fall back to cache, got err: %v", err)
	}
	if dir != cache {
		t.Fatalf("expected cached clone, got %s", dir)
	}
}

func TestRunSkipsWithoutProjectOrRepo(t *testing.T) {
	r, err := Run(Options{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !r.Skipped {
		t.Fatal("expected skip when project id unset")
	}

	r, err = Run(Options{ProjectID: "proj_team"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Skipped {
		t.Fatal("expected skip when repo source unset")
	}
}

func TestRunEndToEndLocalRepo(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "platform-opencode")
	r, err := Run(Options{
		ProjectID:  "proj_team",
		RepoSource: exampleRepoDir(t),
		ConfigDir:  cfg,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r.Skipped {
		t.Fatalf("unexpected skip: %s", r.SkipReason)
	}
	mustExist(t, filepath.Join(cfg, "opencode.json"))
	mustExist(t, filepath.Join(cfg, "agent", "writer.md"))
	if r.Counts[KindAgents] != 1 || r.Counts[KindMCP] != 3 {
		t.Fatalf("counts = %v, want 1 agent and 3 connectors", r.Counts)
	}
}

func TestFromEnvDerivesDefaults(t *testing.T) {
	t.Setenv(EnvProjectID, "proj_x")
	t.Setenv(EnvConfigRepo, "/some/repo")
	t.Setenv(EnvCacheDir, "")
	t.Setenv(EnvConfigDir, "")
	userGlobal := "/home/dev/.config/opencode"
	o := Options{}.FromEnv(userGlobal)
	if o.ProjectID != "proj_x" || o.RepoSource != "/some/repo" {
		t.Fatalf("env not read: %+v", o)
	}
	if o.ConfigDir != filepath.Join("/home/dev/.config", "opencode-platform") {
		t.Fatalf("derived config dir wrong: %s", o.ConfigDir)
	}
	if o.UserGlobalDir != userGlobal {
		t.Fatalf("user global not set: %s", o.UserGlobalDir)
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"add", "-A"},
		{"commit", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}
