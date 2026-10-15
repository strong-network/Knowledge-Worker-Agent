// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package materializer

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// Env var names (interim inputs until the platform wires project
// identity and the config-repo location natively).
const (
	// EnvProjectID identifies the workspace's project. Without it, no artifacts
	// resolve and materialization is a clean no-op.
	EnvProjectID = "KWA_PROJECT_ID"
	// EnvConfigRepo is the config repo source: a git URL or a local dir path.
	EnvConfigRepo = "KWA_CONFIG_REPO_URL"
	// EnvCacheDir overrides where the config repo is cloned/cached.
	EnvCacheDir = "SDS_CONFIG_CACHE_DIR"
	// EnvConfigDir overrides the platform-owned OpenCode config dir target.
	EnvConfigDir = "OPENCODE_CONFIG_DIR"
)

// Options fully specifies a materialization run. Run() populates these from the
// environment when they are empty.
type Options struct {
	ProjectID     string    // workspace project id
	RepoSource    string    // git URL or local path to the config repo
	CacheDir      string    // where the repo is cloned/cached
	ConfigDir     string    // platform-owned OPENCODE_CONFIG_DIR target
	UserGlobalDir string    // user's ~/.config/opencode (never written; wipe guard)
	Log           io.Writer // progress log sink (nil = quiet)
}

// Result reports what a run did, for logging/telemetry.
type Result struct {
	Skipped      bool   // true when materialization was a no-op
	SkipReason   string // human-readable reason when Skipped
	ProjectID    string
	RepoDir      string
	ConfigDir    string
	Counts       map[string]int // artifacts written per kind
	MCPDefaultOn []string
	// Warnings describe artifact content that was accepted but partly ignored
	// (e.g. an MCP artifact declaring "enabled"). The run succeeded.
	Warnings []string
	// Source is the configured repo source, as given (it may hold credentials).
	Source string
	// Stale is set when the repo could not be updated and an earlier clone was
	// used; StaleReason says why.
	Stale       bool
	StaleReason string
	// Commit is the config repo commit applied; zero when the source is not a
	// git checkout of its own.
	Commit Commit
}

// RunRecord is the outcome of a Run.
type RunRecord struct {
	At       time.Time
	Duration time.Duration
	Result   Result
	Err      error
}

var lastRun struct {
	sync.Mutex
	rec, applied *RunRecord
}

// LastRun returns the outcome of the most recent Run in this process.
func LastRun() (RunRecord, bool) {
	lastRun.Lock()
	defer lastRun.Unlock()
	if lastRun.rec == nil {
		return RunRecord{}, false
	}
	return *lastRun.rec, true
}

// LastApplied returns the most recent Run that applied a configuration: the
// one in use, since a failed run leaves the previous one in place.
func LastApplied() (RunRecord, bool) {
	lastRun.Lock()
	defer lastRun.Unlock()
	if lastRun.applied == nil {
		return RunRecord{}, false
	}
	return *lastRun.applied, true
}

// FromEnv builds Options from the environment, filling any zero fields.
// userGlobalDir is passed in by the caller (it already knows OpenCode's Global
// dir); when non-empty it also seeds a default cache/config dir alongside it.
func (o Options) FromEnv(userGlobalDir string) Options {
	if o.ProjectID == "" {
		o.ProjectID = strings.TrimSpace(env.Get(EnvProjectID))
	}
	if o.RepoSource == "" {
		o.RepoSource = strings.TrimSpace(env.Get(EnvConfigRepo))
	}
	if o.CacheDir == "" {
		o.CacheDir = strings.TrimSpace(os.Getenv(EnvCacheDir))
	}
	if o.ConfigDir == "" {
		o.ConfigDir = strings.TrimSpace(os.Getenv(EnvConfigDir))
	}
	if o.UserGlobalDir == "" {
		o.UserGlobalDir = userGlobalDir
	}
	// After the one-folder migration, both live under its root, unless set to a
	// custom place.
	if layout.Migrated() {
		if o.ConfigDir == "" || o.ConfigDir == strings.TrimSpace(os.Getenv(EnvConfigDir)) {
			o.ConfigDir = layout.PlatformConfig()
		}
		if o.CacheDir == "" || o.CacheDir == strings.TrimSpace(os.Getenv(EnvCacheDir)) {
			o.CacheDir = layout.ConfigCache()
		}
	}
	// Derive sensible defaults from the user Global dir's parent when unset.
	if o.ConfigDir == "" && userGlobalDir != "" {
		o.ConfigDir = filepath.Join(filepath.Dir(userGlobalDir), "opencode-platform")
	}
	if o.CacheDir == "" && userGlobalDir != "" {
		o.CacheDir = filepath.Join(filepath.Dir(userGlobalDir), "sds-config-repo")
	}
	return o
}

