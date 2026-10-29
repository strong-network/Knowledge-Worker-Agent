// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package versions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

func setup(t *testing.T) string {
	t.Helper()
	if err := db.Init(filepath.Join(t.TempDir(), "kwa.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := Init(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store = ""; running = map[string]map[string]string{} })
	return t.TempDir()
}

var clock = time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)

// write gives each write its own modification time, as real edits seconds apart have.
func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	if err := os.Chtimes(path, clock, clock); err != nil {
		t.Fatal(err)
	}
}

type got struct {
	v       int
	author  string
	content string
}

func versionsOf(t *testing.T, path string) []got {
	t.Helper()
	list, err := db.FileVersions(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []got
	for _, fv := range list {
		data, err := read(fv.SHA)
		if err != nil {
			t.Fatalf("v%d: %v", fv.V, err)
		}
		out = append(out, got{fv.V, fv.Author, string(data)})
	}
	return out
}

func want(t *testing.T, path string, w ...got) {
	t.Helper()
	g := versionsOf(t, path)
	if len(g) != len(w) {
		t.Fatalf("%s: %d versions %+v, want %+v", filepath.Base(path), len(g), g, w)
	}
	for i := range w {
		if g[i] != w[i] {
			t.Errorf("%s: version %d = %+v, want %+v", filepath.Base(path), i+1, g[i], w[i])
		}
	}
}

func TestATurnMakesOneVersion(t *testing.T) {
	folder := setup(t)
	notes := filepath.Join(folder, "notes.md")
	write(t, notes, "# Notes\n")

	TurnStarted("s1", folder)
	want(t, notes, got{1, "", "# Notes\n"})

	// The agent writes twice, and the viewer looks in between.
	write(t, notes, "# Notes\n\nFirst draft.\n")
	Current(notes)
	want(t, notes, got{1, "", "# Notes\n"}, got{2, Agent, "# Notes\n\nFirst draft.\n"})
	write(t, notes, "# Notes\n\nSecond draft.\n")
	TurnEnded("s1", folder)
	want(t, notes, got{1, "", "# Notes\n"}, got{2, Agent, "# Notes\n\nSecond draft.\n"})

	// The next turn changes nothing at its start, then makes v3.
	TurnStarted("s1", folder)
	write(t, notes, "# Notes\n\nThird.\n")
	TurnEnded("s1", folder)
	want(t, notes, got{1, "", "# Notes\n"}, got{2, Agent, "# Notes\n\nSecond draft.\n"}, got{3, Agent, "# Notes\n\nThird.\n"})
}

func TestAChangeOutsideIsItsOwnVersion(t *testing.T) {
	folder := setup(t)
	notes := filepath.Join(folder, "deep", "notes.md")
	TurnStarted("s1", folder)
	write(t, notes, "agent\n")
	TurnEnded("s1", folder)

	// Edited in another editor between turns: the next turn's start takes it, unattributed.
	write(t, notes, "edited elsewhere\n")
	TurnStarted("s1", folder)
	write(t, notes, "agent again\n")
	TurnEnded("s1", folder)
	want(t, notes, got{1, Agent, "agent\n"}, got{2, "", "edited elsewhere\n"}, got{3, Agent, "agent again\n"})
}

func TestSavesCloseTogetherMakeOneVersion(t *testing.T) {
	folder := setup(t)
	notes := filepath.Join(folder, "notes.md")
	write(t, notes, "one\n")
	Saved(notes, You)
	write(t, notes, "two\n")
	Saved(notes, You)
	want(t, notes, got{1, You, "two\n"})

	// Someone else's save is a version of its own.
	write(t, notes, "three\n")
	Saved(notes, Guest("Robin"))
	want(t, notes, got{1, You, "two\n"}, got{2, "guest:Robin", "three\n"})

	// So is a save of one's own after a while.
	write(t, notes, "four\n")
	Saved(notes, You)
	db.DB.Exec(`UPDATE file_versions SET created_ms = created_ms - ? WHERE v = 3`, (coalesce + time.Minute).Milliseconds())
	write(t, notes, "five\n")
	Saved(notes, You)
	want(t, notes, got{1, You, "two\n"}, got{2, "guest:Robin", "three\n"}, got{3, You, "four\n"}, got{4, You, "five\n"})
}

func TestTheSameContentIsNotANewVersion(t *testing.T) {
	folder := setup(t)
	notes := filepath.Join(folder, "notes.md")
	write(t, notes, "same\n")
	Saved(notes, You)
	write(t, notes, "same\n")
	Saved(notes, Guest("Robin"))
	want(t, notes, got{1, You, "same\n"})
	if fv, _, _ := db.LatestFileVersion(notes); fv.ModTime != clock.UnixNano() {
		t.Errorf("the new modification time wasn't recorded: %d", fv.ModTime)
	}
}

func TestOnlyTextFilesGetVersions(t *testing.T) {
	folder := setup(t)
	write(t, filepath.Join(folder, "chart.png"), "png")
	write(t, filepath.Join(folder, ".hidden.md"), "x")
	write(t, filepath.Join(folder, ".git", "notes.md"), "x")
	write(t, filepath.Join(folder, "node_modules", "pkg", "README.md"), "x")
	write(t, filepath.Join(folder, "nul.txt"), "a\x00b")
	write(t, filepath.Join(folder, "big.csv"), strings.Repeat("x", MaxSize+1))
	write(t, filepath.Join(folder, "ok.csv"), "a,b\n")
	TurnStarted("s1", folder)
	TurnEnded("s1", folder)
	paths, _ := db.FileVersionPaths()
	if len(paths) != 1 || paths[0] != filepath.Join(folder, "ok.csv") {
		t.Errorf("versions of %v, want only ok.csv", paths)
	}
}

func TestOnlyTheNewestAreKept(t *testing.T) {
	folder := setup(t)
	notes := filepath.Join(folder, "notes.md")
	for i := 1; i <= Keep+5; i++ {
		write(t, notes, strings.Repeat("line\n", i))
		Saved(notes, Guest(string(rune('a'+i%2))))
	}
	g := versionsOf(t, notes)
	if len(g) != Keep || g[0].v != 6 || g[len(g)-1].v != Keep+5 {
		t.Fatalf("kept v%d..v%d (%d), want v6..v%d", g[0].v, g[len(g)-1].v, len(g), Keep+5)
	}
	if n := countBlobs(t); n != Keep {
		t.Errorf("%d contents stored, want %d", n, Keep)
	}
}

func countBlobs(t *testing.T) int {
	t.Helper()
	n := 0
	filepath.WalkDir(store, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func TestVersionsFollowAMoveAndGoWithADelete(t *testing.T) {
	folder := setup(t)
	notes := filepath.Join(folder, "drafts", "notes.md")
	write(t, notes, "one\n")
	Saved(notes, You)
	if err := os.Rename(filepath.Join(folder, "drafts"), filepath.Join(folder, "final")); err != nil {
		t.Fatal(err)
	}
	Moved(filepath.Join(folder, "drafts"), filepath.Join(folder, "final"))
	moved := filepath.Join(folder, "final", "notes.md")
	want(t, moved, got{1, You, "one\n"})
	want(t, notes)

	Forget(filepath.Join(folder, "final"))
	want(t, moved)
	if n := countBlobs(t); n != 0 {
		t.Errorf("%d contents left after the delete", n)
	}
}

func TestSweepForgetsWhatIsGone(t *testing.T) {
	folder := setup(t)
	kept, gone := filepath.Join(folder, "kept.md"), filepath.Join(folder, "gone.md")
	write(t, kept, "kept\n")
	write(t, gone, "gone\n")
	Saved(kept, You)
	Saved(gone, You)
	os.Remove(gone)
	os.MkdirAll(filepath.Join(store, "ff"), 0o700)
	os.WriteFile(filepath.Join(store, "ff", "stray.gz"), []byte("x"), 0o600)
	Sweep()
	want(t, kept, got{1, You, "kept\n"})
	want(t, gone)
	if n := countBlobs(t); n != 1 {
		t.Errorf("%d contents after the sweep, want 1", n)
	}
}

func TestAFileInASubfolderBelongsToTheRunningTurn(t *testing.T) {
	folder := setup(t)
	notes := filepath.Join(folder, "reports", "q3.md")
	TurnStarted("s1", folder)
	write(t, notes, "draft\n")
	Current(notes)
	want(t, notes, got{1, Agent, "draft\n"})
	TurnEnded("s1", folder)
	want(t, notes, got{1, Agent, "draft\n"})
}
