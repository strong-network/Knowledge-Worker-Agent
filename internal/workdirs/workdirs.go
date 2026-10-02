// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package workdirs holds the safety rails for deleting chat working
// directories.
//
// Every code path that removes a chat's files — session delete, tidy-up
// cleanup, and the automatic workspace hygiene sweeps — routes through
// RemoveAutoChat. These few lines are the only thing standing between a policy
// bug and a user's home directory, so they live in one place, are deliberately
// narrow, and are tested directly.
//
// The rule: a directory is removable only when it is an *auto-created* per-chat
// folder — the "chat-<id>" directory that HandleNewSession creates. A directory
// the user chose (an existing folder, a cloned repo, a project workspace) never
// matches and is never deleted.
//
// There are two permitted locations. New chats are created under the "Chats"
// subdirectory of the base workspace, alongside "Projects" and "Scheduled
// Tasks". Chats created before that change live directly in the base workspace,
// and their stored workdir is an absolute path that keeps working untouched —
// so the legacy location stays permitted indefinitely rather than stranding
// every existing chat as unrecognized (and, worse, reporting it as an orphan).
package workdirs

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

// AutoChatPrefix is the name prefix of an auto-created per-chat folder. It must
// stay in step with uniqueChatDir in internal/sessions.
const AutoChatPrefix = "chat-"

// ChatsDirName is the subdirectory of the base workspace that new per-chat
// folders are created in, so chats do not litter the workspace root next to
// the user's own files. Title case matches the sibling "Projects" and
// "Scheduled Tasks" folders.
const ChatsDirName = "Chats"

// ChatsRoot returns the directory new per-chat folders belong in, or "" when no
// base workspace is configured. It does not create the directory.
func ChatsRoot() string {
	base := strings.TrimSpace(config.Workspace)
	if base == "" {
		return ""
	}
	return filepath.Join(base, ChatsDirName)
}

// IsAutoChat reports whether dir is an auto-created per-chat workspace folder
// and is therefore safe to delete along with its chat.
//
// Both conditions must hold: the directory is named "chat-*", and its parent is
// either the Chats subdirectory (where new chats are created) or the base
// workspace itself (where chats created before that change still live).
// Requiring a *direct child* of one of those two rather than merely "somewhere
// underneath" matches how these folders are created and rejects a hand-edited
// or corrupted workdir pointing deeper into a real project.
//
// Symlinks are resolved on both sides before comparison, so neither a symlinked
// base workspace nor a symlinked parent can be used to make an unrelated
// directory look like a chat folder.
func IsAutoChat(dir string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return false
	}
	base := strings.TrimSpace(config.Workspace)
	if base == "" {
		return false
	}
	abs, err := filepath.Abs(dir) // also cleans, so a trailing slash is harmless
	if err != nil {
		return false
	}
	name := filepath.Base(abs)
	if !strings.HasPrefix(name, AutoChatPrefix) || name == AutoChatPrefix {
		return false
	}
	parent := filepath.Dir(abs)
	return sameDir(parent, filepath.Join(base, ChatsDirName)) || sameDir(parent, base)
}

// RemoveAutoChat deletes dir when, and only when, IsAutoChat accepts it, and
// reports whether anything was removed. A rejected path is silently ignored:
// callers hand it whatever workdir a session happened to carry, and "this chat
// used a folder the user chose" is the normal case, not an error.
//
// A removal that is attempted and fails is logged, because that leaves a
// directory behind with no session to attribute it to.
func RemoveAutoChat(dir string) bool {
	if !IsAutoChat(dir) {
		return false
	}
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("[workdirs] failed to remove chat workspace %s: %v", dir, err)
		return false
	}
	return true
}

// sameDir reports whether two paths name the same directory, resolving symlinks
// where the path exists. Resolution failures fall back to the cleaned absolute
// path so a missing directory compares by name rather than matching everything.
func sameDir(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(absA); err == nil {
		absA = resolved
	}
	if resolved, err := filepath.EvalSymlinks(absB); err == nil {
		absB = resolved
	}
	return absA == absB
}
