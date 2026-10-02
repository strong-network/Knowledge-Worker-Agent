// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package git

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

func TestCloneFromLocalRepo(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, src)
	gitCommit(t, src, "README.md", "hello\n", "first")
	base := t.TempDir()

	dest, out, err := Clone("file://"+src, "", base)
	if err != nil {
		t.Fatalf("Clone: %v\n%s", err, out)
	}
	if dest != filepath.Join(base, "src") || !isGitRepo(dest) {
		t.Fatalf("dest = %q, repo = %v", dest, isGitRepo(dest))
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "README.md")); string(b) != "hello\n" {
		t.Errorf("README = %q", b)
	}
}

// Printed by the workspace's git wrapper (/strong/bin/git), which then exits 0
// without running git. Worded as the wrapper prints them.
const (
	wrapperNoToken = "You do not have a token deployed for gitlab.com.\n" +
		"Do you wish to deploy a code application token key before continuing ? Input `yes or y` to proceed\n" +
		"unable to handle HTTP Git: unable to handle no deployed token for git application: gitlab.com: unable to read input to proceed: EOF"
	wrapperRestricted = "This workspace has restricted access to repositories hosted at github.com" +
		"Do you wish to proceed anyways ? Input `yes or y` to proceed\n" +
		"unable to handle HTTP Git: failed to proceed: unable to read input to proceed: EOF"
)

// fakeGit puts a `git` on PATH that prints stdout and exits 0 without cloning,
// as the wrapper does when it refuses a host. It records GIT_TERMINAL_PROMPT.
func fakeGit(t *testing.T, stdout string) (envFile string) {
	t.Helper()
	dir := t.TempDir()
	envFile = filepath.Join(dir, "env")
	if err := os.WriteFile(filepath.Join(dir, "out"), []byte(stdout), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"GIT_TERMINAL_PROMPT=$GIT_TERMINAL_PROMPT\" > \"" + envFile + "\"\ncat \"" + filepath.Join(dir, "out") + "\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return envFile
}

func TestCloneTreatsExitZeroWithoutARepoAsFailure(t *testing.T) {
	envFile := fakeGit(t, wrapperNoToken)
	base := t.TempDir()

	dest, out, err := Clone("https://gitlab.com/group/proj.git", "", base)
	if err == nil {
		t.Fatalf("Clone reported success: dest=%q", dest)
	}
	if dest != "" || !strings.Contains(out, "no deployed token") {
		t.Errorf("dest = %q, output = %q", dest, out)
	}
	if entries, _ := os.ReadDir(base); len(entries) != 0 {
		t.Errorf("left behind in base: %v", entries)
	}
	if b, _ := os.ReadFile(envFile); strings.TrimSpace(string(b)) != "GIT_TERMINAL_PROMPT=0" {
		t.Errorf("git ran with %q; a credential prompt could wait forever", b)
	}
}

func cloneRequest(t *testing.T, url string) (int, CloneResult) {
	t.Helper()
	prev := config.Workspace
	config.Workspace = t.TempDir()
	t.Cleanup(func() { config.Workspace = prev })
	rec := httptest.NewRecorder()
	HandleClone(rec, httptest.NewRequest(http.MethodPost, "/api/git/clone",
		strings.NewReader(`{"url":"`+url+`"}`)))
	var res CloneResult
	_ = json.NewDecoder(rec.Body).Decode(&res)
	return rec.Code, res
}

// The modal turns these into "connect the provider first" or "add the repo to
// the workspace", with a link to the page where the token is deployed.
func TestHandleCloneExplainsAWrapperRefusal(t *testing.T) {
	t.Setenv("STRONG_NETWORK_DOMAIN", "portal.example.test")
	cases := []struct {
		name, stdout, url            string
		blocked, host, tokenURL, err string
	}{
		{"no token", wrapperNoToken, "https://gitlab.com/group/proj.git",
			BlockedNoToken, "gitlab.com", "https://portal.example.test/profile/integrations/repositories_tokens",
			"clone failed: the workspace has no token for gitlab.com"},
		{"restricted host", wrapperRestricted, "https://github.com/owner/repo.git",
			BlockedRestricted, "github.com", "",
			"clone failed: this workspace may only clone the github.com repositories added to it"},
		{"unrecognised output", "something else went wrong\n", "https://example.com/x/y.git",
			"", "", "", "clone failed: something else went wrong"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeGit(t, c.stdout)
			code, res := cloneRequest(t, c.url)
			if code != http.StatusBadGateway || res.Success || res.Workspace != "" {
				t.Fatalf("status %d, result %+v", code, res)
			}
			if res.Blocked != c.blocked || res.Host != c.host || res.TokenURL != c.tokenURL || res.Error != c.err {
				t.Errorf("got blocked=%q host=%q token_url=%q error=%q", res.Blocked, res.Host, res.TokenURL, res.Error)
			}
		})
	}
}

// The link goes into an href, so a domain that isn't a plain host gives none.
func TestTokenDeployURLNeedsAPlainDomain(t *testing.T) {
	for domain, want := range map[string]string{
		"":                   "",
		"example.com":        "https://example.com/profile/integrations/repositories_tokens",
		"evil.test/phish?x=": "",
		"user@evil.test":     "",
		"a.test b":           "",
	} {
		t.Setenv("STRONG_NETWORK_DOMAIN", domain)
		if got := tokenDeployURL(); got != want {
			t.Errorf("domain %q: got %q, want %q", domain, got, want)
		}
	}
}
