// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package files

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/durable"
)

// reservedNameMessage is the plain-language error for a reserved-name
// collision at a share root. inputs/, working/ and .system/ are reserved so a
// deliverable cannot shadow them.
func reservedNameMessage(name string) string {
	return fmt.Sprintf("%q is a reserved folder name at the workspace root and cannot be used for a file or folder here. Choose a different name, or create it inside a subfolder.", name)
}

func HandleBrowse(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = config.Workspace
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = config.Workspace
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		abs = config.Workspace
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		http.Error(w, "Permission denied", http.StatusForbidden)
		return
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

	showHidden := true
	if v := r.URL.Query().Get("hidden"); v == "0" || v == "false" || v == "no" {
		showHidden = false
	}

	fsys := os.DirFS(abs)
	budget := TreeBudget
	var dirs []dirEntry
	var files []fileEntry
	for _, e := range entries {
		if !showHidden && strings.HasPrefix(e.Name(), ".") {
			continue
		}
		full := filepath.Join(abs, e.Name())
		var mod time.Time
		size := int64(0)
		if fi, err := e.Info(); err == nil {
			mod, size = fi.ModTime(), fi.Size()
		}
		if e.IsDir() {
			dirs = append(dirs, dirEntry{Name: e.Name(), Path: full, ModTime: TreeModTime(fsys, e.Name(), mod, &budget)})
		} else {
			files = append(files, fileEntry{Name: e.Name(), Path: full, Size: size, ModTime: mod})
		}
	}
	if dirs == nil {
		dirs = []dirEntry{}
	}
	if files == nil {
		files = []fileEntry{}
	}

	parent := filepath.Dir(abs)
	if parent == abs {
		parent = ""
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"path":   abs,
		"parent": parent,
		"dirs":   dirs,
		"files":  files,
	})
}

func HandleFileView(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	abs, _ := filepath.Abs(path)
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	if info.Size() > 2*1024*1024 {
		http.Error(w, "File too large to view (>2MB)", http.StatusBadRequest)
		return
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		http.Error(w, "Permission denied", http.StatusForbidden)
		return
	}
	writeJSON(w, http.StatusOK, ViewResponse(abs, info.Size(), data))
}

// ViewResponse describes a file already read, as the view routes answer: an
// image or a binary as base64, text as it is.
func ViewResponse(path string, size int64, data []byte) map[string]any {
	resp := map[string]any{"path": path, "name": filepath.Base(path), "size": size}
	mime := mimeByExt(filepath.Ext(path))
	switch {
	case strings.HasPrefix(mime, "image/"):
		resp["type"], resp["mime"], resp["content"] = "image", mime, base64.StdEncoding.EncodeToString(data)
	case looksLikeText(data):
		resp["type"], resp["content"] = "text", string(data)
	default:
		resp["type"], resp["content"] = "binary", base64.StdEncoding.EncodeToString(data)
	}
	return resp
}

// looksLikeText is a heuristic: no NUL byte in the first 512 bytes.
func looksLikeText(data []byte) bool {
	return !bytes.Contains(data[:min(len(data), 512)], []byte{0})
}

func HandleFileSave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}
	r.Body.Close()

	if body.Path == "" {
		http.Error(w, "path and content are required", http.StatusBadRequest)
		return
	}

	abs, _ := filepath.Abs(body.Path)
	if name, blocked := durable.ReservedRootCollision(abs); blocked {
		http.Error(w, reservedNameMessage(name), http.StatusConflict)
		return
	}
	os.MkdirAll(filepath.Dir(abs), 0o755)
	if err := os.WriteFile(abs, []byte(body.Content), 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	info, _ := os.Stat(abs)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": abs, "size": info.Size()})
}

