// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package diagnostics collects what a user, or an administrator helping them,
// needs to troubleshoot a workspace: versions, status, the central
// configuration and how it was applied, providers, connectors, settings, disk
// and logs. It only reads; nothing here changes the workspace.
package diagnostics

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/defaults"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/materializer"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/mcp"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodeinstaller"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/providers"
)

// Sources are the facts only the server's main package holds.
type Sources struct {
	Version       string
	BuildTime     string
	StartedAt     time.Time
	Status        func() string
	OpencodeReady func() bool
	ServerBackend bool
	OpencodeBin   func() string
	Models        func() []string
	LogFile       string
	// UserConfigDir is opencode's Global config dir, the user's own.
	UserConfigDir string
}

var src Sources

// Configure sets the server facts. Call it once at startup.
func Configure(s Sources) { src = s }

// Report is the diagnostics overview.
type Report struct {
	GeneratedAt time.Time   `json:"generated_at"`
	App         App         `json:"app"`
	Opencode    Opencode    `json:"opencode"`
	Config      Config      `json:"config"`
	Providers   Providers   `json:"providers"`
	Connectors  Connectors  `json:"connectors"`
	Settings    []Setting   `json:"settings"`
	OldSettings []string    `json:"old_settings"`
	Paths       []PathEntry `json:"paths"`
}

type App struct {
	Version       string    `json:"version"`
	BuildTime     string    `json:"build_time"`
	GoVersion     string    `json:"go_version"`
	Platform      string    `json:"platform"`
	StartedAt     time.Time `json:"started_at"`
	UptimeSeconds int64     `json:"uptime_seconds"`
	Status        string    `json:"status"`
	OpencodeReady bool      `json:"opencode_ready"`
	// Backend is how chats run opencode: "run" per turn, or "serve".
	Backend string `json:"backend"`
}

type Opencode struct {
	Version     string `json:"version"`
	Pinned      string `json:"pinned"`
	MatchesPin  bool   `json:"matches_pin"`
	Bin         string `json:"bin"`
	LogDir      string `json:"log_dir"`
	ConfigDir   string `json:"config_dir"`
	PlatformDir string `json:"platform_dir"`
}

type Config struct {
	// Enabled is the central management switch, which governs providers and
	// connectors. Assigned agents, skills and context apply either way.
	Enabled bool   `json:"enabled"`
	Project string `json:"project"`
	Source  string `json:"source"`
	// SourceKind is "git" for a repository URL, "local" for a folder used in place.
	SourceKind string   `json:"source_kind"`
	LastRun    *LastRun `json:"last_run"`
	// Applied is the run whose configuration is in use; LastRun may be a later,
	// failed attempt.
	Applied *LastRun `json:"applied"`
	// Refresh is the latest refresh started from the diagnostics, or nil.
	Refresh *RefreshState `json:"refresh"`
	// Hidden are the user's own items an assigned item of the same name hides.
	Hidden []Item `json:"hidden"`
	// KeptDefaults are retired default files left in place because the user
	// edited them, relative to the user's opencode config dir.
	KeptDefaults []string `json:"kept_defaults"`
}

type LastRun struct {
	At          time.Time      `json:"at"`
	DurationMS  int64          `json:"duration_ms"`
	OK          bool           `json:"ok"`
	Error       string         `json:"error,omitempty"`
	Skipped     bool           `json:"skipped"`
	SkipReason  string         `json:"skip_reason,omitempty"`
	Stale       bool           `json:"stale"`
	StaleReason string         `json:"stale_reason,omitempty"`
	Commit      *Commit        `json:"commit"`
	Counts      map[string]int `json:"counts"`
	Warnings    []string       `json:"warnings"`
}

type Commit struct {
	Hash    string    `json:"hash"`
	Date    time.Time `json:"date"`
	Subject string    `json:"subject"`
}

type Item struct {
	Kind string `json:"kind"` // agent, skill or connector
	Name string `json:"name"`
}

type Providers struct {
	Mode           string            `json:"mode"`
	FallbackReason string            `json:"fallback_reason,omitempty"`
	Items          []ProviderItem    `json:"items"`
	ModelCount     int               `json:"model_count"`
	DefaultModel   string            `json:"default_model"`
	Presets        map[string]string `json:"presets"`
}

type ProviderItem struct {
	ID            string `json:"id"`
	Label         string `json:"label"`
	Authenticated bool   `json:"authenticated"`
	Reachability  string `json:"reachability"`
	Note          string `json:"note,omitempty"`
	CheckedAt     string `json:"checked_at,omitempty"`
	Models        int    `json:"models"`
}