// Run resolves and materializes the project's config. It is safe to call
// unconditionally at startup: when the project id or repo source is unset it
// returns a Skipped result and makes no changes, so pre-control-plane
// workspaces are unaffected.
func Run(opts Options) (res Result, err error) {
	started := time.Now()
	defer func() {
		rec := &RunRecord{At: started, Duration: time.Since(started), Result: res, Err: err}
		lastRun.Lock()
		lastRun.rec = rec
		if err == nil && !res.Skipped {
			lastRun.applied = rec
		}
		lastRun.Unlock()
	}()
	res = Result{ProjectID: opts.ProjectID, ConfigDir: opts.ConfigDir, Counts: map[string]int{}, Source: opts.RepoSource}

	if opts.ProjectID == "" {
		res.Skipped, res.SkipReason = true, "no project id ("+EnvProjectID+" unset)"
		logf(opts.Log, "  ℹ Central configuration: skipped — %s", res.SkipReason)
		return res, nil
	}
	if opts.RepoSource == "" {
		res.Skipped, res.SkipReason = true, "no config repo ("+EnvConfigRepo+" unset)"
		logf(opts.Log, "  ℹ Central configuration: skipped — %s", res.SkipReason)
		return res, nil
	}
	if opts.ConfigDir == "" {
		return res, fmt.Errorf("materializer: no config dir (%s) resolved", EnvConfigDir)
	}

	repoDir, stale, err := syncRepo(opts.Log, opts.RepoSource, opts.CacheDir)
	if err != nil {
		return res, fmt.Errorf("materializer: %w", err)
	}
	res.RepoDir = repoDir
	if stale != nil {
		res.Stale, res.StaleReason = true, stale.Error()
	}
	res.Commit = RepoCommit(repoDir)

	data, err := os.ReadFile(filepath.Join(repoDir, "assignment.jsonc"))
	if err != nil {
		return res, fmt.Errorf("materializer: read assignment: %w", err)
	}
	assignment, err := ParseAssignment(data)
	if err != nil {
		return res, fmt.Errorf("materializer: %w", err)
	}

	resolved, err := assignment.Resolve(opts.ProjectID)
	if err != nil {
		return res, fmt.Errorf("materializer: %w", err)
	}

	warnings, err := Materialize(opts.ConfigDir, repoDir, resolved, opts.UserGlobalDir)
	if err != nil {
		return res, fmt.Errorf("materializer: %w", err)
	}
	res.Warnings = warnings
	for _, w := range warnings {
		logf(opts.Log, "  ⚠ Central configuration: %s", w)
	}

	for _, kind := range artifactKinds {
		res.Counts[kind] = len(resolved.ByKind[kind])
	}
	res.MCPDefaultOn = resolved.MCPDefaultOn
	logf(opts.Log, "  ✓ Central configuration: project %s → %s (mcp=%d agents=%d context=%d skills=%d provider=%d vocabulary=%d)",
		opts.ProjectID, opts.ConfigDir,
		res.Counts[KindMCP], res.Counts[KindAgents], res.Counts[KindContext],
		res.Counts[KindSkills], res.Counts[KindProvider], res.Counts[KindVocabulary])
	return res, nil
}