func HandleCreateDirectory(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path   string `json:"path"`
		Unique bool   `json:"unique"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}
	r.Body.Close()

	if strings.TrimSpace(body.Path) == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}

	abs, err := filepath.Abs(body.Path)
	if err != nil {
		http.Error(w, "Failed to resolve directory", http.StatusBadRequest)
		return
	}

	if name, blocked := durable.ReservedRootCollision(abs); blocked {
		http.Error(w, reservedNameMessage(name), http.StatusConflict)
		return
	}

	if body.Unique {
		final, err := createUniqueDir(abs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":   true,
			"path": final,
			"name": filepath.Base(final),
		})
		return
	}

	if err := os.MkdirAll(abs, 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		http.Error(w, "Failed to create directory", http.StatusBadRequest)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"path": abs,
		"name": filepath.Base(abs),
	})
}

// uniqueSuffixWords are appended to a desired folder name when it already
// exists. Kept short and pronounceable so the resulting name stays readable.
var uniqueSuffixWords = []string{
	"apple", "breeze", "cedar", "delta", "ember", "falcon", "glow", "harbor",
	"iris", "jade", "koi", "lark", "mint", "nova", "onyx", "pine",
	"quartz", "reef", "sage", "tide", "umbra", "vega", "willow", "xeno",
	"yarrow", "zephyr",
}

// createUniqueDir attempts to create `abs`. If it already exists, it appends
// `-<word>` (and then `-<word>-<n>`) until a free name is found, using
// os.Mkdir for atomic create-if-absent semantics. The parent directory is
// created with MkdirAll if missing.
func createUniqueDir(abs string) (string, error) {
	parent := filepath.Dir(abs)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	base := filepath.Base(abs)

	// First try the requested name.
	if err := os.Mkdir(abs, 0o755); err == nil {
		return abs, nil
	} else if !os.IsExist(err) {
		return "", err
	}

	// Shuffle suffix words so concurrent callers don't all pick the same one.
	words := make([]string, len(uniqueSuffixWords))
	copy(words, uniqueSuffixWords)
	rand.Shuffle(len(words), func(i, j int) { words[i], words[j] = words[j], words[i] })

	for _, w := range words {
		candidate := filepath.Join(parent, base+"-"+w)
		if err := os.Mkdir(candidate, 0o755); err == nil {
			return candidate, nil
		} else if !os.IsExist(err) {
			return "", err
		}
	}

	// Fallback: timestamp + nanos to virtually guarantee uniqueness.
	for i := 0; i < 50; i++ {
		candidate := filepath.Join(parent, fmt.Sprintf("%s-%d", base, time.Now().UnixNano()))
		if err := os.Mkdir(candidate, 0o755); err == nil {
			return candidate, nil
		} else if !os.IsExist(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("could not find a unique name for %q", abs)
}

func HandleFileUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "Failed to parse multipart", http.StatusBadRequest)
		return
	}

	targetDir := strings.TrimSpace(r.FormValue("target_dir"))
	if targetDir == "" {
		targetDir = filepath.Join(config.Workspace, "upload")
	}
	targetDir, err := filepath.Abs(targetDir)
	if err != nil {
		http.Error(w, "Failed to resolve upload directory", http.StatusInternalServerError)
		return
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		http.Error(w, "Failed to prepare upload directory", http.StatusInternalServerError)
		return
	}

	type uploadedFile struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	var uploaded []uploadedFile

	formFiles := r.MultipartForm.File["files"]
	for _, fh := range formFiles {
		filename := filepath.Base(fh.Filename)
		if filename == "" || strings.HasPrefix(filename, ".") {
			filename = fmt.Sprintf("upload_%d", len(uploaded))
		}
		dest := filepath.Join(targetDir, filename)
		// Avoid overwrite
		if _, err := os.Stat(dest); err == nil {
			ext := filepath.Ext(filename)
			stem := strings.TrimSuffix(filename, ext)
			for i := 1; ; i++ {
				dest = filepath.Join(targetDir, fmt.Sprintf("%s_%d%s", stem, i, ext))
				if _, err := os.Stat(dest); err != nil {
					break
				}
			}
		}
		src, err := fh.Open()
		if err != nil {
			continue
		}
		out, err := os.Create(dest)
		if err != nil {
			src.Close()
			continue
		}
		io.Copy(out, src)
		src.Close()
		out.Close()
		info, _ := os.Stat(dest)
		uploaded = append(uploaded, uploadedFile{
			Name: filepath.Base(dest),
			Path: dest,
			Size: info.Size(),
		})
	}
	if uploaded == nil {
		uploaded = []uploadedFile{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "uploaded": uploaded})
}

// HandleFileDelete deletes a file or directory (recursive for directories).
// Body: { "path": "/abs/path" }
func HandleFileDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}
	r.Body.Close()
	if strings.TrimSpace(body.Path) == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	abs, err := filepath.Abs(body.Path)
	if err != nil {
		http.Error(w, "Failed to resolve path", http.StatusBadRequest)
		return
	}
	if abs == "/" || abs == "" {
		http.Error(w, "Refusing to delete root", http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(abs); err != nil {
		http.Error(w, "Path not found", http.StatusNotFound)
		return
	}
	if err := os.RemoveAll(abs); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": abs})
}

// HandleFileRename renames (moves) a file or directory.
// Body: { "src": "/abs/old", "dest": "/abs/new" }
// Refuses to overwrite an existing destination.
func HandleFileRename(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Src  string `json:"src"`
		Dest string `json:"dest"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}
	r.Body.Close()
	if strings.TrimSpace(body.Src) == "" || strings.TrimSpace(body.Dest) == "" {
		http.Error(w, "src and dest are required", http.StatusBadRequest)
		return
	}
	src, err := filepath.Abs(body.Src)
	if err != nil {
		http.Error(w, "Failed to resolve src", http.StatusBadRequest)
		return
	}
	dest, err := filepath.Abs(body.Dest)
	if err != nil {
		http.Error(w, "Failed to resolve dest", http.StatusBadRequest)
		return
	}
	if name, blocked := durable.ReservedRootCollision(dest); blocked {
		http.Error(w, reservedNameMessage(name), http.StatusConflict)
		return
	}
	if _, err := os.Stat(src); err != nil {
		http.Error(w, "Source not found", http.StatusNotFound)
		return
	}
	if _, err := os.Stat(dest); err == nil {
		http.Error(w, "Destination already exists", http.StatusConflict)
		return
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := os.Rename(src, dest); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "src": src, "path": dest})
}

