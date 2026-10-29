// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

// A side chat stops forking its parent once it has a session of its own; a
// cleared session doesn't count.
func TestSetOpencodeSessionEndsTheFork(t *testing.T) {
	setupTestDB(t)
	CreateSession("side", config.SessionConfig{ParentID: "p", Fork: true, Label: "Side chat"})

	SetOpencodeSession("side", "")
	if cfg, _ := GetSessionConfig("side"); !cfg.Fork {
		t.Error("clearing the session ended the fork")
	}
	SetOpencodeSession("side", "ses_own")
	cfg, _ := GetSessionConfig("side")
	if cfg.Fork || cfg.ParentID != "p" || cfg.Label != "Side chat" {
		t.Errorf("after its first session the config is %+v", cfg)
	}
	if GetOpencodeSession("side") != "ses_own" {
		t.Error("session not recorded")
	}
}

func TestFolderInUse(t *testing.T) {
	setupTestDB(t)
	CreateSession("a", config.SessionConfig{Workdir: "/w/Chats/chat-1"})
	if !FolderInUse("/w/Chats/chat-1/") || !FolderInUse("/w/Chats/chat-1") {
		t.Error("a chat's folder should be in use")
	}
	if FolderInUse("/w/Chats/chat-2") || FolderInUse("/w/Chats") {
		t.Error("only a chat's own folder is in use")
	}
	DeleteSession("a")
	if FolderInUse("/w/Chats/chat-1") {
		t.Error("a deleted chat's folder should be free")
	}
}
