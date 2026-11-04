// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package files

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// The file routes open the home folder only, and not the hidden items directly
// in it, where keys and tokens are kept: not to read, write, move or delete.
func TestFileRoutesStayInTheHomeFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KWA_HOME", filepath.Join(home, "Knowledge_Worker_Agent"))
	key := filepath.Join(home, ".ssh", "id_ed25519")
	outside := filepath.Join(t.TempDir(), "elsewhere.txt")
	inside := filepath.Join(home, "notes.txt")
	for _, f := range []string{key, outside, inside} {
		os.MkdirAll(filepath.Dir(f), 0o755)
		os.WriteFile(f, []byte("secret"), 0o600)
	}
	get := func(h http.HandlerFunc, route, path string) int {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest("GET", route+"?path="+url.QueryEscape(path), nil))
		return w.Code
	}
	upload := func(dir string) int {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("target_dir", dir)
		fw, _ := mw.CreateFormFile("files", "authorized_keys")
		fw.Write([]byte("ssh-ed25519 AAAA"))
		mw.Close()
		req := httptest.NewRequest("POST", "/api/files/upload", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		w := httptest.NewRecorder()
		HandleFileUpload(w, req)
		return w.Code
	}

	for _, p := range []string{key, outside} {
		for name, code := range map[string]int{
			"view":      get(HandleFileView, "/api/files/view", p),
			"raw":       get(HandleFileRaw, "/api/files/raw", p),
			"download":  get(HandleFileDownload, "/api/files/download", p),
			"browse":    get(HandleBrowse, "/api/browse", filepath.Dir(p)),
			"save":      postJSON(t, HandleFileSave, "/api/files/save", map[string]string{"path": p, "content": "changed"}).Code,
			"delete":    postJSON(t, HandleFileDelete, "/api/files/delete", map[string]string{"path": p}).Code,
			"rename":    postJSON(t, HandleFileRename, "/api/files/rename", map[string]string{"src": p, "dest": filepath.Join(home, "stolen")}).Code,
			"rename in": postJSON(t, HandleFileRename, "/api/files/rename", map[string]string{"src": inside, "dest": p + ".new"}).Code,
			"duplicate": postJSON(t, HandleFileDuplicate, "/api/files/duplicate", map[string]string{"src": p}).Code,
			"folder":    postJSON(t, HandleCreateDirectory, "/api/files/directory", map[string]string{"path": filepath.Join(filepath.Dir(p), "new")}).Code,
			"upload":    upload(filepath.Dir(p)),
		} {
			if code != http.StatusForbidden {
				t.Errorf("%s %s: %d, want 403", name, p, code)
			}
		}
		if b, _ := os.ReadFile(p); string(b) != "secret" {
			t.Errorf("%s was changed: %q", p, b)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh", "authorized_keys")); err == nil {
		t.Error("an upload landed in ~/.ssh")
	}
	if _, err := os.Stat(inside); err != nil {
		t.Error("a refused rename moved the file")
	}

	// A copy of the home folder, or another folder of its name, would go beside it.
	for name, code := range map[string]int{
		"duplicate": postJSON(t, HandleFileDuplicate, "/api/files/duplicate", map[string]string{"src": home}).Code,
		"folder":    postJSON(t, HandleCreateDirectory, "/api/files/directory", map[string]any{"path": home, "unique": true}).Code,
	} {
		if code != http.StatusForbidden {
			t.Errorf("%s of the home folder: %d, want 403", name, code)
		}
	}
	if made, _ := filepath.Glob(home + "[-_]*"); len(made) != 0 {
		t.Errorf("made beside the home folder: %v", made)
	}

	// The home folder lists without its hidden items, and offers no way up.
	w := httptest.NewRecorder()
	HandleBrowse(w, httptest.NewRequest("GET", "/api/browse?path="+url.QueryEscape(home), nil))
	var listing struct {
		Parent string
		Dirs   []struct{ Name string }
		Files  []struct{ Name string }
	}
	json.Unmarshal(w.Body.Bytes(), &listing)
	if w.Code != http.StatusOK || listing.Parent != "" || len(listing.Dirs) != 0 || len(listing.Files) != 1 || listing.Files[0].Name != "notes.txt" {
		t.Errorf("home listing: %d %s", w.Code, w.Body.String())
	}
	if code := get(HandleFileView, "/api/files/view", inside); code != http.StatusOK {
		t.Errorf("a file in the home folder: %d", code)
	}
}
