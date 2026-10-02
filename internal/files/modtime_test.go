// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package files

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type browsed struct {
	Dirs []struct {
		Name    string    `json:"name"`
		ModTime time.Time `json:"mod_time"`
	} `json:"dirs"`
	Files []struct {
		Name    string    `json:"name"`
		ModTime time.Time `json:"mod_time"`
	} `json:"files"`
}

func browse(t *testing.T, dir string) browsed {
	t.Helper()
	w := httptest.NewRecorder()
	HandleBrowse(w, httptest.NewRequest("GET", "/api/browse?path="+dir, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("browse: %d %s", w.Code, w.Body.String())
	}
	var b browsed
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func (b browsed) dir(t *testing.T, name string) time.Time {
	t.Helper()
	for _, d := range b.Dirs {
		if d.Name == name {
			return d.ModTime
		}
	}
	t.Fatalf("no folder %q in listing", name)
	return time.Time{}
}

func (b browsed) file(t *testing.T, name string) time.Time {
	t.Helper()
	for _, f := range b.Files {
		if f.Name == name {
			return f.ModTime
		}
	}
	t.Fatalf("no file %q in listing", name)
	return time.Time{}
}

// touch sets a path's times, so the tests don't depend on the clock moving.
func touch(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// The file panel marks a file changed when its time moves, and a folder when
// anything under it does, so an edit the agent makes deep in a folder nobody
// has opened still shows.
func TestHandleBrowseModTimes(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	later := base.Add(time.Hour)

	mk := func(rel string) string {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
		return p
	}
	report := mk("report.md")
	deep := mk("working/notes/a.txt")
	mk("working/.cache/blob")
	mk("working/node_modules/pkg/index.js")
	mk(".git/HEAD")
	for _, p := range []string{
		report, deep,
		filepath.Join(dir, "working/.cache/blob"), filepath.Join(dir, "working/.cache"),
		filepath.Join(dir, "working/node_modules/pkg/index.js"), filepath.Join(dir, "working/node_modules/pkg"),
		filepath.Join(dir, "working/node_modules"), filepath.Join(dir, "working/notes"),
		filepath.Join(dir, "working"), filepath.Join(dir, ".git/HEAD"), filepath.Join(dir, ".git"),
	} {
		touch(t, p, base)
	}

	b := browse(t, dir)
	if got := b.file(t, "report.md"); !got.Equal(base) {
		t.Errorf("report.md mod_time = %v, want %v", got, base)
	}
	if got := b.dir(t, "working"); !got.Equal(base) {
		t.Errorf("working mod_time = %v, want %v", got, base)
	}

	// A change two folders down moves the top folder's time. Rewriting a
	// file in place doesn't touch any folder's own time, so only the walk
	// can see it.
	touch(t, deep, later)
	if got := browse(t, dir).dir(t, "working"); !got.Equal(later) {
		t.Errorf("after a nested edit, working mod_time = %v, want %v", got, later)
	}

	// Hidden folders and node_modules are not walked: churn there must not
	// light up the folder around them.
	touch(t, deep, base)
	touch(t, filepath.Join(dir, "working/.cache/blob"), later.Add(time.Hour))
	touch(t, filepath.Join(dir, "working/node_modules/pkg/index.js"), later.Add(time.Hour))
	if got := browse(t, dir).dir(t, "working"); !got.Equal(base) {
		t.Errorf("churn in .cache and node_modules moved working to %v, want %v", got, base)
	}

	// A hidden folder that is listed keeps its own time, since it isn't walked.
	touch(t, filepath.Join(dir, ".git/HEAD"), later)
	if got := browse(t, dir).dir(t, ".git"); !got.Equal(base) {
		t.Errorf(".git mod_time = %v, want its own %v", got, base)
	}
}

// One listing looks at a bounded number of entries in total, however many
// folders it dates.
func TestTreeModTimeBudget(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		p := filepath.Join(dir, "big", "f"+string(rune('a'+i)))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
		touch(t, p, base)
	}
	touch(t, filepath.Join(dir, "big"), base)

	fsys := os.DirFS(dir)
	budget := 5
	TreeModTime(fsys, "big", base, &budget)
	if budget != 0 {
		t.Errorf("budget left = %d, want 0 after a tree larger than it", budget)
	}

	// With nothing left, a folder reports its own time rather than walking.
	own := base.Add(-time.Hour)
	touch(t, filepath.Join(dir, "big", "fa"), base.Add(time.Hour))
	if got := TreeModTime(fsys, "big", own, &budget); !got.Equal(own) {
		t.Errorf("with no budget, got %v, want the folder's own %v", got, own)
	}

	budget = TreeBudget
	if got := TreeModTime(fsys, "big", own, &budget); !got.Equal(base.Add(time.Hour)) {
		t.Errorf("with budget, got %v, want the newest file's %v", got, base.Add(time.Hour))
	}
	if used := TreeBudget - budget; used != 21 {
		t.Errorf("walk used %d entries, want 21 (the folder and its 20 files)", used)
	}
}
