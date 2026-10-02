// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package projects

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/git"
)

// initLocalRepo creates a git repo at dir with one committed file, usable as a
// clone source (no network).
func initLocalRepo(t *testing.T, dir, file, content string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("checkout", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
}

func TestProvisionWorkspace_FreshFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	res, err := ProvisionWorkspace("My Cool Project!!", "")
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	if res.RepoURL != "" {
		t.Errorf("expected no repo url, got %q", res.RepoURL)
	}
	// Slugified folder under ~/Projects.
	want := filepath.Join(home, "Projects", "my-cool-project")
	if res.WorkspacePath != want {
		t.Errorf("workspace = %q, want %q", res.WorkspacePath, want)
	}
	if info, err := os.Stat(res.WorkspacePath); err != nil || !info.IsDir() {
		t.Errorf("workspace dir not created: %v", err)
	}
}

func TestProvisionWorkspace_FreshFolderUniquifies(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	a, err := ProvisionWorkspace("Dupe", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ProvisionWorkspace("Dupe", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.WorkspacePath == b.WorkspacePath {
		t.Errorf("expected unique dirs for same-named projects, both = %q", a.WorkspacePath)
	}
}

func TestClone_LocalUpstream(t *testing.T) {
	// ProvisionWorkspace clones via git.Clone. IsSafeRepoURL intentionally
	// rejects local filesystem paths (only https/ssh/git remotes are allowed),
	// so we exercise the clone mechanism directly against a local upstream to
	// prove a repo-backed workspace ends up with the repo's files.
	upstream := t.TempDir()
	initLocalRepo(t, upstream, "README.md", "# hello from repo\n")

	base := t.TempDir()
	dest, out, err := git.Clone(upstream, "repo-project", base)
	if err != nil {
		t.Fatalf("git.Clone: %v\n%s", err, out)
	}
	if filepath.Dir(dest) != base {
		t.Errorf("clone dest %q not under base %q", dest, base)
	}
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Errorf("cloned file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".git")); err != nil {
		t.Errorf("expected a git repo (missing .git): %v", err)
	}
}

func TestProvisionWorkspace_RejectsBadRepoURL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Unsafe / local-path URLs are rejected (only real git remotes allowed).
	for _, bad := range []string{"--not-a-url", "/tmp/some/local/path", "file:///tmp/x"} {
		if _, err := ProvisionWorkspace("X", bad); err == nil {
			t.Errorf("expected error for repo url %q", bad)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Acme Deal":          "acme-deal",
		"  Trimmed  ":        "trimmed",
		"Weird__Chars!!@#":   "weird-chars",
		"multiple   spaces":  "multiple-spaces",
		"":                  "project",
		"---":               "project",
		"Café résumé":       "caf-rsum",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
