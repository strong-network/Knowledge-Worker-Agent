// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package workdirs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

// The rails decide whether a directory may be deleted. Everything that could
// plausibly be handed to them — a real chat folder, a folder the user chose, a
// traversal, the base workspace itself — is checked here, because a wrong
// answer means deleting something the user cared about.
func TestIsAutoChatAcceptsOnlyAutoCreatedChatFolders(t *testing.T) {
	base := t.TempDir()
	config.Workspace = base

	cases := []struct {
		name string
		dir  string
		want bool
	}{
		{"auto-created chat folder", filepath.Join(base, ChatsDirName, "chat-a1b2c3d4e5f6"), true},
		{"trailing slash is cleaned away", filepath.Join(base, ChatsDirName, "chat-a1b2c3d4e5f6") + "/", true},

		// Chats created before the Chats subdirectory existed still live in the
		// workspace root, and their stored workdir is never rewritten. If these
		// stopped matching, every one of them would be reported as an orphan.
		{"legacy chat folder in the workspace root", filepath.Join(base, "chat-a1b2c3d4e5f6"), true},
		{"legacy folder with a trailing slash", filepath.Join(base, "chat-a1b2c3d4e5f6") + "/", true},

		{"empty path", "", false},
		{"whitespace only", "   ", false},
		{"the base workspace itself", base, false},
		{"the Chats directory itself", filepath.Join(base, ChatsDirName), false},
		{"parent of the base workspace", filepath.Dir(base), false},
		{"root", "/", false},
		{"user-chosen folder under the base", filepath.Join(base, "my-project"), false},
		{"user-chosen folder inside Chats", filepath.Join(base, ChatsDirName, "my-project"), false},
		{"cloned repo under the base", filepath.Join(base, "some-repo"), false},
		{"bare prefix with no id", filepath.Join(base, "chat-"), false},
		{"bare prefix with no id inside Chats", filepath.Join(base, ChatsDirName, "chat-"), false},
		{"chat folder outside the base", filepath.Join(t.TempDir(), "chat-a1b2c3d4e5f6"), false},
		{"Chats directory of a different workspace", filepath.Join(t.TempDir(), ChatsDirName, "chat-a1b2c3d4e5f6"), false},

		// Nesting is rejected: auto folders are always direct children of one of
		// the two permitted parents, so anything deeper is a hand-edited or
		// corrupted workdir and may well be real work inside a project.
		{"nested one level deeper", filepath.Join(base, "project", "chat-a1b2c3d4e5f6"), false},
		{"nested one level deeper under Chats", filepath.Join(base, ChatsDirName, "project", "chat-a1b2c3d4e5f6"), false},
		{"chat folder inside a chat folder", filepath.Join(base, "chat-aaaaaaaaaaaa", "chat-bbbbbbbbbbbb"), false},
		{"chat folder inside a chat folder under Chats", filepath.Join(base, ChatsDirName, "chat-aaaaaaaaaaaa", "chat-bbbbbbbbbbbb"), false},

		// Traversal is cleaned before the parent check, so it cannot escape.
		{"traversal out of the base", filepath.Join(base, "..", "chat-a1b2c3d4e5f6"), false},
		{"traversal back into the base", filepath.Join(base, "sub", "..", "chat-a1b2c3d4e5f6"), true},
		{"traversal back into Chats", filepath.Join(base, ChatsDirName, "sub", "..", "chat-a1b2c3d4e5f6"), true},
		{"traversal out of Chats and out of the base", filepath.Join(base, ChatsDirName, "..", "..", "chat-a1b2c3d4e5f6"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAutoChat(tc.dir); got != tc.want {
				t.Errorf("IsAutoChat(%q) = %v, want %v", tc.dir, got, tc.want)
			}
		})
	}
}

// With no configured base workspace there is no way to tell an auto folder from
// anything else, so nothing qualifies. Failing closed matters: config.Workspace
// is empty in tests and early in startup.
func TestIsAutoChatRejectsEverythingWithoutABaseWorkspace(t *testing.T) {
	config.Workspace = ""
	if IsAutoChat("/home/developer/chat-a1b2c3d4e5f6") {
		t.Error("expected no directory to qualify when the base workspace is unset")
	}
}

