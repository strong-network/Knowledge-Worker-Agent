// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// manifestEntry is one file, folder or link under a renamed source, by its
// path relative to the source.
type manifestEntry struct {
	Move int    `json:"m"`
	Rel  string `json:"rel"`
	Type string `json:"type"` // dir, file, link or other
	Ino  uint64 `json:"ino"`
	Size int64  `json:"size"`
	Link string `json:"link,omitempty"`
}

// recordManifest writes every entry under every renamed source to path.
func recordManifest(p Plan, path string) ([]manifestEntry, error) {
	var out []manifestEntry
	for i, m := range p.Moves {
		if m.Kind != Rename {
			continue
		}
		err := filepath.WalkDir(m.From, func(cur string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if cur != m.From && p.isSource(cur) {
				return skipEntry(d)
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(m.From, cur)
			e := manifestEntry{Move: i, Rel: rel, Ino: info.Sys().(*syscall.Stat_t).Ino}
			switch {
			case d.IsDir():
				e.Type = "dir"
			case d.Type().IsRegular():
				e.Type, e.Size = "file", info.Size()
			case d.Type()&fs.ModeSymlink != 0:
				e.Type = "link"
				if e.Link, err = os.Readlink(cur); err != nil {
					return err
				}
			default:
				e.Type = "other"
			}
			out = append(out, e)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("manifest of %s: %w", m.From, err)
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, e := range out {
		if err := enc.Encode(e); err != nil {
			f.Close()
			return nil, err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, err
	}
	return out, f.Close()
}

// readManifest reads a manifest. One cut short by a crash is read up to the
// cut: nothing moves until it's complete.
func readManifest(path string) ([]manifestEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []manifestEntry
	dec := json.NewDecoder(f)
	for dec.More() {
		var e manifestEntry
		if dec.Decode(&e) != nil {
			break
		}
		out = append(out, e)
	}
	return out, nil
}

// verifyManifest checks every entry at its place, the target when moved, the
// source when not: the same inode and size, or for a link, which holds no data,
// the same target. relinked holds links the migration rewrote, and grown files
// whose contents it changed, by path.
func verifyManifest(p Plan, entries []manifestEntry, moved bool, relinked, grown map[string]bool) []string {
	var problems []string
	for _, e := range entries {
		if e.Move >= len(p.Moves) {
			problems = append(problems, fmt.Sprintf("manifest entry for move %d, which the plan doesn't have", e.Move))
			continue
		}
		m := p.Moves[e.Move]
		base := m.From
		if moved {
			base = m.To
		}
		path := filepath.Join(base, e.Rel)
		info, err := os.Lstat(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s is missing", path))
			continue
		}
		if relinked[path] {
			continue
		}
		if e.Type == "link" {
			if target, _ := os.Readlink(path); target != e.Link {
				problems = append(problems, fmt.Sprintf("%s points to %s, was %s", path, target, e.Link))
			}
			continue
		}
		if ino := info.Sys().(*syscall.Stat_t).Ino; ino != e.Ino {
			problems = append(problems, fmt.Sprintf("%s is a different file (inode %d, was %d)", path, ino, e.Ino))
			continue
		}
		switch e.Type {
		case "file":
			if info.Size() != e.Size && !grown[path] {
				problems = append(problems, fmt.Sprintf("%s changed size (%d, was %d)", path, info.Size(), e.Size))
			}
		}
		if len(problems) >= 20 {
			return append(problems, "and possibly more")
		}
	}
	return problems
}
