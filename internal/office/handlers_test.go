// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package office

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withConverter points the handlers at a converter for one test, and keeps
// everything else they touch out of the real home folder.
func withConverter(t *testing.T, bin string) {
	t.Helper()
	t.Setenv("KWA_HOME", t.TempDir())
	saved := defaultConverter
	defaultConverter = &Converter{Bin: func() string { return bin }, Dir: t.TempDir(), Timeout: 10 * time.Second}
	t.Cleanup(func() { defaultConverter = saved })
}

func get(t *testing.T, h http.HandlerFunc, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/api/files/office-preview?path="+url.QueryEscape(path), nil))
	return rec
}

func TestHandlePreviewServesThePDF(t *testing.T) {
	bin, _ := fakeSoffice(t)
	withConverter(t, bin)
	src := filepath.Join(t.TempDir(), `Q3 "plan".pptx`)
	writeDoc(t, src, "slides")
	rec := get(t, HandlePreview, src)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `inline; filename="Q3 plan.pdf"` {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if rec.Body.String() != "%PDF-fake slides" {
		t.Errorf("body = %q", rec.Body)
	}
}

func TestHandlePreviewErrors(t *testing.T) {
	bin, fake := fakeSoffice(t)
	withConverter(t, bin)
	dir := t.TempDir()
	text := filepath.Join(dir, "notes.txt")
	writeDoc(t, text, "x")
	broken := filepath.Join(dir, "broken.docx")
	writeDoc(t, broken, "x")

	type answer struct {
		Error   string `json:"error"`
		Missing bool   `json:"missing"`
	}
	check := func(name string, rec *httptest.ResponseRecorder, status int, missing bool, contains string) {
		t.Helper()
		var a answer
		json.Unmarshal(rec.Body.Bytes(), &a)
		if rec.Code != status || a.Missing != missing || !strings.Contains(a.Error, contains) {
			t.Errorf("%s: %d %+v, want %d missing=%v containing %q", name, rec.Code, a, status, missing, contains)
		}
	}
	check("no path", get(t, HandlePreview, ""), http.StatusBadRequest, false, "path")
	check("not an office file", get(t, HandlePreview, text), http.StatusUnsupportedMediaType, false, "PowerPoint")
	check("no such file", get(t, HandlePreview, filepath.Join(dir, "gone.pptx")), http.StatusNotFound, false, "not found")
	setMode(t, fake, "nopdf")
	check("conversion failed", get(t, HandlePreview, broken), http.StatusUnprocessableEntity, false, "couldn't open the file")

	defaultConverter.Bin = func() string { return "" }
	check("no LibreOffice", get(t, HandlePreview, broken), http.StatusConflict, true, "not installed")
}

func TestHandleStatus(t *testing.T) {
	withConverter(t, "")
	t.Setenv("PATH", t.TempDir()) // no LibreOffice to find there
	rec := httptest.NewRecorder()
	HandleStatus(rec, httptest.NewRequest("GET", "/api/office/status", nil))
	var st struct {
		Available  bool          `json:"available"`
		Version    string        `json:"version"`
		DownloadMB int           `json:"download_mb"`
		Install    InstallStatus `json:"install"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Available || st.Version != Version || st.DownloadMB != DownloadMB || st.Install.Installing {
		t.Errorf("status = %+v", st)
	}

	installed := installedBin(installDir())
	os.MkdirAll(filepath.Dir(installed), 0o755)
	writeDoc(t, installed, "#!/bin/sh\n")
	os.Chmod(installed, 0o755)
	rec = httptest.NewRecorder()
	HandleStatus(rec, httptest.NewRequest("GET", "/api/office/status", nil))
	json.Unmarshal(rec.Body.Bytes(), &st)
	if !st.Available {
		t.Error("an install under KWA_HOME isn't reported as available")
	}
}
