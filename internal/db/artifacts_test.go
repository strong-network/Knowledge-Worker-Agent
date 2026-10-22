// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

func TestSharedArtifactRows(t *testing.T) {
	setupTestDB(t)
	a := SharedArtifact{ID: "a1", Folder: "/w/Chats/chat-1", RelPath: "out/report.html", SessionID: "s1", Port: 8770}
	if err := CreateSharedArtifact(a); err != nil {
		t.Fatal(err)
	}
	for name, dup := range map[string]SharedArtifact{
		"same file": {ID: "a2", Folder: a.Folder, RelPath: a.RelPath, Port: 8771},
		"same port": {ID: "a3", Folder: a.Folder, RelPath: "other.html", Port: 8770},
	} {
		if CreateSharedArtifact(dup) == nil {
			t.Errorf("%s accepted twice", name)
		}
	}
	got, ok := SharedArtifactAt(a.Folder, a.RelPath)
	if !ok || got.ID != "a1" || got.SessionID != "s1" || got.AllowDownload || len(got.SharedAt) != len("2006-01-02T15:04:05Z") {
		t.Fatalf("got %+v %v", got, ok)
	}
	if SetSharedArtifactDownload("a1", true) != nil || SetSharedArtifactPort("a1", 8775) != nil || ClearArtifactSession("a1") != nil {
		t.Fatal("updates failed")
	}
	got, _ = GetSharedArtifact("a1")
	if !got.AllowDownload || got.Port != 8775 || got.SessionID != "" {
		t.Errorf("after updates: %+v", got)
	}
	if SetSharedArtifactPort("nope", 1) == nil {
		t.Error("an update of a missing row must say so")
	}
	if !FolderHasSharedArtifact(a.Folder) || FolderHasSharedArtifact("/w/Chats/chat-2") || !SharedArtifactFolders()[a.Folder] {
		t.Error("folder lookups")
	}
	if DeleteSharedArtifact("a1") != nil || len(SharedArtifacts()) != 0 {
		t.Error("delete")
	}
}

// A chat whose folder holds a shared file is in use, like a shared chat:
// tidy-up neither offers it for review nor sweeps it when it looks empty.
func TestTidyUpLeavesChatsWithASharedFileAlone(t *testing.T) {
	setupTestDB(t)
	emptyChat(t, "empty-shared", config.SessionConfig{Workdir: "/w/Chats/chat-e/"}, hoursAgo(48))
	emptyChat(t, "empty-plain", config.SessionConfig{Workdir: "/w/Chats/chat-p"}, hoursAgo(48))
	for _, id := range []string{"idle-shared", "idle-plain"} {
		emptyChat(t, id, config.SessionConfig{Label: id, Workdir: "/w/Chats/" + id}, hoursAgo(24*90))
		if err := AddMessage(id, "user", "hello"); err != nil {
			t.Fatal(err)
		}
		setSessionUpdatedAt(t, id, hoursAgo(24*90))
	}
	for i, folder := range []string{"/w/Chats/chat-e", "/w/Chats/idle-shared"} {
		if err := CreateSharedArtifact(SharedArtifact{ID: folder, Folder: folder, RelPath: "r.html", Port: 8770 + i}); err != nil {
			t.Fatal(err)
		}
	}
	empty := EmptyChatCandidates(time.Now(), 24*time.Hour)
	if hasCandidate(empty, "empty-shared") || !hasCandidate(empty, "empty-plain") {
		t.Errorf("sweep candidates: %+v", empty)
	}
	items, _ := ReviewCandidates(time.Now(), 24*time.Hour*30)
	seen := map[string]bool{}
	for _, it := range items {
		seen[it.ID] = true
	}
	if seen["idle-shared"] || !seen["idle-plain"] {
		t.Errorf("review list: %+v", items)
	}
}
