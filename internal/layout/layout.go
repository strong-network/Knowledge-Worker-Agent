// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package layout says where Knowledge Worker Agent keeps its files.
//
// Everything it owns belongs in one folder, the root: KWA_HOME, by default
// ~/Knowledge_Worker_Agent. A one-time migration moves the files there. Until
// it has completed, every location keeps the place it has always had, so code
// that asks this package for its paths changes nothing on disk.
//
// Each location resolves the same way:
//   - an explicit setting is used as given, unless it only repeats an old
//     default and the migration has completed;
//   - otherwise, once the migration has completed, the location is under the
//     root;
//   - otherwise it's where it has always been.
package layout

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
)

// EnvHome names the root folder.
const EnvHome = "KWA_HOME"

// DefaultDirName is the root's name in the home folder when KWA_HOME is unset.
const DefaultDirName = "Knowledge_Worker_Agent"

// legacyHome is the home folder every image uses, and the old default for the
// base folder on any machine.
const legacyHome = "/home/developer"

// Root returns the folder everything belongs in once the migration has run.
func Root() string {
	if v := strings.TrimSpace(os.Getenv(EnvHome)); v != "" {
		return absolute(v)
	}
	return filepath.Join(homeOr(legacyHome), DefaultDirName)
}

// System returns the root's hidden folder for what KWA manages itself.
func System() string { return filepath.Join(Root(), ".system") }

// MigrationDir returns the folder holding each migration's journal and backups.
func MigrationDir() string { return filepath.Join(System(), "migration") }

// CompletionFile returns the file the migration writes when it has completed.
func CompletionFile() string { return filepath.Join(MigrationDir(), "completed") }

// MovedFile returns the record of what the last completed run moved, the
// migration or its undo, written just before it completed.
func MovedFile() string { return filepath.Join(MigrationDir(), "moved.json") }

// Rename is one folder or file the migration moved.
type Rename struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Moved returns what the last completed run moved: the migration, or its
// undo, which moves everything back.
func Moved() []Rename {
	if !ownRoot() {
		return nil
	}
	data, err := os.ReadFile(MovedFile())
	if err != nil {
		return nil
	}
	var out []Rename
	if json.Unmarshal(data, &out) != nil {
		return nil
	}
	return out
}

// OldPath returns where path was before the last completed run moved it, and
// whether it moved at all.
func OldPath(path string) (string, bool) {
	clean := filepath.Clean(path)
	best := -1
	moved := Moved()
	for i, r := range moved {
		to := filepath.Clean(r.To)
		if clean != to && !strings.HasPrefix(clean, to+string(filepath.Separator)) {
			continue
		}
		if best < 0 || len(to) > len(filepath.Clean(moved[best].To)) {
			best = i
		}
	}
	if best < 0 {
		return "", false
	}
	r := moved[best]
	return filepath.Join(r.From, strings.TrimPrefix(clean, filepath.Clean(r.To))), true
}

// Migrated reports whether the migration has completed.
func Migrated() bool {
	if !ownRoot() {
		return false
	}
	info, err := os.Lstat(CompletionFile())
	return err == nil && info.Mode().IsRegular()
}

// ownRoot reports whether the root may be read: without its own KWA_HOME, a
// test must not see the developer's real folder.
func ownRoot() bool {
	return !testing.Testing() || strings.TrimSpace(os.Getenv(EnvHome)) != ""
}

// Names of the locations, as Locations returns them.
const (
	NameDatabase        = "Database"
	NameBackups         = "Database snapshots"
	NameWorkspace       = "Base folder"
	NameChats           = "Chats"
	NameUpload          = "Uploads"
	NameProjects        = "Projects"
	NameScheduledTasks  = "Scheduled tasks"
	NameObsidianVault   = "Obsidian vault"
	NamePlatformConfig  = "Central configuration"
	NameConfigCache     = "Configuration repository cache"
	NameMCPKeys         = "Connector keys"
	NameObsidianInstall = "Obsidian connector"
	NameLogFile         = "Log"
	NameGitHubToken     = "GitHub token"
)

// Location is one place KWA keeps something, as the migration sees it.
type Location struct {
	Name    string
	Setting string // the variable that can set it, or ""
	Current string // where it is until the migration has run
	Moved   string // where the migration puts it
	Custom  bool   // set to a place of the administrator's choosing, which never moves
}

