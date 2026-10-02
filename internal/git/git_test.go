// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package git

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func gitCommit(t *testing.T, dir, file, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", file},
		{"commit", "-q", "-m", msg},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestStatusNotARepo(t *testing.T) {
	tmp := t.TempDir()
	req := httptest.NewRequest("GET", "/api/git/status?path="+tmp, nil)
	rec := httptest.NewRecorder()
	HandleStatus(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
	}
	var st Status
	if err := json.NewDecoder(rec.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.IsRepo {
		t.Fatal("expected IsRepo=false outside a repo")
	}
}

func TestStatusInRepo(t *testing.T) {
	tmp := t.TempDir()
	gitInit(t, tmp)
	gitCommit(t, tmp, "a.txt", "hello", "init")

	req := httptest.NewRequest("GET", "/api/git/status?path="+tmp, nil)
	rec := httptest.NewRecorder()
	HandleStatus(rec, req)

	var st Status
	if err := json.NewDecoder(rec.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if !st.IsRepo {
		t.Fatal("expected IsRepo=true")
	}
	if st.Branch != "main" {
		t.Fatalf("Branch = %q want main", st.Branch)
	}
	if st.Dirty {
		t.Fatal("expected Dirty=false right after commit")
	}
	if st.LastMessage != "init" {
		t.Fatalf("LastMessage = %q", st.LastMessage)
	}
	if st.HasRemote {
		t.Fatal("expected HasRemote=false")
	}

	// Touch a file: should now be dirty.
	if err := os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec2 := httptest.NewRecorder()
	HandleStatus(rec2, httptest.NewRequest("GET", "/api/git/status?path="+tmp, nil))
	var st2 Status
	_ = json.NewDecoder(rec2.Body).Decode(&st2)
	if !st2.Dirty {
		t.Fatal("expected Dirty=true after edit")
	}
}

func TestStatusFromFilePath(t *testing.T) {
	tmp := t.TempDir()
	gitInit(t, tmp)
	gitCommit(t, tmp, "a.txt", "hello", "init")

	req := httptest.NewRequest("GET", "/api/git/status?path="+filepath.Join(tmp, "a.txt"), nil)
	rec := httptest.NewRecorder()
	HandleStatus(rec, req)
	var st Status
	_ = json.NewDecoder(rec.Body).Decode(&st)
	if !st.IsRepo {
		t.Fatal("expected IsRepo=true when path is a file inside a repo")
	}
}

func TestStatusMissingPath(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleStatus(rec, httptest.NewRequest("GET", "/api/git/status", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestPullNotARepo(t *testing.T) {
	tmp := t.TempDir()
	body := strings.NewReader(`{"path":"` + tmp + `"}`)
	req := httptest.NewRequest("POST", "/api/git/pull", body)
	rec := httptest.NewRecorder()
	HandlePull(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPushNoRemoteFails(t *testing.T) {
	tmp := t.TempDir()
	gitInit(t, tmp)
	gitCommit(t, tmp, "a.txt", "hello", "init")

	body := strings.NewReader(`{"path":"` + tmp + `"}`)
	req := httptest.NewRequest("POST", "/api/git/push", body)
	rec := httptest.NewRecorder()
	HandlePush(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	var res opResult
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res.Success {
		t.Fatal("expected push without remote to fail")
	}
	if res.Status.Branch != "main" {
		t.Fatalf("Status.Branch = %q after push attempt", res.Status.Branch)
	}
}

func TestPullFromLocalRemote(t *testing.T) {
	// Create an upstream "remote" repo and a downstream clone, then commit
	// upstream and verify pull picks it up.
	upstream := t.TempDir()
	gitInit(t, upstream)
	gitCommit(t, upstream, "a.txt", "v1", "v1")
	// Allow pushing into the checked-out branch from the clone direction.
	cmd := exec.Command("git", "config", "receive.denyCurrentBranch", "ignore")
	cmd.Dir = upstream
	_, _ = cmd.CombinedOutput()

	clone := t.TempDir()
	cmd = exec.Command("git", "clone", "-q", upstream, clone)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	for _, args := range [][]string{
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		c := exec.Command("git", args...)
		c.Dir = clone
		_, _ = c.CombinedOutput()
	}

	// New commit upstream.
	gitCommit(t, upstream, "a.txt", "v2", "v2")

	body := strings.NewReader(`{"path":"` + clone + `"}`)
	req := httptest.NewRequest("POST", "/api/git/pull", body)
	rec := httptest.NewRecorder()
	HandlePull(rec, req)

	if rec.Code != 200 {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
	}
	var res opResult
	_ = json.NewDecoder(rec.Body).Decode(&res)
	if !res.Success {
		t.Fatalf("pull failed: %s\n%s", res.Error, res.Output)
	}
	got, _ := os.ReadFile(filepath.Join(clone, "a.txt"))
	if string(got) != "v2" {
		t.Fatalf("file content after pull = %q want v2", got)
	}
	if !res.Status.HasRemote {
		t.Fatal("expected HasRemote=true after clone")
	}
}

func TestFetchDetectsBehind(t *testing.T) {
	// A clone that is behind its remote must report Behind>0 ONLY after a
	// fetch (plain status reads the stale @{u} and shows Behind=0).
	upstream := t.TempDir()
	gitInit(t, upstream)
	gitCommit(t, upstream, "a.txt", "v1", "v1")

	clone := t.TempDir()
	cmd := exec.Command("git", "clone", "-q", upstream, clone)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	for _, args := range [][]string{
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		c := exec.Command("git", args...)
		c.Dir = clone
		_, _ = c.CombinedOutput()
	}

	// New commit lands on the remote after the clone.
	gitCommit(t, upstream, "a.txt", "v2", "v2")

	// Plain status must NOT yet see it (no fetch → stale tracking ref).
	reqS := httptest.NewRequest("GET", "/api/git/status?path="+clone, nil)
	recS := httptest.NewRecorder()
	HandleStatus(recS, reqS)
	var pre Status
	_ = json.NewDecoder(recS.Body).Decode(&pre)
	if pre.Behind != 0 {
		t.Fatalf("expected Behind=0 before fetch, got %d", pre.Behind)
	}

	// Fetch → the endpoint updates the tracking ref and recomputes status.
	body := strings.NewReader(`{"path":"` + clone + `"}`)
	req := httptest.NewRequest("POST", "/api/git/fetch", body)
	rec := httptest.NewRecorder()
	HandleFetch(rec, req)
	if rec.Code != 200 {
		t.Fatalf("fetch code = %d body=%s", rec.Code, rec.Body.String())
	}
	var res opResult
	_ = json.NewDecoder(rec.Body).Decode(&res)
	if !res.Success {
		t.Fatalf("fetch failed: %s\n%s", res.Error, res.Output)
	}
	if res.Status.Behind != 1 {
		t.Fatalf("expected Behind=1 after fetch, got %d", res.Status.Behind)
	}
	if !res.Status.HasRemote {
		t.Fatal("expected HasRemote=true")
	}
}

func TestChangesAndCommit(t *testing.T) {
	tmp := t.TempDir()
	gitInit(t, tmp)
	gitCommit(t, tmp, "seed.txt", "seed", "init")

	if err := os.WriteFile(filepath.Join(tmp, "tracked.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommit(t, tmp, "tracked.txt", "v1", "add tracked")

	// Now: modify a tracked file, add an untracked file, delete another.
	if err := os.WriteFile(filepath.Join(tmp, "tracked.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "new.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	// changes endpoint
	req := httptest.NewRequest("GET", "/api/git/changes?path="+tmp, nil)
	rec := httptest.NewRecorder()
	HandleChanges(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("changes status = %d", rec.Code)
	}
	var cr struct {
		Changes []FileChange `json:"changes"`
		Root    string       `json:"root"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cr); err != nil {
		t.Fatal(err)
	}
	if len(cr.Changes) != 2 {
		t.Fatalf("want 2 changes, got %d: %+v", len(cr.Changes), cr.Changes)
	}
	statuses := map[string]string{}
	for _, c := range cr.Changes {
		statuses[c.Path] = c.Status
	}
	if statuses["tracked.txt"] != "M" {
		t.Errorf("tracked.txt status = %q want M", statuses["tracked.txt"])
	}
	if statuses["new.txt"] != "?" {
		t.Errorf("new.txt status = %q want ?", statuses["new.txt"])
	}

	// commit endpoint (no push, no remote)
	body := strings.NewReader(`{"path":"` + tmp + `","message":"test commit","push":false,"all":true}`)
	cReq := httptest.NewRequest("POST", "/api/git/commit", body)
	cRec := httptest.NewRecorder()
	HandleCommit(cRec, cReq)
	if cRec.Code != http.StatusOK {
		t.Fatalf("commit status = %d body=%s", cRec.Code, cRec.Body.String())
	}
	var cres commitResult
	if err := json.Unmarshal(cRec.Body.Bytes(), &cres); err != nil {
		t.Fatal(err)
	}
	if !cres.Success {
		t.Fatalf("commit not successful: %s\n%s", cres.Error, cres.Output)
	}
	if cres.Status.Dirty {
		t.Error("expected clean tree after commit")
	}

	// changes after commit: clean
	rec2 := httptest.NewRecorder()
	HandleChanges(rec2, httptest.NewRequest("GET", "/api/git/changes?path="+tmp, nil))
	var cr2 struct {
		Changes []FileChange `json:"changes"`
	}
	_ = json.Unmarshal(rec2.Body.Bytes(), &cr2)
	if len(cr2.Changes) != 0 {
		t.Fatalf("expected 0 changes after commit, got %d", len(cr2.Changes))
	}
}

func TestCommitRequiresMessage(t *testing.T) {
	tmp := t.TempDir()
	gitInit(t, tmp)
	body := strings.NewReader(`{"path":"` + tmp + `","message":"   "}`)
	req := httptest.NewRequest("POST", "/api/git/commit", body)
	rec := httptest.NewRecorder()
	HandleCommit(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestParsePorcelainZ_BasicStatuses(t *testing.T) {
	// Build a fake -z payload: each entry is "XY path" terminated by NUL.
	in := " M modified.txt\x00A  added.txt\x00 D deleted.txt\x00?? new.txt\x00"
	got := parsePorcelainZ(in)
	if len(got) != 4 {
		t.Fatalf("want 4 entries, got %d: %+v", len(got), got)
	}
	want := []struct {
		path, status string
		staged       bool
	}{
		{"modified.txt", "M", false},
		{"added.txt", "A", true},
		{"deleted.txt", "D", false},
		{"new.txt", "?", false},
	}
	for i, w := range want {
		if got[i].Path != w.path || got[i].Status != w.status || got[i].Staged != w.staged {
			t.Errorf("entry %d = %+v want path=%s status=%s staged=%v",
				i, got[i], w.path, w.status, w.staged)
		}
	}
}

func TestParsePorcelainZ_RenameConsumesOldPath(t *testing.T) {
	// A rename entry is followed by the previous path (-z makes them two
	// separate NUL-terminated records, in the order new<NUL>old<NUL>).
	in := "R  new name.txt\x00old name.txt\x00 M other.txt\x00"
	got := parsePorcelainZ(in)
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(got), got)
	}
	if got[0].Status != "R" || got[0].Path != "new name.txt" || got[0].OldPath != "old name.txt" {
		t.Errorf("rename entry = %+v", got[0])
	}
	if got[1].Status != "M" || got[1].Path != "other.txt" {
		t.Errorf("modified entry = %+v", got[1])
	}
}

func TestParsePorcelainZ_Empty(t *testing.T) {
	got := parsePorcelainZ("")
	if got == nil {
		t.Fatal("parsePorcelainZ('') returned nil; expected empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("want 0 entries, got %+v", got)
	}
}

func TestChangesNotARepoEmpty(t *testing.T) {
	tmp := t.TempDir()
	rec := httptest.NewRecorder()
	HandleChanges(rec, httptest.NewRequest("GET", "/api/git/changes?path="+tmp, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var cr struct {
		Changes []FileChange `json:"changes"`
		Root    string       `json:"root"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cr); err != nil {
		t.Fatal(err)
	}
	if cr.Root != "" || len(cr.Changes) != 0 {
		t.Fatalf("expected empty result, got %+v", cr)
	}
}

func TestChangesMissingPath(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleChanges(rec, httptest.NewRequest("GET", "/api/git/changes", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestCommitNotARepo(t *testing.T) {
	tmp := t.TempDir()
	body := strings.NewReader(`{"path":"` + tmp + `","message":"x"}`)
	rec := httptest.NewRecorder()
	HandleCommit(rec, httptest.NewRequest("POST", "/api/git/commit", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCommitInvalidJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleCommit(rec, httptest.NewRequest("POST", "/api/git/commit", strings.NewReader("{not json")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestPullInvalidJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	HandlePull(rec, httptest.NewRequest("POST", "/api/git/pull", strings.NewReader("{not json")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestCommitNoChangesFails(t *testing.T) {
	tmp := t.TempDir()
	gitInit(t, tmp)
	gitCommit(t, tmp, "seed.txt", "seed", "init")

	body := strings.NewReader(`{"path":"` + tmp + `","message":"empty","all":true}`)
	rec := httptest.NewRecorder()
	HandleCommit(rec, httptest.NewRequest("POST", "/api/git/commit", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var res commitResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Success {
		t.Fatal("expected commit to fail when nothing to commit")
	}
	// Transcript should still record the attempt so the UI can show it.
	if !strings.Contains(res.Output, "git commit") {
		t.Errorf("transcript missing commit invocation: %q", res.Output)
	}
}

func TestStatusAheadBehindCounts(t *testing.T) {
	upstream := t.TempDir()
	gitInit(t, upstream)
	gitCommit(t, upstream, "a.txt", "v1", "v1")
	cmd := exec.Command("git", "config", "receive.denyCurrentBranch", "ignore")
	cmd.Dir = upstream
	_, _ = cmd.CombinedOutput()

	clone := t.TempDir()
	if out, err := exec.Command("git", "clone", "-q", upstream, clone).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	for _, args := range [][]string{
		{"config", "user.email", "t@e"},
		{"config", "user.name", "T"},
	} {
		c := exec.Command("git", args...)
		c.Dir = clone
		_, _ = c.CombinedOutput()
	}

	// Diverge: 1 new commit upstream, 2 new commits local.
	gitCommit(t, upstream, "a.txt", "v2", "v2 upstream")
	gitCommit(t, clone, "b.txt", "b1", "b1 local")
	gitCommit(t, clone, "c.txt", "c1", "c1 local")

	// Fetch into the clone so ahead/behind has fresh data.
	c := exec.Command("git", "fetch", "-q")
	c.Dir = clone
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}

	rec := httptest.NewRecorder()
	HandleStatus(rec, httptest.NewRequest("GET", "/api/git/status?path="+clone, nil))
	var st Status
	_ = json.NewDecoder(rec.Body).Decode(&st)
	if st.Ahead != 2 {
		t.Errorf("Ahead = %d, want 2", st.Ahead)
	}
	if st.Behind != 1 {
		t.Errorf("Behind = %d, want 1", st.Behind)
	}
	if st.Upstream == "" {
		t.Error("expected non-empty Upstream after clone")
	}
}

func TestCommitAndPushIntegration(t *testing.T) {
	upstream := t.TempDir()
	gitInit(t, upstream)
	gitCommit(t, upstream, "seed.txt", "s", "seed")
	cmd := exec.Command("git", "config", "receive.denyCurrentBranch", "ignore")
	cmd.Dir = upstream
	_, _ = cmd.CombinedOutput()

	clone := t.TempDir()
	if out, err := exec.Command("git", "clone", "-q", upstream, clone).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	for _, args := range [][]string{
		{"config", "user.email", "t@e"},
		{"config", "user.name", "T"},
	} {
		c := exec.Command("git", args...)
		c.Dir = clone
		_, _ = c.CombinedOutput()
	}

	if err := os.WriteFile(filepath.Join(clone, "feature.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	body := strings.NewReader(`{"path":"` + clone + `","message":"add feature","push":true,"all":true}`)
	rec := httptest.NewRecorder()
	HandleCommit(rec, httptest.NewRequest("POST", "/api/git/commit", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
	}
	var res commitResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatalf("commit+push failed: %s\n%s", res.Error, res.Output)
	}
	if !strings.Contains(res.Output, "git push") {
		t.Errorf("transcript missing push: %q", res.Output)
	}
	// Upstream is non-bare with denyCurrentBranch=ignore, so the working
	// tree doesn't update on push — but the new commit must be reachable
	// in the upstream object DB.
	logCmd := exec.Command("git", "log", "--oneline", "main")
	logCmd.Dir = upstream
	logOut, _ := logCmd.CombinedOutput()
	if !strings.Contains(string(logOut), "add feature") {
		t.Errorf("upstream log missing pushed commit:\n%s", logOut)
	}
	if res.Status.Ahead != 0 {
		t.Errorf("Status.Ahead = %d after push, want 0", res.Status.Ahead)
	}
}

func TestIsSafeRepoURL(t *testing.T) {
	ok := []string{
		"https://github.com/owner/repo.git",
		"http://example.com/x.git",
		"ssh://git@github.com/owner/repo.git",
		"git://host/repo.git",
		"git@github.com:owner/repo.git",
	}
	for _, u := range ok {
		if !IsSafeRepoURL(u) {
			t.Errorf("IsSafeRepoURL(%q) = false, want true", u)
		}
	}
	bad := []string{
		"",
		"-oProxyCommand=evil",
		"--upload-pack=evil",
		"/etc/passwd",
		"file:///etc/passwd",
		"owner/repo",           // no scheme / not scp-like
		"git@github.com",       // scp-like requires a colon
	}
	for _, u := range bad {
		if IsSafeRepoURL(u) {
			t.Errorf("IsSafeRepoURL(%q) = true, want false", u)
		}
	}
}

func TestRepoNameFromURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/owner/repo.git": "repo",
		"https://github.com/owner/repo":     "repo",
		"git@github.com:owner/My-Repo.git":  "My-Repo",
		"ssh://git@host/a/b/c.git":          "c",
	}
	for in, want := range cases {
		if got := repoNameFromURL(in); got != want {
			t.Errorf("repoNameFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeRepoName(t *testing.T) {
	cases := map[string]string{
		"my-repo":       "my-repo",
		"weird name!!":  "weird-name",
		"../../escape":  "escape",
		".hidden":       "hidden",
	}
	for in, want := range cases {
		if got := sanitizeRepoName(in); got != want {
			t.Errorf("sanitizeRepoName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiscoverRepos(t *testing.T) {
	base := t.TempDir()
	// A top-level repo.
	alpha := filepath.Join(base, "alpha")
	if err := os.MkdirAll(alpha, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, alpha)
	// A repo nested one level down.
	beta := filepath.Join(base, "work", "beta")
	if err := os.MkdirAll(beta, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, beta)
	// A plain (non-repo) folder and a noise dir that must be skipped.
	if err := os.MkdirAll(filepath.Join(base, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(alpha, "node_modules", "junk"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := discoverRepos(base, 2)
	names := map[string]bool{}
	for _, r := range got {
		names[r.Name] = true
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("expected alpha and beta repos, got %+v", got)
	}
	if names["notes"] || names["junk"] {
		t.Errorf("non-repo/noise folders should be excluded, got %+v", got)
	}
	// Results are sorted by name.
	for i := 1; i < len(got); i++ {
		if strings.ToLower(got[i-1].Name) > strings.ToLower(got[i].Name) {
			t.Errorf("results not sorted: %+v", got)
			break
		}
	}
}

func TestCommitAndPush_NothingToCommit(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	// Make an initial commit so HEAD exists, then call with no changes.
	gitCommit(t, dir, "a.txt", "hello", "init")

	out, err := CommitAndPush(dir, "noop")
	if err != nil {
		t.Fatalf("nothing-to-commit should not error, got: %v (%s)", err, out)
	}
	if !strings.Contains(out, "nothing to commit") {
		t.Errorf("expected 'nothing to commit' in transcript, got: %s", out)
	}
}

func TestCommitAndPush_CommitsNewFile(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	gitCommit(t, dir, "seed.txt", "x", "seed")

	// New file — CommitAndPush should stage+commit it. Push will fail (no
	// remote), so we accept an error but verify the commit happened.
	if err := os.WriteFile(filepath.Join(dir, "PROJECT_SUMMARY.md"), []byte("# summary"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = CommitAndPush(dir, "add summary")

	// The file should be committed regardless of push outcome.
	logOut := runIn(dir, "log", "-1", "--pretty=%s")
	if !strings.Contains(logOut, "add summary") {
		t.Errorf("expected commit 'add summary', got log: %q", logOut)
	}
	// Working tree should be clean (file was committed, not left staged/dirty).
	st := runIn(dir, "status", "--porcelain")
	if strings.TrimSpace(st) != "" {
		t.Errorf("expected clean tree after commit, got: %q", st)
	}
}

// makeBareRemote creates a bare repository to serve as a push target.
func makeBareRemote(t *testing.T) string {
	t.Helper()
	remote := t.TempDir()
	cmd := exec.Command("git", "init", "--bare", "-q", remote)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}
	return remote
}

func TestAttachRemoteRejectsBadURL(t *testing.T) {
	dir := t.TempDir()
	if _, err := AttachRemote(dir, "--upload-pack=evil"); err == nil {
		t.Fatal("expected AttachRemote to reject an unsafe URL")
	}
	if _, err := AttachRemote(dir, ""); err == nil {
		t.Fatal("expected AttachRemote to reject an empty URL")
	}
}

func TestAttachRemoteInitsAndPushes(t *testing.T) {
	remote := makeBareRemote(t)
	work := t.TempDir()
	// A non-repo workspace with a deliverable already present.
	if err := os.WriteFile(filepath.Join(work, "report.md"), []byte("# hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := AttachRemote(work, remote)
	if err != nil {
		t.Fatalf("AttachRemote failed: %v\n%s", err, out)
	}

	// The workspace is now a repo with an origin pointing at the remote.
	if !strings.Contains(runIn(work, "remote", "-v"), remote) {
		t.Errorf("origin not set to remote; remotes:\n%s", runIn(work, "remote", "-v"))
	}
	// HEAD exists (at least the initial commit was made) and an upstream is set.
	if strings.TrimSpace(runIn(work, "rev-parse", "--verify", "HEAD")) == "" {
		t.Error("expected HEAD to exist after AttachRemote")
	}
	if strings.TrimSpace(runIn(work, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")) == "" {
		t.Error("expected an upstream to be configured after AttachRemote")
	}

	// Idempotent: attaching again to the same remote succeeds.
	if _, err := AttachRemote(work, remote); err != nil {
		t.Errorf("AttachRemote should be idempotent, got: %v", err)
	}
}

func TestAttachRemoteUpdatesExistingOrigin(t *testing.T) {
	remote := makeBareRemote(t)
	work := t.TempDir()
	gitInit(t, work)
	gitCommit(t, work, "a.txt", "x", "init")
	// Pre-existing origin pointing somewhere else.
	cmd := exec.Command("git", "remote", "add", "origin", "https://example.com/old.git")
	cmd.Dir = work
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed origin: %v\n%s", err, out)
	}

	if _, err := AttachRemote(work, remote); err != nil {
		t.Fatalf("AttachRemote failed: %v", err)
	}
	if got := runIn(work, "remote", "get-url", "origin"); !strings.Contains(got, remote) {
		t.Errorf("origin should be updated to %q, got %q", remote, got)
	}
}
