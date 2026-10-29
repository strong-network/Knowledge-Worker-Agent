// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package versions

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
)

// list asks for path's versions as the viewer does, which takes a version of a change first.
func list(t *testing.T, path string) []versionJSON {
	t.Helper()
	w := httptest.NewRecorder()
	HandleList(w, httptest.NewRequest("GET", "/api/files/versions?path="+url.QueryEscape(path), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d %s", w.Code, w.Body)
	}
	var body struct{ Versions []versionJSON }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Versions
}

func TestTheVersionRoutes(t *testing.T) {
	folder := setup(t)
	notes := filepath.Join(folder, "notes.md")
	write(t, notes, "one\n")
	Saved(notes, Guest("Robin"))
	write(t, notes, "two\n")
	Saved(notes, You)

	l := list(t, notes)
	if len(l) != 2 || l[0].Author != "guest" || l[0].Name != "Robin" || l[1].Author != You || l[1].Size != 4 || l[1].At == "" {
		t.Errorf("list = %+v", l)
	}

	get := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		HandleContent(w, httptest.NewRequest("GET", "/api/files/version?path="+url.QueryEscape(notes)+query, nil))
		return w
	}
	if w := get("&v=1"); w.Code != 200 || w.Body.String() != "one\n" || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("v1 = %d %q %v", w.Code, w.Body, w.Header())
	}
	if w := get("&v=1&download=1"); w.Code != 200 || w.Header().Get("Content-Disposition") != `attachment; filename=notes-v1.md` {
		t.Errorf("download = %d %v", w.Code, w.Header())
	}
	if w := get("&v=9"); w.Code != http.StatusNotFound {
		t.Errorf("v9 = %d", w.Code)
	}
	if w := get(""); w.Code != http.StatusBadRequest {
		t.Errorf("no v = %d", w.Code)
	}

	// A file that doesn't get versions has none, and isn't an error.
	png := filepath.Join(folder, "chart.png")
	write(t, png, "png")
	if l := list(t, png); len(l) != 0 {
		t.Errorf("png list = %+v", l)
	}
}
