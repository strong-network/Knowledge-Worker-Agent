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

// sharedRepo builds an upstream holding the example configuration in config/,
// beside code it should not check out, and returns its file:// URL.
func sharedRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	upstream := filepath.Join(t.TempDir(), "upstream")
	if err := copyDir(exampleRepoDir(t), filepath.Join(upstream, "config")); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"code/main.go": "package main\n", "README.md": "shared\n"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(upstream, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(upstream, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitInit(t, upstream)
	// Serve partial clones, as a hosted repository does.
	if out, err := exec.Command("git", "-C", upstream, "config", "uploadpack.allowFilter", "true").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return "file://" + upstream
}

func TestSyncRepoSubdirClonesOnlyThatFolder(t *testing.T) {
	url := sharedRepo(t)
	cache := filepath.Join(t.TempDir(), "cache", "repo")
	root, dir, _, err := syncRepo(nil, url, "config/", cache)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if root != cache || dir != filepath.Join(cache, "config") {
		t.Fatalf("root %s, dir %s", root, dir)
	}
	mustExist(t, filepath.Join(dir, "assignment.jsonc"))
	if _, err := os.Stat(filepath.Join(cache, "code")); !os.IsNotExist(err) {
		t.Fatal("checked out a folder outside the configuration")
	}
	if c := RepoCommit(root); len(c.Hash) != 40 {
		t.Fatalf("commit = %+v, want the repository's", c)
	}

	// A pull keeps the folder; dropping the setting checks out the whole
	// repository, and setting it again narrows it back.
	if _, _, stale, err := syncRepo(nil, url, "config", cache); err != nil || stale != nil {
		t.Fatalf("pull: %v %v", err, stale)
	}
	if _, dir, _, err := syncRepo(nil, url, "", cache); err != nil || dir != cache {
		t.Fatalf("whole repo: %s, %v", dir, err)
	}
	mustExist(t, filepath.Join(cache, "code", "main.go"))
	if _, _, _, err := syncRepo(nil, url, "config", cache); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cache, "code")); !os.IsNotExist(err) {
		t.Fatal("the folder setting was not applied to the cached clone")
	}
}

func TestSyncRepoSubdirOfALocalPath(t *testing.T) {
	parent := t.TempDir()
	if err := copyDir(exampleRepoDir(t), filepath.Join(parent, "config")); err != nil {
		t.Fatal(err)
	}
	root, dir, _, err := syncRepo(nil, parent, "config", t.TempDir())
	if err != nil || root != parent || dir != filepath.Join(parent, "config") {
		t.Fatalf("root %s, dir %s, err %v", root, dir, err)
	}
}

func TestSyncRepoRejectsABadSubdir(t *testing.T) {
	url := sharedRepo(t)
	for _, sub := range []string{"../config", "/etc", "config/../../x"} {
		cache := filepath.Join(t.TempDir(), "repo")
		if _, _, _, err := syncRepo(nil, url, sub, cache); err == nil {
			t.Errorf("%q: accepted a folder outside the repository", sub)
		}
		if _, err := os.Stat(cache); !os.IsNotExist(err) {
			t.Errorf("%q: cloned before checking the folder", sub)
		}
	}
	if _, _, _, err := syncRepo(nil, url, "missing", filepath.Join(t.TempDir(), "repo")); err == nil {
		t.Error("accepted a folder the repository does not have")
	}
}

// Pointing the source at another repository replaces the cached clone; before,
// the workspace kept pulling the old one.
func TestSyncRepoReclonesWhenTheSourceChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	old := filepath.Join(t.TempDir(), "old")
	if err := copyDir(exampleRepoDir(t), old); err != nil {
		t.Fatal(err)
	}
	gitInit(t, old)
	cache := filepath.Join(t.TempDir(), "cache", "repo")
	if _, err := SyncRepo(nil, "file://"+old, cache); err != nil {
		t.Fatal(err)
	}
	url := sharedRepo(t)
	_, dir, _, err := syncRepo(nil, url, "config", cache)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	mustExist(t, filepath.Join(dir, "assignment.jsonc"))
	if got := originURL(cache); got != url {
		t.Fatalf("cached clone is of %s, want %s", got, url)
	}
}

func TestRunWithASubdir(t *testing.T) {
	url := sharedRepo(t)
	cfg := filepath.Join(t.TempDir(), "platform")
	res, err := Run(Options{
		ProjectID:  "proj_team",
		RepoSource: url,
		RepoSubdir: "config",
		CacheDir:   filepath.Join(t.TempDir(), "cache", "repo"),
		ConfigDir:  cfg,
	})
	if err != nil || res.Skipped {
		t.Fatalf("run: %+v, %v", res, err)
	}
	if len(res.Commit.Hash) != 40 || filepath.Base(res.RepoDir) != "config" {
		t.Fatalf("commit %q, repo dir %s", res.Commit.Hash, res.RepoDir)
	}
	mustExist(t, filepath.Join(cfg, "agent", "writer.md"))
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
	t.Setenv(EnvConfigSubdir, " config ")
	t.Setenv(EnvCacheDir, "")
	t.Setenv(EnvConfigDir, "")
	userGlobal := "/home/dev/.config/opencode"
	o := Options{}.FromEnv(userGlobal)
	if o.ProjectID != "proj_x" || o.RepoSource != "/some/repo" || o.RepoSubdir != "config" {
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
