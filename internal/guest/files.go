// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package guest

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/durable"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/files"
)

// Guest file access: the chat's own folder and nothing above it.
// The folder is opened as an os.Root per request, so every name is resolved
// against an open directory handle: no name escapes it, and a symlink swapped
// in mid-request cannot either. Reads and additive writes only, and no hidden
// path at all — those hold configuration (.opencode, .git, .env), not content.

const (
	maxView   = 2 << 20
	maxSave   = 5 << 20
	maxUpload = 50 << 20
)

var (
	errNoFolder     = errors.New("This chat has no folder to share yet.")
	errFolderIsHome = errors.New("Files can't be shared from this chat: its folder is your home folder or contains it, and so holds your credentials.")
)

// FilesFolder returns the folder guests of this chat would see with files on,
// or why files cannot be shared from it.
func FilesFolder(workdir string) (string, error) {
	root, dir, err := openJail(workdir)
	if err != nil {
		return "", err
	}
	root.Close()
	return dir, nil
}

func openJail(workdir string) (*os.Root, string, error) {
	w := strings.TrimSpace(workdir)
	if w == "" || !filepath.IsAbs(w) {
		return nil, "", errNoFolder
	}
	dir := filepath.Clean(w)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "", errNoFolder
	}
	if holdsHome(root) {
		root.Close()
		return nil, "", errFolderIsHome
	}
	return root, dir, nil
}

// holdsHome reports whether the opened folder is the home folder or one of its
// ancestors. It compares the open handle, not the path it was opened by, so a
// folder swapped for a link after the check cannot pass it.
func holdsHome(root *os.Root) bool {
	here, err := root.Stat(".")
	if err != nil {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return true
	}
	homes := []string{filepath.Clean(home)}
	if real, err := filepath.EvalSymlinks(home); err == nil {
		homes = append(homes, real)
	}
	for _, h := range homes {
		for d := h; ; d = filepath.Dir(d) {
			if fi, err := os.Stat(d); err == nil && os.SameFile(here, fi) {
				return true
			}
			if d == filepath.Dir(d) {
				break
			}
		}
	}
	return false
}

// guestPath turns a path from a guest into a name inside the chat's folder.
// Guests send folder-relative paths only; an absolute one is a bug or an
// attack, never a legitimate call.
func guestPath(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" || p == "." {
		return ".", true
	}
	if !filepath.IsLocal(p) {
		return "", false
	}
	clean := filepath.Clean(p)
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if hidden(part) {
			return "", false
		}
	}
	return clean, true
}

func hidden(name string) bool { return strings.HasPrefix(name, ".") }

// wire is how a name inside the folder travels to the guest page: slashes, and
// "" for the folder itself.
func wire(name string) string {
	if name == "." {
		return ""
	}
	return filepath.ToSlash(name)
}

// jail resolves the chat and the folder for a guest file request, answering the
// request itself when it cannot proceed.
func (s *server) jail(w http.ResponseWriter, r *http.Request, write bool) (*os.Root, string, identity, bool) {
	if write && !s.sameOrigin(w, r) {
		return nil, "", identity{}, false
	}
	sid := r.PathValue("id")
	if !shared(w, sid) {
		return nil, "", identity{}, false
	}
	id, ok := mustIdentify(w, r)
	if !ok {
		return nil, "", identity{}, false
	}
	if !db.SessionShareOptions(sid).AllowFiles {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Files aren't shared in this chat."})
		return nil, "", identity{}, false
	}
	cfg, _ := db.GetSessionConfig(sid)
	root, dir, err := openJail(cfg.Workdir)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Files aren't available in this chat."})
		return nil, "", identity{}, false
	}
	return root, dir, id, true
}

func badPath(w http.ResponseWriter) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": "That path is not in this chat's folder."})
}

type dirEntry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	ModTime time.Time `json:"mod_time"` // newest change at or under the folder
}

type fileEntry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

