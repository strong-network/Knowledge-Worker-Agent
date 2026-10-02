// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package guest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The guest's listing carries times the same way, and dating a folder stays in
// the chat's folder: a link out of it cannot bring an outside file's time in.
func TestGuestBrowseModTimes(t *testing.T) {
	f := newFilesEnv(t, true)
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, p := range []string{"notes.md", "docs/plan.txt", "docs"} {
		os.Chtimes(filepath.Join(f.dir, p), base, base)
	}
	// A link from docs to the home folder, where a file changes later. The
	// link itself is new, and counts; what it leads to must not.
	os.Symlink(filepath.Dir(f.secret), filepath.Join(f.dir, "docs", "out"))
	outside := time.Now().Add(24 * time.Hour)
	os.Chtimes(f.secret, outside, outside)

	type entry struct {
		Name    string    `json:"name"`
		ModTime time.Time `json:"mod_time"`
	}
	list := func() (dirs, files map[string]time.Time) {
		t.Helper()
		code, body, _ := f.do("GET", f.url("browse", ""), "", nil)
		if code != 200 {
			t.Fatalf("browse: %d %s", code, body)
		}
		var b struct{ Dirs, Files []entry }
		if err := json.Unmarshal([]byte(body), &b); err != nil {
			t.Fatal(err)
		}
		dirs, files = map[string]time.Time{}, map[string]time.Time{}
		for _, d := range b.Dirs {
			dirs[d.Name] = d.ModTime
		}
		for _, e := range b.Files {
			files[e.Name] = e.ModTime
		}
		return dirs, files
	}

	dirs, files := list()
	if !files["notes.md"].Equal(base) {
		t.Errorf("notes.md mod_time = %v, want %v", files["notes.md"], base)
	}
	if !dirs["docs"].Before(outside) {
		t.Errorf("docs mod_time = %v: the time of a file outside the folder leaked in", dirs["docs"])
	}

	later := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	os.Chtimes(filepath.Join(f.dir, "docs", "plan.txt"), later, later)
	if dirs, _ := list(); !dirs["docs"].Equal(later) {
		t.Errorf("after an edit inside, docs mod_time = %v, want %v", dirs["docs"], later)
	}
}
