// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package materializer

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SyncRepo makes the config repo available locally and returns the directory to
// resolve artifacts from.
//
// Behavior:
//   - If source is a local filesystem path (an existing directory), it is used
//     directly — no clone (useful for testing and for pre-provisioned repos).
//   - Otherwise source is treated as a git URL cloned/pulled into cacheDir.
//     On the first run it is cloned; on subsequent runs it is fetched and
//     hard-reset to the remote head.
//   - If the remote is unreachable but a prior clone exists in cacheDir, that
//     cached clone is used (functional-with-stale). A
//     net-new workspace with no cache and an unreachable repo is an error.
//
// git invocations use an argument vector (never a shell) and a timeout.
func SyncRepo(logw io.Writer, source, cacheDir string) (string, error) {
	dir, _, err := syncRepo(logw, source, cacheDir)
	return dir, err
}

// syncRepo is SyncRepo, also returning why it fell back to the cached clone.
func syncRepo(logw io.Writer, source, cacheDir string) (dir string, stale error, err error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", nil, fmt.Errorf("sync: empty config repo source")
	}

	// Local path source: use in place.
	if info, err := os.Stat(source); err == nil && info.IsDir() {
		logf(logw, "  ℹ Using local config repo at %s", source)
		return source, nil, nil
	}

	if strings.TrimSpace(cacheDir) == "" {
		return "", nil, fmt.Errorf("sync: empty cache dir")
	}
	haveCache := isGitRepo(cacheDir)

	if haveCache {
		if err := gitPull(cacheDir); err != nil {
			logf(logw, "  ⚠ Config repo pull failed (%v); using cached clone", err)
			return cacheDir, err, nil
		}
		return cacheDir, nil, nil
	}

	// No cache yet — must clone.
	if err := gitClone(source, cacheDir); err != nil {
		return "", nil, fmt.Errorf("sync: clone config repo: %w", err)
	}
	return cacheDir, nil, nil
}

// Commit identifies a config repo commit.
type Commit struct {
	Hash    string
	Date    time.Time
	Subject string
}

// RepoCommit returns the commit checked out in dir, or a zero Commit when dir
// is not the top of a git checkout. A plain folder inside some other
// repository must not report that repository's commit.
func RepoCommit(dir string) Commit {
	top, err := runGit(dir, "rev-parse", "--show-toplevel")
	if err != nil || !samePath(strings.TrimSpace(top), dir) {
		return Commit{}
	}
	out, err := runGit(dir, "log", "-1", "--format=%H%x00%cI%x00%s")
	if err != nil {
		return Commit{}
	}
	parts := strings.SplitN(strings.TrimSpace(out), "\x00", 3)
	if len(parts) != 3 {
		return Commit{}
	}
	date, _ := time.Parse(time.RFC3339, parts[1])
	return Commit{Hash: parts[0], Date: date, Subject: parts[2]}
}

func samePath(a, b string) bool {
	resolve := func(p string) string {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		return p
	}
	return a != "" && resolve(a) == resolve(b)
}

func gitClone(url, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	_ = os.RemoveAll(dest)
	out, err := runGit("", "clone", "--depth", "1", "--", url, dest)
	if err != nil {
		_ = os.RemoveAll(dest)
		return fmt.Errorf("%v: %s", err, firstLine(out))
	}
	return nil
}

func gitPull(dir string) error {
	if out, err := runGit(dir, "fetch", "--depth", "1", "origin"); err != nil {
		return fmt.Errorf("fetch: %v: %s", err, firstLine(out))
	}
	// Determine the remote default branch head and hard-reset to it so a
	// force-pushed or rebased config repo still applies cleanly.
	head, err := runGit(dir, "rev-parse", "--abbrev-ref", "origin/HEAD")
	ref := strings.TrimSpace(head)
	if err != nil || ref == "" {
		ref = "origin/HEAD"
	}
	if out, err := runGit(dir, "reset", "--hard", ref); err != nil {
		return fmt.Errorf("reset: %v: %s", err, firstLine(out))
	}
	return nil
}

func runGit(dir string, args ...string) (string, error) {
	ctxCmd := exec.Command("git", args...)
	if dir != "" {
		ctxCmd.Dir = dir
	}
	// Bound the operation so a hung network op can't stall startup forever.
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = ctxCmd.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		_ = ctxCmd.Process.Kill()
		return "", fmt.Errorf("git %s timed out", strings.Join(args, " "))
	}
	return string(out), err
}

func isGitRepo(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && info.IsDir()
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func logf(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, format+"\n", args...)
}
