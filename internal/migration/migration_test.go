// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"bytes"
	"database/sql"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodeinstaller"
)

// home gives the test a home folder of its own, with the root in it and no
// location set to anything but its default.
func home(t *testing.T) (h, root string) {
	t.Helper()
	h = t.TempDir()
	root = filepath.Join(h, layout.DefaultDirName)
	t.Setenv("HOME", h)
	t.Setenv(layout.EnvHome, root)
	t.Setenv("COPILOT_WORKSPACE", h) // the default is the literal /home/developer
	for _, v := range []string{"COPILOT_DB_PATH", "OPENCODE_CONFIG_DIR", "SDS_CONFIG_CACHE_DIR",
		"COPILOT_OBSIDIAN_INSTALL_DIR", "KWA_OBSIDIAN_VAULT", "COPILOT_OBSIDIAN_VAULT", "XDG_CONFIG_HOME", "XDG_DATA_HOME",
		"COPILOT_GITHUB_PAT_PATH", "COPILOT_HOME"} {
		t.Setenv(v, "")
	}
	stubOpencode(t, nil, opencodeinstaller.PinnedVersion)
	return h, root
}

func stubOpencode(t *testing.T, pids []string, version string) {
	t.Helper()
	prevP, prevV, prevS := opencodeProcesses, opencodeVersion, opencodeSettle
	opencodeProcesses = func() []string { return pids }
	opencodeVersion = func(string) string { return version }
	opencodeSettle = 0
	t.Cleanup(func() { opencodeProcesses, opencodeVersion, opencodeSettle = prevP, prevV, prevS })
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func link(t *testing.T, target, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

func sqliteDB(t *testing.T, path string, stmts ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func moveByFrom(p Plan, from string) (Move, bool) {
	for _, m := range p.Moves {
		if m.From == from {
			return m, true
		}
	}
	return Move{}, false
}

func TestModeFromEnv(t *testing.T) {
	home(t)
	for v, want := range map[string]Mode{"": On, "off": Off, "dry-run": DryRun, "ON": On, "undo": Undo} {
		t.Setenv(EnvMode, v)
		if got, err := ModeFromEnv(); got != want || err != nil {
			t.Errorf("%q: got %q, %v; want %q", v, got, err, want)
		}
	}
	t.Setenv(EnvMode, "maybe")
	if got, err := ModeFromEnv(); got != Off || err == nil {
		t.Errorf("unknown value: got %q, %v; want off and an error", got, err)
	}

	// An undo's record with no completion record: the undo lasts until asked otherwise.
	write(t, layout.MovedFile(), `[{"from":"/a","to":"/b"}]`)
	for v, want := range map[string]Mode{"": Off, "on": On, "dry-run": DryRun} {
		t.Setenv(EnvMode, v)
		if got, _ := ModeFromEnv(); got != want {
			t.Errorf("after an undo, %q: got %q, want %q", v, got, want)
		}
	}
	write(t, layout.CompletionFile(), "")
	t.Setenv(EnvMode, "")
	if got, _ := ModeFromEnv(); got != On || Undone() {
		t.Errorf("migrated: got %q, undone %v", got, Undone())
	}
}

func TestPlan(t *testing.T) {
	h, root := home(t)
	write(t, filepath.Join(h, ".copilot-web.db"), "db")
	write(t, filepath.Join(h, "copilot-web.db"), "stray")
	write(t, filepath.Join(h, "Chats", "chat-new", "a.md"), "new")
	write(t, filepath.Join(h, "chat-old", "b.md"), "old")
	link(t, filepath.Join(h, "Chats", "chat-new"), filepath.Join(h, "chat-link"))
	write(t, filepath.Join(h, "Projects", "p", "c.md"), "project")
	write(t, filepath.Join(h, ".config", "opencode-platform", "opencode.json"), "{}")
	write(t, filepath.Join(h, ".local", "lib", "node_modules", "obsidian-mcp", "dist", "main.js"), "js")
	link(t, "../lib/node_modules/obsidian-mcp/dist/main.js", filepath.Join(h, ".local", "bin", "obsidian-mcp"))
	t.Setenv("SDS_CONFIG_CACHE_DIR", "/srv/cache")

	p := NewPlan()
	sys := filepath.Join(root, ".system")
	want := map[string]string{
		filepath.Join(h, ".copilot-web.db"):                               filepath.Join(sys, "knowledge-worker-agent.db"),
		filepath.Join(h, "Chats"):                                         filepath.Join(root, "Chats"),
		filepath.Join(h, "chat-old"):                                      filepath.Join(root, "Chats", "chat-old"),
		filepath.Join(h, "Projects"):                                      filepath.Join(root, "Projects"),
		filepath.Join(h, ".config", "opencode-platform"):                  filepath.Join(sys, "config"),
		filepath.Join(h, ".local", "lib", "node_modules", "obsidian-mcp"): filepath.Join(sys, "obsidian", "lib", "node_modules", "obsidian-mcp"),
		filepath.Join(h, ".local", "bin", "obsidian-mcp"):                 filepath.Join(sys, "obsidian", "bin", "obsidian-mcp"),
	}
	for from, to := range want {
		m, ok := moveByFrom(p, from)
		if !ok || m.To != to {
			t.Errorf("move of %s: got %+v, want to %s", from, m, to)
		}
	}
	if len(p.Moves) != len(want) {
		t.Errorf("%d moves, want %d: %+v", len(p.Moves), len(want), p.Moves)
	}
	if m, _ := moveByFrom(p, filepath.Join(h, ".local", "bin", "obsidian-mcp")); m.Kind != Copy {
		t.Errorf("the Obsidian connector is %s, want copied", m.Kind)
	}
	if m, _ := moveByFrom(p, filepath.Join(h, "chat-old")); m.Name != LegacyChatsName || m.Entries != 2 {
		t.Errorf("legacy chat: %+v", m)
	}
	if _, ok := moveByFrom(p, filepath.Join(h, "chat-link")); ok {
		t.Error("a chat-* link moved")
	}
	if len(p.Kept) != 1 || p.Kept[0].Name != layout.NameConfigCache {
		t.Errorf("kept: %+v", p.Kept)
	}
	if len(p.OtherDatabases) != 1 || p.OtherDatabases[0] != filepath.Join(h, "copilot-web.db") {
		t.Errorf("other databases: %v", p.OtherDatabases)
	}
	absent := map[string]bool{}
	for _, l := range p.Absent {
		absent[l.Name] = true
	}
	if !absent[layout.NameScheduledTasks] || !absent[layout.NameUpload] {
		t.Errorf("absent: %+v", p.Absent)
	}
}

func TestACustomBaseFolderKeepsItsChats(t *testing.T) {
	h, _ := home(t)
	custom := filepath.Join(h, "work")
	t.Setenv("COPILOT_WORKSPACE", custom)
	write(t, filepath.Join(custom, "Chats", "chat-a", "a.md"), "a")
	write(t, filepath.Join(custom, "chat-old", "b.md"), "b")
	p := NewPlan()
	for _, m := range p.Moves {
		if under(m.From, custom) {
			t.Errorf("moved from the custom base folder: %+v", m)
		}
	}
}

func TestRemap(t *testing.T) {
	h, root := home(t)
	write(t, filepath.Join(h, "Chats", "chat-a", "x"), "")
	write(t, filepath.Join(h, "chat-b", "y"), "")
	p := NewPlan()
	for in, want := range map[string]string{
		filepath.Join(h, "Chats", "chat-a"):       filepath.Join(root, "Chats", "chat-a"),
		filepath.Join(h, "Chats"):                 filepath.Join(root, "Chats"),
		filepath.Join(h, "chat-b", "y"):           filepath.Join(root, "Chats", "chat-b", "y"),
		filepath.Join(h, "Chats", "chat-a") + "/": filepath.Join(root, "Chats", "chat-a"),
	} {
		if got, ok := p.Remap(in); !ok || got != want {
			t.Errorf("Remap(%s) = %s, %v; want %s", in, got, ok, want)
		}
	}
	for _, in := range []string{h, filepath.Join(h, "Chatsroom"), filepath.Join(h, "chat-c")} {
		if got, ok := p.Remap(in); ok {
			t.Errorf("Remap(%s) = %s; want it left alone", in, got)
		}
	}
}

func TestCheckMoves(t *testing.T) {
	h, root := home(t)
	write(t, filepath.Join(h, "Projects", "p"), "")
	write(t, filepath.Join(h, "Chats", "chat-same", "a"), "")
	write(t, filepath.Join(h, "chat-same", "b"), "")
	write(t, filepath.Join(root, "Projects", "already-there"), "")
	problems := NewPlan().checkMoves()
	var got []string
	for _, p := range problems {
		got = append(got, p.String())
	}
	joined := strings.Join(got, "\n")
	if len(problems) != 2 || !strings.Contains(joined, "Projects: "+filepath.Join(root, "Projects")+" already exists") ||
		!strings.Contains(joined, filepath.Join(root, "Chats", "chat-same")+" already exists") {
		t.Errorf("problems:\n%s", joined)
	}

	t.Setenv(layout.EnvHome, filepath.Join(h, "Projects", "kwa"))
	if problems := NewPlan().checkMoves(); len(problems) == 0 || problems[0].Check != "target inside its source" {
		t.Errorf("a root inside Projects: %v", problems)
	}
}

func TestScanStoredPaths(t *testing.T) {
	h, _ := home(t)
	write(t, filepath.Join(h, "Chats", "chat-a", "x"), "")
	write(t, filepath.Join(h, "Projects", "p", "x"), "")
	db := filepath.Join(h, ".copilot-web.db")
	sqliteDB(t, db,
		`CREATE TABLE sessions (id TEXT, config TEXT)`,
		`CREATE TABLE projects (id TEXT, workspace_path TEXT)`,
		`CREATE TABLE scheduled_tasks (id TEXT, workdir TEXT)`,
		`INSERT INTO sessions VALUES ('1', '{"workdir":"`+filepath.Join(h, "Chats", "chat-a")+`","add_dirs":["`+filepath.Join(h, "Projects", "p")+`"]}')`,
		`INSERT INTO sessions VALUES ('2', '{"workdir":"`+h+`"}')`,
		`INSERT INTO sessions VALUES ('3', 'not json')`,
		`INSERT INTO projects VALUES ('p', '`+filepath.Join(h, "Projects", "p")+`')`,
		`INSERT INTO scheduled_tasks VALUES ('t', '')`,
	)
	got, err := ScanStoredPaths(db, NewPlan())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"sessions.workdir": 1, "sessions.add_dirs": 1, "projects.workspace_path": 1}
	if len(got.Rewritten) != len(want) || got.Kept != 1 {
		t.Errorf("got %+v, want %v and 1 kept", got, want)
	}
	for k, v := range want {
		if got.Rewritten[k] != v {
			t.Errorf("%s = %d, want %d", k, got.Rewritten[k], v)
		}
	}
	if _, err := ScanStoredPaths(filepath.Join(h, "missing.db"), NewPlan()); err != nil {
		t.Errorf("a missing database: %v", err)
	}
}

func TestScanOpencodeSessions(t *testing.T) {
	h, _ := home(t)
	write(t, filepath.Join(h, "Chats", "chat-a", "x"), "")
	sqliteDB(t, OpencodeDatabase(),
		`CREATE TABLE session (id TEXT, directory TEXT, parent_id TEXT)`,
		`CREATE TABLE message (session_id TEXT, data TEXT)`,
		`CREATE TABLE part (session_id TEXT, data TEXT)`,
		`INSERT INTO session VALUES ('s1', '`+filepath.Join(h, "Chats", "chat-a")+`', NULL)`,
		`INSERT INTO session VALUES ('s2', '`+filepath.Join(h, "Chats", "chat-a")+`', 's1')`,
		`INSERT INTO session VALUES ('s3', '`+filepath.Join(h, "Chats", "chat-gone")+`', NULL)`,
		`INSERT INTO session VALUES ('s4', '`+h+`', NULL)`,
		`INSERT INTO message VALUES ('s1', '12345'), ('s4', '1234567890')`,
		`INSERT INTO part VALUES ('s2', '123')`,
	)
	got, err := ScanOpencodeSessions(OpencodeDatabase(), NewPlan())
	if err != nil {
		t.Fatal(err)
	}
	if want := (OpencodeSessions{Repoint: 2, Children: 1, Missing: 1, Data: 8}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestScanLinks(t *testing.T) {
	h, _ := home(t)
	write(t, filepath.Join(h, "Projects", "p", "asset.png"), "png")
	write(t, filepath.Join(h, "Chats", "chat-a", "doc.md"), "")
	link(t, filepath.Join(h, "Projects", "p", "asset.png"), filepath.Join(h, "Chats", "chat-a", "abs"))
	link(t, "doc.md", filepath.Join(h, "Chats", "chat-a", "same-folder"))
	link(t, "../../Projects/p", filepath.Join(h, "Chats", "chat-a", "across-same-depth"))
	link(t, "/usr/share", filepath.Join(h, "Chats", "chat-a", "elsewhere"))
	write(t, filepath.Join(h, "chat-old", "x"), "")
	link(t, "../Projects/p", filepath.Join(h, "chat-old", "breaks"))
	link(t, filepath.Join(h, "Projects"), filepath.Join(h, "Chats", "chat-a", ".git", "ignored"))

	got := ScanLinks(NewPlan())
	if len(got.Rewrite) != 1 || got.Rewrite[0] != filepath.Join(h, "Chats", "chat-a", "abs") {
		t.Errorf("rewrite: %v", got.Rewrite)
	}
	if len(got.Broken) != 1 || got.Broken[0] != filepath.Join(h, "chat-old", "breaks") {
		t.Errorf("broken: %v", got.Broken)
	}
}

func TestScanMentions(t *testing.T) {
	h, _ := home(t)
	old := filepath.Join(h, "Projects", "p", "logo.png")
	write(t, filepath.Join(h, "Projects", "p", "page.html"), `<img src="`+old+`">`)
	write(t, filepath.Join(h, "Projects", "p", "plain.md"), "nothing here")
	write(t, filepath.Join(h, "Projects", "p", "blob.bin"), "\x00"+old)
	write(t, filepath.Join(h, "Projects", "p", ".venv", "pyvenv.cfg"), "home = /usr/bin")
	write(t, filepath.Join(h, "Projects", "p", ".venv", "bin", "activate"), `VIRTUAL_ENV="`+filepath.Join(h, "Projects", "p", ".venv")+`"`)
	write(t, filepath.Join(h, "Projects", "p", ".git", "config"), old)

	got := ScanMentions(NewPlan())
	if len(got.Files) != 1 || got.Files[0] != filepath.Join(h, "Projects", "p", "page.html") {
		t.Errorf("files: %v", got.Files)
	}
	if len(got.Envs) != 1 || got.Envs[0] != filepath.Join(h, "Projects", "p", ".venv") {
		t.Errorf("environments: %v", got.Envs)
	}
}

func TestAssess(t *testing.T) {
	h, _ := home(t)
	write(t, filepath.Join(h, "Chats", "chat-a", "x"), "")
	db := filepath.Join(h, ".copilot-web.db")
	sqliteDB(t, db, `CREATE TABLE sessions (id TEXT, config TEXT)`)
	sqliteDB(t, OpencodeDatabase(),
		`CREATE TABLE session (id TEXT, directory TEXT, parent_id TEXT)`,
		`INSERT INTO session VALUES ('s1', '`+filepath.Join(h, "Chats", "chat-a")+`', NULL)`)
	prev := freeSpace
	t.Cleanup(func() { freeSpace = prev })

	freeSpace = func(string) (uint64, uint64, error) { return 1 << 40, 1 << 30, nil }
	r := Assess(db, "opencode")
	if !r.Ready() {
		t.Fatalf("not ready: %v %v", r.Problems, r.Errors)
	}
	if r.Sessions.Repoint != 1 || r.NeedBytes < headroom || r.Duration != perSession+60e9 {
		t.Errorf("report: %+v", r)
	}

	freeSpace = func(string) (uint64, uint64, error) { return 1 << 20, 10, nil }
	stubOpencode(t, []string{"4242"}, "v0.0.1")
	r = Assess(db, "opencode")
	checks := map[string]bool{}
	for _, p := range r.Problems {
		checks[p.Check] = true
	}
	for _, c := range []string{"disk space", "inodes", "opencode is running", "opencode version"} {
		if !checks[c] {
			t.Errorf("missing problem %q in %v", c, r.Problems)
		}
	}
	var out bytes.Buffer
	r.Write(&out, DryRun)
	for _, s := range []string{"nothing has changed", "Would stop it: disk space: needs", "free up at least", "Chats: ~/Chats → ~/" + layout.DefaultDirName + "/Chats"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("the report lacks %q:\n%s", s, out.String())
		}
	}
}

// TestADryRunChangesNothing compares every entry in the home folder before and
// after a full assessment.
func TestADryRunChangesNothing(t *testing.T) {
	h, root := home(t)
	write(t, filepath.Join(h, "Chats", "chat-a", "x.md"), "x")
	write(t, filepath.Join(h, "chat-old", "y.md"), "y")
	write(t, filepath.Join(h, "Projects", "p", "z.md"), filepath.Join(h, "Projects"))
	sqliteDB(t, filepath.Join(h, ".copilot-web.db"), `CREATE TABLE sessions (id TEXT, config TEXT)`)
	snapshot := func() string {
		var b strings.Builder
		_ = filepath.WalkDir(h, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			info, _ := d.Info()
			b.WriteString(path + " " + info.Mode().String() + " " + info.ModTime().String() + "\n")
			if d.Type().IsRegular() {
				data, _ := os.ReadFile(path)
				b.Write(data)
			}
			return nil
		})
		return b.String()
	}
	before := snapshot()
	var out bytes.Buffer
	Assess(filepath.Join(h, ".copilot-web.db"), "opencode").Write(&out, DryRun)
	if after := snapshot(); after != before {
		t.Errorf("the home folder changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if _, err := os.Lstat(root); err == nil {
		t.Error("the dry run created the root")
	}
}

func TestAnOpencodeThatExitsSoonDoesntRefuse(t *testing.T) {
	stubOpencode(t, nil, opencodeinstaller.PinnedVersion)
	calls := 0
	opencodeProcesses = func() []string {
		if calls++; calls < 3 {
			return []string{"4242"}
		}
		return nil
	}
	opencodeSettle = 2 * time.Second
	if got := checkOpencode("opencode"); len(got) != 0 {
		t.Errorf("an opencode that exited refused the run: %v", got)
	}
	opencodeProcesses = func() []string { return []string{"4242"} }
	opencodeSettle = 300 * time.Millisecond
	if got := checkOpencode("opencode"); len(got) != 1 || got[0].Check != "opencode is running" {
		t.Errorf("an opencode that stays: %v", got)
	}
}

func TestRewriteConfigChangesOnlyThePaths(t *testing.T) {
	h, root := home(t)
	p := Plan{Root: root, Moves: []Move{{From: filepath.Join(h, "Chats"), To: filepath.Join(root, "Chats"), Kind: Rename, Dir: true}}}
	old, moved := filepath.Join(h, "Chats", "chat-a"), filepath.Join(root, "Chats", "chat-a")
	for raw, want := range map[string]string{
		`{"model":"m","workdir":"` + old + `","x":{"workdir":"` + old + `"}}`:        `{"model":"m","workdir":"` + moved + `","x":{"workdir":"` + old + `"}}`,
		`{ "add_dirs" : [ "/elsewhere", "` + old + `" ] , "workdir": "/elsewhere" }`: `{ "add_dirs" : ["/elsewhere","` + moved + `"] , "workdir": "/elsewhere" }`,
		`{"workdir":"/elsewhere"}`: `{"workdir":"/elsewhere"}`,
		`not json`:                 `not json`,
	} {
		if got, _ := rewriteConfig(raw, p); got != want {
			t.Errorf("rewriteConfig(%s)\n = %s\nwant %s", raw, got, want)
		}
	}
}

func TestOnlyAnOpencodeOnTheSameDatabaseCounts(t *testing.T) {
	h, _ := home(t)
	ours := filepath.Join(h, "data")
	t.Setenv("XDG_DATA_HOME", ours)
	bin := filepath.Join(t.TempDir(), "opencode")
	if err := os.Symlink("/bin/sleep", bin); err != nil {
		t.Fatal(err)
	}
	// A grandchild, since the check leaves this process's own children alone.
	start := func(env ...string) string {
		cmd := exec.Command("/bin/sh", "-c", `"$0" 30 >/dev/null 2>&1 & echo $!`, bin)
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		pid := strings.TrimSpace(string(out))
		t.Cleanup(func() {
			if n, err := strconv.Atoi(pid); err == nil {
				syscall.Kill(n, syscall.SIGKILL)
			}
		})
		return pid
	}
	elsewhere := start("HOME="+h, "XDG_DATA_HOME="+filepath.Join(h, "other"))
	same := start("HOME="+h, "XDG_DATA_HOME="+ours)
	byHome := start("HOME=" + filepath.Dir(filepath.Dir(ours))) // ~/.local/share is elsewhere
	found := strings.Join(findOpencodeProcesses(), " ") + " "
	if !strings.Contains(found, same+" ") {
		t.Errorf("an opencode on the same database wasn't found: %s", found)
	}
	for _, pid := range []string{elsewhere, byHome} {
		if strings.Contains(found, pid+" ") {
			t.Errorf("an opencode on another database counts: %s in %s", pid, found)
		}
	}
}

func TestSameCounts(t *testing.T) {
	before := map[string]int64{"session": 5, "project_directory": 30}
	for _, c := range []struct {
		after map[string]int64
		ok    bool
	}{
		{map[string]int64{"session": 5, "project_directory": 30}, true},
		{map[string]int64{"session": 5, "project_directory": 32}, true},
		{map[string]int64{"session": 5, "project_directory": 33}, false},
		{map[string]int64{"session": 5, "project_directory": 29}, false},
		{map[string]int64{"session": 6, "project_directory": 30}, false},
		{map[string]int64{"session": 5, "project_directory": 30, "new": 1}, false},
	} {
		if err := sameCounts(before, c.after, map[string]int64{"project_directory": 2}); (err == nil) != c.ok {
			t.Errorf("sameCounts(%v) = %v", c.after, err)
		}
	}
}