type Connectors struct {
	Total        int      `json:"total"`
	On           int      `json:"on"`
	Connected    int      `json:"connected"`
	NotConnected []string `json:"not_connected"`
	Assigned     int      `json:"assigned"`
	StatusKnown  bool     `json:"status_known"`
}

type Setting struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	// From is the old name the value was read from, when it isn't Name itself.
	From string `json:"from,omitempty"`
}

type PathEntry struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

// Collect builds the report. It reads files and cached state, and runs
// `opencode --version`; it makes no network calls.
func Collect() Report {
	now := time.Now()
	r := Report{
		GeneratedAt: now,
		App:         collectApp(now),
		Opencode:    collectOpencode(),
		Config:      collectConfig(),
		Providers:   collectProviders(),
		Connectors:  collectConnectors(),
		Settings:    collectSettings(),
		OldSettings: env.OldInUse(),
	}
	if r.OldSettings == nil {
		r.OldSettings = []string{}
	}
	r.Paths = []PathEntry{
		{"Knowledge Worker Agent folder", layout.Root()},
		{"Chats", config.Workspace},
		{"Database", config.DBPath},
		{"Log", src.LogFile},
		{"Assigned configuration", r.Opencode.PlatformDir},
		{"Your opencode configuration", src.UserConfigDir},
		{"opencode data", opencodeDataDir()},
	}
	return r
}

func collectApp(now time.Time) App {
	a := App{
		Version:   src.Version,
		BuildTime: src.BuildTime,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
		StartedAt: src.StartedAt,
		Backend:   "run",
	}
	if !src.StartedAt.IsZero() {
		a.UptimeSeconds = int64(now.Sub(src.StartedAt).Seconds())
	}
	if src.Status != nil {
		a.Status = src.Status()
	}
	if src.OpencodeReady != nil {
		a.OpencodeReady = src.OpencodeReady()
	}
	if src.ServerBackend {
		a.Backend = "serve"
	}
	return a
}

var semver = regexp.MustCompile(`\d+\.\d+\.\d+`)

func collectOpencode() Opencode {
	o := Opencode{
		Pinned:      opencodeinstaller.PinnedVersion,
		LogDir:      filepath.Join(opencodeDataDir(), "log"),
		ConfigDir:   src.UserConfigDir,
		PlatformDir: os.Getenv(config.EnvOpencodeConfigDir),
	}
	if src.OpencodeBin != nil {
		o.Bin = src.OpencodeBin()
		o.Version = strings.TrimSpace(opencodeinstaller.VersionString(o.Bin))
	}
	o.MatchesPin = o.Version != "" && semver.FindString(o.Version) == strings.TrimPrefix(o.Pinned, "v")
	return o
}

func collectConfig() Config {
	c := Config{
		Enabled:      config.CentralConfigEnabled(),
		Project:      strings.TrimSpace(env.Get(materializer.EnvProjectID)),
		Hidden:       hiddenItems(os.Getenv(config.EnvOpencodeConfigDir), src.UserConfigDir),
		KeptDefaults: []string{},
	}
	source := strings.TrimSpace(env.Get(materializer.EnvConfigRepo))
	c.Source = redactSource(source)
	if source != "" {
		c.SourceKind = "git"
		if info, err := os.Stat(source); err == nil && info.IsDir() {
			c.SourceKind = "local"
		}
	}
	if src.UserConfigDir != "" {
		c.KeptDefaults = append(c.KeptDefaults, defaults.KeptEditedFiles(src.UserConfigDir)...)
	}
	if rec, ok := materializer.LastRun(); ok {
		c.LastRun = lastRunOf(rec)
	}
	if rec, ok := materializer.LastApplied(); ok {
		c.Applied = lastRunOf(rec)
	}
	c.Refresh = CurrentRefresh()
	return c
}

func lastRunOf(rec materializer.RunRecord) *LastRun {
	res := rec.Result
	l := &LastRun{
		At:          rec.At,
		DurationMS:  rec.Duration.Milliseconds(),
		OK:          rec.Err == nil,
		Skipped:     res.Skipped,
		SkipReason:  res.SkipReason,
		Stale:       res.Stale,
		StaleReason: Redact(res.StaleReason),
		Counts:      res.Counts,
		Warnings:    []string{},
	}
	if rec.Err != nil {
		l.Error = Redact(rec.Err.Error())
	}
	for _, w := range res.Warnings {
		l.Warnings = append(l.Warnings, Redact(w))
	}
	if res.Commit.Hash != "" {
		l.Commit = &Commit{Hash: res.Commit.Hash, Date: res.Commit.Date, Subject: res.Commit.Subject}
	}
	if l.Counts == nil {
		l.Counts = map[string]int{}
	}
	return l
}