func TestRemoveAutoChatDeletesTheFolderAndItsContents(t *testing.T) {
	base := t.TempDir()
	config.Workspace = base

	dir := filepath.Join(base, "chat-a1b2c3d4e5f6")
	if err := os.MkdirAll(filepath.Join(dir, "working"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "working", "notes.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !RemoveAutoChat(dir) {
		t.Fatal("expected the auto chat folder to be removed")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("expected %s to be gone, stat err = %v", dir, err)
	}
}

func TestRemoveAutoChatLeavesAUserChosenFolderAlone(t *testing.T) {
	base := t.TempDir()
	config.Workspace = base

	chosen := filepath.Join(base, "my-existing-project")
	if err := os.MkdirAll(chosen, 0o755); err != nil {
		t.Fatal(err)
	}

	if RemoveAutoChat(chosen) {
		t.Error("expected a user-chosen folder to be rejected")
	}
	if _, err := os.Stat(chosen); err != nil {
		t.Errorf("expected %s to survive, stat err = %v", chosen, err)
	}
}

// A workdir pointing at a symlink must remove the link, never what it targets.
// This is the difference between deleting a chat and deleting the directory it
// happened to reference.
func TestRemoveAutoChatFollowingASymlinkRemovesOnlyTheLink(t *testing.T) {
	base := t.TempDir()
	config.Workspace = base

	target := filepath.Join(t.TempDir(), "precious")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	keepMe := filepath.Join(target, "keep.txt")
	if err := os.WriteFile(keepMe, []byte("important"), 0o644); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(base, "chat-a1b2c3d4e5f6")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	RemoveAutoChat(link)

	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("expected the symlink itself to be removed, stat err = %v", err)
	}
	if _, err := os.Stat(keepMe); err != nil {
		t.Fatalf("symlink target was destroyed: %v", err)
	}
}

// A symlinked base workspace must still match its own children. Without
// resolving both sides the comparison would fail and real chat folders would
// leak instead of being cleaned up.
func TestIsAutoChatResolvesASymlinkedBaseWorkspace(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	dir := filepath.Join(real, "chat-a1b2c3d4e5f6")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	config.Workspace = link
	if !IsAutoChat(dir) {
		t.Error("expected a chat folder to match a symlinked base workspace")
	}
	config.Workspace = real
	if !IsAutoChat(filepath.Join(link, "chat-a1b2c3d4e5f6")) {
		t.Error("expected a chat folder reached through a symlink to match the real base")
	}
}

func TestChatsRoot(t *testing.T) {
	base := t.TempDir()
	config.Workspace = base
	if got, want := ChatsRoot(), filepath.Join(base, ChatsDirName); got != want {
		t.Errorf("ChatsRoot() = %q, want %q", got, want)
	}

	// Fails closed alongside IsAutoChat, so a caller cannot be handed a bare
	// "Chats" relative path early in startup.
	config.Workspace = ""
	if got := ChatsRoot(); got != "" {
		t.Errorf("ChatsRoot() with no workspace = %q, want empty", got)
	}
}

// A chat created in the Chats subdirectory must be deletable, or chats would
// accumulate forever with nothing able to clean them up.
func TestRemoveAutoChatDeletesAFolderInTheChatsSubdirectory(t *testing.T) {
	base := t.TempDir()
	config.Workspace = base

	dir := filepath.Join(base, ChatsDirName, "chat-a1b2c3d4e5f6")
	if err := os.MkdirAll(filepath.Join(dir, "working"), 0o755); err != nil {
		t.Fatal(err)
	}

	if !RemoveAutoChat(dir) {
		t.Fatal("expected a chat folder under Chats/ to be removed")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("expected %s to be gone, stat err = %v", dir, err)
	}
	// Only the chat goes; the shared parent stays for every other chat.
	if _, err := os.Stat(filepath.Join(base, ChatsDirName)); err != nil {
		t.Errorf("the Chats directory itself must survive, stat err = %v", err)
	}
}

// The whole point of keeping the legacy location permitted: a chat that existed
// before the move must still be deletable through the normal path.
func TestRemoveAutoChatDeletesALegacyFolderInTheWorkspaceRoot(t *testing.T) {
	base := t.TempDir()
	config.Workspace = base

	dir := filepath.Join(base, "chat-a1b2c3d4e5f6")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if !RemoveAutoChat(dir) {
		t.Fatal("expected a legacy chat folder in the workspace root to be removed")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("expected %s to be gone, stat err = %v", dir, err)
	}
}
