// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package versions keeps earlier versions of the text files in chats' folders,
// so the file viewer can show one and highlight what changed since the version
// before. A version is taken when an agent turn starts (whatever changed since,
// by whoever), when it ends (the agent's changes: one version per turn), and
// when someone saves or uploads a file. Contents are stored once per sha256,
// compressed, outside the database.
package versions

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

const (
	// Keep is how many versions of a file are kept.
	Keep = 50
	// MaxSize is the largest file that gets versions.
	MaxSize = 1 << 20
	// A folder's walk looks at no more files than this.
	maxFiles = 2000
	// Saves by the same person this close together make one version.
	coalesce = 10 * time.Minute
)

// Who made a version. "" is someone outside the app, or a file that was there
// before its first version.
const (
	Agent = "agent"
	You   = "you"
)

// Guest is who made a version saved by a guest of a shared chat.
func Guest(name string) string { return "guest:" + name }

// The text files that get versions.
var textExts = map[string]bool{
	".md": true, ".markdown": true, ".mdx": true, ".txt": true, ".csv": true, ".tsv": true,
	".json": true, ".jsonl": true, ".yaml": true, ".yml": true, ".toml": true, ".ini": true, ".xml": true,
	".html": true, ".htm": true, ".css": true, ".js": true, ".mjs": true, ".cjs": true, ".ts": true,
	".tsx": true, ".jsx": true, ".vue": true, ".py": true, ".go": true, ".java": true, ".kt": true,
	".c": true, ".h": true, ".cpp": true, ".cs": true, ".rs": true, ".rb": true, ".php": true,
	".sh": true, ".bash": true, ".ps1": true, ".sql": true, ".tex": true, ".rst": true,
}

// Folders a walk doesn't go into, besides hidden ones.
var skipDirs = map[string]bool{"node_modules": true, "__pycache__": true, "venv": true}

var (
	store   string     // where contents are kept; versions are off until Init sets it
	capMu   sync.Mutex // one change to the versions at a time
	turnMu  sync.Mutex
	running = map[string]map[string]string{} // folder -> session -> its running turn
)

// Init keeps contents in dir.
func Init(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	store = dir
	return nil
}

// Versioned reports whether a file named name gets versions.
func Versioned(name string) bool {
	return !strings.HasPrefix(name, ".") && textExts[strings.ToLower(filepath.Ext(name))]
}

// TurnStarted takes a version of whatever changed in folder since the last one,
// and marks a turn of sessionID as running there.
func TurnStarted(sessionID, folder string) {
	folder = cleanDir(folder)
	if folder == "" || store == "" {
		return
	}
	captureFolder(folder, "", "")
	turnMu.Lock()
	defer turnMu.Unlock()
	if running[folder] == nil {
		running[folder] = map[string]string{}
	}
	running[folder][sessionID] = sessionID + "@" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// TurnEnded takes the version the turn made of each file it changed.
func TurnEnded(sessionID, folder string) {
	folder = cleanDir(folder)
	if folder == "" || store == "" {
		return
	}
	turnMu.Lock()
	turn := running[folder][sessionID]
	delete(running[folder], sessionID)
	if len(running[folder]) == 0 {
		delete(running, folder)
	}
	turnMu.Unlock()
	if turn != "" {
		captureFolder(folder, Agent, turn)
	}
}

// runningTurn returns a turn running in a folder that holds path, or "".
func runningTurn(path string) string {
	turnMu.Lock()
	defer turnMu.Unlock()
	for folder, turns := range running {
		if within(path, folder) {
			for _, t := range turns {
				return t
			}
		}
	}
	return ""
}

// Saved takes a version of path, which author has just saved or uploaded.
func Saved(path, author string) {
	if store == "" || !Versioned(filepath.Base(path)) {
		return
	}
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
		capture(path, info, author, "")
	}
}

// Moved follows a file or a folder that has moved from src to dest.
func Moved(src, dest string) {
	if store == "" {
		return
	}
	capMu.Lock()
	defer capMu.Unlock()
	gone, err := db.ForgetFileVersions(dest)
	if err == nil {
		err = db.MoveFileVersions(src, dest)
	}
	if err != nil {
		log.Printf("[versions] moving %s to %s: %v", src, dest, err)
	}
	release(gone)
}

