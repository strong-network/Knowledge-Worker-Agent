// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// Options are what Run needs from the server's configuration.
type Options struct {
	OurDB       string // our database, where it is now
	OpencodeBin string
	Host        string
	Port        int
	PortHeld    bool // the caller holds the port, for the status page
	Release     string
	Out         io.Writer
	Progress    func(Progress)
	Undo        bool // move everything back instead
}

// Progress is where a run is, for the status page.
type Progress struct {
	Phase       string
	Done, Total int           // sessions, while exporting or re-pointing them
	Left        time.Duration // the whole run's estimated time left; 0 when unknown
}

// Refused is Run's error when something stops it before anything changes.
type Refused struct {
	Reasons []string
	Report  *Report // when the checks ran
}

func (e *Refused) Error() string { return "nothing changed: " + strings.Join(e.Reasons, "; ") }

func refused(reasons ...string) error { return &Refused{Reasons: reasons} }

// stepHook runs after each step; tests use it to stop a run partway.
var stepHook = func(step string) error { return nil }

// Run carries out the migration, or with o.Undo, undoes it. Every check runs
// first, and any problem stops it with nothing changed. Then each step runs in
// order, with each change journaled before it's made; any failure rolls back
// everything done so far.
func Run(o Options) (err error) {
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	mode, what, assessFn := On, "One-folder migration", Assess
	if o.Undo {
		mode, what, assessFn = Undo, "Undoing the one-folder migration", AssessUndo
	}
	say := func(format string, a ...any) { fmt.Fprintf(out, "  · "+what+": "+format+"\n", a...) }
	report := func(p Progress) {
		if o.Progress != nil {
			o.Progress(p)
		}
	}
	if layout.Migrated() != o.Undo {
		if o.Undo {
			say("nothing to undo; the migration hasn't run")
		} else {
			say("already done")
		}
		return nil
	}

	// Checks. Nothing has changed if any fails.
	report(Progress{Phase: "Checking your files"})
	if exists(o.OurDB) {
		lock, lockErr := lockDatabase(o.OurDB)
		if lockErr != nil {
			return refused(lockErr.Error())
		}
		defer lock.Close() // last, once every connection to the database is closed
	}
	if !o.PortHeld {
		if err := portFree(o.Host, o.Port); err != nil {
			return refused(err.Error())
		}
	}
	r := assessFn(o.OurDB, o.OpencodeBin)
	if !r.Ready() {
		r.Write(out, mode)
		reasons := append([]string{}, r.Errors...)
		for _, p := range r.Problems {
			reasons = append(reasons, p.String())
		}
		return &Refused{Reasons: reasons, Report: &r}
	}
	ocDB := OpencodeDatabase()
	sessions, missing, err := sessionsToRepoint(ocDB, r.Plan)
	if err != nil {
		return err
	}
	if len(sessions) != r.Sessions.Repoint {
		return refused(fmt.Sprintf("%d sessions to re-point, but the report counted %d", len(sessions), r.Sessions.Repoint))
	}
	if len(sessions) > 0 {
		if p := checkIntegrityFull("opencode's database", ocDB); len(p) > 0 {
			return refused(p[0].String())
		}
	}
	if p := checkIntegrityFull("our database", o.OurDB); len(p) > 0 {
		return refused(p[0].String())
	}
	rewrites, err := planRewrites(o.OurDB, r.Plan)
	if err != nil {
		return err
	}
	if n, want := countPaths(rewrites), total(r.Stored.Rewritten); n != want {
		return refused(fmt.Sprintf("%d stored paths to rewrite, but the report counted %d", n, want))
	}

	// Time left: each session is exported, then imported, at a pace measured as
	// the run goes; about a minute besides.
	ops, opsDone, opsStart := 2*len(sessions), 0, time.Time{}
	left := func() time.Duration {
		per := perSession / 2
		if opsDone > 0 {
			per = time.Since(opsStart) / time.Duration(opsDone)
		}
		return per*time.Duration(ops-opsDone) + time.Minute
	}

	dir, err := newRunDir()
	if err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "plan.json"), r.Plan.Moves); err != nil {
		return err
	}
	j, err := createJournal(dir)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			say("%v; setting everything back", err)
			report(Progress{Phase: "Setting everything back"})
			if rbErr := rollback(dir, j, "failed: "+err.Error()); rbErr != nil {
				err = fmt.Errorf("%w; setting it back failed too: %v. The journal is in %s", err, rbErr, dir)
			} else {
				err = fmt.Errorf("%w; everything was set back", err)
			}
		}
		j.close()
	}()
	if err = j.add(Entry{Op: opBegin, Path: r.Plan.Root, Detail: o.Release, Mode: mode}); err != nil {
		return err
	}
	say("started; journal, backups and report in %s", dir)
	report(Progress{Phase: "Backing up", Left: left()})

	manifest, err := recordManifest(r.Plan, filepath.Join(dir, "manifest.jsonl"))
	if err != nil {
		return err
	}
	if err = stepHook("manifest"); err != nil {
		return err
	}

	ourCounts := map[string]int64{}
	if exists(o.OurDB) {
		if ourCounts, err = copyDatabase(o.OurDB, filepath.Join(dir, "database.db")); err != nil {
			return err
		}
	}
	if err = stepHook("copy database"); err != nil {
		return err
	}

	var ocCounts map[string]int64
	cfgHome := filepath.Join(dir, "opencode-config")
	if len(sessions) > 0 {
		if err = emptyOpencodeConfig(cfgHome); err != nil {
			return err
		}
		exports := filepath.Join(dir, "opencode")
		if err = os.MkdirAll(exports, 0o700); err != nil {
			return err
		}
		start := time.Now()
		opsStart = start
		for i := range sessions {
			report(Progress{Phase: "Saving chat history", Done: i, Total: len(sessions), Left: left()})
			if err = exportSession(o.OpencodeBin, cfgHome, exports, &sessions[i]); err != nil {
				return err
			}
			opsDone++
			progress(say, "exported", i+1, len(sessions), start)
		}
		report(Progress{Phase: "Backing up", Left: left()})
		if err = stepHook("export"); err != nil {
			return err
		}
		copyPath := filepath.Join(dir, "opencode.db")
		if ocCounts, err = copyDatabase(ocDB, copyPath); err != nil {
			return err
		}
		if err = j.add(Entry{Op: opCopyDB, Path: copyPath, To: ocDB}); err != nil {
			return err
		}
		if err = stepHook("copy opencode database"); err != nil {
			return err
		}
	}

	if err = hardLinkBackup(r.Plan, filepath.Join(dir, "backup")); err != nil {
		return err
	}
	if err = copySmallFiles(filepath.Join(dir, "files")); err != nil {
		return err
	}
	if err = stepHook("backup"); err != nil {
		return err
	}

	report(Progress{Phase: "Moving files", Left: left()})
	newDB := o.OurDB
	for i, m := range r.Plan.Moves {
		if err = moveOne(m, j, filepath.Join(dir, "removed", fmt.Sprintf("%02d-%s", i, filepath.Base(m.From)))); err != nil {
			return err
		}
		if m.Name == layout.NameDatabase {
			newDB = m.To
		}
		if err = stepHook(fmt.Sprintf("move %d", i+1)); err != nil {
			return err
		}
	}
	say("moved %d places into %s", len(r.Plan.Moves), r.Plan.Root)

	if len(rewrites) > 0 {
		if err = applyRewrites(newDB, rewrites, j); err != nil {
			return err
		}
	}
	if err = stepHook("rewrite"); err != nil {
		return err
	}

	if len(sessions) > 0 {
		if err = j.add(Entry{Op: opRepoint}); err != nil {
			return err
		}
		start := time.Now()
		for i, s := range sessions {
			report(Progress{Phase: "Updating chat history", Done: i, Total: len(sessions), Left: left()})
			if err = importSession(o.OpencodeBin, cfgHome, s); err != nil {
				return err
			}
			opsDone++
			progress(say, "re-pointed", i+1, len(sessions), start)
			if err = stepHook(fmt.Sprintf("repoint %d", i+1)); err != nil {
				return err
			}
		}
	}

	relinked := map[string]bool{}
	for _, l := range r.Links.Rewrite {
		var newLink string
		if newLink, err = relink(r.Plan, l, j); err != nil {
			return err
		}
		relinked[newLink] = true
	}
	if err = stepHook("relink"); err != nil {
		return err
	}

	report(Progress{Phase: "Checking every file", Left: time.Minute})
	problems := verifyManifest(r.Plan, manifest, true, relinked, map[string]bool{newDB: true})
	for _, rw := range rewrites {
		for _, pc := range rw.Paths {
			if pc.Existed && !exists(pc.New) {
				problems = append(problems, fmt.Sprintf("stored path %s doesn't exist", pc.New))
			}
		}
	}
	for _, m := range r.Plan.Moves {
		if m.Kind == Copy {
			if got := measure(Move{From: m.To, Dir: m.Dir}); got.Entries != m.Entries || got.Size != m.Size {
				problems = append(problems, fmt.Sprintf("the copy %s differs from %s", m.To, m.From))
			}
		}
	}
	if exists(newDB) {
		after, cErr := rowCounts(newDB)
		if cErr == nil {
			cErr = sameCounts(ourCounts, after, nil)
		}
		if cErr != nil {
			problems = append(problems, "our database: "+cErr.Error())
		}
	}
	if len(sessions) > 0 {
		problems = append(problems, verifyRepointed(ocDB, r.Plan, sessions, missing)...)
		after, cErr := rowCounts(ocDB)
		if cErr == nil {
			// opencode's import adds a git project's new folder to the project's folders.
			cErr = sameCounts(ocCounts, after, map[string]int64{"project_directory": int64(newFolders(sessions))})
		}
		if cErr != nil {
			problems = append(problems, "opencode's database: "+cErr.Error())
		}
	}
	if len(problems) > 0 {
		err = fmt.Errorf("verification failed: %s", strings.Join(firstN(problems, 5), "; "))
		return err
	}
	if err = stepHook("verify"); err != nil {
		return err
	}

	if err = writeReport(filepath.Join(dir, "report.txt"), r, mode); err != nil {
		return err
	}
	if err = complete(dir, r.Plan, o.Undo, j); err != nil {
		return err
	}
	if err = stepHook("complete"); err != nil {
		return err
	}
	if err = j.add(Entry{Op: opComplete}); err != nil {
		return err
	}
	if o.Undo {
		removeEmptyParents(r.Plan)
	}
	report(Progress{Phase: "Done"})
	say("done: %d places moved, %d stored paths rewritten, %d sessions re-pointed, %d links rewritten",
		len(r.Plan.Moves), countPaths(rewrites), len(sessions), len(relinked))
	return nil
}

