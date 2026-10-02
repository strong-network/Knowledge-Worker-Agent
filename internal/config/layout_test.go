// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout/layouttest"
)

func TestInitKeepsTodaysPlacesBeforeTheMigration(t *testing.T) {
	t.Setenv(layout.EnvHome, t.TempDir())
	ws := t.TempDir()
	db := filepath.Join(t.TempDir(), "copilot-web.db")
	t.Setenv("COPILOT_WORKSPACE", ws)
	t.Setenv("COPILOT_DB_PATH", db)

	Init()

	if Workspace != ws || DBPath != db {
		t.Errorf("Workspace=%q DBPath=%q, want %q and %q", Workspace, DBPath, ws, db)
	}
}

func TestInitFollowsTheRootAfterTheMigration(t *testing.T) {
	root := layouttest.Migrated(t)
	// The images' own values, which count as old defaults.
	t.Setenv("COPILOT_WORKSPACE", "/home/developer")
	t.Setenv("COPILOT_DB_PATH", "/home/developer/copilot-web.db")

	Init()

	if Workspace != root {
		t.Errorf("Workspace = %q, want the root %q", Workspace, root)
	}
	if want := filepath.Join(root, ".system", "knowledge-worker-agent.db"); DBPath != want {
		t.Errorf("DBPath = %q, want %q", DBPath, want)
	}
}

func TestInitKeepsACustomDatabaseAfterTheMigration(t *testing.T) {
	layouttest.Migrated(t)
	db := filepath.Join(t.TempDir(), "custom.db")
	t.Setenv("COPILOT_DB_PATH", db)

	Init()

	if DBPath != db {
		t.Errorf("DBPath = %q, want the custom %q", DBPath, db)
	}
}

func TestWorkspaceInstructionNamesAMovedChatsOldFolder(t *testing.T) {
	root := layouttest.Migrated(t)
	moved := `[{"from":"/home/alex/chat-old","to":"` + filepath.Join(root, "Chats", "chat-old") + `"}]`
	if err := os.WriteFile(layout.MovedFile(), []byte(moved), 0o644); err != nil {
		t.Fatal(err)
	}
	got := WorkspaceInstruction(filepath.Join(root, "Chats", "chat-old"))
	if !strings.Contains(got, "This workspace was at /home/alex/chat-old until Knowledge Worker Agent moved it.") {
		t.Errorf("moved chat:\n%s", got)
	}
	if got := WorkspaceInstruction(filepath.Join(root, "Chats", "chat-new")); strings.Contains(got, "moved it") {
		t.Errorf("a chat that never moved:\n%s", got)
	}
}