func (s *server) filesBrowse(w http.ResponseWriter, r *http.Request) {
	root, _, _, ok := s.jail(w, r, false)
	if !ok {
		return
	}
	defer root.Close()
	rel, ok := guestPath(r.URL.Query().Get("path"))
	if !ok {
		badPath(w)
		return
	}
	f, err := root.Open(rel)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	dirs, list := []dirEntry{}, []fileEntry{}
	// Folders are dated through the root too, so the walk stays in the folder.
	fsys := root.FS()
	budget := files.TreeBudget
	for _, e := range entries {
		if hidden(e.Name()) {
			continue
		}
		name := filepath.Join(rel, e.Name())
		// Stat through the root: a link that leads out of the folder fails
		// here and is not listed.
		info, err := root.Stat(name)
		if err != nil {
			continue
		}
		switch {
		case info.IsDir():
			mod := files.TreeModTime(fsys, filepath.ToSlash(name), info.ModTime(), &budget)
			dirs = append(dirs, dirEntry{Name: e.Name(), Path: wire(name), ModTime: mod})
		case info.Mode().IsRegular():
			list = append(list, fileEntry{Name: e.Name(), Path: wire(name), Size: info.Size(), ModTime: info.ModTime()})
		}
	}
	parent := ""
	if rel != "." {
		parent = wire(filepath.Dir(rel))
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": wire(rel), "parent": parent, "dirs": dirs, "files": list})
}

// regular opens a path for reading and reports its info, refusing folders.
func regular(w http.ResponseWriter, r *http.Request, root *os.Root) (string, fs.FileInfo, bool) {
	rel, ok := guestPath(r.URL.Query().Get("path"))
	if !ok || rel == "." {
		badPath(w)
		return "", nil, false
	}
	info, err := root.Stat(rel)
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "File not found", http.StatusNotFound)
		return "", nil, false
	}
	return rel, info, true
}

func (s *server) filesView(w http.ResponseWriter, r *http.Request) {
	root, _, _, ok := s.jail(w, r, false)
	if !ok {
		return
	}
	defer root.Close()
	rel, info, ok := regular(w, r, root)
	if !ok {
		return
	}
	if info.Size() > maxView {
		http.Error(w, "File too large to view (>2MB)", http.StatusBadRequest)
		return
	}
	data, err := root.ReadFile(rel)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, files.ViewResponse(wire(rel), info.Size(), data))
}

func (s *server) filesRaw(w http.ResponseWriter, r *http.Request) {
	root, _, _, ok := s.jail(w, r, false)
	if !ok {
		return
	}
	defer root.Close()
	rel, info, ok := regular(w, r, root)
	if !ok {
		return
	}
	f, err := root.Open(rel)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	name := filepath.Base(rel)
	h := w.Header()
	h.Del("Content-Security-Policy") // secureHeaders' frame-ancestors 'none'; replaced below
	files.SetInlineHeaders(h, name)
	h.Set("Content-Disposition", disposition("inline", name))
	// The guest page previews in a frame of its own; every other guest
	// response forbids framing. Anything but a PDF is sandboxed even when
	// opened on its own, since this origin holds the guest's cookie: a script
	// in an uploaded page must not be able to act as them. Chrome's PDF viewer
	// refuses a sandboxed response, and runs PDF scripts in its own sandbox.
	h.Set("X-Frame-Options", "SAMEORIGIN")
	csp := h.Get("Content-Security-Policy")
	if csp == "" && strings.ToLower(filepath.Ext(name)) != ".pdf" {
		csp = "default-src 'none'; img-src data: blob:; style-src 'unsafe-inline'; sandbox"
	}
	if csp == "" {
		csp = "frame-ancestors 'self'"
	} else {
		csp += "; frame-ancestors 'self'"
	}
	h.Set("Content-Security-Policy", csp)
	files.Revalidate(h)
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func (s *server) filesDownload(w http.ResponseWriter, r *http.Request) {
	root, _, _, ok := s.jail(w, r, false)
	if !ok {
		return
	}
	defer root.Close()
	rel, ok := guestPath(r.URL.Query().Get("path"))
	if !ok {
		badPath(w)
		return
	}
	info, err := root.Stat(rel)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	if info.IsDir() {
		zipFolder(w, root, rel)
		return
	}
	if !info.Mode().IsRegular() {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	f, err := root.Open(rel)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	files.Revalidate(w.Header())
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", disposition("attachment", filepath.Base(rel)))
	http.ServeContent(w, r, filepath.Base(rel), info.ModTime(), f)
}

func disposition(kind, name string) string {
	if v := mime.FormatMediaType(kind, map[string]string{"filename": name}); v != "" {
		return v
	}
	return kind
}

// zipFolder streams a folder through the root's own fs.FS, so the archive can
// only hold what the folder holds. Links and hidden entries are left out.
func zipFolder(w http.ResponseWriter, root *os.Root, rel string) {
	top := filepath.Base(rel)
	if rel == "." {
		top = "files"
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", disposition("attachment", top+".zip"))
	zw := zip.NewWriter(w)
	defer zw.Close()
	fsys := root.FS()
	start := filepath.ToSlash(rel)
	fs.WalkDir(fsys, start, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == start {
			return nil
		}
		if hidden(d.Name()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		inner := p
		if start != "." {
			inner = strings.TrimPrefix(p, start+"/")
		}
		name := path.Join(top, inner)
		if d.IsDir() {
			_, err := zw.Create(name + "/")
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return nil
		}
		hdr.Name, hdr.Method = name, zip.Deflate
		out, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		in, err := fsys.Open(p)
		if err != nil {
			return nil
		}
		defer in.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

// reserved refuses a name the durable-storage layout keeps for itself at the
// folder's top, as the owner's routes do.
func reserved(w http.ResponseWriter, dir, rel string) bool {
	if name, blocked := durable.ReservedRootCollision(filepath.Join(dir, rel)); blocked {
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("%q is a reserved folder name here. Choose a different name.", name)})
		return true
	}
	return false
}

func (s *server) filesSave(w http.ResponseWriter, r *http.Request) {
	root, dir, id, ok := s.jail(w, r, true)
	if !ok {
		return
	}
	defer root.Close()
	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSave+4096)).Decode(&body) != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	rel, ok := guestPath(body.Path)
	if !ok || rel == "." {
		badPath(w)
		return
	}
	if reserved(w, dir, rel) {
		return
	}
	if info, err := root.Stat(rel); err == nil && !info.Mode().IsRegular() {
		badPath(w)
		return
	}
	if info, err := root.Stat(filepath.Dir(rel)); err != nil || !info.IsDir() {
		http.Error(w, "Folder not found", http.StatusNotFound)
		return
	}
	if err := root.WriteFile(rel, []byte(body.Content), 0o644); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "The file could not be saved."})
		return
	}
	log.Printf("[guest] session=%s file saved by guest %s", r.PathValue("id"), id.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": wire(rel), "size": len(body.Content)})
}

