// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package git exposes a tiny set of HTTP handlers that wrap `git status`,
// `git pull --ff-only` and `git push` against a path supplied by the
// frontend. The intent is "make it easier to sync a file you just edited" —
// not a general-purpose git client. We deliberately reject paths that aren't
// inside a repo and return the raw command output so the UI can show success
// or failure verbatim.
package git

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

// Status describes the high-level state of the repo containing path.
type Status struct {
	IsRepo      bool   `json:"is_repo"`
	Root        string `json:"root,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Upstream    string `json:"upstream,omitempty"`
	Ahead       int    `json:"ahead"`
	Behind      int    `json:"behind"`
	Dirty       bool   `json:"dirty"`
	HasRemote   bool   `json:"has_remote"`
	LastMessage string `json:"last_message,omitempty"`
	LastSHA     string `json:"last_sha,omitempty"`
	Remote      string `json:"remote,omitempty"`
}

// HandleStatus answers GET /api/git/status?path=...
func HandleStatus(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path is required"})
		return
	}
	abs, err := resolveDir(path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	root, ok := repoRoot(abs)
	if !ok {
		writeJSON(w, http.StatusOK, Status{IsRepo: false})
		return
	}

	st := Status{IsRepo: true, Root: root}
	st.Branch = strings.TrimSpace(runIn(root, "rev-parse", "--abbrev-ref", "HEAD"))
	st.Upstream = strings.TrimSpace(runIn(root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"))
	if st.Upstream != "" {
		// "<ahead> <behind>"
		out := strings.TrimSpace(runIn(root, "rev-list", "--left-right", "--count", "@{u}...HEAD"))
		fields := strings.Fields(out)
		if len(fields) == 2 {
			st.Behind, _ = strconv.Atoi(fields[0])
			st.Ahead, _ = strconv.Atoi(fields[1])
		}
	}
	st.Dirty = strings.TrimSpace(runIn(root, "status", "--porcelain")) != ""
	st.Remote = strings.TrimSpace(runIn(root, "remote"))
	st.HasRemote = st.Remote != ""
	if line := strings.TrimSpace(runIn(root, "log", "-1", "--pretty=%h %s")); line != "" {
		parts := strings.SplitN(line, " ", 2)
		st.LastSHA = parts[0]
		if len(parts) == 2 {
			st.LastMessage = parts[1]
		}
	}
	writeJSON(w, http.StatusOK, st)
}

type opRequest struct {
	Path string `json:"path"`
}

type opResult struct {
	Success bool   `json:"success"`
	Output  string `json:"output"`
	Error   string `json:"error,omitempty"`
	Status  Status `json:"status"`
}

// HandlePull answers POST /api/git/pull, body {"path":"..."}.
func HandlePull(w http.ResponseWriter, r *http.Request) {
	runOp(w, r, "pull", "--ff-only")
}

// HandlePush answers POST /api/git/push, body {"path":"..."}.
func HandlePush(w http.ResponseWriter, r *http.Request) {
	runOp(w, r, "push")
}

// HandleFetch answers POST /api/git/fetch, body {"path":"..."}. It contacts the
// remote (git fetch --prune) to update the remote-tracking refs, then returns
// the recomputed Status — so Status.Behind reflects the LIVE remote position
// (unlike GET /api/git/status, which only reads the locally-cached @{u}). Used
// by the periodic remote-refresh that suggests pulling when the branch is
// behind. --prune drops stale remote refs.
func HandleFetch(w http.ResponseWriter, r *http.Request) {
	runOp(w, r, "fetch", "--prune")
}

// FileChange describes a single modified path in the working tree.
//
// Status uses the two-letter code from `git status --porcelain` (XY); we
// expose it as a one-letter normalized status for the UI plus the raw code.
type FileChange struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
	XY      string `json:"xy"`
	Status  string `json:"status"` // M, A, D, R, C, U, ? (untracked), I (ignored)
	Staged  bool   `json:"staged"`
}

// HandleChanges answers GET /api/git/changes?path=...
func HandleChanges(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path is required"})
		return
	}
	abs, err := resolveDir(path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	root, ok := repoRoot(abs)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"changes": []FileChange{}, "root": ""})
		return
	}
	out := runIn(root, "status", "--porcelain=1", "-z", "--untracked-files=all")
	changes := parsePorcelainZ(out)
	writeJSON(w, http.StatusOK, map[string]any{"changes": changes, "root": root})
}

// parsePorcelainZ parses NUL-separated `git status --porcelain=1 -z` output.
// Rename/copy entries are followed by the original path (also NUL-terminated),
// so we can't just split on NUL — we have to know when to consume an extra
// record.
func parsePorcelainZ(out string) []FileChange {
	changes := []FileChange{}
	if out == "" {
		return changes
	}
	parts := strings.Split(out, "\x00")
	for i := 0; i < len(parts); i++ {
		entry := parts[i]
		if len(entry) < 4 {
			continue
		}
		xy := entry[:2]
		path := entry[3:]
		fc := FileChange{Path: path, XY: xy}
		x, y := xy[0], xy[1]
		switch {
		case x == '?' && y == '?':
			fc.Status = "?"
		case x == '!' && y == '!':
			fc.Status = "I"
		case x == 'R' || y == 'R':
			fc.Status = "R"
			if i+1 < len(parts) {
				fc.OldPath = parts[i+1]
				i++
			}
		case x == 'C' || y == 'C':
			fc.Status = "C"
			if i+1 < len(parts) {
				fc.OldPath = parts[i+1]
				i++
			}
		case x == 'A' || y == 'A':
			fc.Status = "A"
		case x == 'D' || y == 'D':
			fc.Status = "D"
		case x == 'U' || y == 'U':
			fc.Status = "U"
		default:
			fc.Status = "M"
		}
		fc.Staged = x != ' ' && x != '?' && x != '!'
		changes = append(changes, fc)
	}
	return changes
}

type commitRequest struct {
	Path    string `json:"path"`
	Message string `json:"message"`
	Push    bool   `json:"push"`
	All     bool   `json:"all"` // stage all tracked+untracked before commit (default true)
}

type commitResult struct {
	Success bool   `json:"success"`
	Output  string `json:"output"`
	Error   string `json:"error,omitempty"`
	Status  Status `json:"status"`
}

// HandleCommit answers POST /api/git/commit.
//
// Stages everything by default (`git add -A`), commits with the provided
// message, and optionally runs `git push` afterwards. The combined output of
// every step is returned so the UI can show the full transcript.
func HandleCommit(w http.ResponseWriter, r *http.Request) {
	var req commitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path is required"})
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "commit message is required"})
		return
	}
	abs, err := resolveDir(req.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	root, ok := repoRoot(abs)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a git repository"})
		return
	}

	stageAll := req.All || !hasStagedChanges(root)
	var transcript strings.Builder
	run := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true")
		out, err := cmd.CombinedOutput()
		fmt.Fprintf(&transcript, "$ git %s\n%s", strings.Join(args, " "), string(out))
		if len(out) == 0 || out[len(out)-1] != '\n' {
			transcript.WriteByte('\n')
		}
		return err
	}

	res := commitResult{Success: true}
	if stageAll {
		if err := run("add", "-A"); err != nil {
			res.Success = false
			res.Error = err.Error()
		}
	}
	if res.Success {
		if err := run("commit", "-m", req.Message); err != nil {
			res.Success = false
			res.Error = err.Error()
		}
	}
	if res.Success && req.Push {
		if err := run("push"); err != nil {
			res.Success = false
			res.Error = err.Error()
		}
	}
	res.Output = transcript.String()
	res.Status = readStatus(root)
	writeJSON(w, http.StatusOK, res)
}

func hasStagedChanges(root string) bool {
	cmd := exec.Command("git", "diff", "--cached", "--quiet")
	cmd.Dir = root
	return cmd.Run() != nil
}

// RepoState reports whether dir resolves to a git repository and, if so,
// whether that repository has at least one configured remote. Both checks are
// local-only (no network), so this is safe to call on a hot path without
// risking a credential hang. A non-repo directory yields (false, false).
func RepoState(dir string) (isRepo bool, hasRemote bool) {
	abs, e := resolveDir(dir)
	if e != nil {
		return false, false
	}
	root, ok := repoRoot(abs)
	if !ok {
		return false, false
	}
	return true, strings.TrimSpace(runIn(root, "remote")) != ""
}

// CommitAndPush stages all changes, commits them with the given message, and
// pushes — reusable by non-HTTP callers (e.g. the project summarizer). It
// resolves the enclosing repo from dir. Returns the combined git transcript.
// A "nothing to commit" state is treated as success (no error).
func CommitAndPush(dir, message string) (output string, err error) {
	abs, e := resolveDir(dir)
	if e != nil {
		return "", e
	}
	root, ok := repoRoot(abs)
	if !ok {
		return "", fmt.Errorf("not a git repository")
	}
	var transcript strings.Builder
	run := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true")
		out, runErr := cmd.CombinedOutput()
		fmt.Fprintf(&transcript, "$ git %s\n%s", strings.Join(args, " "), string(out))
		if len(out) == 0 || out[len(out)-1] != '\n' {
			transcript.WriteByte('\n')
		}
		return runErr
	}

	if e := run("add", "-A"); e != nil {
		return transcript.String(), fmt.Errorf("git add failed: %w", e)
	}
	// If there's nothing staged, there's nothing to commit — succeed quietly.
	if !hasStagedChanges(root) {
		fmt.Fprintf(&transcript, "(nothing to commit)\n")
		return transcript.String(), nil
	}
	if e := run(commitArgs(root, message)...); e != nil {
		return transcript.String(), fmt.Errorf("git commit failed: %w", e)
	}
	if e := run("push"); e != nil {
		return transcript.String(), fmt.Errorf("git push failed: %w", e)
	}
	return transcript.String(), nil
}

// commitArgs builds `git commit -m <message>` args, prepending a local identity
// fallback (via -c) only when the repo/user has no committer identity
// configured. A real user.name/user.email (repo or global) always wins because
// this fallback is only added when none is present. This keeps automatic
// durable-storage sync commits from failing on a workspace where git identity was never
// set, without overriding a user who has configured one.
func commitArgs(root, message string) []string {
	if hasGitIdentity(root) {
		return []string{"commit", "-m", message}
	}
	return []string{
		"-c", "user.email=knowledge-worker-agent@localhost",
		"-c", "user.name=Knowledge Worker Agent",
		"commit", "-m", message,
	}
}

// hasGitIdentity reports whether a committer identity is configured (repo or
// global) for the repo at root.
func hasGitIdentity(root string) bool {
	return strings.TrimSpace(runIn(root, "config", "user.email")) != ""
}

// isSafeRemoteTarget accepts either a safe remote URL (IsSafeRepoURL) or a
// local filesystem path usable as a Git remote. Local remotes are legitimate
// for air-gapped / self-hosted deployments (a bare repo on a mounted volume)
// and for tests. A leading '-' is always rejected so the value can never be
// mistaken for a git flag.
func isSafeRemoteTarget(u string) bool {
	u = strings.TrimSpace(u)
	if u == "" || strings.HasPrefix(u, "-") {
		return false
	}
	if IsSafeRepoURL(u) {
		return true
	}
	// A local path remote: absolute path, or an explicit file:// URL.
	if strings.HasPrefix(u, "file://") {
		return true
	}
	return filepath.IsAbs(u)
}

// AttachRemote binds a local workspace directory to a durable Git remote so it
// can be backed up ("enable backup"). It:
//   - validates remoteURL (same rules as clone),
//   - initializes a git repo in dir if one is not already present,
//   - sets (or updates) the "origin" remote to remoteURL,
//   - ensures at least one commit exists (creating an initial empty commit if
//     the workspace has no history yet), and
//   - pushes the current branch to origin, setting upstream.
//
// It is safe to call repeatedly (idempotent): an already-attached, already-
// pushed workspace results in a no-op push. All git invocations set the
// credential-hang guards so a remote that would prompt fails fast instead of
// hanging. Returns the combined git transcript.
//
// This does NOT create the remote repository itself — remoteURL must already
// exist on the customer's Git endpoint (GitHub / GHE / self-hosted). Auto-
// provisioning an empty repo via a provider API is a separate concern.
func AttachRemote(dir, remoteURL string) (output string, err error) {
	remoteURL = strings.TrimSpace(remoteURL)
	if !isSafeRemoteTarget(remoteURL) {
		return "", fmt.Errorf("invalid repository URL")
	}
	abs, e := resolveDir(dir)
	if e != nil {
		return "", e
	}

	var transcript strings.Builder
	// run executes git in the resolved dir (or the repo root once initialized).
	runAt := func(at string, args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = at
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true")
		out, runErr := cmd.CombinedOutput()
		fmt.Fprintf(&transcript, "$ git %s\n%s", strings.Join(args, " "), string(out))
		if len(out) == 0 || out[len(out)-1] != '\n' {
			transcript.WriteByte('\n')
		}
		return string(out), runErr
	}

	// Ensure the workspace is a git repo.
	root, isRepo := repoRoot(abs)
	if !isRepo {
		if _, e := runAt(abs, "init"); e != nil {
			return transcript.String(), fmt.Errorf("git init failed: %w", e)
		}
		root = abs
	}

	// Point origin at the durable remote (add, or update if it already exists).
	if existing := strings.TrimSpace(runIn(root, "remote")); strings.Contains(existing, "origin") {
		if _, e := runAt(root, "remote", "set-url", "origin", remoteURL); e != nil {
			return transcript.String(), fmt.Errorf("git remote set-url failed: %w", e)
		}
	} else {
		if _, e := runAt(root, "remote", "add", "origin", remoteURL); e != nil {
			return transcript.String(), fmt.Errorf("git remote add failed: %w", e)
		}
	}

	// Guarantee history exists so the first push has a ref to publish. A fresh
	// `git init` has no commits; create an empty initial commit in that case.
	if strings.TrimSpace(runIn(root, "rev-parse", "--verify", "HEAD")) == "" {
		args := append(commitArgs(root, "Knowledge Worker Agent: initialize durable store"), "--allow-empty")
		if _, e := runAt(root, args...); e != nil {
			return transcript.String(), fmt.Errorf("git commit failed: %w", e)
		}
	}

	branch := strings.TrimSpace(runIn(root, "rev-parse", "--abbrev-ref", "HEAD"))
	if branch == "" || branch == "HEAD" {
		branch = "main"
	}
	if _, e := runAt(root, "push", "-u", "origin", branch); e != nil {
		return transcript.String(), fmt.Errorf("git push failed: %w", e)
	}
	return transcript.String(), nil
}

func runOp(w http.ResponseWriter, r *http.Request, args ...string) {
	var req opRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path is required"})
		return
	}
	abs, err := resolveDir(req.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	root, ok := repoRoot(abs)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a git repository"})
		return
	}

	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		// Suppress interactive auth prompts — the server is non-interactive,
		// so failures should surface immediately rather than block forever.
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=true",
	)
	out, runErr := cmd.CombinedOutput()
	res := opResult{
		Success: runErr == nil,
		Output:  string(out),
	}
	if runErr != nil {
		res.Error = runErr.Error()
	}
	res.Status = readStatus(root)
	writeJSON(w, http.StatusOK, res)
}

// runOpAt runs `git <args>` in dir with interactive auth suppressed, returning
// combined output and any error. Shared by the exported non-HTTP helpers below.
func runOpAt(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// StatusAt resolves the repo root for dir and returns its Status (branch,
// ahead/behind vs the cached upstream, dirty, remote). {IsRepo:false} if dir is
// not a git repository. Reusable by non-HTTP callers (e.g. project auto-sync).
func StatusAt(dir string) Status {
	root, ok := repoRoot(dir)
	if !ok {
		return Status{IsRepo: false}
	}
	return readStatus(root)
}

// FetchAt runs `git fetch --prune` in dir's repo so ahead/behind reflects the
// live remote. Returns combined output and any error. No-op-safe on a repo with
// no remote (git reports and returns an error, which the caller may ignore).
func FetchAt(dir string) (string, error) {
	root, ok := repoRoot(dir)
	if !ok {
		return "", fmt.Errorf("not a git repository")
	}
	return runOpAt(root, "fetch", "--prune")
}

// PullFastForwardAt runs `git pull --ff-only` in dir's repo. It only advances a
// branch that can be fast-forwarded; it never creates a merge commit and fails
// (without changing anything) if a merge would be required. Safe for automated
// sync of a clean, non-diverged workspace.
func PullFastForwardAt(dir string) (string, error) {
	root, ok := repoRoot(dir)
	if !ok {
		return "", fmt.Errorf("not a git repository")
	}
	return runOpAt(root, "pull", "--ff-only")
}

func readStatus(root string) Status {
	st := Status{IsRepo: true, Root: root}
	st.Branch = strings.TrimSpace(runIn(root, "rev-parse", "--abbrev-ref", "HEAD"))
	st.Upstream = strings.TrimSpace(runIn(root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"))
	if st.Upstream != "" {
		out := strings.TrimSpace(runIn(root, "rev-list", "--left-right", "--count", "@{u}...HEAD"))
		fields := strings.Fields(out)
		if len(fields) == 2 {
			st.Behind, _ = strconv.Atoi(fields[0])
			st.Ahead, _ = strconv.Atoi(fields[1])
		}
	}
	st.Dirty = strings.TrimSpace(runIn(root, "status", "--porcelain")) != ""
	st.Remote = strings.TrimSpace(runIn(root, "remote"))
	st.HasRemote = st.Remote != ""
	if line := strings.TrimSpace(runIn(root, "log", "-1", "--pretty=%h %s")); line != "" {
		parts := strings.SplitN(line, " ", 2)
		st.LastSHA = parts[0]
		if len(parts) == 2 {
			st.LastMessage = parts[1]
		}
	}
	return st
}

func runIn(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, _ := cmd.Output()
	return string(out)
}

// CloneRequest is the body for POST /api/git/clone.
type CloneRequest struct {
	URL  string `json:"url"`
	Name string `json:"name,omitempty"` // optional folder name override
}
// RepoInfo describes a local git repository discovered under the base workspace.
type RepoInfo struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
	Remote string `json:"remote,omitempty"`
}

// HandleDiscoverRepos answers GET /api/git/repos. It scans the base workspace
// (a bounded number of levels deep) for directories that contain a ".git"
// entry — i.e. existing local git repositories — and returns them for the user
// to pick as a chat workspace. Results are sorted by name.
func HandleDiscoverRepos(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimSpace(config.Workspace)
	if base == "" {
		base = "/home/developer"
	}
	repos := discoverRepos(base, 2)
	// Never return null so the client always gets a JSON array.
	if repos == nil {
		repos = []RepoInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": repos})
}

// discoverRepos walks up to maxDepth levels below root looking for directories
// that are git repositories (contain a ".git" file or directory). It does not
// descend into a repository once found, and skips common heavy/noise dirs.
func discoverRepos(root string, maxDepth int) []RepoInfo {
	var out []RepoInfo
	skip := map[string]bool{
		"node_modules": true, ".Trash": true, ".cache": true, ".local": true,
		".npm": true, ".config": true, "vendor": true, "dist": true, "build": true,
	}

	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > maxDepth {
			return
		}
		// Is this dir itself a repo?
		if isGitRepo(dir) {
			out = append(out, repoInfo(dir))
			return // don't descend into a repo
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") || skip[name] {
				continue
			}
			walk(filepath.Join(dir, name), depth+1)
		}
	}
	// Start one level in: the base workspace itself is rarely the repo, but
	// check it too for completeness.
	if isGitRepo(root) {
		return []RepoInfo{repoInfo(root)}
	}
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") || skip[name] {
				continue
			}
			walk(filepath.Join(root, name), 1)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// isGitRepo reports whether dir contains a .git entry (dir or file, the latter
// for worktrees/submodules).
func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// repoInfo builds a RepoInfo for a known-repo directory, best-effort filling in
// the current branch and origin/first remote.
func repoInfo(dir string) RepoInfo {
	info := RepoInfo{Name: filepath.Base(dir), Path: dir}
	info.Branch = strings.TrimSpace(runIn(dir, "rev-parse", "--abbrev-ref", "HEAD"))
	if remote := strings.TrimSpace(runIn(dir, "remote", "get-url", "origin")); remote != "" {
		info.Remote = remote
	}
	return info
}

// CloneResult is returned by POST /api/git/clone.
type CloneResult struct {
	Success   bool   `json:"success"`
	Workspace string `json:"workspace,omitempty"`
	Output    string `json:"output"`
	Error     string `json:"error,omitempty"`
	// Set when the workspace refused the host before cloning (see BlockedError).
	Blocked  string `json:"blocked,omitempty"`
	Host     string `json:"host,omitempty"`
	TokenURL string `json:"token_url,omitempty"`
}

// HandleClone answers POST /api/git/clone with body {"url": "...", "name": "..."}.
// It clones the given repository into a fresh, uniquely-named folder under the
// configured base workspace and returns that folder as the chat's workspace.
// The URL is validated to a small set of safe schemes and the clone runs with
// an argument vector (never a shell), so prompt/URL text can't inject flags.
func HandleClone(w http.ResponseWriter, r *http.Request) {
	var req CloneRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, CloneResult{Error: "invalid JSON"})
		return
	}
	r.Body.Close()

	url := strings.TrimSpace(req.URL)
	if !IsSafeRepoURL(url) {
		writeJSON(w, http.StatusBadRequest, CloneResult{
			Error: "enter an https:// , ssh:// or git@… repository URL",
		})
		return
	}

	base := strings.TrimSpace(config.Workspace)
	if base == "" {
		base = "/home/developer"
	}
	dest, out, err := Clone(url, req.Name, base)
	if err != nil {
		res := CloneResult{Output: out, Error: "clone failed: " + cloneErrorLine(out, err.Error())}
		var b *BlockedError
		if errors.As(err, &b) {
			res.Error = "clone failed: " + b.Error()
			res.Blocked, res.Host = b.Reason, b.Host
			if b.Reason == BlockedNoToken {
				res.TokenURL = tokenDeployURL()
			}
		}
		writeJSON(w, http.StatusBadGateway, res)
		return
	}
	writeJSON(w, http.StatusOK, CloneResult{
		Success:   true,
		Workspace: dest,
		Output:    out,
	})
}

// Clone shallow-clones url into a uniquely-named directory under base, using
// name (or the repo name derived from the URL) as the folder name. Returns the
// destination path and git's combined output. On failure the partial clone
// directory is removed so a retry starts clean. Callers should validate the
// URL with IsSafeRepoURL first. Reused by the projects feature to back
// a project workspace with a repo.
func Clone(url, name, base string) (dest, output string, err error) {
	folder := sanitizeRepoName(name)
	if folder == "" {
		folder = sanitizeRepoName(repoNameFromURL(url))
	}
	if folder == "" {
		folder = "repo"
	}
	dest = uniqueDir(filepath.Join(base, folder))

	// --depth 1: a shallow clone is enough to start working and much faster.
	cmd := exec.Command("git", "clone", "--depth", "1", "--", url, dest)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true")
	out, cerr := cmd.CombinedOutput()
	if cerr != nil || !isGitRepo(dest) {
		if b := blockedBy(string(out)); b != nil {
			cerr = b
		} else if cerr == nil {
			cerr = errors.New("git exited without cloning")
		}
	}
	if cerr != nil {
		_ = os.RemoveAll(dest)
		return "", string(out), cerr
	}
	return dest, string(out), nil
}

// Reasons the workspace refuses a clone before git runs.
const (
	BlockedNoToken    = "no_token"   // no token deployed for the host
	BlockedRestricted = "restricted" // only repositories added to the workspace
)

// BlockedError is a clone the workspace's git wrapper (/strong/bin/git)
// refused. The wrapper asks on stdin, reads EOF and exits 0 without running
// git, so its message is the only signal.
type BlockedError struct{ Reason, Host string }

func (e *BlockedError) Error() string {
	if e.Reason == BlockedRestricted {
		return "this workspace may only clone the " + e.Host + " repositories added to it"
	}
	return "the workspace has no token for " + e.Host
}

// Worded as the platform's git wrapper prints them.
var (
	noTokenRe    = regexp.MustCompile(`(?m)^You do not have a token deployed for (\S+)\.\s*$`)
	restrictedRe = regexp.MustCompile(`This workspace has restricted access to repositories hosted at ([A-Za-z0-9.-]+?)(?:Do you wish|\s|$)`)
)

func blockedBy(out string) *BlockedError {
	if m := noTokenRe.FindStringSubmatch(out); m != nil {
		return &BlockedError{Reason: BlockedNoToken, Host: m[1]}
	}
	if m := restrictedRe.FindStringSubmatch(out); m != nil {
		return &BlockedError{Reason: BlockedRestricted, Host: m[1]}
	}
	return nil
}

// tokenDeployURL is the SecurSpaces page where a user connects a Git provider,
// built as the wrapper builds it. "" when the platform domain isn't known.
func tokenDeployURL() string {
	d := strings.TrimSpace(os.Getenv("STRONG_NETWORK_DOMAIN"))
	if d == "" || strings.ContainsAny(d, "/?#@\\ ") {
		return ""
	}
	return "https://" + d + "/profile/integrations/repositories_tokens"
}

// IsSafeRepoURL accepts common git remote forms and rejects anything that could
// be interpreted as a flag or a local option-file path.
func IsSafeRepoURL(u string) bool {
	if u == "" || strings.HasPrefix(u, "-") {
		return false
	}
	if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") ||
		strings.HasPrefix(u, "ssh://") || strings.HasPrefix(u, "git://") {
		return true
	}
	// scp-like syntax: git@host:owner/repo(.git)
	if strings.HasPrefix(u, "git@") && strings.Contains(u, ":") {
		return true
	}
	return false
}

// repoNameFromURL extracts the final path segment (minus any .git suffix).
func repoNameFromURL(u string) string {
	s := u
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSuffix(s, ".git")
	return s
}

// sanitizeRepoName keeps a folder name to safe characters.
func sanitizeRepoName(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), ".-")
}

// uniqueDir returns base if it doesn't exist, otherwise base-2, base-3, …
func uniqueDir(base string) string {
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return base
	}
	for i := 2; i < 1000; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
	return fmt.Sprintf("%s-%d", base, os.Getpid())
}

func firstLine(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// cloneErrorLine picks the most useful line from git's output — preferring a
// "fatal:"/"error:"/"remote:" line over the generic "Cloning into…" preamble.
func cloneErrorLine(out, fallback string) string {
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		low := strings.ToLower(ln)
		if strings.HasPrefix(low, "fatal:") || strings.HasPrefix(low, "error:") ||
			(strings.HasPrefix(low, "remote:") && ln != "remote:") {
			return ln
		}
	}
	return firstLine(out, fallback)
}


// repoRoot returns the toplevel of the git repo containing dir, or ("", false).
func repoRoot(dir string) (string, bool) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	root := strings.TrimSpace(string(out))
	return root, root != ""
}

// resolveDir turns the user-supplied path into an absolute directory we can
// shell out from. If path is a file, the parent directory is used.
func resolveDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("path not found")
	}
	if !info.IsDir() {
		abs = filepath.Dir(abs)
	}
	return abs, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
