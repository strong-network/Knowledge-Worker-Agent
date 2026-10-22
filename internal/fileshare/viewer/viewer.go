// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package viewer is what coworkers reach when they open a shared file: one
// listener per shared file, on the port its Workspace App publishes, serving
// that one file and nothing else. Nothing on it takes a path, so a request
// can't name another file, and nothing on it writes. It must never read the
// owner's platform token or call the workspace sidecar. The owner's preview
// listener (preview.go) lives here for the same reasons.
package viewer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// MaxSize is the largest file that can be shared: a viewer downloads all of
// it on every open.
const MaxSize = 25 << 20

// Kind is how a shared file is shown.
type Kind string

const (
	KindHTML     Kind = "html"
	KindMarkdown Kind = "markdown"
	KindPDF      Kind = "pdf"
	KindImage    Kind = "image"
)

var types = map[string]struct {
	kind  Kind
	ctype string
}{
	".html": {KindHTML, "text/html; charset=utf-8"},
	".htm":  {KindHTML, "text/html; charset=utf-8"},
	".pdf":  {KindPDF, "application/pdf"},
	".png":  {KindImage, "image/png"},
	".jpg":  {KindImage, "image/jpeg"},
	".jpeg": {KindImage, "image/jpeg"},
	".gif":  {KindImage, "image/gif"},
	".webp": {KindImage, "image/webp"},
	// Shown only through <img>, where its scripts never run.
	".svg": {KindImage, "image/svg+xml"},
	// Rendered on the server; downloaded as written.
	".md":       {KindMarkdown, "text/markdown; charset=utf-8"},
	".markdown": {KindMarkdown, "text/markdown; charset=utf-8"},
}

// KindOf returns how a file of that name is shown, and false for a type that
// can't be shared.
func KindOf(name string) (Kind, bool) {
	t, ok := types[strings.ToLower(filepath.Ext(name))]
	return t.kind, ok
}

// Sandbox is the iframe sandbox an HTML file runs in. Scripts run; nothing runs
// on this origin (no allow-same-origin), and links open in a new tab.
const Sandbox = "allow-scripts allow-popups allow-popups-to-escape-sandbox allow-downloads allow-modals"

// markdownSandbox is a rendered Markdown file's: it has no scripts, only links.
const markdownSandbox = "allow-popups allow-popups-to-escape-sandbox"

// The policies a shared file is served under. The HTML one is the security
// boundary: whatever the file does, it can load and send nothing.
const (
	htmlPolicy = "default-src 'none'; script-src 'unsafe-inline' 'unsafe-eval'; style-src 'unsafe-inline'; " +
		"img-src data: blob:; font-src data:; media-src data: blob:; connect-src 'none'; frame-src 'none'; " +
		"worker-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'; sandbox " + Sandbox
	markdownPolicy = "default-src 'none'; script-src 'none'; style-src 'unsafe-inline'; " +
		"img-src data: blob:; font-src data:; media-src data: blob:; connect-src 'none'; frame-src 'none'; " +
		"worker-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'; sandbox " + markdownSandbox
	imagePolicy = "default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:; frame-ancestors 'self'; sandbox"
	// The browser's own PDF viewer doesn't run in a sandboxed frame.
	pdfPolicy = "default-src 'none'; frame-ancestors 'self'"
)

// ErrUnavailable is why a shared file can't be shown now.
type ErrUnavailable struct{ Reason string }

func (e *ErrUnavailable) Error() string { return e.Reason }

// Open opens relPath inside folder the way a viewer's request does: through
// os.Root, so neither ".." nor a symlink leaves the folder, with no hidden path
// part, a type that can be shared, and within MaxSize. The caller closes it.
func Open(folder, relPath string) (*os.File, fs.FileInfo, error) {
	if _, ok := KindOf(relPath); !ok {
		return nil, nil, &ErrUnavailable{"This type of file can't be shared."}
	}
	for _, part := range strings.Split(filepath.ToSlash(relPath), "/") {
		if part == "" || strings.HasPrefix(part, ".") {
			return nil, nil, &ErrUnavailable{"Files in hidden folders can't be shared."}
		}
	}
	root, err := os.OpenRoot(folder)
	if err != nil {
		return nil, nil, &ErrUnavailable{"The file is no longer there."}
	}
	defer root.Close()
	f, err := root.Open(relPath)
	if err != nil {
		return nil, nil, &ErrUnavailable{"The file is no longer there."}
	}
	info, err := f.Stat()
	switch {
	case err != nil || !info.Mode().IsRegular():
		f.Close()
		return nil, nil, &ErrUnavailable{"The file is no longer there."}
	case info.Size() > MaxSize:
		f.Close()
		return nil, nil, &ErrUnavailable{"The file is larger than 25 MB."}
	}
	return f, info, nil
}

var (
	mu      sync.Mutex
	servers = map[int]*http.Server{}
)

// Bind starts serving the shared file id on 127.0.0.1:port. It fails when
// the port is taken. The file is looked up on every request, so a change to
// it, or its deletion, applies at once.
func Bind(id string, port int) error {
	mu.Lock()
	defer mu.Unlock()
	return serveLocked(port, handler(id))
}

// serveLocked starts h on 127.0.0.1:port; the caller holds mu.
func serveLocked(port int, h http.Handler) error {
	if _, ok := servers[port]; ok {
		return fmt.Errorf("port %d is already serving a shared file", port)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	servers[port] = srv
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[fileshare] serve on %d: %v", port, err)
		}
	}()
	return nil
}

