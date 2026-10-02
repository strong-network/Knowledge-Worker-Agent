// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package tidyup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/workdirs"
)

func mkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func write(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func orphanNames(orphans []Orphan) map[string]bool {
	out := map[string]bool{}
	for _, o := range orphans {
		out[o.Name] = true
	}
	return out
}

func TestFindOrphansListsOnlyUnreferencedChatFolders(t *testing.T) {
	setupDB(t)
	base := t.TempDir()
	config.Workspace = base

	live := mkdir(t, filepath.Join(base, "chat-liveliveliv"))
	if err := db.CreateSession("live", config.SessionConfig{Workdir: live}); err != nil {
		t.Fatal(err)
	}
	mkdir(t, filepath.Join(base, "chat-orphanorpha"))
	// A directory the user made themselves, which happens to sit next to ours.
	mkdir(t, filepath.Join(base, "my-important-files"))
	// A chat folder belonging to a project chat: referenced, so not an orphan.
	projectDir := mkdir(t, filepath.Join(base, "chat-projectproj"))
	if err := db.CreateSession("in-project", config.SessionConfig{Workdir: projectDir}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionProject("in-project", "p1"); err != nil {
		t.Fatal(err)
	}
	// A file, not a directory, that matches the naming pattern.
	write(t, filepath.Join(base, "chat-notadirector"), 4)

	names := orphanNames(FindOrphans())

	if !names["chat-orphanorpha"] {
		t.Error("the unreferenced chat folder should be listed")
	}
	for _, name := range []string{"chat-liveliveliv", "my-important-files", "chat-projectproj", "chat-notadirector"} {
		if names[name] {
			t.Errorf("%s must not be listed as an orphan", name)
		}
	}
	if len(names) != 1 {
		t.Errorf("expected exactly 1 orphan, got %d: %v", len(names), names)
	}
}

// The orphan list is the input to a delete, so it must fail towards doing
// nothing. On an empty database every folder on disk is unreferenced — that is
// the one moment the answer has to be "none", not "all of them".
func TestFindOrphansRefusesToScanWithoutAKnownSessionSet(t *testing.T) {
	setupDB(t)
	base := t.TempDir()
	config.Workspace = base
	mkdir(t, filepath.Join(base, "chat-orphanorpha"))
	mkdir(t, filepath.Join(base, "chat-anotheranot"))

	if got := FindOrphans(); len(got) != 0 {
		t.Errorf("an empty session list must produce no orphans, got %d", len(got))
	}

	// Same reasoning when the database cannot be read at all.
	db.Close()
	if got := FindOrphans(); len(got) != 0 {
		t.Errorf("an unreadable session list must produce no orphans, got %d", len(got))
	}
}

func TestFindOrphansReturnsEmptyWithoutAWorkspace(t *testing.T) {
	setupDB(t)
	prev := config.Workspace
	t.Cleanup(func() { config.Workspace = prev })
	config.Workspace = ""

	if got := FindOrphans(); got == nil || len(got) != 0 {
		t.Errorf("expected an empty, non-nil list, got %#v", got)
	}
}

// Chat folders live in two places during and after the move to a Chats
// subdirectory: new ones inside it, and pre-existing ones in the workspace
// root. Both are scanned, so neither an old nor a new abandoned folder becomes
// invisible and undeletable.
func TestFindOrphansScansBothTheChatsSubdirectoryAndTheWorkspaceRoot(t *testing.T) {
	setupDB(t)
	base := t.TempDir()
	config.Workspace = base

	live := mkdir(t, filepath.Join(base, workdirs.ChatsDirName, "chat-liveliveliv"))
	if err := db.CreateSession("live", config.SessionConfig{Workdir: live}); err != nil {
		t.Fatal(err)
	}
	// A chat created before the move, still referenced by its session: its
	// stored absolute path must keep matching, or it would be offered for
	// deletion while in use.
	legacyLive := mkdir(t, filepath.Join(base, "chat-legacylivex"))
	if err := db.CreateSession("legacy-live", config.SessionConfig{Workdir: legacyLive}); err != nil {
		t.Fatal(err)
	}

	mkdir(t, filepath.Join(base, workdirs.ChatsDirName, "chat-neworphanxx"))
	mkdir(t, filepath.Join(base, "chat-oldorphanxx"))
	mkdir(t, filepath.Join(base, workdirs.ChatsDirName, "my-notes"))

	names := orphanNames(FindOrphans())

	for _, name := range []string{"chat-neworphanxx", "chat-oldorphanxx"} {
		if !names[name] {
			t.Errorf("%s should be listed as an orphan", name)
		}
	}
	for _, name := range []string{"chat-liveliveliv", "chat-legacylivex", "my-notes"} {
		if names[name] {
			t.Errorf("%s must not be listed as an orphan", name)
		}
	}
	if len(names) != 2 {
		t.Errorf("expected exactly 2 orphans, got %d: %v", len(names), names)
	}
}

// A workspace that has never created a chat since the move has no Chats
// directory at all. That is the normal state on first run, not a failure, and
// it must not stop the root scan from finding legacy orphans.
func TestFindOrphansToleratesAMissingChatsSubdirectory(t *testing.T) {
	setupDB(t)
	base := t.TempDir()
	config.Workspace = base

	live := mkdir(t, filepath.Join(base, "chat-liveliveliv"))
	if err := db.CreateSession("live", config.SessionConfig{Workdir: live}); err != nil {
		t.Fatal(err)
	}
	mkdir(t, filepath.Join(base, "chat-oldorphanxx"))

	if _, err := os.Stat(filepath.Join(base, workdirs.ChatsDirName)); !os.IsNotExist(err) {
		t.Fatalf("precondition: expected no Chats directory, stat err = %v", err)
	}

	names := orphanNames(FindOrphans())
	if !names["chat-oldorphanxx"] || len(names) != 1 {
		t.Errorf("expected only the legacy orphan, got %v", names)
	}
}

func TestFindOrphansReportsSizeAndLastModified(t *testing.T) {
	setupDB(t)
	base := t.TempDir()
	config.Workspace = base

	live := mkdir(t, filepath.Join(base, "chat-liveliveliv"))
	if err := db.CreateSession("live", config.SessionConfig{Workdir: live}); err != nil {
		t.Fatal(err)
	}
	orphan := mkdir(t, filepath.Join(base, "chat-orphanorpha"))
	write(t, filepath.Join(orphan, "a.txt"), 100)

	found := FindOrphans()
	if len(found) != 1 {
		t.Fatalf("expected 1 orphan, got %d", len(found))
	}
	o := found[0]
	if o.Path != orphan {
		t.Errorf("path = %q, want %q", o.Path, orphan)
	}
	if o.SizeBytes != 100 {
		t.Errorf("size = %d, want 100", o.SizeBytes)
	}
	// The folder name and its age are the only clues the user has: with no
	// session row there is nothing else to identify it by.
	if o.ModifiedAt == "" {
		t.Error("last-modified must be reported")
	}
}

func TestDirSize(t *testing.T) {
	base := t.TempDir()
	write(t, filepath.Join(base, "a.txt"), 100)
	sub := mkdir(t, filepath.Join(base, "sub"))
	write(t, filepath.Join(sub, "b.txt"), 50)

	if got := DirSize(base); got != 150 {
		t.Errorf("DirSize = %d, want 150", got)
	}
	if got := DirSize(filepath.Join(base, "does-not-exist")); got != 0 {
		t.Errorf("an unreadable directory should measure 0, got %d", got)
	}
}

// A symlink is a pointer, not content. Following one would attribute somebody
// else's disk to this chat, and could count the same bytes twice.
func TestDirSizeDoesNotFollowSymlinks(t *testing.T) {
	outside := t.TempDir()
	write(t, filepath.Join(outside, "big.bin"), 4096)

	base := t.TempDir()
	write(t, filepath.Join(base, "a.txt"), 100)
	if err := os.Symlink(outside, filepath.Join(base, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if got := DirSize(base); got != 100 {
		t.Errorf("DirSize = %d, want 100 (the symlinked tree must not be counted)", got)
	}
}
