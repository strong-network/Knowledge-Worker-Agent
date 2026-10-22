// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func onPlatform(t *testing.T) {
	t.Setenv("STRONG_NETWORK_WORKSPACE_ID", "7")
	t.Setenv("STRONG_NETWORK_WORKSPACE_APPS_DOMAIN", "proxy.example")
}

func TestAPreviewLinkServesItsOneFile(t *testing.T) {
	onPlatform(t)
	folder := t.TempDir()
	write(t, filepath.Join(folder, "report.html"), `<h1>Q3</h1><script>draw()</script>`)
	write(t, filepath.Join(folder, "other.html"), `OTHER-FILE`)
	tok, exp := NewPreview(filepath.Join(folder, "report.html"))
	if d := time.Until(exp); d < 59*time.Minute || d > time.Hour {
		t.Errorf("expires in %s", d)
	}
	h := previewHandler()

	rec := get(h, "GET", "/p/"+tok)
	page, csp := rec.Body.String(), rec.Header().Get("Content-Security-Policy")
	if rec.Code != http.StatusOK {
		t.Fatalf("page: %d", rec.Code)
	}
	for _, want := range []string{`<iframe src="/p/` + tok + `/content" sandbox="` + Sandbox + `" title="report.html">`,
		`window.parent.postMessage('kwa-preview-loaded', '*')`} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	for _, want := range []string{"default-src 'none'", "frame-src 'self'", "frame-ancestors 'self' https://*.proxy.example"} {
		if !strings.Contains(csp, want) {
			t.Errorf("page policy lacks %q: %s", want, csp)
		}
	}
	nonce := strings.SplitN(strings.SplitN(csp, "'nonce-", 2)[1], "'", 2)[0]
	if !strings.Contains(page, `<script nonce="`+nonce+`">`) || strings.Contains(csp, "unsafe-inline") {
		t.Errorf("the page runs only its own script: %s", csp)
	}

	rec = get(h, "GET", "/p/"+tok+"/content")
	csp = rec.Header().Get("Content-Security-Policy")
	if rec.Code != http.StatusOK || rec.Body.String() != linkScript+`<h1>Q3</h1><script>draw()</script>` {
		t.Fatalf("content: %d %q", rec.Code, rec.Body)
	}
	for _, want := range []string{"script-src 'unsafe-inline' 'unsafe-eval'", "connect-src 'none'", "form-action 'none'",
		"frame-ancestors 'self' https://*.proxy.example;", "sandbox " + Sandbox} {
		if !strings.Contains(csp, want) {
			t.Errorf("content policy lacks %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "allow-same-origin") || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("content headers: %v", rec.Header())
	}

	write(t, filepath.Join(folder, "report.html"), "version 2")
	if rec := get(h, "GET", "/p/"+tok+"/content"); rec.Body.String() != linkScript+"version 2" {
		t.Errorf("content after a change: %q", rec.Body)
	}
	other, _ := NewPreview(filepath.Join(folder, "nothing.html"))
	for _, p := range []string{"/", "/p/", "/p/" + tok + "x", "/p/" + tok + "/content/x", "/p/" + tok + "/../other.html",
		"/content", "/p/" + other + "/content"} {
		if rec := get(h, "GET", p); rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "OTHER-FILE") {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Body)
		}
	}
	if rec := get(h, "POST", "/p/"+tok+"/content"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rec.Code)
	}
}