// hiddenItems lists the user's own agents, skills and connectors that an
// assigned one of the same name hides. The skills Knowledge Worker Agent bundles
// are left out: their copies are its own, not the user's.
func hiddenItems(platformDir, userDir string) []Item {
	out := []Item{}
	if platformDir == "" || userDir == "" {
		return out
	}
	stems := func(dir string) map[string]bool {
		m := map[string]bool{}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				m[strings.TrimSuffix(e.Name(), ".md")] = true
			}
		}
		return m
	}
	dirs := func(paths ...string) map[string]bool {
		m := map[string]bool{}
		for _, p := range paths {
			entries, _ := os.ReadDir(p)
			for _, e := range entries {
				if e.IsDir() {
					m[e.Name()] = true
				}
			}
		}
		return m
	}
	assignedAgents := stems(filepath.Join(platformDir, "agent"))
	for name := range stems(filepath.Join(userDir, "agent")) {
		if assignedAgents[name] {
			out = append(out, Item{"agent", name})
		}
	}
	bundled := map[string]bool{}
	for _, name := range defaults.BundledSkillNames() {
		bundled[name] = true
	}
	assignedSkills := dirs(filepath.Join(platformDir, "skills"), filepath.Join(platformDir, "skill"))
	for name := range dirs(filepath.Join(userDir, "skills"), filepath.Join(userDir, "skill")) {
		if assignedSkills[name] && !bundled[name] {
			out = append(out, Item{"skill", name})
		}
	}
	for _, name := range mcp.ShadowedServers() {
		out = append(out, Item{"connector", name})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func collectProviders() Providers {
	reg := providers.Resolve()
	var models []string
	if src.Models != nil {
		models = src.Models()
	}
	p := Providers{
		Mode:           string(reg.Mode),
		FallbackReason: reg.FallbackReason,
		Items:          []ProviderItem{},
		ModelCount:     len(models),
		DefaultModel:   config.DefaultModel(),
		Presets:        config.PresetModels(),
	}
	if p.Presets == nil {
		p.Presets = map[string]string{}
	}
	for _, pr := range reg.Providers {
		n := 0
		for _, m := range models {
			if strings.HasPrefix(m, pr.ID+"/") {
				n++
			}
		}
		p.Items = append(p.Items, ProviderItem{
			ID:            pr.ID,
			Label:         pr.Label,
			Authenticated: pr.Authenticated,
			Reachability:  string(pr.Reachability),
			Note:          Redact(pr.ReachabilityNote),
			CheckedAt:     pr.ReachabilityCheckedAt,
			Models:        n,
		})
	}
	return p
}

func collectConnectors() Connectors {
	c := Connectors{NotConnected: []string{}}
	servers, err := mcp.GlobalServers()
	if err != nil {
		return c
	}
	status := db.AllMcpStatus()
	c.StatusKnown = len(status) > 0
	for _, s := range servers {
		c.Total++
		if mcp.IsProvisioned(s.Name) {
			c.Assigned++
		}
		if !s.Enabled {
			continue
		}
		c.On++
		if status[strings.ToLower(s.Name)] || status[s.Name] {
			c.Connected++
		} else {
			c.NotConnected = append(c.NotConnected, s.Name)
		}
	}
	return c
}

// A setting whose name suggests a secret never shows its value.
var secretName = regexp.MustCompile(`(?i)(SECRET|TOKEN|PASSWORD|PASSWD|CREDENTIAL|_PAT\b|_KEY\b|APIKEY)`)

// collectSettings lists the KWA_ settings in effect, including those read
// from an old name.
func collectSettings() []Setting {
	seen := map[string]bool{}
	out := []Setting{}
	add := func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		value := env.Get(name)
		if strings.TrimSpace(value) == "" {
			return
		}
		s := Setting{Name: name}
		for _, n := range env.Names(name) {
			if strings.TrimSpace(os.Getenv(n)) != "" {
				if n != name {
					s.From = n
				}
				break
			}
		}
		switch {
		case secretName.MatchString(name):
			s.Value = "(set)"
		case name == materializer.EnvConfigRepo:
			s.Value = redactSource(value)
		default:
			s.Value = Redact(value)
		}
		out = append(out, s)
	}
	for _, kv := range os.Environ() {
		if name, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(name, "KWA_") {
			add(name)
		}
	}
	for _, name := range env.Renamed() {
		add(name)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// opencodeDataDir mirrors opencode's own data dir: $XDG_DATA_HOME/opencode,
// else ~/.local/share/opencode.
func opencodeDataDir() string {
	base := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "opencode")
}