// HandleFileMove is functionally the same as rename — move a path to a new
// location. Provided as a separate endpoint for semantic clarity.
func HandleFileMove(w http.ResponseWriter, r *http.Request) {
	HandleFileRename(w, r)
}

// HandleFileDuplicate copies a file or directory.
// Body: { "src": "/abs/path", "dest"?: "/abs/optional" }
// If dest is omitted, an auto-numbered sibling copy is created
// (e.g. foo.txt → foo_copy.txt → foo_copy_1.txt).
func HandleFileDuplicate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Src  string `json:"src"`
		Dest string `json:"dest"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}
	r.Body.Close()
	if strings.TrimSpace(body.Src) == "" {
		http.Error(w, "src is required", http.StatusBadRequest)
		return
	}
	src, err := filepath.Abs(body.Src)
	if err != nil {
		http.Error(w, "Failed to resolve src", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(src)
	if err != nil {
		http.Error(w, "Source not found", http.StatusNotFound)
		return
	}

	dest := strings.TrimSpace(body.Dest)
	if dest == "" {
		dir := filepath.Dir(src)
		base := filepath.Base(src)
		ext := ""
		stem := base
		if !info.IsDir() {
			ext = filepath.Ext(base)
			stem = strings.TrimSuffix(base, ext)
		}
		candidate := filepath.Join(dir, fmt.Sprintf("%s_copy%s", stem, ext))
		for i := 1; ; i++ {
			if _, err := os.Stat(candidate); err != nil {
				dest = candidate
				break
			}
			candidate = filepath.Join(dir, fmt.Sprintf("%s_copy_%d%s", stem, i, ext))
		}
	} else {
		dest, err = filepath.Abs(dest)
		if err != nil {
			http.Error(w, "Failed to resolve dest", http.StatusBadRequest)
			return
		}
		if _, err := os.Stat(dest); err == nil {
			http.Error(w, "Destination already exists", http.StatusConflict)
			return
		}
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := copyPath(src, dest); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "src": src, "path": dest})
}

// copyPath recursively copies a file or directory from src to dest.
func copyPath(src, dest string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.MkdirAll(dest, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyPath(filepath.Join(src, e.Name()), filepath.Join(dest, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func HandleFileDownload(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	abs, _ := filepath.Abs(path)
	info, err := os.Stat(abs)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	if info.IsDir() {
		streamFolderZip(w, abs)
		return
	}
	Revalidate(w.Header())
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(abs)))
	http.ServeFile(w, r, abs)
}

// streamFolderZip streams the folder rooted at abs to the client as a .zip,
// with entry paths relative to (and prefixed by) the folder's own name. The
// archive is streamed so server memory stays flat regardless of folder size.
func streamFolderZip(w http.ResponseWriter, abs string) {
	root := filepath.Base(abs)
	if root == "" || root == "." || root == string(filepath.Separator) {
		root = "download"
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.zip"`, root))

	zw := zip.NewWriter(w)
	defer zw.Close()

	filepath.Walk(abs, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable entries, keep archiving the rest
		}
		if info.IsDir() && !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(abs, p)
		if err != nil {
			return nil
		}
		if rel == "." {
			return nil // skip the root itself
		}
		name := filepath.ToSlash(filepath.Join(root, rel))

		if info.IsDir() {
			_, err := zw.Create(name + "/")
			return err
		}
		if !info.Mode().IsRegular() {
			return nil // skip symlinks, sockets, devices, etc.
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return nil
		}
		hdr.Name = name
		hdr.Method = zip.Deflate
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer in.Close()
		_, err = io.Copy(fw, in)
		return err
	})
}

