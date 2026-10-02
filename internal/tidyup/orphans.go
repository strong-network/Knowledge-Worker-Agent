// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Workspace hygiene: finding and measuring abandoned chat folders.
//
// An orphan is a "chat-*" directory under the base workspace that no session
// references. Before workspace hygiene shipped, these were created every time
// a chat was deleted, and they are unreachable by design: with no session row
// there is nothing in the UI to attribute them to, and nothing to surface them
// from.
//
// They are never deleted automatically. The user has never seen them, so we
// show them first and delete on confirmation.
package tidyup

import (
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/workdirs"
)

// Orphan is a chat workspace folder with no session behind it.
type Orphan struct {
	// Path is the absolute directory path.
	Path string `json:"path"`
	// Name is the folder name, which is all the identity an orphan has.
	Name string `json:"name"`
	// SizeBytes is the total size of its contents.
	SizeBytes int64 `json:"size_bytes"`
	// ModifiedAt is when the folder was last written, in RFC3339. It is the
	// only clue to when the chat that owned it was last used.
	ModifiedAt string `json:"modified_at"`
}

// FindOrphans returns the auto-created chat folders under the base workspace
// that no session references.
//
// It refuses to scan unless the session set could be read completely: a partial
// or failed read makes live directories look orphaned, and this list is the
// input to a delete. An empty database is treated the same way — on a fresh or
// broken database every folder on disk would qualify, which is precisely when
// the answer must be "nothing". Never returns nil.
func FindOrphans() []Orphan {
	out := []Orphan{}

	base := config.Workspace
	if base == "" {
		return out
	}

	known, ok := db.SessionWorkdirs()
	if !ok {
		log.Printf("[hygiene] orphan scan skipped: the session list could not be read")
		return out
	}
	if len(known) == 0 {
		// No session anywhere claims a directory. Either there are no sessions,
		// or something is wrong; in both cases every chat folder on disk would
		// look orphaned and deleting them would be catastrophic.
		return out
	}

	// Both permitted locations are scanned: the Chats subdirectory where new
	// chats are created, and the base workspace itself where chats created
	// before that change still live. Missing directories are skipped, so a
	// workspace that has only ever had one of the two is not an error.
	for _, root := range []string{workdirs.ChatsRoot(), base} {
		if root == "" {
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			if !os.IsNotExist(err) {
				log.Printf("[hygiene] orphan scan skipped %s: %v", root, err)
			}
			continue
		}

		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			path := filepath.Join(root, e.Name())
			// The same rails that gate deletion also decide what counts as a
			// chat folder, so nothing can be listed here that could not be
			// deleted.
			if !workdirs.IsAutoChat(path) {
				continue
			}
			if known[path] {
				continue
			}
			orphan := Orphan{Path: path, Name: e.Name(), SizeBytes: DirSize(path)}
			if info, err := e.Info(); err == nil {
				orphan.ModifiedAt = info.ModTime().UTC().Format(time.RFC3339)
			}
			out = append(out, orphan)
		}
	}
	return out
}

// DirSize returns the total size of the files under dir, or 0 if it cannot be
// read. Symlinks are not followed — WalkDir reports the link itself, so a link
// pointing outside the folder contributes nothing rather than counting a
// directory that is not really there.
//
// This is a best-effort figure shown next to a chat, not an accounting record;
// an unreadable subdirectory is skipped rather than failing the whole walk.
func DirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip what we cannot read, keep walking
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
