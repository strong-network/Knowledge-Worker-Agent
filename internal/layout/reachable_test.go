// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package layout

import (
	"path/filepath"
	"testing"
)

func TestReachable(t *testing.T) {
	home, root := setup(t)
	complete(t)
	for path, want := range map[string]bool{
		home:                                 true,
		filepath.Join(home, "notes", "a.md"): true,
		filepath.Join(home, "repo", ".github", "x"): true,
		filepath.Join(home, "repo", ".env"):         true,
		filepath.Join(home, ".ssh", "id_ed25519"):   false,
		filepath.Join(home, ".config", "opencode"):  false,
		filepath.Join(home, ".bashrc"):              false,
		filepath.Join(home, "notes", "..", ".ssh"):  false,
		root: true,
		filepath.Join(root, "Chats", "chat-1", "a"): true,
		filepath.Join(root, ".system", "mcp-keys"):  false,
		filepath.Join(root, ".system"):              false,
		"/etc/passwd":                               false,
		"/":                                         false,
		filepath.Dir(home):                          false,
		"":                                          false,
	} {
		if _, got := Reachable(path); got != want {
			t.Errorf("Reachable(%q) = %v, want %v", path, got, want)
		}
	}
	if abs, _ := Reachable(filepath.Join(home, "a", "..", "b")); abs != filepath.Join(home, "b") {
		t.Errorf("not cleaned: %q", abs)
	}
}

// The old layout's base folder was the home folder itself; that mustn't let
// the hidden items in.
func TestReachableWithTheHomeFolderAsBase(t *testing.T) {
	home, _ := setup(t)
	t.Setenv("COPILOT_WORKSPACE", home)
	if _, ok := Reachable(filepath.Join(home, ".ssh", "id_ed25519")); ok {
		t.Error("a base folder holding the home folder let its hidden items in")
	}
	if _, ok := Reachable(filepath.Join(home, "Chats", "a")); !ok {
		t.Error("a chat folder in the home folder is refused")
	}
}