// HandleFileRaw serves a single file's raw bytes with an inline
// Content-Disposition and a best-effort Content-Type so an <iframe> (e.g. the
// PDF or HTML preview) renders it in place instead of prompting a download.
func HandleFileRaw(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	abs, _ := filepath.Abs(path)
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	SetInlineHeaders(w.Header(), filepath.Base(abs))
	Revalidate(w.Header())
	http.ServeFile(w, r, abs)
}

// SetInlineHeaders marks a file's bytes for showing in place rather than
// downloading: a best-effort type, and for markup a policy that stops it
// fetching anything.
func SetInlineHeaders(h http.Header, name string) {
	ext := strings.ToLower(filepath.Ext(name))
	if ct := contentTypeByExt(ext); ct != "" {
		h.Set("Content-Type", ct)
	}
	if csp := contentSecurityPolicy(ext); csp != "" {
		h.Set("Content-Security-Policy", csp)
	}
	h.Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, name))
	h.Set("X-Content-Type-Options", "nosniff")
}

// contentSecurityPolicy returns the policy to serve a previewable document
// under, or "" for types that carry no markup.
//
// HTML files here are usually written by the agent or pulled off the web
// rather than by the person reading them, so the preview treats the document
// as untrusted. The frontend renders it in a sandboxed iframe, which stops it
// executing; this policy stops it *fetching*. Without it a file can still
// reach the network through <img>, <link> and friends — enough to phone home
// with the fact that it was opened, and to leak the workspace's egress in the
// referer. Verified: an external beacon arrives without this header and does
// not with it.
//
// The two are deliberately independent. The sandbox lives in the frontend and
// the policy travels with the response, so a future caller that embeds this
// URL without a sandbox attribute still cannot be used to exfiltrate.
//
// data: images are allowed because self-contained reports routinely inline
// their charts, and inline styles because HTML written for a single file
// almost always carries a <style> block — refusing those would render most
// real documents as unformatted text for no security gain, since neither can
// originate a request.
func contentSecurityPolicy(ext string) string {
	switch ext {
	case ".html", ".htm":
		return "default-src 'none'; img-src data: blob:; style-src 'unsafe-inline'; font-src data:; media-src data: blob:; form-action 'none'; base-uri 'none'; sandbox"
	}
	return ""
}

// contentTypeByExt returns a Content-Type for extensions we want to render
// inline in the panel. Falls back to the image table used elsewhere.
func contentTypeByExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".pdf":
		return "application/pdf"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".html", ".htm":
		// Set explicitly rather than left to ServeFile's sniffing, so the
		// charset is always declared and the type can never be inferred from
		// content that happens to look like something else.
		return "text/html; charset=utf-8"
	}
	return mimeByExt(ext)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

var imageExts = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".svg":  "image/svg+xml",
	".bmp":  "image/bmp",
	".ico":  "image/x-icon",
	".avif": "image/avif",
	".tiff": "image/tiff",
	".tif":  "image/tiff",
}

func mimeByExt(ext string) string {
	if m, ok := imageExts[strings.ToLower(ext)]; ok {
		return m
	}
	return ""
}