func TestAPreviewLinkExpires(t *testing.T) {
	t0 := time.Now()
	now = func() time.Time { return t0 }
	t.Cleanup(func() { now = time.Now })
	folder := t.TempDir()
	write(t, filepath.Join(folder, "r.html"), "x")
	tok, _ := NewPreview(filepath.Join(folder, "r.html"))
	h := previewHandler()
	now = func() time.Time { return t0.Add(PreviewTTL - time.Second) }
	if rec := get(h, "GET", "/p/"+tok+"/content"); rec.Code != http.StatusOK {
		t.Fatalf("before it expires: %d", rec.Code)
	}
	now = func() time.Time { return t0.Add(PreviewTTL) }
	rec := get(h, "GET", "/p/"+tok)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "This preview link has expired.") {
		t.Errorf("page: %d %s", rec.Code, rec.Body)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors") || strings.Contains(csp, "script-src") {
		t.Errorf("expired page policy: %s", csp)
	}
	if rec := get(h, "GET", "/p/"+tok+"/content"); rec.Code != http.StatusNotFound {
		t.Errorf("content: %d", rec.Code)
	}
}

func TestPreviewLinksAreBounded(t *testing.T) {
	t0 := time.Now()
	t.Cleanup(func() { now = time.Now })
	var first, last string
	for i := 0; i < maxPreviews+5; i++ {
		now = func() time.Time { return t0.Add(time.Duration(i) * time.Millisecond) }
		tok, _ := NewPreview(fmt.Sprintf("/x/%d.html", i))
		if i == 0 {
			first = tok
		}
		last = tok
	}
	previewMu.Lock()
	n := len(previews)
	previewMu.Unlock()
	if _, ok := previewPath(first); ok || n != maxPreviews {
		t.Errorf("%d links kept; the first still works: %v", n, ok)
	}
	if _, ok := previewPath(last); !ok {
		t.Error("the newest link was dropped")
	}
}

func TestOnlyHTMLIsPreviewed(t *testing.T) {
	folder := t.TempDir()
	for _, name := range []string{"ok.html", "notes.md", "deck.pdf", "chart.svg", ".hidden.html"} {
		write(t, filepath.Join(folder, name), "x")
	}
	big := filepath.Join(folder, "big.html")
	f, _ := os.Create(big)
	f.Truncate(MaxSize + 1)
	f.Close()
	if f, _, err := OpenPreview(filepath.Join(folder, "ok.html")); err != nil {
		t.Errorf("ok.html: %v", err)
	} else {
		f.Close()
	}
	for _, name := range []string{"notes.md", "deck.pdf", "chart.svg", ".hidden.html", "big.html", "gone.html"} {
		if f, _, err := OpenPreview(filepath.Join(folder, name)); err == nil {
			f.Close()
			t.Errorf("%s was opened", name)
		}
	}
}

func TestOutsideThePlatformOnlyThisOriginFrames(t *testing.T) {
	t.Setenv("STRONG_NETWORK_WORKSPACE_ID", "")
	t.Setenv("STRONG_NETWORK_WORKSPACE_APPS_DOMAIN", "proxy.example")
	if AppsDomain() != "" || previewAncestors() != "frame-ancestors 'self'" {
		t.Errorf("%q %q", AppsDomain(), previewAncestors())
	}
}

func TestBindPreviewOnce(t *testing.T) {
	_, folder := share(t, "r.html", "artifact", false)
	free := func() int {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		return ln.Addr().(*net.TCPAddr).Port
	}
	port, artifactPort := free(), free()
	t.Cleanup(func() { Unbind(port); Unbind(artifactPort) })
	if err := BindPreview(port); err != nil {
		t.Fatal(err)
	}
	if err := BindPreview(port); err != nil {
		t.Errorf("binding again: %v", err)
	}
	if !Bound(port) || Bind("a1", port) == nil {
		t.Error("the preview port must refuse a shared file")
	}
	if err := Bind("a1", artifactPort); err != nil {
		t.Fatal(err)
	}
	if BindPreview(artifactPort) == nil {
		t.Error("the preview took a shared file's port")
	}
	tok, _ := NewPreview(filepath.Join(folder, "r.html"))
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/p/%s/content", port, tok))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != linkScript+"artifact" {
		t.Errorf("served %q", b)
	}
	Unbind(port)
	if err := BindPreview(port); err != nil {
		t.Errorf("binding after it was closed: %v", err)
	}
}
