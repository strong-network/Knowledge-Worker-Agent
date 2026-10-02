// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package files

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The file panel previews a file by URL, and the URL stays the same when the
// agent rewrites the file. Without Cache-Control the browser may reuse its old
// copy, which is how a preview came to show content a download didn't.
func TestFileResponsesRevalidate(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "report.pdf")
	os.WriteFile(fp, []byte("%PDF-1.4 old"), 0o644)
	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	os.Chtimes(fp, old, old)

	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		url     string
	}{
		{"raw", HandleFileRaw, "/api/files/raw?path=" + fp},
		{"download", HandleFileDownload, "/api/files/download?path=" + fp},
	} {
		t.Run(tc.name, func(t *testing.T) {
			os.WriteFile(fp, []byte("%PDF-1.4 old"), 0o644)
			os.Chtimes(fp, old, old)

			w := httptest.NewRecorder()
			tc.handler(w, httptest.NewRequest("GET", tc.url, nil))
			if got := w.Header().Get("Cache-Control"); got != "no-cache" {
				t.Fatalf("Cache-Control = %q, want no-cache", got)
			}
			lastMod := w.Header().Get("Last-Modified")
			if lastMod == "" {
				t.Fatal("no Last-Modified, so the browser cannot revalidate cheaply")
			}

			// Unchanged: revalidation is a 304, not a second download.
			req := httptest.NewRequest("GET", tc.url, nil)
			req.Header.Set("If-Modified-Since", lastMod)
			w = httptest.NewRecorder()
			tc.handler(w, req)
			if w.Code != http.StatusNotModified {
				t.Fatalf("unchanged file: got %d, want 304", w.Code)
			}

			// Rewritten: the same revalidation now brings the new content.
			os.WriteFile(fp, []byte("%PDF-1.4 new"), 0o644)
			req = httptest.NewRequest("GET", tc.url, nil)
			req.Header.Set("If-Modified-Since", lastMod)
			w = httptest.NewRecorder()
			tc.handler(w, req)
			if w.Code != http.StatusOK || w.Body.String() != "%PDF-1.4 new" {
				t.Fatalf("rewritten file: got %d %q, want 200 with the new content", w.Code, w.Body.String())
			}
		})
	}
}
