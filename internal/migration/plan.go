// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package migration moves everything Knowledge Worker Agent keeps into one
// folder, the root from internal/layout. This file works out what would move.
package migration

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// EnvMode switches the migration: unset runs it, unless an undo was the last
// run; "off" does nothing, "dry-run" reports what it would do, "on" moves
// the files, and "undo" moves them back.
const EnvMode = "KWA_MIGRATE"

// Mode is how the migration runs.
type Mode string

const (
	Off    Mode = "off"
	DryRun Mode = "dry-run"
	On     Mode = "on"
	Undo   Mode = "undo"
)

// ModeFromEnv reads EnvMode. Unset means On, or Off after an undo, so an undo
// lasts until "on" asks for the migration again. An unknown value is an
// error, and means Off.
func ModeFromEnv() (Mode, error) {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv(EnvMode))); v {
	case "":
		if Undone() {
			return Off, nil
		}
		return On, nil
	case "off", "0", "false", "no":
		return Off, nil
	case "dry-run", "dryrun":
		return DryRun, nil
	case "on", "1", "true", "yes":
		return On, nil
	case "undo":
		return Undo, nil
	default:
		return Off, fmt.Errorf("%s=%q is not a mode: use dry-run, on or undo", EnvMode, v)
	}
}

// Undone reports whether the last completed run was an undo: it leaves its
// record of what moved, and no completion record.
func Undone() bool { return !layout.Migrated() && len(layout.Moved()) > 0 }

// Kind is how a move is carried out.
type Kind string

const (
	// Rename moves a file or folder within one filesystem; nothing is copied.
	Rename Kind = "rename"
	// Copy leaves the original where it is, for things in folders other tools share.
	Copy Kind = "copy"
	// Uncopy sets a copy the migration made aside in the run's folder, when
	// undoing it; the original is still in place.
	Uncopy Kind = "uncopy"
)

// Move is one change the migration makes to the filesystem.
type Move struct {
	Name     string // the location it belongs to
	From, To string
	Kind     Kind
	Dir      bool  // a folder rather than a file or link
	Size     int64 // bytes in regular files under From
	Entries  int   // files, folders and links under From, From included
	Dirs     int   // folders under From, From included
}

// Plan is what the migration would do, as things are now.
type Plan struct {
	Root           string
	Moves          []Move
	Kept           []layout.Location // custom settings, which never move
	Absent         []layout.Location // nothing there yet, so nothing to move
	OtherDatabases []string          // old database files that aren't the one in use
}

// LegacyChatsName names the moves of chats from before the Chats folder.
const LegacyChatsName = "Legacy chats"

// UndoPlan works out what undoing the migration would do now. It changes
// nothing. Each rename is reversed, newest first, so a legacy chat leaves
// Chats before Chats moves; a place deleted since is skipped. A location
// created in the root since moves to its old place too, and each Obsidian copy
// is set aside, or moved back if its original has gone.
func UndoPlan() Plan {
	p := Plan{Root: layout.Root()}
	locations := layout.Locations()
	names := map[string]string{}
	for _, l := range locations {
		names[filepath.Clean(l.Moved)] = l.Name
	}
	reversed := map[string]bool{}
	moved := layout.Moved()
	for i := len(moved) - 1; i >= 0; i-- {
		r := moved[i]
		reversed[filepath.Clean(r.To)] = true
		info, err := os.Lstat(r.To)
		if err != nil {
			continue
		}
		name := names[filepath.Clean(r.To)]
		if name == "" {
			name = LegacyChatsName
		}
		p.Moves = append(p.Moves, Move{Name: name, From: r.To, To: r.From, Kind: Rename, Dir: info.IsDir()})
	}
	for _, l := range locations {
		switch {
		case l.Name == layout.NameWorkspace || l.Name == layout.NameLogFile:
			continue
		case l.Custom:
			p.Kept = append(p.Kept, l)
			continue
		case l.Current == "" || samePath(l.Current, l.Moved) || reversed[filepath.Clean(l.Moved)]:
			continue
		case l.Name == layout.NameObsidianInstall:
			p.undoObsidian(l)
			continue
		}
		if info, err := os.Lstat(l.Moved); err == nil {
			p.Moves = append(p.Moves, Move{Name: l.Name, From: l.Moved, To: l.Current, Kind: Rename, Dir: info.IsDir()})
		}
	}
	for i := range p.Moves {
		p.Moves[i] = measureSkipping(p.Moves[i], p.isSource)
	}
	return p
}

// undoObsidian sets aside each copy of the Obsidian connector whose original
// is still in place, and moves back one whose original has gone.
func (p *Plan) undoObsidian(l layout.Location) {
	for _, rel := range []string{filepath.Join("lib", "node_modules", "obsidian-mcp"), filepath.Join("bin", "obsidian-mcp")} {
		copied := filepath.Join(l.Moved, rel)
		info, err := os.Lstat(copied)
		if err != nil {
			continue
		}
		kind := Uncopy
		if _, err := os.Lstat(filepath.Join(l.Current, rel)); err != nil {
			kind = Rename
		}
		p.Moves = append(p.Moves, Move{Name: l.Name, From: copied, To: filepath.Join(l.Current, rel), Kind: kind, Dir: info.IsDir()})
	}
}

