// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// migrated runs the migration on the fixture, and returns the database's new
// place.
func migrated(t *testing.T, ourDB string) string {
	t.Helper()
	if err := Run(options(t, ourDB)); err != nil {
		t.Fatal(err)
	}
	return layout.Database()
}

func undoOptions(t *testing.T, ourDB string) Options {
	o := options(t, ourDB)
	o.Undo = true
	return o
}

// sameUpdateTimes sets every opencode session's update time back to the
// fixture's: the stand-in's import bumps it, as opencode's does.
func sameUpdateTimes(t *testing.T) {
	t.Helper()
	db, err := sql.Open("sqlite", OpencodeDatabase())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE session SET time_updated = 1`); err != nil {
		t.Fatal(err)
	}
}

// rootHolds lists what's in the root besides the migrations' folder.
func rootHolds(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || path == root {
			return nil
		}
		if path == layout.MigrationDir() {
			return filepath.SkipDir
		}
		if rel, _ := filepath.Rel(root, path); rel != ".system" {
			out = append(out, rel)
		}
		return nil
	})
	return out
}

func TestUndoPlan(t *testing.T) {
	h, root, ourDB := fixture(t)
	migrated(t, ourDB)
	if err := os.RemoveAll(filepath.Join(root, "upload")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(h, ".local", "bin", "obsidian-mcp")); err != nil {
		t.Fatal(err)
	}
	p := UndoPlan()
	var got []string
	for _, m := range p.Moves {
		got = append(got, fmt.Sprintf("%s %s %s -> %s", m.Kind, m.Name, strings.TrimPrefix(m.From, h), strings.TrimPrefix(m.To, h)))
	}
	want := []string{
		"rename Legacy chats /Knowledge_Worker_Agent/Chats/chat-old -> /chat-old",
		"rename GitHub token /Knowledge_Worker_Agent/.system/github-pat.json -> /.copilot/github-pat.json",
		"rename Connector keys /Knowledge_Worker_Agent/.system/mcp-keys -> /.config/sds-mcp-keys",
		"rename Configuration repository cache /Knowledge_Worker_Agent/.system/config-repo -> /.config/sds-config-repo",
		"rename Central configuration /Knowledge_Worker_Agent/.system/config -> /.config/opencode-platform",
		"rename Obsidian vault /Knowledge_Worker_Agent/ObsidianVault -> /ObsidianVault",
		"rename Scheduled tasks /Knowledge_Worker_Agent/Scheduled Tasks -> /Scheduled Tasks",
		"rename Projects /Knowledge_Worker_Agent/Projects -> /Projects",
		"rename Chats /Knowledge_Worker_Agent/Chats -> /Chats",
		"rename Database snapshots /Knowledge_Worker_Agent/.system/backups -> /.copilot-web.db.backups",
		"rename Database /Knowledge_Worker_Agent/.system/knowledge-worker-agent.db -> /.copilot-web.db",
		"uncopy Obsidian connector /Knowledge_Worker_Agent/.system/obsidian/lib/node_modules/obsidian-mcp -> /.local/lib/node_modules/obsidian-mcp",
		"rename Obsidian connector /Knowledge_Worker_Agent/.system/obsidian/bin/obsidian-mcp -> /.local/bin/obsidian-mcp",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("moves:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	chats, _ := moveByFrom(p, filepath.Join(root, "Chats"))
	if chats.Entries != 6 {
		t.Errorf("Chats counts %d entries; the legacy chat inside it is its own move", chats.Entries)
	}
	if !p.isSource(filepath.Join(root, "Chats", "chat-old")) || p.isSource(filepath.Join(root, "Chats", "chat-a")) {
		t.Error("isSource")
	}
}

func TestUndoPutsEverythingBack(t *testing.T) {
	h, root, ourDB := fixture(t)
	before := snapshot(t, h, root)
	migrated(t, ourDB)
	forward, _ := os.ReadFile(layout.MovedFile())

	// The backups are removed after 30 days; undo doesn't need them.
	if n, err := Prune(time.Now()); err != nil || n != 0 {
		t.Fatalf("Prune now = %d, %v", n, err)
	}
	if n, err := Prune(time.Now().Add(Retention + time.Hour)); err != nil || n != 1 {
		t.Fatalf("Prune after 30 days = %d, %v", n, err)
	}
	run := filepath.Join(layout.MigrationDir(), strings.TrimSpace(readFile(t, layout.CompletionFile())))
	for _, f := range runCopies {
		if exists(filepath.Join(run, f)) {
			t.Errorf("after 30 days, the run still holds %s", f)
		}
	}
	for _, f := range []string{"journal.jsonl", "plan.json", "manifest.jsonl", "report.txt"} {
		if !exists(filepath.Join(run, f)) {
			t.Errorf("after 30 days, the run lacks its %s", f)
		}
	}

	if err := Run(undoOptions(t, layout.Database())); err != nil {
		t.Fatal(err)
	}
	if layout.Migrated() || lastOp(t) != opComplete {
		t.Fatalf("not undone: migrated=%v", layout.Migrated())
	}
	t.Setenv(EnvMode, "")
	if mode, _ := ModeFromEnv(); mode != Off {
		t.Errorf("after the undo, an unset %s means %q, want off", EnvMode, mode)
	}
	sameUpdateTimes(t)
	// opencode keeps every folder a git project has had, as it does its removed worktrees.
	db, err := sql.Open("sqlite", OpencodeDatabase())
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`DELETE FROM project_directory WHERE directory = ?`, filepath.Join(root, "Projects", "p"))
	db.Close()
	if n, _ := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("opencode's project lacks its folder in the root: %d rows, %v", n, err)
	}
	if after := snapshot(t, h, root); after != before {
		t.Fatalf("not as before the migration:\n--- before\n%s\n--- after\n%s", before, after)
	}
	if left := rootHolds(t, root); len(left) != 0 {
		t.Errorf("the root still holds %v", left)
	}
	if got := layout.Database(); got != ourDB {
		t.Errorf("layout.Database() = %s after undoing", got)
	}
	if old, ok := layout.OldPath(filepath.Join(h, "Chats", "chat-a", "notes.md")); !ok || old != filepath.Join(root, "Chats", "chat-a", "notes.md") {
		t.Errorf("OldPath after undoing = %s, %v", old, ok)
	}
	if _, stray := StrayDatabase(); stray {
		t.Error("a stray database after undoing")
	}
	undoRun := filepath.Join(layout.MigrationDir(), newestRun(t))
	if got := readFile(t, filepath.Join(undoRun, "moved.json")); got != string(forward) {
		t.Errorf("the migration's record wasn't kept in the undo's run:\n%s", got)
	}
	for _, f := range []string{"completed", "removed/12-obsidian-mcp", "removed/13-obsidian-mcp", "database.db", "opencode.db"} {
		if !exists(filepath.Join(undoRun, f)) {
			t.Errorf("the undo's run lacks %s", f)
		}
	}

	// A migration that fails after the undo keeps the undo's record.
	undone := readFile(t, layout.MovedFile())
	stepHook = func(s string) error {
		if s == "rewrite" {
			return fmt.Errorf("stopped at %s", s)
		}
		return nil
	}
	if err := Run(options(t, ourDB)); err == nil {
		t.Fatal("the migration didn't fail")
	}
	stepHook = func(string) error { return nil }
	if got, _ := os.ReadFile(layout.MovedFile()); string(got) != undone {
		t.Errorf("after a failed migration, the record of what moved is %q, was the undo's", got)
	}

	// And it can migrate again.
	if err := Run(options(t, ourDB)); err != nil {
		t.Fatalf("migrating again: %v", err)
	}
	if !layout.Migrated() || !exists(filepath.Join(root, ".system", "obsidian", "bin", "obsidian-mcp")) {
		t.Error("not migrated again")
	}
}

func TestUndoKeepsWhatWasMadeSinceTheMigration(t *testing.T) {
	h, root, ourDB := fixture(t)
	// Scheduled tasks didn't exist before the migration.
	if err := os.RemoveAll(filepath.Join(h, "Scheduled Tasks")); err != nil {
		t.Fatal(err)
	}
	sqliteDB(t, ourDB, `DELETE FROM scheduled_tasks`)
	db := migrated(t, ourDB)

	newChat, newTask := filepath.Join(root, "Chats", "chat-new"), filepath.Join(root, "Scheduled Tasks", "task2")
	write(t, filepath.Join(newChat, "new.md"), "new")
	write(t, filepath.Join(newTask, "out.md"), "out")
	link(t, filepath.Join(newTask, "out.md"), filepath.Join(newChat, "task-out"))
	if err := os.RemoveAll(filepath.Join(root, "Chats", "chat-old")); err != nil {
		t.Fatal(err)
	}
	sqliteDB(t, db,
		`INSERT INTO sessions VALUES ('s5', '{"workdir":"`+newChat+`"}')`,
		`DELETE FROM sessions WHERE id = 's2'`,
		`INSERT INTO scheduled_tasks VALUES ('t2', '`+newTask+`')`)
	sqliteDB(t, OpencodeDatabase(),
		`INSERT INTO session VALUES ('ses_new', '`+newChat+`', NULL, '`+strings.TrimPrefix(newChat, "/")+`', 1)`,
		`INSERT INTO message VALUES ('ses_new_m1', 'ses_new', 'x')`,
		`INSERT INTO part VALUES ('ses_new_m1_p', 'ses_new_m1', 'ses_new', 'y')`)

	if err := Run(undoOptions(t, db)); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(h, "Chats", "chat-new", "new.md")); string(data) != "new" {
		t.Error("the chat made since didn't move back")
	}
	if !exists(filepath.Join(h, "Scheduled Tasks", "task2", "out.md")) {
		t.Error("the scheduled task made since didn't move back")
	}
	if target, _ := os.Readlink(filepath.Join(h, "Chats", "chat-new", "task-out")); target != filepath.Join(h, "Scheduled Tasks", "task2", "out.md") {
		t.Errorf("a link made since points to %s", target)
	}
	if exists(filepath.Join(h, "chat-old")) {
		t.Error("a chat deleted since came back")
	}
	dump := dumpDB(t, ourDB)
	for _, want := range []string{
		`"workdir":"` + filepath.Join(h, "Chats", "chat-new") + `"`,
		"scheduled_tasks [t2 " + filepath.Join(h, "Scheduled Tasks", "task2") + "]",
		`"workdir":"` + filepath.Join(h, "Chats", "chat-a") + `"`,
	} {
		if !strings.Contains(dump, want) {
			t.Errorf("our database lacks %s:\n%s", want, dump)
		}
	}
	oc := dumpDB(t, OpencodeDatabase())
	for _, want := range []string{
		"session [ses_new " + filepath.Join(h, "Chats", "chat-new") + " ",
		"session [ses_a " + filepath.Join(h, "Chats", "chat-a") + " ",
		"session [ses_c " + filepath.Join(root, "Chats", "chat-old") + " ", // its folder is gone, so it's left
		"session [ses_gone " + filepath.Join(h, "Chats", "chat-gone") + " ",
	} {
		if !strings.Contains(oc, want) {
			t.Errorf("opencode's database lacks %q:\n%s", want, oc)
		}
	}
	if left := rootHolds(t, root); len(left) != 0 {
		t.Errorf("the root still holds %v", left)
	}
}

// migratedState describes everything but the migrations' folder: the home
// folder and the root, as the migration left them.
func migratedState(t *testing.T, h string) string {
	t.Helper()
	return snapshot(t, h, layout.MigrationDir()) + readFile(t, layout.MovedFile())
}

func TestAFailedUndoStepSetsTheMigrationBack(t *testing.T) {
	steps := []string{"manifest", "copy database", "export", "copy opencode database", "backup",
		"move 1", "move 8", "move 10", "move 11", "move 12", "rewrite", "repoint 1", "repoint 4", "relink", "verify", "complete"}
	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			h, _, ourDB := fixture(t)
			db := migrated(t, ourDB)
			before := migratedState(t, h)
			fired := false
			stepHook = func(s string) error {
				if s == step {
					fired = true
					return fmt.Errorf("stopped at %s", s)
				}
				return nil
			}
			err := Run(undoOptions(t, db))
			if !fired {
				t.Fatalf("the undo never reached %s", step)
			}
			if err == nil || !strings.Contains(err.Error(), "everything was set back") {
				t.Fatalf("Run: %v", err)
			}
			if !layout.Migrated() {
				t.Fatal("the migration no longer counts as completed")
			}
			if after := migratedState(t, h); after != before {
				t.Fatalf("not set back:\n--- before\n%s\n--- after\n%s", before, after)
			}
			if op := lastOp(t); op != opRolledBack {
				t.Errorf("the journal ends with %s", op)
			}
			if reason, failed := LastFailure("", Undo); !failed || !strings.Contains(reason, "stopped at "+step) {
				t.Errorf("LastFailure(undo) = %q, %v", reason, failed)
			}
			if _, failed := LastFailure("", On); failed {
				t.Error("a failed undo counts as a failed migration")
			}
		})
	}
}

func TestACrashedUndoIsSetBackAtTheNextStart(t *testing.T) {
	for _, step := range []string{"manifest", "backup", "move 1", "move 9", "move 12", "rewrite", "repoint 2", "relink", "verify", "complete"} {
		t.Run(step, func(t *testing.T) {
			h, _, ourDB := fixture(t)
			db := migrated(t, ourDB)
			before := migratedState(t, h)
			cmd := exec.Command(os.Args[0])
			cmd.Env = append(os.Environ(), "KWA_TEST_CRASH_AT="+step, "KWA_TEST_UNDO=1", "KWA_TEST_DB="+db, "KWA_TEST_PORT="+strconv.Itoa(freePort(t)))
			out, err := cmd.CombinedOutput()
			if code := cmd.ProcessState.ExitCode(); code != 3 {
				t.Fatalf("the undo exited %d, not at %s: %v\n%s", code, step, err, out)
			}
			if err := Recover(io.Discard); err != nil {
				t.Fatalf("Recover: %v", err)
			}
			if !layout.Migrated() {
				t.Fatal("the migration no longer counts as completed")
			}
			if after := migratedState(t, h); after != before {
				t.Fatalf("not set back:\n--- before\n%s\n--- after\n%s", before, after)
			}
			if _, failed := LastFailure("", Undo); failed {
				t.Error("an interrupted undo counts as failed")
			}
		})
	}
}

func TestUndoRefusesWhenADatabaseIsBackAtTheOldPlace(t *testing.T) {
	h, _, ourDB := fixture(t)
	db := migrated(t, ourDB)
	sqliteDB(t, ourDB, `CREATE TABLE sessions (id TEXT PRIMARY KEY, config TEXT)`)
	before := migratedState(t, h)
	err := Run(undoOptions(t, db))
	var r *Refused
	if !errors.As(err, &r) || !strings.Contains(err.Error(), "target exists: Database: "+ourDB) || r.Report == nil || !r.Report.Undo {
		t.Fatalf("Run: %v", err)
	}
	if after := migratedState(t, h); after != before {
		t.Fatalf("something changed:\n--- before\n%s\n--- after\n%s", before, after)
	}
}

func TestUndoWithoutAMigrationDoesNothing(t *testing.T) {
	h, root, ourDB := fixture(t)
	before := snapshot(t, h, root)
	if err := Run(undoOptions(t, ourDB)); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(t, h, root); after != before || exists(layout.MigrationDir()) {
		t.Error("something changed")
	}
}

func TestPruneLeavesARunInProgressAlone(t *testing.T) {
	_, _, ourDB := fixture(t)
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "KWA_TEST_CRASH_AT=move 3", "KWA_TEST_DB="+ourDB, "KWA_TEST_PORT="+strconv.Itoa(freePort(t)))
	if out, _ := cmd.CombinedOutput(); cmd.ProcessState.ExitCode() != 3 {
		t.Fatalf("the run exited %d\n%s", cmd.ProcessState.ExitCode(), out)
	}
	if n, err := Prune(time.Now().Add(2 * Retention)); err != nil || n != 0 {
		t.Fatalf("Prune = %d, %v", n, err)
	}
	if !exists(filepath.Join(layout.MigrationDir(), newestRun(t), "database.db")) {
		t.Error("Prune removed the copies of a run recovery needs")
	}
}

func newestRun(t *testing.T) string {
	t.Helper()
	runs, err := os.ReadDir(layout.MigrationDir())
	if err != nil {
		t.Fatal(err)
	}
	newest := ""
	for _, r := range runs {
		if r.IsDir() && r.Name() > newest {
			newest = r.Name()
		}
	}
	return newest
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRollingBackAnOlderJournalRemovesTheCompletionRecords(t *testing.T) {
	home(t)
	dir := filepath.Join(layout.MigrationDir(), "20260101T000000Z")
	write(t, filepath.Join(dir, journalName), `{"op":"begin","path":"x","detail":"1.1.0"}`+"\n")
	write(t, layout.CompletionFile(), "20260101T000000Z\n")
	write(t, layout.MovedFile(), "[]\n")
	if err := Recover(io.Discard); err != nil {
		t.Fatal(err)
	}
	if exists(layout.CompletionFile()) || exists(layout.MovedFile()) {
		t.Error("an older journal's completion records are still there")
	}
}