func newFolders(sessions []session) int {
	seen := map[string]bool{}
	for _, s := range sessions {
		seen[s.NewDir] = true
	}
	return len(seen)
}

// newRunDir creates a folder of the run's own, named by its start time so runs
// sort in order, with a suffix when another started in the same second.
func newRunDir() (string, error) {
	if err := os.MkdirAll(layout.MigrationDir(), 0o700); err != nil {
		return "", err
	}
	base := filepath.Join(layout.MigrationDir(), time.Now().UTC().Format("20060102T150405Z"))
	for i := 0; i < 100; i++ {
		dir := base
		if i > 0 {
			dir = fmt.Sprintf("%s-%02d", base, i)
		}
		if err := os.Mkdir(dir, 0o700); err == nil {
			return dir, nil
		} else if !os.IsExist(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("no free run folder in %s", layout.MigrationDir())
}

// moveOne carries out one move, creating the folders above its target first.
// A copy being undone goes to aside, in the run's folder.
func moveOne(m Move, j *journal, aside string) error {
	if m.Kind == Uncopy {
		if err := os.MkdirAll(filepath.Dir(aside), 0o700); err != nil {
			return err
		}
		if err := j.add(Entry{Op: opRename, From: m.From, To: aside}); err != nil {
			return err
		}
		return os.Rename(m.From, aside)
	}
	if err := makeParents(filepath.Dir(m.To), j); err != nil {
		return err
	}
	if m.Kind == Copy {
		if err := j.add(Entry{Op: opCopy, From: m.From, To: m.To}); err != nil {
			return err
		}
		return copyTree(m.From, m.To)
	}
	detail := ""
	if m.Name == layout.NameDatabase {
		detail = "database"
		if err := checkpoint(m.From); err != nil {
			return err
		}
	}
	if err := j.add(Entry{Op: opRename, From: m.From, To: m.To, Detail: detail}); err != nil {
		return err
	}
	return os.Rename(m.From, m.To)
}

func makeParents(dir string, j *journal) error {
	var missing []string
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil || d == filepath.Dir(d) {
			break
		}
		missing = append(missing, d)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := j.add(Entry{Op: opMkdir, Path: missing[i]}); err != nil {
			return err
		}
		if err := os.Mkdir(missing[i], 0o755); err != nil {
			return err
		}
	}
	return nil
}

// checkpoint writes the database's write-ahead log into its file and closes
// it, so it's one file, renamed whole. Renaming its three files one by one
// could lose the latest writes if the process stopped in between.
func checkpoint(path string) error {
	db, err := openWritable(path)
	if err != nil {
		return err
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		db.Close()
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	for _, s := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(path + s); err == nil {
			return fmt.Errorf("%s%s is still there after a checkpoint", path, s)
		}
	}
	return nil
}

// relink points a link inside a moved folder at the new place of its target.
func relink(p Plan, oldLink string, j *journal) (string, error) {
	link, _ := p.Remap(oldLink)
	target, err := os.Readlink(link)
	if err != nil {
		return "", err
	}
	newTarget, moved := p.Remap(target)
	if !moved {
		return link, nil
	}
	if err := j.add(Entry{Op: opRelink, Path: link, Old: target, New: newTarget}); err != nil {
		return "", err
	}
	if err := os.Remove(link); err != nil {
		return "", err
	}
	return link, os.Symlink(newTarget, link)
}

// copyTree copies a file, folder or link, keeping links as links.
func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(cur string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, cur)
		dst := filepath.Join(to, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.Mkdir(dst, info.Mode().Perm())
		case d.Type().IsRegular():
			return copyFile(cur, dst, info.Mode().Perm())
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(cur)
			if err != nil {
				return err
			}
			return os.Symlink(target, dst)
		}
		return nil
	})
}

