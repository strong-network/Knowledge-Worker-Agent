// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The preview listener runs the scripts of an HTML file the owner opens in
// the app, on an origin of its own: on the app's origin, a script could reach
// the owner's API. It serves a file only through a link made for it.

// PreviewTTL is how long a preview link works.
const PreviewTTL = time.Hour

// maxPreviews bounds the links kept at once; the one expiring first goes.
const maxPreviews = 1000

type previewLink struct {
	path    string
	expires time.Time
}

var (
	previewMu sync.Mutex
	previews  = map[string]previewLink{}
	// previewPort is the port the preview listener was bound on; mu guards it.
	previewPort int
	now         = time.Now
)

// NewPreview makes a link to the HTML file at path, an absolute path, and
// returns its token and when it expires. Links live in memory only.
func NewPreview(path string) (string, time.Time) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := base64.RawURLEncoding.EncodeToString(b)
	previewMu.Lock()
	defer previewMu.Unlock()
	t := now()
	for k, l := range previews {
		if !t.Before(l.expires) {
			delete(previews, k)
		}
	}
	if len(previews) >= maxPreviews {
		first := ""
		for k, l := range previews {
			if first == "" || l.expires.Before(previews[first].expires) {
				first = k
			}
		}
		delete(previews, first)
	}
	exp := t.Add(PreviewTTL)
	previews[token] = previewLink{path, exp}
	return token, exp
}

func previewPath(token string) (string, bool) {
	previewMu.Lock()
	defer previewMu.Unlock()
	l, ok := previews[token]
	if !ok || !now().Before(l.expires) {
		return "", false
	}
	return l.path, true
}

// OpenPreview opens the HTML file at path for the preview, under the rules
// of Open. The caller closes it.
func OpenPreview(path string) (*os.File, fs.FileInfo, error) {
	if k, _ := KindOf(path); k != KindHTML {
		return nil, nil, &ErrUnavailable{"Only HTML files run their scripts in the preview."}
	}
	return Open(filepath.Dir(path), filepath.Base(path))
}

// BindPreview starts the preview listener on 127.0.0.1:port, unless it's
// already there. It fails when something else holds the port.
func BindPreview(port int) error {
	mu.Lock()
	defer mu.Unlock()
	if previewPort == port && servers[port] != nil {
		return nil
	}
	if err := serveLocked(port, previewHandler()); err != nil {
		return err
	}
	previewPort = port
	return nil
}

func previewHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /p/{token}", previewPage)
	mux.HandleFunc("GET /p/{token}/content", previewContent)
	return secured(mux)
}

// previewAncestors lets the app frame the preview. The app's hostname holds
// the id of the custom access item it's opened through, which isn't known here.
func previewAncestors() string {
	if d := AppsDomain(); d != "" {
		return "frame-ancestors 'self' https://*." + d
	}
	return "frame-ancestors 'self'"
}

// previewTmpl frames the file and tells the app the frame got in: the
// message carries nothing, and the app takes it only from the preview origin.
var previewTmpl = template.Must(template.New("preview").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="referrer" content="no-referrer">
<title>{{.Name}}</title>
<style nonce="{{.Nonce}}">html, body { margin: 0; height: 100%; } iframe { display: block; border: 0; width: 100%; height: 100%; background: #fff; }</style>
</head>
<body>
<iframe src="/p/{{.Token}}/content" sandbox="{{.Sandbox}}" title="{{.Name}}"></iframe>
<script nonce="{{.Nonce}}">if (window.parent !== window) { window.parent.postMessage('kwa-preview-loaded', '*'); }</script>
</body>
</html>
`))

const expiredPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Preview expired</title></head>
<body style="font: 14px system-ui, sans-serif; color: #59636e; text-align: center; padding-top: 40px">This preview link has expired. Open the file again to preview it.</body></html>
`

func previewPage(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	token := r.PathValue("token")
	path, ok := previewPath(token)
	if !ok {
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; "+previewAncestors())
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, expiredPage)
		return
	}
	n := nonce()
	h.Set("Content-Security-Policy", fmt.Sprintf("default-src 'none'; script-src 'nonce-%[1]s'; style-src 'nonce-%[1]s'; "+
		"frame-src 'self'; base-uri 'none'; form-action 'none'; %[2]s", n, previewAncestors()))
	err := previewTmpl.Execute(w, struct{ Nonce, Name, Token, Sandbox string }{n, filepath.Base(path), token, Sandbox})
	if err != nil {
		log.Printf("[fileshare] preview page: %v", err)
	}
}

func previewContent(w http.ResponseWriter, r *http.Request) {
	path, ok := previewPath(r.PathValue("token"))
	if !ok {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	f, info, err := OpenPreview(path)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	src, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, "The file can't be shown", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Content-Security-Policy", strings.Replace(htmlPolicy, "frame-ancestors 'self'", previewAncestors(), 1))
	http.ServeContent(w, r, "", info.ModTime(), bytes.NewReader(withLinkScript(src)))
}
