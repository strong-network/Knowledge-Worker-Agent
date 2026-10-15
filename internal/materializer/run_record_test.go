// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package materializer

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRepoCommitReadsTheCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	if err := copyDir(exampleRepoDir(t), repo); err != nil {
		t.Fatal(err)
	}
	gitInit(t, repo)
	c := RepoCommit(repo)
	if len(c.Hash) != 40 || c.Subject != "init" || c.Date.IsZero() {
		t.Fatalf("RepoCommit = %+v, want the init commit", c)
	}
	// A folder inside the checkout is not a checkout of its own.
	if c := RepoCommit(filepath.Join(repo, "artifacts")); c.Hash != "" {
		t.Fatalf("subfolder reported commit %s", c.Hash)
	}
	if c := RepoCommit(t.TempDir()); c.Hash != "" {
		t.Fatalf("plain folder reported commit %s", c.Hash)
	}
}

func TestRunRecordsAStaleCacheAndTheCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	upstream := filepath.Join(t.TempDir(), "upstream")
	if err := copyDir(exampleRepoDir(t), upstream); err != nil {
		t.Fatal(err)
	}
	gitInit(t, upstream)
	opts := Options{
		ProjectID:  "proj_team",
		RepoSource: "file://" + upstream,
		CacheDir:   filepath.Join(t.TempDir(), "cache", "repo"),
		ConfigDir:  filepath.Join(t.TempDir(), "platform"),
	}
	res, err := Run(opts)
	if err != nil || res.Stale || len(res.Commit.Hash) != 40 || res.Source != opts.RepoSource {
		t.Fatalf("first run: %+v, %v", res, err)
	}
	os.RemoveAll(upstream)
	res, err = Run(opts)
	if err != nil || !res.Stale || res.StaleReason == "" || len(res.Commit.Hash) != 40 {
		t.Fatalf("unreachable repo: want the cached clone, marked stale; got %+v, %v", res, err)
	}
	rec, ok := LastRun()
	if !ok || !rec.Result.Stale || rec.Err != nil || rec.At.IsZero() {
		t.Fatalf("LastRun = %+v, %v", rec, ok)
	}
}

func TestLastRunRecordsAFailure(t *testing.T) {
	_, err := Run(Options{
		ProjectID:  "proj_team",
		RepoSource: filepath.Join(t.TempDir(), "missing"),
		CacheDir:   filepath.Join(t.TempDir(), "cache"),
		ConfigDir:  filepath.Join(t.TempDir(), "platform"),
	})
	rec, ok := LastRun()
	if err == nil || !ok || rec.Err == nil || rec.Err.Error() != err.Error() {
		t.Fatalf("LastRun = %+v, %v; Run error %v", rec, ok, err)
	}
}

func TestRunFromAFolderInsideAnotherRepositoryReportsNoCommit(t *testing.T) {
	res, err := Run(Options{
		ProjectID:  "proj_team",
		RepoSource: exampleRepoDir(t),
		ConfigDir:  filepath.Join(t.TempDir(), "platform"),
	})
	if err != nil || res.Commit.Hash != "" {
		t.Fatalf("commit = %q, err %v; want none", res.Commit.Hash, err)
	}
}

func TestLastAppliedSurvivesALaterFailure(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "platform")
	if _, err := Run(Options{ProjectID: "proj_team", RepoSource: exampleRepoDir(t), ConfigDir: cfg}); err != nil {
		t.Fatal(err)
	}
	ok, _ := LastApplied()
	if _, err := Run(Options{ProjectID: "proj_team", RepoSource: filepath.Join(t.TempDir(), "missing"), CacheDir: t.TempDir(), ConfigDir: cfg}); err == nil {
		t.Fatal("want an error")
	}
	last, _ := LastRun()
	applied, has := LastApplied()
	if !has || !applied.At.Equal(ok.At) || last.Err == nil || last.At.Equal(applied.At) {
		t.Fatalf("applied %+v, last %+v", applied, last)
	}
	if _, err := Run(Options{ProjectID: ""}); err != nil {
		t.Fatal(err)
	}
	if again, _ := LastApplied(); !again.At.Equal(ok.At) {
		t.Fatal("a skipped run must not count as applied")
	}
}