// Unbind stops serving on port.
func Unbind(port int) {
	mu.Lock()
	srv := servers[port]
	delete(servers, port)
	mu.Unlock()
	if srv != nil {
		srv.Close()
	}
}

// Bound reports whether a shared file is served on port.
func Bound(port int) bool {
	mu.Lock()
	defer mu.Unlock()
	_, ok := servers[port]
	return ok
}

// Shutdown stops every listener.
func Shutdown(ctx context.Context) {
	mu.Lock()
	all := servers
	servers = map[int]*http.Server{}
	mu.Unlock()
	for _, srv := range all {
		if srv.Shutdown(ctx) != nil {
			srv.Close()
		}
	}
}

// Origin is the address a shared file on port is opened at, built as the
// platform builds a Workspace App URL. Outside the platform it's the local port.
func Origin(port int) string {
	domain := AppsDomain()
	if domain == "" {
		return fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	return fmt.Sprintf("https://ws-%s-port-%d.%s", strings.TrimSpace(os.Getenv("STRONG_NETWORK_WORKSPACE_ID")), port, domain)
}

// AppsDomain is the domain the platform publishes Workspace Apps on, or ""
// outside the platform.
func AppsDomain() string {
	if strings.TrimSpace(os.Getenv("STRONG_NETWORK_WORKSPACE_ID")) == "" {
		return ""
	}
	return strings.TrimSpace(os.Getenv("STRONG_NETWORK_WORKSPACE_APPS_DOMAIN"))
}

func handler(id string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { page(w, id) })
	mux.HandleFunc("GET /content", func(w http.ResponseWriter, r *http.Request) { content(w, r, id, false) })
	mux.HandleFunc("GET /download", func(w http.ResponseWriter, r *http.Request) { content(w, r, id, true) })
	mux.HandleFunc("GET /meta", func(w http.ResponseWriter, r *http.Request) { meta(w, id) })
	return secured(mux)
}

func secured(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// lookup returns the shared file and its name, or why it can't be shown.
func lookup(id string) (db.SharedArtifact, error) {
	a, ok := db.GetSharedArtifact(id)
	if !ok {
		return a, &ErrUnavailable{"This file is no longer shared."}
	}
	return a, nil
}

func content(w http.ResponseWriter, r *http.Request, id string, download bool) {
	a, err := lookup(id)
	if err == nil && download && !a.AllowDownload {
		err = &ErrUnavailable{"Downloading isn't allowed."}
	}
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	f, info, err := Open(a.Folder, a.RelPath)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	name := filepath.Base(a.RelPath)
	t := types[strings.ToLower(filepath.Ext(name))]
	var body io.ReadSeeker = f
	h := w.Header()
	h.Set("Content-Type", t.ctype)
	h.Set("Cache-Control", "no-cache")
	if download {
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	} else {
		h.Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": name}))
		switch t.kind {
		case KindHTML:
			src, err := io.ReadAll(f)
			if err != nil {
				http.Error(w, "The file can't be shown", http.StatusInternalServerError)
				return
			}
			h.Set("Content-Security-Policy", htmlPolicy)
			body = bytes.NewReader(withLinkScript(src))
		case KindMarkdown:
			page, err := markdownPage(f, name)
			if err != nil {
				log.Printf("[fileshare] render %s: %v", name, err)
				http.Error(w, "The file can't be shown", http.StatusInternalServerError)
				return
			}
			h.Set("Content-Type", "text/html; charset=utf-8")
			h.Set("Content-Security-Policy", markdownPolicy)
			body = bytes.NewReader(page)
		case KindPDF:
			h.Set("Content-Security-Policy", pdfPolicy)
		default:
			h.Set("Content-Security-Policy", imagePolicy)
		}
	}
	http.ServeContent(w, r, "", info.ModTime(), body)
}

func meta(w http.ResponseWriter, id string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	a, err := lookup(id)
	var info fs.FileInfo
	if err == nil {
		var f *os.File
		if f, info, err = Open(a.Folder, a.RelPath); err == nil {
			f.Close()
		}
	}
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name": filepath.Base(a.RelPath), "size": info.Size(),
		"updated": info.ModTime().UTC().Format(time.RFC3339Nano), "download": a.AllowDownload,
	})
}

type pageData struct {
	Nonce, Name, Owner, Updated, Problem string
	Kind                                 Kind
	Download                             bool
	Sandbox                              string
}

func page(w http.ResponseWriter, id string) {
	d := pageData{Nonce: nonce(), Owner: strings.TrimSpace(config.OwnerFullName), Sandbox: Sandbox}
	status := http.StatusOK
	a, err := lookup(id)
	if err == nil {
		d.Name, d.Download = filepath.Base(a.RelPath), a.AllowDownload
		d.Kind, _ = KindOf(a.RelPath)
		if d.Kind == KindMarkdown {
			d.Sandbox = markdownSandbox
		}
		var f *os.File
		var info fs.FileInfo
		if f, info, err = Open(a.Folder, a.RelPath); err == nil {
			f.Close()
			d.Updated = info.ModTime().UTC().Format(time.RFC3339Nano)
		}
	}
	if err != nil {
		d.Problem, status = err.Error(), http.StatusNotFound
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", fmt.Sprintf("default-src 'none'; script-src 'nonce-%[1]s'; style-src 'nonce-%[1]s'; "+
		"img-src 'self'; frame-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'", d.Nonce))
	h.Set("X-Frame-Options", "DENY")
	w.WriteHeader(status)
	if err := pageTmpl.Execute(w, d); err != nil {
		log.Printf("[fileshare] viewer page: %v", err)
	}
}

func nonce() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