// Forget deletes the versions of path and of everything under it.
func Forget(path string) {
	if store == "" || path == "" {
		return
	}
	capMu.Lock()
	defer capMu.Unlock()
	gone, err := db.ForgetFileVersions(filepath.Clean(path))
	if err != nil {
		log.Printf("[versions] forgetting %s: %v", path, err)
	}
	release(gone)
}

// Sweep forgets the files that are gone, and the contents nothing refers to.
func Sweep() {
	if store == "" {
		return
	}
	paths, err := db.FileVersionPaths()
	if err != nil {
		log.Printf("[versions] sweep: %v", err)
		return
	}
	for _, p := range paths {
		if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			Forget(p)
		}
	}
	capMu.Lock()
	defer capMu.Unlock()
	used, err := db.FileVersionSHAs()
	if err != nil {
		return
	}
	filepath.WalkDir(store, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := filepath.Base(filepath.Dir(p)) + strings.TrimSuffix(d.Name(), ".gz")
		if strings.HasSuffix(d.Name(), ".tmp") || !used[name] {
			os.Remove(p)
		}
		return nil
	})
}

func captureFolder(folder, author, turn string) {
	n := 0
	filepath.WalkDir(folder, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != folder && (strings.HasPrefix(d.Name(), ".") || skipDirs[d.Name()]) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !Versioned(d.Name()) {
			return nil
		}
		if n++; n > maxFiles {
			return fs.SkipAll
		}
		if info, err := d.Info(); err == nil {
			capture(p, info, author, turn)
		}
		return nil
	})
}

// capture takes a version of the file at path if it has changed since its
// latest one. A turn's changes and one person's saves close together update
// one version rather than making many.
func capture(path string, info fs.FileInfo, author, turn string) {
	if info.Size() > MaxSize {
		return
	}
	capMu.Lock()
	defer capMu.Unlock()
	latest, ok, err := db.LatestFileVersion(path)
	if err != nil {
		log.Printf("[versions] %s: %v", path, err)
		return
	}
	modTime := info.ModTime().UnixNano()
	if ok && latest.ModTime == modTime && latest.Size == info.Size() {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > MaxSize || bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	if ok && latest.SHA == sha {
		db.TouchFileVersion(path, latest.V, modTime)
		return
	}
	if err := put(sha, data); err != nil {
		log.Printf("[versions] storing %s: %v", path, err)
		return
	}
	now := time.Now().UnixMilli()
	fv := db.FileVersion{Path: path, V: latest.V + 1, SHA: sha, Size: int64(len(data)), ModTime: modTime, Author: author, Turn: turn, At: now}
	if ok && sameVersion(latest, author, turn, now) {
		fv.V, fv.Author, fv.Turn = latest.V, latest.Author, latest.Turn
		if err := db.UpdateFileVersion(fv); err != nil {
			log.Printf("[versions] %s: %v", path, err)
			return
		}
		release([]string{latest.SHA})
		return
	}
	if err := db.AddFileVersion(fv); err != nil {
		log.Printf("[versions] %s: %v", path, err)
		return
	}
	gone, _ := db.PruneFileVersions(path, Keep)
	release(gone)
}

func sameVersion(latest db.FileVersion, author, turn string, now int64) bool {
	if turn != "" {
		return latest.Turn == turn
	}
	return author != "" && author != Agent && latest.Turn == "" && latest.Author == author && now-latest.At < coalesce.Milliseconds()
}

func blobPath(sha string) string {
	return filepath.Join(store, sha[:2], sha[2:]+".gz")
}

func put(sha string, data []byte) error {
	dest := blobPath(sha)
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), "*.tmp")
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(tmp)
	_, err = zw.Write(data)
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dest)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

func read(sha string) ([]byte, error) {
	f, err := os.Open(blobPath(sha))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(zr, MaxSize+1))
}

// release deletes the contents no version refers to any more. The caller holds capMu.
func release(shas []string) {
	for _, sha := range shas {
		if len(sha) > 2 && !db.FileVersionSHAInUse(sha) {
			os.Remove(blobPath(sha))
		}
	}
}

// Current takes a version of any change to path since its latest version, so
// the newest is what the file holds now. A change while a turn runs in a folder
// holding path is the agent's.
func Current(path string) {
	if store == "" || !Versioned(filepath.Base(path)) {
		return
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	if turn := runningTurn(path); turn != "" {
		capture(path, info, Agent, turn)
	} else {
		capture(path, info, "", "")
	}
}

func cleanDir(dir string) string {
	if dir == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return ""
	}
	return abs
}

func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/")
}
