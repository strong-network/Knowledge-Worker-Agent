// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// setPATEnv points the PAT store at a temp file and clears token env vars so
// tests are hermetic. Returns the store path.
func setPATEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "github-pat.json")
	t.Setenv("COPILOT_GITHUB_PAT_PATH", path)
	for _, k := range githubTokenEnvVars {
		t.Setenv(k, "")
	}
	// Isolate the login token too: point auth.json at an empty dir.
	t.Setenv("XDG_DATA_HOME", dir)
	return path
}

func TestGitHubMCPToken_Precedence(t *testing.T) {
	setPATEnv(t)
	SetOnGitHubTokenChange(nil)

	// 1. Nothing configured → none.
	if tok, src := GitHubMCPToken(); tok != "" || src != TokenSourceNone {
		t.Fatalf("empty: got (%q,%s), want (\"\",none)", tok, src)
	}

	// 2. Stored PAT wins over nothing.
	if err := StoreGitHubPAT("pat-stored"); err != nil {
		t.Fatal(err)
	}
	if tok, src := GitHubMCPToken(); tok != "pat-stored" || src != TokenSourceStored {
		t.Fatalf("stored: got (%q,%s), want (pat-stored,stored)", tok, src)
	}

	// 3. Env wins over stored.
	t.Setenv("GITHUB_TOKEN", "pat-env")
	if tok, src := GitHubMCPToken(); tok != "pat-env" || src != TokenSourceEnv {
		t.Fatalf("env: got (%q,%s), want (pat-env,env)", tok, src)
	}
}

func TestStoreAndClearGitHubPAT(t *testing.T) {
	path := setPATEnv(t)
	SetOnGitHubTokenChange(nil)

	if err := StoreGitHubPAT("  tok-abc  "); err != nil {
		t.Fatal(err)
	}
	if got := LoadGitHubPAT(); got != "tok-abc" {
		t.Fatalf("LoadGitHubPAT = %q, want tok-abc (trimmed)", got)
	}
	// 0600 perms.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 600", perm)
	}

	if err := ClearGitHubPAT(); err != nil {
		t.Fatal(err)
	}
	if got := LoadGitHubPAT(); got != "" {
		t.Errorf("after clear LoadGitHubPAT = %q, want empty", got)
	}
	// Clearing a non-existent PAT is not an error.
	if err := ClearGitHubPAT(); err != nil {
		t.Errorf("second clear should be no-op, got %v", err)
	}
}

func TestTokenChangeCallbackFires(t *testing.T) {
	setPATEnv(t)
	fired := 0
	SetOnGitHubTokenChange(func() { fired++ })
	t.Cleanup(func() { SetOnGitHubTokenChange(nil) })

	_ = StoreGitHubPAT("x")
	_ = ClearGitHubPAT()
	if fired != 2 {
		t.Errorf("callback fired %d times, want 2", fired)
	}
}

// The hook is registered from the startup goroutine while HTTP handlers may
// already be storing a PAT, so registering and firing must not race. Only
// meaningful under -race, which is why the two run concurrently here.
func TestTokenChangeCallbackIsRaceFree(t *testing.T) {
	setPATEnv(t)
	t.Cleanup(func() { SetOnGitHubTokenChange(nil) })

	var fired atomic.Int32
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			SetOnGitHubTokenChange(func() { fired.Add(1) })
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = StoreGitHubPAT("x")
		}
	}()
	wg.Wait()
}

func TestValidateGitHubToken(t *testing.T) {
	// Classic PAT with repo scope.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-OAuth-Scopes", "read:user, repo, gist")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"login":"octocat"}`))
	}))
	defer srv.Close()
	orig := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = orig })

	chk := ValidateGitHubToken(context.Background(), "good")
	if !chk.Valid || chk.Login != "octocat" || !chk.CanReadPRs {
		t.Fatalf("good token: %+v", chk)
	}

	// Unauthorized token.
	if chk := ValidateGitHubToken(context.Background(), "bad"); chk.Valid || chk.Error == "" {
		t.Fatalf("bad token should be invalid: %+v", chk)
	}
}

func TestValidateGitHubToken_FineGrained(t *testing.T) {
	// Fine-grained PATs return no X-OAuth-Scopes header.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"login":"fg-user"}`))
	}))
	defer srv.Close()
	orig := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = orig })

	chk := ValidateGitHubToken(context.Background(), "fg")
	if !chk.Valid || !chk.CanReadPRs || len(chk.Scopes) != 0 {
		t.Fatalf("fine-grained: %+v", chk)
	}
}

func TestHandleSetAndClearGitHubPAT(t *testing.T) {
	setPATEnv(t)
	SetOnGitHubTokenChange(nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-OAuth-Scopes", "repo")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"login":"me"}`))
	}))
	defer srv.Close()
	orig := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = orig })

	// Set.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/github/pat", strings.NewReader(`{"token":"tok-1"}`))
	HandleSetGitHubPAT(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("set status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if LoadGitHubPAT() != "tok-1" {
		t.Errorf("PAT not stored, got %q", LoadGitHubPAT())
	}

	// Status reflects stored source.
	rr = httptest.NewRecorder()
	HandleGitHubPATStatus(rr, httptest.NewRequest(http.MethodGet, "/api/github/pat/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d", rr.Code)
	}

	// Empty token rejected.
	rr = httptest.NewRecorder()
	HandleSetGitHubPAT(rr, httptest.NewRequest(http.MethodPost, "/api/github/pat", strings.NewReader(`{"token":"  "}`)))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("empty token status = %d, want 400", rr.Code)
	}

	// Clear.
	rr = httptest.NewRecorder()
	HandleClearGitHubPAT(rr, httptest.NewRequest(http.MethodDelete, "/api/github/pat", nil))
	if rr.Code != http.StatusOK || LoadGitHubPAT() != "" {
		t.Errorf("clear failed: code=%d stored=%q", rr.Code, LoadGitHubPAT())
	}
}
