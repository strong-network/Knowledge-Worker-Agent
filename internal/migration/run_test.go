// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodeinstaller"
)

// TestMain lets the test binary stand in for opencode, and run a migration that
// dies at a given step, as a crash would.
func TestMain(m *testing.M) {
	if os.Getenv("KWA_FAKE_OPENCODE") == "1" {
		os.Exit(fakeOpencode(os.Args[1:]))
	}
	if at := os.Getenv("KWA_TEST_CRASH_AT"); at != "" {
		fakeEverything()
		stepHook = func(step string) error {
			if step == at {
				os.Exit(3)
			}
			return nil
		}
		port, _ := strconv.Atoi(os.Getenv("KWA_TEST_PORT"))
		if err := Run(Options{OurDB: os.Getenv("KWA_TEST_DB"), OpencodeBin: os.Args[0], Host: "127.0.0.1", Port: port, Undo: os.Getenv("KWA_TEST_UNDO") == "1"}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeEverything() {
	opencodeExtraEnv = []string{"KWA_FAKE_OPENCODE=1", "KWA_FAKE_FAIL=" + os.Getenv("KWA_FAKE_FAIL")}
	opencodeProcesses = func() []string { return nil }
	opencodeVersion = func(string) string { return opencodeinstaller.PinnedVersion }
	freeSpace = func(string) (uint64, uint64, error) { return 1 << 40, 1 << 30, nil }
}

// fakeOpencode implements opencode's export and import against its database.
// KWA_FAKE_FAIL=export:<id>, import:<id> or cut:<id> makes one of them fail.
func fakeOpencode(args []string) int {
	fail := os.Getenv("KWA_FAKE_FAIL")
	db, err := sql.Open("sqlite", OpencodeDatabase())
	if err != nil {
		return 1
	}
	defer db.Close()
	switch {
	case len(args) == 2 && args[0] == "export":
		if fail == "export:"+args[1] {
			fmt.Fprintln(os.Stderr, "export failed")
			return 1
		}
		var dir string
		if db.QueryRow(`SELECT directory FROM session WHERE id = ?`, args[1]).Scan(&dir) != nil {
			return 1
		}
		type msg struct {
			Info  map[string]string   `json:"info"`
			Parts []map[string]string `json:"parts"`
		}
		out := struct {
			Info     map[string]string `json:"info"`
			Messages []msg             `json:"messages"`
		}{Info: map[string]string{"id": args[1], "directory": dir}}
		rows, _ := db.Query(`SELECT id FROM message WHERE session_id = ? ORDER BY id`, args[1])
		var ids []string
		for rows.Next() {
			var id string
			rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			m := msg{Info: map[string]string{"id": id}, Parts: []map[string]string{}}
			prs, _ := db.Query(`SELECT id FROM part WHERE message_id = ? ORDER BY id`, id)
			for prs.Next() {
				var pid string
				prs.Scan(&pid)
				m.Parts = append(m.Parts, map[string]string{"id": pid})
			}
			prs.Close()
			out.Messages = append(out.Messages, m)
		}
		b, _ := json.Marshal(out)
		if fail == "cut:"+args[1] {
			b = b[:len(b)/2]
		}
		os.Stdout.Write(b)
		fmt.Fprintln(os.Stderr, "Exporting session: "+args[1])
		return 0
	case len(args) == 2 && args[0] == "import":
		var e struct {
			Info struct {
				ID string `json:"id"`
			} `json:"info"`
		}
		b, err := os.ReadFile(args[1])
		if err != nil || json.Unmarshal(b, &e) != nil {
			return 1
		}
		if fail == "import:"+e.Info.ID {
			fmt.Println("import failed")
			return 1
		}
		cwd, _ := os.Getwd()
		if _, err := db.Exec(`UPDATE session SET directory = ?, path = ?, time_updated = time_updated + 1 WHERE id = ?`,
			cwd, strings.TrimPrefix(cwd, "/"), e.Info.ID); err != nil {
			return 1
		}
		// As opencode does, a session in a git project adds its new folder to the project's.
		if strings.Contains(cwd, "/Projects/") {
			if _, err := db.Exec(`INSERT OR IGNORE INTO project_directory VALUES ('proj_p', ?)`, cwd); err != nil {
				return 1
			}
		}
		fmt.Println("Imported session: " + e.Info.ID)
		return 0
	}
	return 2
}

// fixture builds a home folder with something in every place the migration
// moves, and returns our database.
func fixture(t *testing.T) (h, root, ourDB string) {
	t.Helper()
	h, root = home(t)
	fakeEverything()
	prevHook := stepHook
	t.Cleanup(func() { stepHook = prevHook; opencodeExtraEnv = nil })
	t.Setenv("KWA_FAKE_FAIL", "")

	ourDB = filepath.Join(h, ".copilot-web.db")
	sqliteDB(t, ourDB, `PRAGMA journal_mode=WAL`,
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, config TEXT)`,
		`CREATE TABLE projects (id TEXT PRIMARY KEY, workspace_path TEXT)`,
		`CREATE TABLE scheduled_tasks (id TEXT PRIMARY KEY, workdir TEXT)`,
		`CREATE TABLE messages (id INTEGER PRIMARY KEY, session_id TEXT, content TEXT)`,
		`INSERT INTO sessions VALUES ('s1', '{"model":"m","workdir":"`+filepath.Join(h, "Chats", "chat-a")+`","add_dirs":["`+filepath.Join(h, "Projects", "p")+`"]}')`,
		`INSERT INTO sessions VALUES ('s2', '{"workdir":"`+filepath.Join(h, "chat-old")+`"}')`,
		`INSERT INTO sessions VALUES ('s3', '{"workdir":"`+h+`"}')`,
		`INSERT INTO sessions VALUES ('s4', '{"workdir":"`+filepath.Join(h, "Chats", "chat-gone")+`"}')`,
		`INSERT INTO projects VALUES ('p1', '`+filepath.Join(h, "Projects", "p")+`')`,
		`INSERT INTO scheduled_tasks VALUES ('t1', '`+filepath.Join(h, "Scheduled Tasks", "task")+`')`,
		`INSERT INTO messages (session_id, content) VALUES ('s1', 'look at `+filepath.Join(h, "Projects", "p")+`')`,
	)
	write(t, filepath.Join(h, ".copilot-web.db.backups", "20260930T000000Z.db"), "snapshot")
	write(t, filepath.Join(h, "Chats", "chat-a", "notes.md"), "notes")
	write(t, filepath.Join(h, "Chats", "chat-a", ".system", "metadata"), "{}")
	link(t, filepath.Join(h, "Projects", "p", "logo.png"), filepath.Join(h, "Chats", "chat-a", "logo"))
	write(t, filepath.Join(h, "chat-old", "old.md"), "old")
	write(t, filepath.Join(h, "Projects", "p", "logo.png"), "png")
	write(t, filepath.Join(h, "Projects", "p", "page.html"), `<img src="`+filepath.Join(h, "Projects", "p", "logo.png")+`">`)
	write(t, filepath.Join(h, "Scheduled Tasks", "task", "out.md"), "out")
	write(t, filepath.Join(h, "upload", "a.pdf"), "pdf")
	write(t, filepath.Join(h, "ObsidianVault", ".obsidian", "app.json"), "{}")
	write(t, filepath.Join(h, ".config", "opencode-platform", "opencode.json"), "{}")
	write(t, filepath.Join(h, ".config", "sds-config-repo", "README.md"), "repo")
	write(t, filepath.Join(h, ".config", "sds-mcp-keys", "acme"), "")
	write(t, filepath.Join(h, ".config", "opencode", "opencode.json"), `{"mcp":{}}`)
	write(t, filepath.Join(h, ".copilot", "github-pat.json"), `{"token":"t"}`)
	write(t, filepath.Join(h, ".local", "lib", "node_modules", "obsidian-mcp", "dist", "main.js"), "js")
	link(t, "../lib/node_modules/obsidian-mcp/dist/main.js", filepath.Join(h, ".local", "bin", "obsidian-mcp"))

	sqliteDB(t, OpencodeDatabase(),
		`CREATE TABLE session (id TEXT PRIMARY KEY, directory TEXT, parent_id TEXT, path TEXT, time_updated INTEGER)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, data TEXT)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT, data TEXT)`,
		`CREATE TABLE event (id INTEGER PRIMARY KEY, data TEXT)`,
		`INSERT INTO event (data) VALUES ('e1'), ('e2')`,
		`CREATE TABLE project_directory (project_id TEXT, directory TEXT, PRIMARY KEY (project_id, directory))`,
		`INSERT INTO project_directory VALUES ('proj_p', '`+filepath.Join(h, "Projects", "p")+`')`,
	)
	db, err := sql.Open("sqlite", OpencodeDatabase())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []struct{ id, dir, parent string }{
		{"ses_a", filepath.Join(h, "Chats", "chat-a"), ""},
		{"ses_b", filepath.Join(h, "Chats", "chat-a"), "ses_a"},
		{"ses_c", filepath.Join(h, "chat-old"), ""},
		{"ses_d", filepath.Join(h, "Projects", "p"), ""},
		{"ses_gone", filepath.Join(h, "Chats", "chat-gone"), ""},
		{"ses_home", h, ""},
	} {
		if _, err := db.Exec(`INSERT INTO session VALUES (?, ?, NULLIF(?, ''), ?, 1)`, s.id, s.dir, s.parent, strings.TrimPrefix(s.dir, "/")); err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= 2; i++ {
			mid := fmt.Sprintf("%s_m%d", s.id, i)
			db.Exec(`INSERT INTO message VALUES (?, ?, 'x')`, mid, s.id)
			db.Exec(`INSERT INTO part VALUES (?, ?, ?, 'y')`, mid+"_p", mid, s.id)
		}
	}
	db.Close()
	return h, root, ourDB
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func options(t *testing.T, ourDB string) Options {
	return Options{OurDB: ourDB, OpencodeBin: os.Args[0], Host: "127.0.0.1", Port: freePort(t)}
}

// snapshot describes everything in the home folder outside the root: each
// entry's path, type, inode and contents, and every database's rows. A
// database's own file is compared by its rows, since restoring one changes its
// bytes and inode but nothing in it.
func snapshot(t *testing.T, h, root string) string {
	t.Helper()
	var b strings.Builder
	var dbs []string
	_ = filepath.WalkDir(h, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == root {
			return filepath.SkipDir
		}
		name := d.Name()
		if strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") {
			return nil
		}
		info, _ := d.Info()
		fmt.Fprintf(&b, "%s %s", strings.TrimPrefix(path, h), info.Mode().Type())
		switch {
		case strings.HasSuffix(name, ".db") && d.Type().IsRegular():
			dbs = append(dbs, path)
		case d.Type()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(path)
			fmt.Fprintf(&b, " -> %s", target)
		default:
			fmt.Fprintf(&b, " ino=%d", info.Sys().(*syscall.Stat_t).Ino)
			if d.Type().IsRegular() {
				data, _ := os.ReadFile(path)
				fmt.Fprintf(&b, " %q", data)
			}
		}
		b.WriteString("\n")
		return nil
	})
	for _, p := range dbs {
		b.WriteString(dumpDB(t, p))
	}
	return b.String()
}

func dumpDB(t *testing.T, path string) string {
	t.Helper()
	db, err := openReadOnly(path)
	if err != nil {
		return path + ": " + err.Error() + "\n"
	}
	defer db.Close()
	var b strings.Builder
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		return path + ": not a database\n"
	}
	var tables []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	for _, tbl := range tables {
		rs, _ := db.Query(`SELECT * FROM "` + tbl + `" ORDER BY 1`)
		cols, _ := rs.Columns()
		for rs.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			rs.Scan(ptrs...)
			fmt.Fprintf(&b, "%s %s %v\n", filepath.Base(path), tbl, vals)
		}
		rs.Close()
	}
	return b.String()
}

func lastOp(t *testing.T) string {
	t.Helper()
	runs, err := os.ReadDir(layout.MigrationDir())
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, r := range runs {
		if r.IsDir() {
			dirs = append(dirs, r.Name())
		}
	}
	sort.Strings(dirs)
	entries, err := readJournal(filepath.Join(layout.MigrationDir(), dirs[len(dirs)-1]))
	if err != nil {
		t.Fatal(err)
	}
	return entries[len(entries)-1].Op
}

// setBack checks the home folder is as before, and the root holds nothing but
// the run's journal.
func setBack(t *testing.T, before, after, root string) {
	t.Helper()
	if after != before {
		t.Fatalf("not set back:\n--- before\n%s\n--- after\n%s", before, after)
	}
	if layout.Migrated() {
		t.Error("the migration counts as completed")
	}
	for _, p := range []string{"Chats", "Projects", "upload", "ObsidianVault", "Scheduled Tasks",
		".system/knowledge-worker-agent.db", ".system/backups", ".system/config", ".system/config-repo", ".system/mcp-keys", ".system/obsidian"} {
		if exists(filepath.Join(root, p)) {
			t.Errorf("%s is still in the root", p)
		}
	}
	runs, _ := os.ReadDir(layout.MigrationDir())
	for _, r := range runs {
		for _, copied := range []string{"backup", "database.db", "opencode", "opencode.db"} {
			if exists(filepath.Join(layout.MigrationDir(), r.Name(), copied)) {
				t.Errorf("run %s still holds its %s", r.Name(), copied)
			}
		}
	}
}

func TestRunMovesEverything(t *testing.T) {
	h, root, ourDB := fixture(t)
	plan := NewPlan()
	inodes := map[string]uint64{}
	for _, m := range plan.Renames() {
		filepath.WalkDir(m.From, func(path string, d fs.DirEntry, err error) error {
			info, _ := d.Info()
			inodes[path] = info.Sys().(*syscall.Stat_t).Ino
			return nil
		})
	}

	if err := Run(options(t, ourDB)); err != nil {
		t.Fatal(err)
	}
	if !layout.Migrated() || lastOp(t) != opComplete {
		t.Fatalf("not completed: migrated=%v", layout.Migrated())
	}
	for old, ino := range inodes {
		moved, _ := plan.Remap(old)
		info, err := os.Lstat(moved)
		if err != nil {
			t.Errorf("%s is missing", moved)
			continue
		}
		if got := info.Sys().(*syscall.Stat_t).Ino; got != ino && !strings.HasSuffix(moved, "/logo") {
			t.Errorf("%s is a different file", moved)
		}
		if exists(old) {
			t.Errorf("%s is still at its old place", old)
		}
	}

	sys := filepath.Join(root, ".system")
	db := filepath.Join(sys, "knowledge-worker-agent.db")
	if exists(ourDB) || !exists(db) {
		t.Fatalf("our database didn't move")
	}
	dump := dumpDB(t, db)
	for _, want := range []string{
		`"workdir":"` + filepath.Join(root, "Chats", "chat-a") + `"`,
		`"add_dirs":["` + filepath.Join(root, "Projects", "p") + `"]`,
		`"model":"m"`,
		`"workdir":"` + filepath.Join(root, "Chats", "chat-old") + `"`,
		`"workdir":"` + h + `"`,
		`"workdir":"` + filepath.Join(root, "Chats", "chat-gone") + `"`,
		"projects [p1 " + filepath.Join(root, "Projects", "p") + "]",
		"scheduled_tasks [t1 " + filepath.Join(root, "Scheduled Tasks", "task") + "]",
		"look at " + filepath.Join(h, "Projects", "p"), // history isn't rewritten
	} {
		if !strings.Contains(dump, want) {
			t.Errorf("our database lacks %s:\n%s", want, dump)
		}
	}

	oc := dumpDB(t, OpencodeDatabase())
	for _, want := range []string{
		"session [ses_a " + filepath.Join(root, "Chats", "chat-a"),
		"session [ses_b " + filepath.Join(root, "Chats", "chat-a"),
		"session [ses_c " + filepath.Join(root, "Chats", "chat-old"),
		"session [ses_d " + filepath.Join(root, "Projects", "p"),
		"session [ses_gone " + filepath.Join(h, "Chats", "chat-gone"),
		"session [ses_home " + h + " ",
	} {
		if !strings.Contains(oc, want) {
			t.Errorf("opencode's database lacks %q:\n%s", want, oc)
		}
	}

	if target, _ := os.Readlink(filepath.Join(root, "Chats", "chat-a", "logo")); target != filepath.Join(root, "Projects", "p", "logo.png") {
		t.Errorf("link points to %s", target)
	}
	if target, _ := os.Readlink(filepath.Join(sys, "obsidian", "bin", "obsidian-mcp")); target != "../lib/node_modules/obsidian-mcp/dist/main.js" {
		t.Errorf("Obsidian launcher copy points to %s", target)
	}
	if data, _ := os.ReadFile(filepath.Join(sys, "obsidian", "bin", "obsidian-mcp")); string(data) != "js" {
		t.Errorf("Obsidian launcher copy doesn't resolve: %q", data)
	}
	if !exists(filepath.Join(h, ".local", "bin", "obsidian-mcp")) {
		t.Error("the original Obsidian install was removed")
	}
	if got := layout.Database(); got != db {
		t.Errorf("layout.Database() = %s after the migration", got)
	}
	if exists(filepath.Join(h, ".copilot", "github-pat.json")) || readFile(t, filepath.Join(sys, "github-pat.json")) != `{"token":"t"}` {
		t.Error("the GitHub token didn't move")
	}

	runs, _ := os.ReadDir(layout.MigrationDir())
	run := ""
	for _, r := range runs {
		if r.IsDir() {
			run = filepath.Join(layout.MigrationDir(), r.Name())
		}
	}
	for _, f := range []string{"report.txt", "database.db", "opencode.db", "opencode/ses_a.json", "files/opencode.json", "manifest.jsonl"} {
		if !exists(filepath.Join(run, f)) {
			t.Errorf("the run lacks %s", f)
		}
	}
	info, err := os.Stat(filepath.Join(root, "Chats", "chat-a", "notes.md"))
	if err != nil || info.Sys().(*syscall.Stat_t).Nlink < 2 {
		t.Error("the hard-link backup doesn't hold the chat's file")
	}

	if err := Run(options(t, db)); err != nil {
		t.Errorf("a second run: %v", err)
	}
}

func TestAFailedStepSetsEverythingBack(t *testing.T) {
	steps := []string{"manifest", "copy database", "export", "copy opencode database", "backup",
		"move 1", "move 2", "move 7", "move 13", "rewrite", "repoint 1", "repoint 4", "relink", "verify", "complete"}
	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			h, root, ourDB := fixture(t)
			before := snapshot(t, h, root)
			fired := false
			stepHook = func(s string) error {
				if s == step {
					fired = true
					return fmt.Errorf("stopped at %s", s)
				}
				return nil
			}
			err := Run(options(t, ourDB))
			if !fired {
				t.Fatalf("the run never reached %s", step)
			}
			if err == nil || !strings.Contains(err.Error(), "everything was set back") {
				t.Fatalf("Run: %v", err)
			}
			setBack(t, before, snapshot(t, h, root), root)
			if op := lastOp(t); op != opRolledBack {
				t.Errorf("the journal ends with %s", op)
			}
		})
	}
}

func TestACrashIsSetBackAtTheNextStart(t *testing.T) {
	for _, step := range []string{"manifest", "export", "backup", "move 1", "move 9", "move 13", "rewrite", "repoint 2", "relink", "verify", "complete"} {
		t.Run(step, func(t *testing.T) {
			h, root, ourDB := fixture(t)
			before := snapshot(t, h, root)
			cmd := exec.Command(os.Args[0])
			cmd.Env = append(os.Environ(), "KWA_TEST_CRASH_AT="+step, "KWA_TEST_DB="+ourDB, "KWA_TEST_PORT="+strconv.Itoa(freePort(t)))
			out, err := cmd.CombinedOutput()
			if code := cmd.ProcessState.ExitCode(); code != 3 {
				t.Fatalf("the run exited %d, not at %s: %v\n%s", code, step, err, out)
			}
			if op := lastOp(t); op == opRolledBack || op == opComplete {
				t.Fatalf("the crashed run ends with %s", op)
			}
			if err := Recover(io.Discard); err != nil {
				t.Fatalf("Recover: %v", err)
			}
			setBack(t, before, snapshot(t, h, root), root)
			if err := Recover(io.Discard); err != nil {
				t.Errorf("a second Recover: %v", err)
			}
		})
	}
}

func TestRunRefusesWhenSomethingWouldStopIt(t *testing.T) {
	h, root, ourDB := fixture(t)
	write(t, filepath.Join(root, "Projects", "mine.md"), "already here")
	before := snapshot(t, h, root)
	if err := Run(options(t, ourDB)); err == nil || !strings.Contains(err.Error(), "nothing changed") {
		t.Fatalf("Run: %v", err)
	}
	if snapshot(t, h, root) != before || exists(layout.MigrationDir()) {
		t.Error("something changed")
	}
}

func TestRunRefusesWhileTheServerHoldsTheDatabase(t *testing.T) {
	_, _, ourDB := fixture(t)
	if err := HoldDatabase(ourDB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { held.Close(); held = nil })
	if err := Run(options(t, ourDB)); err == nil || !strings.Contains(err.Error(), "another Knowledge Worker Agent process") {
		t.Fatalf("Run: %v", err)
	}
	if exists(layout.MigrationDir()) {
		t.Error("a run started")
	}
}

func TestRunRefusesWhenThePortIsTaken(t *testing.T) {
	_, _, ourDB := fixture(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	o := options(t, ourDB)
	o.Port = l.Addr().(*net.TCPAddr).Port
	if err := Run(o); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("Run: %v", err)
	}
}

func TestABadExportStopsItBeforeAnythingMoves(t *testing.T) {
	for _, fail := range []string{"cut:ses_c", "export:ses_d"} {
		t.Run(fail, func(t *testing.T) {
			h, root, ourDB := fixture(t)
			t.Setenv("KWA_FAKE_FAIL", fail)
			fakeEverything()
			before := snapshot(t, h, root)
			err := Run(options(t, ourDB))
			if err == nil || !strings.Contains(err.Error(), "export of session") {
				t.Fatalf("Run: %v", err)
			}
			setBack(t, before, snapshot(t, h, root), root)
		})
	}
}

func TestAFailedImportRestoresOpencodesDatabase(t *testing.T) {
	h, root, ourDB := fixture(t)
	t.Setenv("KWA_FAKE_FAIL", "import:ses_c")
	fakeEverything()
	before := snapshot(t, h, root)
	if err := Run(options(t, ourDB)); err == nil || !strings.Contains(err.Error(), "import of session ses_c") {
		t.Fatalf("Run: %v", err)
	}
	setBack(t, before, snapshot(t, h, root), root)
}

func TestRecoverLeavesARunInProgressAlone(t *testing.T) {
	_, _, _ = fixture(t)
	dir := filepath.Join(layout.MigrationDir(), "20260101T000000Z")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	j, err := createJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()
	j.add(Entry{Op: opBegin})
	if err := Recover(io.Discard); err == nil || !strings.Contains(err.Error(), "another process is running the migration") {
		t.Errorf("Recover: %v", err)
	}
}

func TestReadJournalIgnoresALineCutShort(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, journalName), []byte(`{"op":"begin"}`+"\n"+`{"op":"rena`), 0o600)
	entries, err := readJournal(dir)
	if err != nil || len(entries) != 1 {
		t.Errorf("got %v, %v", entries, err)
	}
	os.WriteFile(filepath.Join(dir, journalName), []byte(`{"op":"begin"}`+"\n"+`{"op":"rena`+"\n"+`{"op":"mkdir"}`+"\n"), 0o600)
	if _, err := readJournal(dir); err == nil {
		t.Error("a broken line in the middle was accepted")
	}
}

func TestRunReportsProgressAndRecordsWhatMoved(t *testing.T) {
	h, root, ourDB := fixture(t)
	var phases []string
	sawCount := false
	o := options(t, ourDB)
	o.Progress = func(p Progress) {
		if len(phases) == 0 || phases[len(phases)-1] != p.Phase {
			phases = append(phases, p.Phase)
		}
		if p.Phase == "Updating chat history" && p.Total == 4 {
			sawCount = true
		}
	}
	if err := Run(o); err != nil {
		t.Fatal(err)
	}
	want := []string{"Checking your files", "Backing up", "Saving chat history", "Backing up", "Moving files",
		"Updating chat history", "Checking every file", "Done"}
	if strings.Join(phases, "|") != strings.Join(want, "|") || !sawCount {
		t.Errorf("phases %v (counted %v), want %v", phases, sawCount, want)
	}
	if old, ok := layout.OldPath(filepath.Join(root, "Chats", "chat-old", "old.md")); !ok || old != filepath.Join(h, "chat-old", "old.md") {
		t.Errorf("OldPath = %s, %v", old, ok)
	}
	if _, ok := StrayDatabase(); ok {
		t.Error("a stray database right after the migration")
	}
	write(t, ourDB, "an older release made this")
	if got, ok := StrayDatabase(); !ok || got != ourDB {
		t.Errorf("StrayDatabase = %s, %v", got, ok)
	}
}

func TestAFailedRunWaitsForTheNextRelease(t *testing.T) {
	_, _, ourDB := fixture(t)
	stepHook = func(s string) error {
		if s == "rewrite" {
			return fmt.Errorf("stopped at %s", s)
		}
		return nil
	}
	o := options(t, ourDB)
	o.Release = "1.2.0"
	if err := Run(o); err == nil {
		t.Fatal("the run didn't fail")
	}
	if reason, failed := LastFailure("1.2.0", On); !failed || !strings.Contains(reason, "stopped at rewrite") {
		t.Errorf("LastFailure(this release) = %q, %v", reason, failed)
	}
	if _, failed := LastFailure("1.3.0", On); failed {
		t.Error("a new release counts the failure too")
	}
}

func TestAnInterruptedRunIsTriedAgain(t *testing.T) {
	_, _, ourDB := fixture(t)
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "KWA_TEST_CRASH_AT=move 3", "KWA_TEST_DB="+ourDB, "KWA_TEST_PORT="+strconv.Itoa(freePort(t)))
	if out, _ := cmd.CombinedOutput(); cmd.ProcessState.ExitCode() != 3 {
		t.Fatalf("the run exited %d\n%s", cmd.ProcessState.ExitCode(), out)
	}
	if err := Recover(io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, failed := LastFailure("", On); failed {
		t.Error("an interrupted run counts as failed")
	}
	if err := Run(options(t, ourDB)); err != nil {
		t.Errorf("running again: %v", err)
	}
}
