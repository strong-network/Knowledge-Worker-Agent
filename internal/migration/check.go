// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodeinstaller"
)

// Problem is a reason the migration can't run now. Any problem stops it before
// anything changes.
type Problem struct {
	Check  string
	Detail string
}

func (p Problem) String() string { return p.Check + ": " + p.Detail }

// Test hooks.
var (
	opencodeProcesses = findOpencodeProcesses
	opencodeVersion   = opencodeinstaller.InstalledVersion
	freeSpace         = statfs
	opencodeSettle    = 15 * time.Second
)

// checkMoves finds moves that can't be carried out as renames.
func (p Plan) checkMoves() []Problem {
	var out []Problem
	for _, m := range p.Moves {
		switch {
		case m.Kind == Uncopy:
			if !sameDevice(m.From, layout.MigrationDir()) {
				out = append(out, Problem{"different filesystems", fmt.Sprintf("%s: %s and %s aren't on one filesystem, so it can't be set aside", m.Name, m.From, layout.MigrationDir())})
			}
		case under(m.To, m.From):
			out = append(out, Problem{"target inside its source", fmt.Sprintf("%s would move into itself: %s", m.Name, m.To)})
		case p.targetTaken(m):
			out = append(out, Problem{"target exists", fmt.Sprintf("%s: %s already exists", m.Name, m.To)})
		case m.Kind == Rename && !sameDevice(m.From, m.To):
			out = append(out, Problem{"different filesystems", fmt.Sprintf("%s: %s and %s aren't on one filesystem, so it can't be renamed", m.Name, m.From, p.Root)})
		}
	}
	return out
}

// targetTaken reports whether m's target exists, now or once the other moves
// have run: a legacy chat's target is inside the moved Chats folder.
func (p Plan) targetTaken(m Move) bool {
	if _, err := os.Lstat(m.To); err == nil {
		return true
	}
	for _, o := range p.Moves {
		if o.Kind != Rename || samePath(o.To, m.To) || !under(m.To, o.To) {
			continue
		}
		rel := strings.TrimPrefix(filepath.Clean(m.To), filepath.Clean(o.To))
		if _, err := os.Lstat(filepath.Clean(o.From) + rel); err == nil {
			return true
		}
	}
	return false
}

// checkOpencode finds opencode processes that could write to its database
// during the move, and an opencode version whose export and import weren't tested.
func checkOpencode(bin string) []Problem {
	var out []Problem
	if pids := settledOpencodeProcesses(); len(pids) > 0 {
		out = append(out, Problem{"opencode is running", fmt.Sprintf("process %s; the migration doesn't start while opencode can write to its database", strings.Join(pids, ", "))})
	}
	if v := opencodeVersion(bin); v != opencodeinstaller.PinnedVersion {
		if v == "" {
			v = "none that runs"
		}
		out = append(out, Problem{"opencode version", fmt.Sprintf("%s is %s; re-pointing chats was tested with %s", bin, v, opencodeinstaller.PinnedVersion)})
	}
	return out
}

// checkIntegrity runs SQLite's quick check on a database that will be copied
// and rewritten.
func checkIntegrity(name, path string) []Problem {
	db, err := openReadOnly(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return []Problem{{"database check", fmt.Sprintf("%s: %v", name, err)}}
	}
	defer db.Close()
	var res string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&res); err != nil {
		return []Problem{{"database check", fmt.Sprintf("%s: %v", name, err)}}
	}
	if res != "ok" {
		return []Problem{{"database check", fmt.Sprintf("%s: %s", name, res)}}
	}
	return nil
}

// settledOpencodeProcesses finds opencode processes, waiting a while for them
// to exit first: a server stopped a moment ago can leave its short commands
// running, and opencode mcp list takes several seconds. Refusing for one would
// only cost the user another restart.
func settledOpencodeProcesses() []string {
	deadline := time.Now().Add(opencodeSettle)
	for {
		pids := opencodeProcesses()
		if len(pids) == 0 || !time.Now().Before(deadline) {
			return pids
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// findOpencodeProcesses lists this user's opencode processes that use the
// database the migration changes, except the ones this process started. One
// whose environment can't be read counts.
func findOpencodeProcesses() []string {
	ours := OpencodeDatabase()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	self := os.Getpid()
	var out []string
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		dir := filepath.Join("/proc", e.Name())
		if info, err := os.Stat(dir); err != nil || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()) {
			continue
		}
		if parentPID(dir) == self {
			continue
		}
		cmd, _ := os.ReadFile(filepath.Join(dir, "cmdline"))
		argv0, _, _ := strings.Cut(string(cmd), "\x00")
		exe, _ := os.Readlink(filepath.Join(dir, "exe"))
		if filepath.Base(argv0) != "opencode" && filepath.Base(exe) != "opencode" {
			continue
		}
		if db := opencodeDatabaseOf(dir); db != "" && !samePath(db, ours) {
			continue
		}
		out = append(out, e.Name())
	}
	return out
}

// opencodeDatabaseOf works out the database of the process at procDir from
// its environment, as OpencodeDatabase does; "" when it can't be read.
func opencodeDatabaseOf(procDir string) string {
	env, err := os.ReadFile(filepath.Join(procDir, "environ"))
	if err != nil {
		return ""
	}
	var home, data string
	for _, kv := range strings.Split(string(env), "\x00") {
		if v, ok := strings.CutPrefix(kv, "HOME="); ok {
			home = v
		} else if v, ok := strings.CutPrefix(kv, "XDG_DATA_HOME="); ok {
			data = strings.TrimSpace(v)
		}
	}
	if data == "" {
		if home == "" {
			return ""
		}
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(data, "opencode", "opencode.db")
}

func parentPID(procDir string) int {
	stat, err := os.ReadFile(filepath.Join(procDir, "stat"))
	if err != nil {
		return 0
	}
	// The command name is in parentheses and may hold spaces; fields follow it.
	_, rest, ok := strings.Cut(string(stat), ") ")
	if !ok {
		return 0
	}
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
}

// sameDevice reports whether a and the nearest existing folder above b are on
// one filesystem, so a can be renamed to b.
func sameDevice(a, b string) bool {
	da, ok1 := device(a)
	db, ok2 := device(nearestExisting(b))
	return ok1 && ok2 && da == db
}

func device(path string) (uint64, bool) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true
}

func nearestExisting(path string) string {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		if _, err := os.Lstat(p); err == nil || p == filepath.Dir(p) {
			return p
		}
	}
}

// statfs returns the bytes and inodes free to this user on path's filesystem.
func statfs(path string) (bytes, inodes uint64, err error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(nearestExisting(path), &s); err != nil {
		return 0, 0, err
	}
	return s.Bavail * uint64(s.Bsize), s.Ffree, nil
}