func (s *server) filesDirectory(w http.ResponseWriter, r *http.Request) {
	root, dir, id, ok := s.jail(w, r, true)
	if !ok {
		return
	}
	defer root.Close()
	var body struct {
		Path string `json:"path"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	rel, ok := guestPath(body.Path)
	if !ok || rel == "." {
		badPath(w)
		return
	}
	if reserved(w, dir, rel) {
		return
	}
	if err := root.Mkdir(rel, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "Something with that name already exists."})
			return
		}
		http.Error(w, "Folder not found", http.StatusNotFound)
		return
	}
	log.Printf("[guest] session=%s folder created by guest %s", r.PathValue("id"), id.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": wire(rel), "name": filepath.Base(rel)})
}

// filesUpload never overwrites: each file is created exclusively, under a
// numbered name when the one it came with is taken.
func (s *server) filesUpload(w http.ResponseWriter, r *http.Request) {
	root, dir, id, ok := s.jail(w, r, true)
	if !ok {
		return
	}
	defer root.Close()
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "The upload is too large or malformed."})
		return
	}
	defer r.MultipartForm.RemoveAll()
	target, ok := guestPath(r.FormValue("target_dir"))
	if !ok {
		badPath(w)
		return
	}
	if _, err := root.Stat(target); err != nil {
		if reserved(w, dir, target) {
			return
		}
		if err := root.MkdirAll(target, 0o755); err != nil {
			http.Error(w, "Folder not found", http.StatusNotFound)
			return
		}
	}
	uploaded := []fileEntry{}
	for i, fh := range r.MultipartForm.File["files"] {
		name := filepath.Base(fh.Filename)
		if name == "" || name == "." || name == string(filepath.Separator) || hidden(name) {
			name = fmt.Sprintf("upload_%d", i)
		}
		if target == "." && durable.ReservedNames[name] {
			name = "upload_" + name
		}
		out, rel, err := createFree(root, target, name)
		if err != nil {
			continue
		}
		src, err := fh.Open()
		if err != nil {
			out.Close()
			continue
		}
		n, _ := io.Copy(out, src)
		src.Close()
		out.Close()
		uploaded = append(uploaded, fileEntry{Name: filepath.Base(rel), Path: wire(rel), Size: n})
	}
	log.Printf("[guest] session=%s %d file(s) uploaded by guest %s", r.PathValue("id"), len(uploaded), id.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "uploaded": uploaded})
}

func createFree(root *os.Root, dir, name string) (*os.File, string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 0; i < 1000; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s_%d%s", stem, i, ext)
		}
		rel := filepath.Join(dir, candidate)
		f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return f, rel, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", fs.ErrExist
}
