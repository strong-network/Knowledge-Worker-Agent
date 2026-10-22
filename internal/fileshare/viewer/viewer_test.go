// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// share records a file as shared and returns its handler.
func share(t *testing.T, name, body string, allowDownload bool) (http.Handler, string) {
	t.Helper()
	if err := db.Init(filepath.Join(t.TempDir(), "t.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	folder := t.TempDir()
	if body != "" {
		write(t, filepath.Join(folder, name), body)
	}
	a := db.SharedArtifact{ID: "a1", Folder: folder, RelPath: name, Port: 8770, AllowDownload: allowDownload}
	if err := db.CreateSharedArtifact(a); err != nil {
		t.Fatal(err)
	}
	return handler(a.ID), folder
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func get(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestHTMLIsServedUnderTheArtifactPolicy(t *testing.T) {
	h, _ := share(t, "report.html", `<h1>Q3</h1><script>draw()</script>`, false)
	rec := get(h, "GET", "/content")
	if rec.Code != http.StatusOK || rec.Body.String() != linkScript+`<h1>Q3</h1><script>draw()</script>` {
		t.Fatalf("content: %d %q", rec.Code, rec.Body)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'unsafe-inline' 'unsafe-eval'", "connect-src 'none'",
		"form-action 'none'", "frame-ancestors 'self'", "sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox"} {
		if !strings.Contains(csp, want) {
			t.Errorf("policy lacks %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "allow-same-origin") {
		t.Errorf("the file must not run on this origin: %s", csp)
	}
	for k, want := range map[string]string{"Content-Type": "text/html; charset=utf-8", "X-Content-Type-Options": "nosniff",
		"Referrer-Policy": "no-referrer", "Cache-Control": "no-cache"} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestViewerPageFramesTheFileInTheSandbox(t *testing.T) {
	prev := config.OwnerFullName
	config.OwnerFullName = "Alex <Example>"
	t.Cleanup(func() { config.OwnerFullName = prev })
	h, _ := share(t, "report.html", `<p>hi</p>`, false)
	rec := get(h, "GET", "/")
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("page: %d", rec.Code)
	}
	for _, want := range []string{`<iframe src="/content" sandbox="` + Sandbox + `"`, "Shared by Alex &lt;Example&gt;", "report.html"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if !strings.Contains(body, `id="download" class="button" href="/download" hidden`) {
		t.Error("Download must be hidden when it isn't allowed")
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "script-src 'nonce-") || strings.Contains(csp, "unsafe-inline") {
		t.Errorf("page policy: %s", csp)
	}
	nonce := strings.SplitN(strings.SplitN(csp, "'nonce-", 2)[1], "'", 2)[0]
	if !strings.Contains(body, `<script nonce="`+nonce+`">`) {
		t.Error("the page's script must carry the nonce")
	}
	if again := get(h, "GET", "/").Header().Get("Content-Security-Policy"); again == csp {
		t.Error("the nonce must change per page")
	}
}

func TestDownloadFollowsTheOwnersChoice(t *testing.T) {
	h, _ := share(t, "q3 report.html", `<p>x</p>`, false)
	if rec := get(h, "GET", "/download"); rec.Code != http.StatusNotFound {
		t.Fatalf("download while off: %d", rec.Code)
	}
	if err := db.SetSharedArtifactDownload("a1", true); err != nil {
		t.Fatal(err)
	}
	rec := get(h, "GET", "/download")
	if rec.Code != http.StatusOK || rec.Body.String() != `<p>x</p>` {
		t.Fatalf("download: %d %q", rec.Code, rec.Body)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="q3 report.html"` {
		t.Errorf("disposition: %s", cd)
	}
	if strings.Contains(get(h, "GET", "/").Body.String(), `href="/download" hidden`) {
		t.Error("Download must show once allowed")
	}
}

func TestMetaReportsTheCurrentVersion(t *testing.T) {
	h, folder := share(t, "dash.html", `v1`, true)
	var m struct {
		Name     string `json:"name"`
		Size     int64  `json:"size"`
		Updated  string `json:"updated"`
		Download bool   `json:"download"`
	}
	rec := get(h, "GET", "/meta")
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil || m.Name != "dash.html" || m.Size != 2 || !m.Download || m.Updated == "" {
		t.Fatalf("meta: %s (%v)", rec.Body, err)
	}
	write(t, filepath.Join(folder, "dash.html"), "version 2")
	if rec := get(h, "GET", "/content"); rec.Body.String() != linkScript+"version 2" {
		t.Errorf("content after a change: %q", rec.Body)
	}
}

func TestOnlyReadsAreServed(t *testing.T) {
	h, _ := share(t, "r.html", `x`, true)
	if rec := get(h, "POST", "/content"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rec.Code)
	}
	if rec := get(h, "HEAD", "/content"); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD: %d %q", rec.Code, rec.Body)
	}
	for _, p := range []string{"/content/../x", "/other.html", "/api/files/view", "/s/x", "/content/x"} {
		if rec := get(h, "GET", p); rec.Code == http.StatusOK || rec.Body.String() == "x" {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Body)
		}
	}
}

func TestEachTypeGetsItsPolicy(t *testing.T) {
	for name, want := range map[string]string{
		"chart.svg": "sandbox", "photo.png": "sandbox", "deck.pdf": "default-src 'none'; frame-ancestors 'self'",
	} {
		h, _ := share(t, name, "data", false)
		rec := get(h, "GET", "/content")
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.HasSuffix(csp, want) {
			t.Errorf("%s policy: %s", name, csp)
		}
		if strings.HasSuffix(name, ".pdf") && strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") {
			t.Error("a sandbox stops the browser's PDF viewer")
		}
		page := get(h, "GET", "/").Body.String()
		img := strings.Contains(page, `<img src="/content"`)
		if img == strings.HasSuffix(name, ".pdf") {
			t.Errorf("%s shown as image=%v", name, img)
		}
	}
}

func TestMarkdownIsRenderedUnderItsPolicy(t *testing.T) {
	src := "# Q3 & a < b\n\n<script>alert(1)</script>\n\nSee [the ticket](https://example.com/t/1) and [above](#q3--a--b).\n"
	h, _ := share(t, "Q3 notes.md", src, true)
	rec := get(h, "GET", "/content")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("content: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, want := range []string{"<title>Q3 notes.md</title>", `<body class="markdown-body">`, githubMarkdownCSS,
		`<h1 id="q3--a--b">Q3 &amp; a &lt; b</h1>`, `<a href="https://example.com/t/1" target="_blank" rel="noopener noreferrer">the ticket</a>`,
		`<a href="#q3--a--b">above</a>`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %.80q", want)
		}
	}
	if strings.Contains(body, "<script") || strings.Contains(body, "alert(1)") {
		t.Errorf("HTML written into the file was rendered:\n%s", body)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'none'", "connect-src 'none'", "frame-ancestors 'self'",
		"sandbox " + markdownSandbox} {
		if !strings.Contains(csp, want) {
			t.Errorf("policy lacks %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "allow-scripts") || strings.Contains(csp, "allow-same-origin") || strings.Contains(csp, "unsafe-inline' 'unsafe-eval") {
		t.Errorf("a rendered Markdown file runs nothing: %s", csp)
	}
	if rec := get(h, "HEAD", "/content"); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD: %d", rec.Code)
	}
	if rec := get(h, "GET", "/download"); rec.Body.String() != src || rec.Header().Get("Content-Type") != "application/octet-stream" {
		t.Errorf("download must be the file as written: %q", rec.Body)
	}
	page := get(h, "GET", "/").Body.String()
	if !strings.Contains(page, `<iframe src="/content" sandbox="allow-popups allow-popups-to-escape-sandbox" title="Q3 notes.md">`) {
		t.Errorf("the page must frame it without scripts:\n%s", page)
	}
	for _, name := range []string{"README.MD", "a.markdown"} {
		if k, ok := KindOf(name); !ok || k != KindMarkdown {
			t.Errorf("KindOf(%s) = %s, %v", name, k, ok)
		}
	}
}

func TestOpenRefusesWhatCantBeShared(t *testing.T) {
	folder := t.TempDir()
	outside := t.TempDir()
	write(t, filepath.Join(folder, "ok.html"), "ok")
	write(t, filepath.Join(folder, ".system", "x.html"), "hidden")
	write(t, filepath.Join(folder, "notes.txt"), "text")
	write(t, filepath.Join(outside, "secret.html"), "secret")
	if err := os.Symlink(filepath.Join(outside, "secret.html"), filepath.Join(folder, "link.html")); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(folder, "big.html")
	f, _ := os.Create(big)
	f.Truncate(MaxSize + 1)
	f.Close()
	if f, _, err := Open(folder, "ok.html"); err != nil {
		t.Errorf("ok.html: %v", err)
	} else {
		f.Close()
	}
	for rel, why := range map[string]string{
		".system/x.html": "hidden", "notes.txt": "type", "link.html": "symlink out of the folder",
		"../" + filepath.Base(outside) + "/secret.html": "outside the folder", "big.html": "size", "gone.html": "missing",
	} {
		if f, _, err := Open(folder, rel); err == nil {
			f.Close()
			t.Errorf("%s (%s) was opened", rel, why)
		}
	}
}

func TestAMissingFileSaysSo(t *testing.T) {
	h, _ := share(t, "gone.html", "", false)
	rec := get(h, "GET", "/")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "The file is no longer there.") {
		t.Errorf("page: %d %s", rec.Code, rec.Body)
	}
	if rec := get(h, "GET", "/content"); rec.Code != http.StatusNotFound {
		t.Errorf("content: %d", rec.Code)
	}
	if err := db.DeleteSharedArtifact("a1"); err != nil {
		t.Fatal(err)
	}
	if rec := get(h, "GET", "/"); !strings.Contains(rec.Body.String(), "This file is no longer shared.") {
		t.Errorf("after stopping: %s", rec.Body)
	}
}

func TestBindServesOnePortAndUnbindCloses(t *testing.T) {
	_, folder := share(t, "r.html", "hello", false)
	_ = folder
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := Bind("a1", port); err == nil {
		t.Fatal("bound a port something else holds")
	}
	ln.Close()
	if err := Bind("a1", port); err != nil {
		t.Fatal(err)
	}
	if !Bound(port) || Bind("a1", port) == nil {
		t.Fatal("a bound port must refuse a second file")
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/content", port))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != linkScript+"hello" {
		t.Errorf("served %q", b)
	}
	Unbind(port)
	if Bound(port) {
		t.Error("still bound")
	}
	if _, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/content", port)); err == nil {
		t.Error("the port still answers")
	}
}

func TestOriginFollowsThePlatform(t *testing.T) {
	t.Setenv("STRONG_NETWORK_WORKSPACE_ID", "")
	if got := Origin(8770); got != "http://127.0.0.1:8770" {
		t.Errorf("local: %s", got)
	}
	t.Setenv("STRONG_NETWORK_WORKSPACE_ID", "42")
	t.Setenv("STRONG_NETWORK_WORKSPACE_APPS_DOMAIN", "proxy.example.com")
	if got := Origin(8771); got != "https://ws-42-port-8771.proxy.example.com" {
		t.Errorf("platform: %s", got)
	}
}

// Nothing the viewer imports, however indirectly, may be the sidecar client
// or the owner's side of sharing.
func TestViewerDependenciesExcludeTheSidecar(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	deps := strings.Fields(string(out))
	if len(deps) < 5 {
		t.Fatalf("go list returned %d packages; the check would pass vacuously", len(deps))
	}
	for _, d := range deps {
		for _, banned := range []string{"/internal/workspaceapps", "/internal/sharing", "/internal/fileshare"} {
			if strings.HasSuffix(d, banned) {
				t.Errorf("the viewer depends on %s", d)
			}
		}
	}
}

// The proxy delivers the owner's token to any port of the workspace, so
// the viewer must not be able to read it.
func TestViewerPackageCannotReachTheOwnerToken(t *testing.T) {
	fset := token.NewFileSet()
	entries, _ := os.ReadDir(".")
	checked := 0
	for _, ent := range entries {
		name := ent.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			if strings.Contains(imp.Path.Value, "internal/workspaceapps") {
				t.Errorf("%s imports the sidecar client", name)
			}
		}
		src, _ := os.ReadFile(name)
		if strings.Contains(strings.ToLower(string(src)), "cloud_editor_user_token") {
			t.Errorf("%s mentions the owner token header", name)
		}
	}
	if checked == 0 {
		t.Fatal("no source files checked")
	}
}
