// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// A migration by an earlier release left the token file at its old place.
func TestMoveGitHubTokenAfterAnEarlierMigration(t *testing.T) {
	h, root, ourDB := fixture(t)
	old := filepath.Join(h, ".copilot", "github-pat.json")
	moved := filepath.Join(root, ".system", "github-pat.json")
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	db := migrated(t, ourDB)
	write(t, old, `{"token":"t"}`)
	info, _ := os.Stat(old)
	ino := info.Sys().(*syscall.Stat_t).Ino

	if from, err := MoveGitHubToken(); err != nil || from != old {
		t.Fatalf("MoveGitHubToken() = %q, %v; want it moved from %s", from, err, old)
	}
	if exists(old) || readFile(t, moved) != `{"token":"t"}` {
		t.Fatal("the token file didn't move")
	}
	if got := layout.GitHubToken(); got != moved {
		t.Errorf("layout.GitHubToken() = %s", got)
	}
	if from, err := MoveGitHubToken(); err != nil || from != "" {
		t.Errorf("a second call: %q, %v", from, err)
	}

	if err := Run(undoOptions(t, db)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(old)
	if err != nil || info.Sys().(*syscall.Stat_t).Ino != ino || exists(moved) {
		t.Errorf("undo didn't move the token file back: %v", err)
	}
}

func TestMoveGitHubTokenLeavesItAlone(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T, h, root string){
		"the migration hasn't run": func(t *testing.T, h, root string) {},
		"its folder is custom": func(t *testing.T, h, root string) {
			t.Setenv("COPILOT_HOME", filepath.Join(h, ".copilot-custom"))
			write(t, filepath.Join(h, ".copilot-custom", "github-pat.json"), "custom")
			markCompleted(t)
		},
		"one is in the root already": func(t *testing.T, h, root string) {
			write(t, filepath.Join(root, ".system", "github-pat.json"), "new")
			markCompleted(t)
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, root := home(t)
			write(t, filepath.Join(h, ".copilot", "github-pat.json"), "old")
			setup(t, h, root)
			if from, err := MoveGitHubToken(); err != nil || from != "" {
				t.Errorf("MoveGitHubToken() = %q, %v; want nothing moved", from, err)
			}
			if !exists(filepath.Join(h, ".copilot", "github-pat.json")) {
				t.Error("the old file moved")
			}
		})
	}
}

func markCompleted(t *testing.T) {
	t.Helper()
	write(t, layout.CompletionFile(), "")
}
