// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package layout

import (
	"os"
	"path/filepath"
	"testing"
)

var settings = []string{
	"COPILOT_WORKSPACE", "COPILOT_DB_PATH", "OPENCODE_CONFIG_DIR", "SDS_CONFIG_CACHE_DIR",
	"COPILOT_OBSIDIAN_INSTALL_DIR", "KWA_OBSIDIAN_VAULT", "COPILOT_OBSIDIAN_VAULT", "XDG_CONFIG_HOME",
	"COPILOT_GITHUB_PAT_PATH", "COPILOT_HOME",
}

// setup gives the test its own home folder and root, with no location settings.
func setup(t *testing.T) (home, root string) {
	t.Helper()
	home = t.TempDir()
	root = filepath.Join(t.TempDir(), "kwa")
	t.Setenv("HOME", home)
	t.Setenv(EnvHome, root)
	for _, s := range settings {
		t.Setenv(s, "")
	}
	return home, root
}

func complete(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(MigrationDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(CompletionFile(), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

type location struct {
	name string
	get  func() string
}

var locations = []location{
	{"Workspace", Workspace},
	{"Database", Database},
	{"Projects", Projects},
	{"ScheduledTasks", ScheduledTasks},
	{"PlatformConfig", PlatformConfig},
	{"ConfigCache", ConfigCache},
	{"MCPKeys", MCPKeys},
	{"ObsidianInstall", ObsidianInstall},
	{"ObsidianVault", ObsidianVault},
	{"LogFile", LogFile},
	{"GitHubToken", GitHubToken},
}

func TestBeforeTheMigrationEveryLocationKeepsItsPlace(t *testing.T) {
	home, _ := setup(t)
	want := map[string]string{
		"Workspace":       "/home/developer",
		"Database":        filepath.Join(home, ".copilot-web.db"),
		"Projects":        filepath.Join(home, "Projects"),
		"ScheduledTasks":  filepath.Join(home, "Scheduled Tasks"),
		"PlatformConfig":  filepath.Join(home, ".config", "opencode-platform"),
		"ConfigCache":     filepath.Join(home, ".config", "sds-config-repo"),
		"MCPKeys":         filepath.Join(home, ".config", "sds-mcp-keys"),
		"ObsidianInstall": filepath.Join(home, ".local"),
		"ObsidianVault":   filepath.Join(home, "ObsidianVault"),
		"LogFile":         "/tmp/chat-logs/chat.log",
		"GitHubToken":     filepath.Join(home, ".copilot", "github-pat.json"),
	}
	if Migrated() {
		t.Fatal("Migrated() = true with no completion file")
	}
	for _, l := range locations {
		if got := l.get(); got != want[l.name] {
			t.Errorf("%s = %q, want %q", l.name, got, want[l.name])
		}
	}
}

func TestAfterTheMigrationEveryLocationIsUnderTheRoot(t *testing.T) {
	_, root := setup(t)
	complete(t)
	sys := filepath.Join(root, ".system")
	want := map[string]string{
		"Workspace":       root,
		"Database":        filepath.Join(sys, "knowledge-worker-agent.db"),
		"Projects":        filepath.Join(root, "Projects"),
		"ScheduledTasks":  filepath.Join(root, "Scheduled Tasks"),
		"PlatformConfig":  filepath.Join(sys, "config"),
		"ConfigCache":     filepath.Join(sys, "config-repo"),
		"MCPKeys":         filepath.Join(sys, "mcp-keys"),
		"ObsidianInstall": filepath.Join(sys, "obsidian"),
		"ObsidianVault":   filepath.Join(root, "ObsidianVault"),
		"LogFile":         filepath.Join(sys, "logs", "knowledge-worker-agent.log"),
		"GitHubToken":     filepath.Join(sys, "github-pat.json"),
	}
	if !Migrated() {
		t.Fatal("Migrated() = false with a completion file")
	}
	for _, l := range locations {
		if got := l.get(); got != want[l.name] {
			t.Errorf("%s = %q, want %q", l.name, got, want[l.name])
		}
	}
}

func TestXDGConfigHomeIsHonouredBeforeTheMigration(t *testing.T) {
	setup(t)
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	if got := PlatformConfig(); got != "/tmp/xdg/opencode-platform" {
		t.Errorf("PlatformConfig = %q", got)
	}
	if got := MCPKeys(); got != "/tmp/xdg/sds-mcp-keys" {
		t.Errorf("MCPKeys = %q", got)
	}
}

func TestACustomSettingIsKeptBeforeAndAfter(t *testing.T) {
	setup(t)
	custom := map[string]location{
		"COPILOT_WORKSPACE":            {"/srv/work", Workspace},
		"COPILOT_DB_PATH":              {"/srv/kwa.db", Database},
		"OPENCODE_CONFIG_DIR":          {"/srv/platform", PlatformConfig},
		"SDS_CONFIG_CACHE_DIR":         {"/srv/cache", ConfigCache},
		"COPILOT_OBSIDIAN_INSTALL_DIR": {"/srv/obsidian", ObsidianInstall},
		"KWA_OBSIDIAN_VAULT":           {"/srv/vault", ObsidianVault},
		"COPILOT_GITHUB_PAT_PATH":      {"/srv/pat.json", GitHubToken},
	}
	for env, l := range custom {
		t.Setenv(env, l.name)
	}
	for _, phase := range []string{"before", "after"} {
		if phase == "after" {
			complete(t)
		}
		for env, l := range custom {
			if got := l.get(); got != l.name {
				t.Errorf("%s %s the migration: got %q, want %q", env, phase, got, l.name)
			}
		}
	}
}

func TestAnOldDefaultIsUsedBeforeAndIgnoredAfter(t *testing.T) {
	home, root := setup(t)
	sys := filepath.Join(root, ".system")
	cases := []struct {
		env, value string
		get        func() string
		moved      string
	}{
		{"COPILOT_WORKSPACE", "/home/developer", Workspace, root},
		{"COPILOT_WORKSPACE", home, Workspace, root},
		{"COPILOT_DB_PATH", filepath.Join(home, ".copilot-web.db"), Database, filepath.Join(sys, "knowledge-worker-agent.db")},
		{"COPILOT_DB_PATH", "/home/developer/.copilot-web.db", Database, filepath.Join(sys, "knowledge-worker-agent.db")},
		{"COPILOT_DB_PATH", "/home/developer/copilot-web.db", Database, filepath.Join(sys, "knowledge-worker-agent.db")},
		{"OPENCODE_CONFIG_DIR", filepath.Join(home, ".config", "opencode-platform"), PlatformConfig, filepath.Join(sys, "config")},
		{"SDS_CONFIG_CACHE_DIR", filepath.Join(home, ".config", "sds-config-repo") + "/", ConfigCache, filepath.Join(sys, "config-repo")},
	}
	for _, c := range cases {
		t.Run(c.env+"="+c.value, func(t *testing.T) {
			_ = os.RemoveAll(MigrationDir())
			t.Setenv(c.env, c.value)
			if got := c.get(); got != c.value {
				t.Errorf("before: got %q, want the setting %q", got, c.value)
			}
			complete(t)
			if got := c.get(); got != c.moved {
				t.Errorf("after: got %q, want %q", got, c.moved)
			}
		})
	}
}

// COPILOT_HOME names the folder the token file is in: a folder of the
// administrator's choosing keeps it, and the old default doesn't.
func TestTheGitHubTokensFolderSetting(t *testing.T) {
	home, root := setup(t)
	t.Setenv("COPILOT_HOME", "/srv/copilot")
	complete(t)
	if got := GitHubToken(); got != "/srv/copilot/github-pat.json" {
		t.Errorf("custom folder: %q", got)
	}
	t.Setenv("COPILOT_HOME", filepath.Join(home, ".copilot"))
	if got := GitHubToken(); got != filepath.Join(root, ".system", "github-pat.json") {
		t.Errorf("old default: %q", got)
	}
	t.Setenv("COPILOT_GITHUB_PAT_PATH", "/srv/pat.json")
	if got := GitHubToken(); got != "/srv/pat.json" {
		t.Errorf("the file's own setting wins: %q", got)
	}
}

func TestOnlyACompletionFileCounts(t *testing.T) {
	setup(t)
	if err := os.MkdirAll(CompletionFile(), 0o755); err != nil {
		t.Fatal(err)
	}
	if Migrated() {
		t.Error("a folder named like the completion file counted as completion")
	}
}

func TestATestWithoutItsOwnRootNeverSeesARealMigration(t *testing.T) {
	home, _ := setup(t)
	t.Setenv(EnvHome, "")
	done := filepath.Join(home, DefaultDirName, ".system", "migration", "completed")
	if err := os.MkdirAll(filepath.Dir(done), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(done, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if Migrated() {
		t.Error("Migrated() = true for the home folder's root without KWA_HOME")
	}
	if got := Workspace(); got != "/home/developer" {
		t.Errorf("Workspace = %q, want today's", got)
	}
}

func TestRoot(t *testing.T) {
	home, _ := setup(t)
	t.Setenv(EnvHome, "")
	if got := Root(); got != filepath.Join(home, DefaultDirName) {
		t.Errorf("default Root = %q", got)
	}
	wd, _ := os.Getwd()
	t.Setenv(EnvHome, "rel/kwa")
	if got := Root(); got != filepath.Join(wd, "rel", "kwa") {
		t.Errorf("relative KWA_HOME: Root = %q", got)
	}
}

func TestBackups(t *testing.T) {
	_, root := setup(t)
	custom := "/srv/kwa.db"
	if got := Backups(custom); got != custom+".backups" {
		t.Errorf("before: %q", got)
	}
	complete(t)
	if got := Backups(Database()); got != filepath.Join(root, ".system", "backups") {
		t.Errorf("moved database: %q", got)
	}
	if got := Backups(custom); got != custom+".backups" {
		t.Errorf("custom database after: %q", got)
	}
}

func TestLocations(t *testing.T) {
	home, root := setup(t)
	sys := filepath.Join(root, ".system")
	t.Setenv("COPILOT_WORKSPACE", "/home/developer")
	t.Setenv("SDS_CONFIG_CACHE_DIR", "/srv/cache")
	byName := map[string]Location{}
	for _, l := range Locations() {
		byName[l.Name] = l
	}
	want := []Location{
		{Name: NameDatabase, Setting: "COPILOT_DB_PATH", Current: filepath.Join(home, ".copilot-web.db"), Moved: filepath.Join(sys, "knowledge-worker-agent.db")},
		{Name: NameBackups, Setting: "COPILOT_DB_PATH", Current: filepath.Join(home, ".copilot-web.db.backups"), Moved: filepath.Join(sys, "backups")},
		{Name: NameWorkspace, Setting: "COPILOT_WORKSPACE", Current: "/home/developer", Moved: root},
		{Name: NameChats, Setting: "COPILOT_WORKSPACE", Current: "/home/developer/Chats", Moved: filepath.Join(root, "Chats")},
		{Name: NameUpload, Setting: "COPILOT_WORKSPACE", Current: "/home/developer/upload", Moved: filepath.Join(root, "upload")},
		{Name: NameProjects, Current: filepath.Join(home, "Projects"), Moved: filepath.Join(root, "Projects")},
		{Name: NameConfigCache, Setting: "SDS_CONFIG_CACHE_DIR", Current: "/srv/cache", Moved: filepath.Join(sys, "config-repo"), Custom: true},
		{Name: NameObsidianInstall, Setting: "COPILOT_OBSIDIAN_INSTALL_DIR", Current: filepath.Join(home, ".local"), Moved: filepath.Join(sys, "obsidian")},
		{Name: NameGitHubToken, Setting: "COPILOT_GITHUB_PAT_PATH", Current: filepath.Join(home, ".copilot", "github-pat.json"), Moved: filepath.Join(sys, "github-pat.json")},
	}
	for _, w := range want {
		if got := byName[w.Name]; got != w {
			t.Errorf("%s:\n got %+v\nwant %+v", w.Name, got, w)
		}
	}
	if len(byName) != 14 {
		t.Errorf("%d locations, want 14", len(byName))
	}

	// A custom database keeps its snapshots next to it.
	t.Setenv("COPILOT_DB_PATH", "/srv/kwa.db")
	for _, l := range Locations() {
		if l.Name == NameBackups && (!l.Custom || l.Current != "/srv/kwa.db.backups") {
			t.Errorf("custom database's snapshots: %+v", l)
		}
	}
}

func TestOldPath(t *testing.T) {
	home, root := setup(t)
	write := func(path, data string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	moved := `[{"from":"` + filepath.Join(home, "Chats") + `","to":"` + filepath.Join(root, "Chats") + `"},` +
		`{"from":"` + filepath.Join(home, "chat-old") + `","to":"` + filepath.Join(root, "Chats", "chat-old") + `"}]`
	if _, ok := OldPath(filepath.Join(root, "Chats", "chat-a")); ok {
		t.Error("an old path before anything moved")
	}
	write(MovedFile(), moved)
	complete(t)
	for in, want := range map[string]string{
		filepath.Join(root, "Chats", "chat-a", "x.md"): filepath.Join(home, "Chats", "chat-a", "x.md"),
		filepath.Join(root, "Chats", "chat-old"):       filepath.Join(home, "chat-old"),
		filepath.Join(root, "Chats"):                   filepath.Join(home, "Chats"),
	} {
		if got, ok := OldPath(in); !ok || got != want {
			t.Errorf("OldPath(%s) = %s, %v; want %s", in, got, ok, want)
		}
	}
	for _, in := range []string{home, filepath.Join(root, "Chatsroom"), filepath.Join(root, "Projects")} {
		if got, ok := OldPath(in); ok {
			t.Errorf("OldPath(%s) = %s; want none", in, got)
		}
	}

	// An undo's record, with no completion: a chat moved back was in the root.
	if err := os.Remove(CompletionFile()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(MovedFile()); err != nil {
		t.Fatal(err)
	}
	write(MovedFile(), `[{"from":"`+filepath.Join(root, "Chats")+`","to":"`+filepath.Join(home, "Chats")+`"}]`)
	if got, ok := OldPath(filepath.Join(home, "Chats", "chat-a")); !ok || got != filepath.Join(root, "Chats", "chat-a") {
		t.Errorf("after an undo, OldPath = %s, %v", got, ok)
	}
}
