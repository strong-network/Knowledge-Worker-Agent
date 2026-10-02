// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Measured on this product's own data: exporting a session takes about 1.1 s and
// importing it 1.4 s.
const perSession = 2500 * time.Millisecond

// headroom is the space left free after the migration, so the workspace keeps
// working.
const headroom = 1 << 30

// Report is what the migration would do and what it needs, as things are now.
type Report struct {
	Plan     Plan
	Stored   StoredPaths
	Sessions OpencodeSessions
	Links    Links
	Mentions Mentions

	NeedBytes, FreeBytes   uint64
	NeedInodes, FreeInodes uint64
	Duration               time.Duration

	Problems []Problem // any of these stops the migration
	Errors   []string  // parts of the report that couldn't be worked out
	Undo     bool      // the report is of undoing the migration
}

// Ready reports whether the migration could run now.
func (r Report) Ready() bool { return len(r.Problems) == 0 && len(r.Errors) == 0 }

// Assess works out the report for our database at ourDB and the opencode
// binary at opencodeBin. It reads, and changes nothing.
func Assess(ourDB, opencodeBin string) Report { return assess(NewPlan(), ourDB, opencodeBin) }

// AssessUndo works out the report for undoing the migration. It reads, and
// changes nothing.
func AssessUndo(ourDB, opencodeBin string) Report {
	r := assess(UndoPlan(), ourDB, opencodeBin)
	r.Undo = true
	return r
}

func assess(plan Plan, ourDB, opencodeBin string) Report {
	r := Report{Plan: plan}
	ocDB := OpencodeDatabase()
	var err error
	if r.Stored, err = ScanStoredPaths(ourDB, r.Plan); err != nil {
		r.Errors = append(r.Errors, "stored paths: "+err.Error())
	}
	if r.Sessions, err = ScanOpencodeSessions(ocDB, r.Plan); err != nil {
		r.Errors = append(r.Errors, "opencode's sessions: "+err.Error())
	}
	r.Links = ScanLinks(r.Plan)
	r.Mentions = ScanMentions(r.Plan)

	r.Problems = append(r.Problems, r.Plan.checkMoves()...)
	if r.Sessions.Repoint > 0 {
		r.Problems = append(r.Problems, checkOpencode(opencodeBin)...)
		r.Problems = append(r.Problems, checkIntegrity("opencode's database", ocDB)...)
	}
	r.Problems = append(r.Problems, checkIntegrity("our database", ourDB)...)

	r.estimate(ourDB, ocDB)
	if free, inodes, err := freeSpace(r.Plan.Root); err != nil {
		r.Errors = append(r.Errors, "free space: "+err.Error())
	} else {
		r.FreeBytes, r.FreeInodes = free, inodes
		if r.NeedBytes > free {
			r.Problems = append(r.Problems, Problem{"disk space", fmt.Sprintf("needs %s, %s is free; free up at least %s", human(r.NeedBytes), human(free), human(r.NeedBytes-free))})
		}
		if r.NeedInodes > inodes {
			r.Problems = append(r.Problems, Problem{"inodes", fmt.Sprintf("needs %s, %s are free", count(r.NeedInodes), count(inodes))})
		}
	}
	return r
}

// estimate works out the space, inodes and time the migration needs: copies of
// both databases, an export of every session it re-points, room for opencode's
// database to grow as they're re-imported, the Obsidian copy, and headroom.
// Renames and the hard-link backup take no space for file contents.
func (r *Report) estimate(ourDB, ocDB string) {
	data := uint64(r.Sessions.Data) * 11 / 10
	if r.Sessions.Repoint > 0 {
		r.NeedBytes += fileSize(ocDB) + fileSize(ocDB+"-wal") + 2*data
	}
	r.NeedBytes += fileSize(ourDB) + fileSize(ourDB+"-wal")
	for _, m := range r.Plan.Moves {
		if m.Kind == Copy {
			r.NeedBytes += uint64(m.Size)
			r.NeedInodes += uint64(m.Entries)
		} else {
			r.NeedInodes += uint64(m.Dirs)
		}
	}
	r.NeedBytes += headroom
	r.NeedInodes += uint64(r.Sessions.Repoint) + 1000
	r.Duration = time.Duration(r.Sessions.Repoint)*perSession + time.Minute
}