// isSource reports whether path is a rename's source. Undoing has one inside
// another, a legacy chat inside Chats, so each walk of a source skips the
// sources inside it.
func (p Plan) isSource(path string) bool {
	for _, m := range p.Moves {
		if m.Kind == Rename && samePath(m.From, path) {
			return true
		}
	}
	return false
}

// NewPlan works out what the migration would do now. It changes nothing.
func NewPlan() Plan {
	p := Plan{Root: layout.Root()}
	var workspace, database layout.Location
	for _, l := range layout.Locations() {
		switch l.Name {
		case layout.NameWorkspace:
			workspace = l
			continue // the base folder itself stays; what's in it moves below
		case layout.NameLogFile:
			continue // a new log starts in the root; the old one is in /tmp
		case layout.NameDatabase:
			database = l
		}
		if l.Custom {
			p.Kept = append(p.Kept, l)
			continue
		}
		if l.Current == "" || samePath(l.Current, l.Moved) {
			continue
		}
		if l.Name == layout.NameObsidianInstall {
			p.addObsidian(l)
			continue
		}
		info, err := os.Lstat(l.Current)
		if err != nil {
			p.Absent = append(p.Absent, l)
			continue
		}
		p.Moves = append(p.Moves, measure(Move{Name: l.Name, From: l.Current, To: l.Moved, Kind: Rename, Dir: info.IsDir()}))
	}
	if !workspace.Custom {
		p.addLegacyChats(workspace.Current)
	}
	p.findOtherDatabases(database)
	return p
}

// addLegacyChats moves each chat-* folder in the base folder into Chats, where
// chats have been created since.
func (p *Plan) addLegacyChats(base string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	chats := filepath.Join(p.Root, "Chats")
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, "chat-") || name == "chat-" {
			continue // ReadDir reports a link as not a folder, so links stay
		}
		p.Moves = append(p.Moves, measure(Move{Name: LegacyChatsName, From: filepath.Join(base, name), To: filepath.Join(chats, name), Kind: Rename, Dir: true}))
	}
}

// addObsidian copies the Obsidian connector's package and its launcher, which
// is a relative link into the package, so the copy needs no network. The
// install folder is shared with other tools, so nothing else in it moves.
func (p *Plan) addObsidian(l layout.Location) {
	for _, rel := range []string{filepath.Join("lib", "node_modules", "obsidian-mcp"), filepath.Join("bin", "obsidian-mcp")} {
		from := filepath.Join(l.Current, rel)
		info, err := os.Lstat(from)
		if err != nil {
			continue
		}
		p.Moves = append(p.Moves, measure(Move{Name: l.Name, From: from, To: filepath.Join(l.Moved, rel), Kind: Copy, Dir: info.IsDir()}))
	}
}

// findOtherDatabases lists old database files that exist but aren't in use.
// The migration moves only the one in use.
func (p *Plan) findOtherDatabases(inUse layout.Location) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return
	}
	for _, c := range []string{filepath.Join(home, ".copilot-web.db"), filepath.Join(home, "copilot-web.db")} {
		if samePath(c, inUse.Current) {
			continue
		}
		if info, err := os.Stat(c); err == nil && info.Mode().IsRegular() {
			p.OtherDatabases = append(p.OtherDatabases, c)
		}
	}
	sort.Strings(p.OtherDatabases)
}

// Renames returns the folder renames, whose stored paths are rewritten.
func (p Plan) Renames() []Move {
	var out []Move
	for _, m := range p.Moves {
		if m.Kind == Rename && m.Dir {
			out = append(out, m)
		}
	}
	return out
}

// Remap returns where path ends up after the plan's folder renames, and
// whether it's under one of them at all.
func (p Plan) Remap(path string) (string, bool) {
	clean := filepath.Clean(path)
	best := -1
	for i, m := range p.Moves {
		if m.Kind != Rename || !under(clean, m.From) {
			continue
		}
		if best < 0 || len(m.From) > len(p.Moves[best].From) {
			best = i
		}
	}
	if best < 0 {
		return path, false
	}
	m := p.Moves[best]
	return filepath.Join(m.To, strings.TrimPrefix(clean, filepath.Clean(m.From))), true
}

// Totals returns the bytes and entries the plan touches.
func (p Plan) Totals() (size int64, entries int) {
	for _, m := range p.Moves {
		size += m.Size
		entries += m.Entries
	}
	return size, entries
}

func measure(m Move) Move { return measureSkipping(m, nil) }

// measureSkipping measures m, leaving out what's under a path skip reports.
func measureSkipping(m Move, skip func(string) bool) Move {
	m.Size, m.Entries, m.Dirs = 0, 0, 0
	if !m.Dir {
		m.Entries = 1
		if info, err := os.Lstat(m.From); err == nil && info.Mode().IsRegular() {
			m.Size = info.Size()
		}
		return m
	}
	_ = filepath.WalkDir(m.From, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if skip != nil && path != m.From && skip(path) {
			return skipEntry(d)
		}
		m.Entries++
		if d.IsDir() {
			m.Dirs++
		} else if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				m.Size += info.Size()
			}
		}
		return nil
	})
	return m
}

// skipEntry leaves an entry out of a walk: a folder with everything in it.
func skipEntry(d fs.DirEntry) error {
	if d.IsDir() {
		return filepath.SkipDir
	}
	return nil
}

// under reports whether path is dir or inside it.
func under(path, dir string) bool {
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}