func progress(say func(string, ...any), verb string, done, total int, start time.Time) {
	if done != total && done%25 != 0 {
		return
	}
	left := time.Duration(0)
	if done > 0 {
		left = time.Since(start) / time.Duration(done) * time.Duration(total-done)
	}
	say("%s %d of %d sessions; about %d minutes left for this part", verb, done, total, int(left.Minutes()+0.5))
}

func writeReport(path string, r Report, mode Mode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	r.Write(f, mode)
	fmt.Fprintln(f, "\n    Every file that mentions an old path:")
	for _, m := range r.Mentions.Files {
		fmt.Fprintln(f, "      "+m)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// complete records what moved, then that the migration has completed, or
// when undoing, that it no longer has. From then on, every location is in the
// root, or back at its old place. The previous record of what moved, and when
// undoing the completion record, are set aside in the run's folder, so a
// rollback can put them back.
func complete(dir string, p Plan, undo bool, j *journal) error {
	var moved []layout.Rename
	for _, m := range p.Moves {
		if m.Kind == Rename {
			moved = append(moved, layout.Rename{From: m.From, To: m.To})
		}
	}
	if err := setAside(layout.MovedFile(), filepath.Join(dir, "moved.json"), j); err != nil {
		return err
	}
	if err := j.add(Entry{Op: opCreate, Path: layout.MovedFile()}); err != nil {
		return err
	}
	if err := writeJSON(layout.MovedFile(), moved); err != nil {
		return err
	}
	if undo {
		if err := setAside(layout.CompletionFile(), filepath.Join(dir, "completed"), j); err != nil {
			return err
		}
		return syncDir(layout.MigrationDir())
	}
	if err := j.add(Entry{Op: opCreate, Path: layout.CompletionFile()}); err != nil {
		return err
	}
	tmp := layout.CompletionFile() + ".tmp"
	if err := os.WriteFile(tmp, []byte(filepath.Base(dir)+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, layout.CompletionFile()); err != nil {
		return err
	}
	return syncDir(layout.MigrationDir())
}

// removeEmptyParents removes the folders in the root that undoing left empty,
// such as those that held the Obsidian copy. Nothing outside the root.
func removeEmptyParents(p Plan) {
	for _, m := range p.Moves {
		for d := filepath.Dir(m.From); under(d, p.Root) && !samePath(d, p.Root); d = filepath.Dir(d) {
			if os.Remove(d) != nil {
				break
			}
		}
	}
}

// setAside moves the file at path to to, if it's there.
func setAside(path, to string, j *journal) error {
	if !exists(path) {
		return nil
	}
	if err := j.add(Entry{Op: opRename, From: path, To: to}); err != nil {
		return err
	}
	return os.Rename(path, to)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