// Locations returns every location with both its current and its moved place,
// whether or not the migration has run.
func Locations() []Location {
	ws, db := workspaceSpec().location(), databaseSpec().location()
	under := func(name string, base Location, dir, moved string) Location {
		return Location{Name: name, Setting: base.Setting, Current: filepath.Join(base.Current, dir), Moved: moved, Custom: base.Custom}
	}
	return []Location{
		db,
		{Name: NameBackups, Setting: db.Setting, Current: db.Current + ".backups", Moved: filepath.Join(System(), "backups"), Custom: db.Custom},
		ws,
		under(NameChats, ws, "Chats", filepath.Join(Root(), "Chats")),
		under(NameUpload, ws, "upload", filepath.Join(Root(), "upload")),
		projectsSpec().location(),
		scheduledTasksSpec().location(),
		obsidianVaultSpec().location(),
		platformConfigSpec().location(),
		configCacheSpec().location(),
		mcpKeysSpec().location(),
		obsidianInstallSpec().location(),
		logFileSpec().location(),
		githubTokenSpec().location(),
	}
}

// Workspace returns the base folder: where new clones and the file view start,
// and the parent of the Chats folder.
func Workspace() string { return workspaceSpec().resolve() }

// Database returns the database file.
func Database() string { return databaseSpec().resolve() }

func movedDatabase() string { return filepath.Join(System(), "knowledge-worker-agent.db") }

// Backups returns the folder holding snapshots of the database at db. A
// database at a custom path keeps its snapshots next to it.
func Backups(db string) string {
	if Migrated() && sameFile(db, movedDatabase()) {
		return filepath.Join(System(), "backups")
	}
	return db + ".backups"
}

// Projects returns the folder holding project workspaces.
func Projects() string { return projectsSpec().resolve() }

// ScheduledTasks returns the folder holding scheduled tasks' workspaces.
func ScheduledTasks() string { return scheduledTasksSpec().resolve() }

// PlatformConfig returns the folder central configuration is written to and
// opencode reads as OPENCODE_CONFIG_DIR, or "" when the home folder is unknown.
func PlatformConfig() string { return platformConfigSpec().resolve() }

// ConfigCache returns the folder the configuration repository is cached in, or
// "" when the home folder is unknown.
func ConfigCache() string { return configCacheSpec().resolve() }

// MCPKeys returns the folder holding connectors' API keys, or "" when the home
// folder is unknown.
func MCPKeys() string { return mcpKeysSpec().resolve() }

// ObsidianInstall returns the folder obsidian-mcp is installed under.
func ObsidianInstall() string { return obsidianInstallSpec().resolve() }

// ObsidianVault returns the Obsidian connector's vault.
func ObsidianVault() string { return obsidianVaultSpec().resolve() }

// LogFile returns the server's log file when KWA_LOG_FILE doesn't name one.
func LogFile() string { return logFileSpec().resolve() }

// GitHubToken returns the file holding the GitHub token saved in the app.
func GitHubToken() string { return githubTokenSpec().resolve() }

// spec is a location's setting (env, or "" for none), its place before the
// migration (today), its place after it (moved), and any other values that
// count as old defaults besides today. dirEnv names a setting for the folder
// a file location is in, read when env isn't set.
type spec struct {
	name, env, dirEnv, today, moved string
	also                            []string
}

func workspaceSpec() spec {
	return spec{name: NameWorkspace, env: "COPILOT_WORKSPACE", today: legacyHome, moved: Root(), also: []string{homeOr("")}}
}

func databaseSpec() spec {
	today := "./copilot-web.db"
	var also []string
	if h := homeOr(""); h != "" {
		today = filepath.Join(h, ".copilot-web.db")
		also = append(also, filepath.Join(h, "copilot-web.db"))
	}
	also = append(also,
		filepath.Join(legacyHome, ".copilot-web.db"),
		filepath.Join(legacyHome, "copilot-web.db"))
	return spec{name: NameDatabase, env: "COPILOT_DB_PATH", today: today, moved: movedDatabase(), also: also}
}