// Write prints the report as indented lines for the log.
func (r Report) Write(w io.Writer, mode Mode) {
	p := func(format string, a ...any) { fmt.Fprintf(w, "    "+format+"\n", a...) }
	what := "One-folder migration"
	if r.Undo {
		what = "Undoing the one-folder migration"
	}
	if mode == DryRun {
		fmt.Fprintf(w, "  · %s, dry run: nothing has changed.\n", what)
	} else {
		fmt.Fprintf(w, "  · %s (%s=%s):\n", what, EnvMode, mode)
	}
	p("Root: %s", r.Plan.Root)

	var legacy []Move
	for _, m := range r.Plan.Moves {
		if m.Name == LegacyChatsName {
			legacy = append(legacy, m)
			continue
		}
		if m.Kind == Uncopy {
			p("%s: the copy %s is set aside, since %s is still there", m.Name, tilde(m.From), tilde(m.To))
			continue
		}
		verb := "→"
		if m.Kind == Copy {
			verb = "copied to"
		}
		p("%s: %s %s %s (%s, %s entries)", m.Name, tilde(m.From), verb, tilde(m.To), human(uint64(m.Size)), count(uint64(m.Entries)))
	}
	if len(legacy) > 0 {
		var size, entries int64
		for _, m := range legacy {
			size += m.Size
			entries += int64(m.Entries)
		}
		p("%s: %d chat-* folders in %s → %s (%s, %s entries)", LegacyChatsName, len(legacy), tilde(filepath.Dir(legacy[0].From)), tilde(filepath.Dir(legacy[0].To)), human(uint64(size)), count(uint64(entries)))
	}
	for _, l := range r.Plan.Kept {
		p("Kept where it is, set by %s: %s (%s)", l.Setting, l.Current, l.Name)
	}
	for _, l := range r.Plan.Absent {
		p("Nothing to move yet: %s (%s)", tilde(l.Current), l.Name)
	}
	for _, db := range r.Plan.OtherDatabases {
		p("Another database, not in use, isn't moved: %s", tilde(db))
	}

	p("Stored paths rewritten: %d%s; left as they are: %d", total(r.Stored.Rewritten), fields(r.Stored.Rewritten), r.Stored.Kept)
	p("opencode sessions re-pointed: %d (%d of them subagent sessions); left as they are, their folder already gone: %d",
		r.Sessions.Repoint, r.Sessions.Children, r.Sessions.Missing)
	p("Links rewritten: %d; relative links that would point elsewhere: %d", len(r.Links.Rewrite), len(r.Links.Broken))
	for _, l := range r.Links.Broken {
		p("  would point elsewhere: %s", tilde(l))
	}
	p("Files that mention an old path, not edited: %d, and %d Python environments", len(r.Mentions.Files), len(r.Mentions.Envs))
	for _, e := range r.Mentions.Envs {
		p("  Python environment: %s", tilde(e))
	}
	for _, f := range firstN(r.Mentions.Files, 10) {
		p("  %s", tilde(f))
	}
	if n := len(r.Mentions.Files) - 10; n > 0 {
		p("  and %d more", n)
	}

	p("Disk space: needs %s, %s free; inodes: needs %s, %s free", human(r.NeedBytes), human(r.FreeBytes), count(r.NeedInodes), count(r.FreeInodes))
	p("Time: about %d minutes, while Knowledge Worker Agent is unavailable", int((r.Duration+time.Minute-1)/time.Minute))
	for _, e := range r.Errors {
		p("Couldn't work out: %s", e)
	}
	if r.Ready() {
		p("Ready: no problems found.")
		return
	}
	for _, pr := range r.Problems {
		p("Would stop it: %s", pr)
	}
}

func fileSize(path string) uint64 {
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return uint64(info.Size())
	}
	return 0
}

func total(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func fields(m map[string]int) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %d", k, m[k])
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// tilde shortens the home folder to ~ for reading.
func tilde(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + path[len(home):]
	}
	return path
}

// Bytes formats a size for people, such as "3.5 GB".
func Bytes(b uint64) string { return human(b) }

func human(b uint64) string {
	const unit = 1000
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	v, suffix := float64(b), "kMGTP"
	i := -1
	for v >= unit && i < len(suffix)-1 {
		v /= unit
		i++
	}
	return fmt.Sprintf("%.1f %cB", v, suffix[i])
}

func count(n uint64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