func projectsSpec() spec {
	return spec{name: NameProjects, today: filepath.Join(homeOr(legacyHome), "Projects"), moved: filepath.Join(Root(), "Projects")}
}

func scheduledTasksSpec() spec {
	return spec{name: NameScheduledTasks, today: filepath.Join(homeOr(legacyHome), "Scheduled Tasks"), moved: filepath.Join(Root(), "Scheduled Tasks")}
}

func platformConfigSpec() spec {
	return spec{name: NamePlatformConfig, env: "OPENCODE_CONFIG_DIR", today: inConfigHome("opencode-platform"), moved: filepath.Join(System(), "config")}
}

func configCacheSpec() spec {
	return spec{name: NameConfigCache, env: "SDS_CONFIG_CACHE_DIR", today: inConfigHome("sds-config-repo"), moved: filepath.Join(System(), "config-repo")}
}

func mcpKeysSpec() spec {
	return spec{name: NameMCPKeys, today: inConfigHome("sds-mcp-keys"), moved: filepath.Join(System(), "mcp-keys")}
}

func obsidianInstallSpec() spec {
	today := "/usr/local"
	if h := homeOr(""); h != "" {
		today = filepath.Join(h, ".local")
	}
	return spec{name: NameObsidianInstall, env: "COPILOT_OBSIDIAN_INSTALL_DIR", today: today, moved: filepath.Join(System(), "obsidian")}
}

func obsidianVaultSpec() spec {
	today := "/tmp/ObsidianVault"
	if h := homeOr(""); h != "" {
		today = filepath.Join(h, "ObsidianVault")
	}
	return spec{name: NameObsidianVault, env: "KWA_OBSIDIAN_VAULT", today: today, moved: filepath.Join(Root(), "ObsidianVault")}
}

func logFileSpec() spec {
	return spec{name: NameLogFile, today: "/tmp/chat-logs/chat.log", moved: filepath.Join(System(), "logs", "knowledge-worker-agent.log")}
}

func githubTokenSpec() spec {
	return spec{name: NameGitHubToken, env: "COPILOT_GITHUB_PAT_PATH", dirEnv: "COPILOT_HOME",
		today: filepath.Join(homeOr(legacyHome), ".copilot", "github-pat.json"), moved: filepath.Join(System(), "github-pat.json")}
}

// setting returns the location's explicit setting, or "".
func (s spec) setting() string {
	if s.env != "" {
		if v := strings.TrimSpace(env.Get(s.env)); v != "" {
			return v
		}
	}
	if s.dirEnv != "" {
		if d := strings.TrimSpace(os.Getenv(s.dirEnv)); d != "" {
			return filepath.Join(d, filepath.Base(s.today))
		}
	}
	return ""
}

// resolve returns where the location is now: an explicit setting as given,
// unless it only repeats an old default and the migration has completed; the
// moved place once the migration has completed; today's place before.
func (s spec) resolve() string {
	migrated := Migrated()
	if v := s.setting(); v != "" {
		if !migrated || !isOldDefault(v, s.today, s.also) {
			return v
		}
	}
	if migrated {
		return s.moved
	}
	return s.today
}

func (s spec) location() Location {
	l := Location{Name: s.name, Setting: s.env, Current: s.today, Moved: s.moved}
	if v := s.setting(); v != "" {
		l.Current = v
		l.Custom = !isOldDefault(v, s.today, s.also)
	}
	return l
}

func isOldDefault(v, today string, also []string) bool {
	v = absolute(v)
	for _, d := range append([]string{today}, also...) {
		if d != "" && v == absolute(d) {
			return true
		}
	}
	return false
}

// inConfigHome returns name in the user's configuration folder, as opencode
// resolves it: XDG_CONFIG_HOME, else ~/.config.
func inConfigHome(name string) string {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, name)
	}
	if h := homeOr(""); h != "" {
		return filepath.Join(h, ".config", name)
	}
	return ""
}

func homeOr(fallback string) string {
	if h, err := os.UserHomeDir(); err == nil && strings.TrimSpace(h) != "" {
		return h
	}
	return fallback
}

func absolute(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return filepath.Clean(p)
}

func sameFile(a, b string) bool {
	if absolute(a) == absolute(b) {
		return true
	}
	ai, err1 := os.Stat(a)
	bi, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(ai, bi)
}
