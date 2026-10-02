// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package files

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

func TestHandleBrowse(t *testing.T) {
	dir := t.TempDir()
	config.Workspace = dir

	os.WriteFile(filepath.Join(dir, "file1.txt"), []byte("hello"), 0o644)
	os.Mkdir(filepath.Join(dir, "subdir"), 0o755)
	os.WriteFile(filepath.Join(dir, ".hidden"), []byte("secret"), 0o644)

	req := httptest.NewRequest("GET", "/api/browse?path="+dir, nil)
	w := httptest.NewRecorder()
	HandleBrowse(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)

	dirs, _ := resp["dirs"].([]any)
	files, _ := resp["files"].([]any)

	if len(dirs) != 1 {
		t.Errorf("expected 1 dir, got %d", len(dirs))
	}
	if len(files) != 2 {
		t.Errorf("expected 2 files (hidden included), got %d", len(files))
	}

	// Verify hidden=0 hides dotfiles.
	req2 := httptest.NewRequest("GET", "/api/browse?path="+dir+"&hidden=0", nil)
	w2 := httptest.NewRecorder()
	HandleBrowse(w2, req2)
	var resp2 map[string]any
	json.Unmarshal(w2.Body.Bytes(), &resp2)
	files2, _ := resp2["files"].([]any)
	if len(files2) != 1 {
		t.Errorf("expected 1 file with hidden=0, got %d", len(files2))
	}
}

func TestHandleBrowseDefaultsToWorkspace(t *testing.T) {
	dir := t.TempDir()
	config.Workspace = dir

	req := httptest.NewRequest("GET", "/api/browse", nil)
	w := httptest.NewRecorder()
	HandleBrowse(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["path"] != dir {
		t.Errorf("expected path %q, got %q", dir, resp["path"])
	}
}

func TestHandleFileView(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "test.txt")
	os.WriteFile(fp, []byte("hello world"), 0o644)

	req := httptest.NewRequest("GET", "/api/files/view?path="+fp, nil)
	w := httptest.NewRecorder()
	HandleFileView(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["type"] != "text" {
		t.Errorf("expected type 'text', got %q", resp["type"])
	}
	if resp["content"] != "hello world" {
		t.Errorf("expected content 'hello world', got %q", resp["content"])
	}
}

func TestHandleFileViewNotFound(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/files/view?path=/nonexistent/file.txt", nil)
	w := httptest.NewRecorder()
	HandleFileView(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandleFileViewMissingPath(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/files/view", nil)
	w := httptest.NewRecorder()
	HandleFileView(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleFileSave(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "new_file.txt")

	body := `{"path":"` + fp + `","content":"saved content"}`
	req := httptest.NewRequest("POST", "/api/files/save", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	HandleFileSave(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	data, err := os.ReadFile(fp)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "saved content" {
		t.Errorf("expected 'saved content', got %q", string(data))
	}
}

func TestHandleCreateDirectoryCreatesNestedPath(t *testing.T) {
	dir := t.TempDir()
	targetDir := filepath.Join(dir, "workspace", "uploads", "screenshots")

	body := `{"path":"` + targetDir + `"}`
	req := httptest.NewRequest("POST", "/api/files/directory", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	HandleCreateDirectory(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	info, err := os.Stat(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %q to be a directory", targetDir)
	}
}

func TestHandleCreateDirectoryRejectsEmptyPath(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/files/directory", strings.NewReader(`{"path":"  "}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	HandleCreateDirectory(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleCreateDirectoryUniqueReturnsRequestedNameWhenFree(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "my-chat")

	body := `{"path":"` + target + `","unique":true}`
	req := httptest.NewRequest("POST", "/api/files/directory", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	HandleCreateDirectory(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Path != target {
		t.Errorf("expected path %q, got %q", target, resp.Path)
	}
	if resp.Name != "my-chat" {
		t.Errorf("expected name %q, got %q", "my-chat", resp.Name)
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("expected %q to exist as dir", target)
	}
}

func TestHandleCreateDirectoryUniqueAppendsSuffixOnCollision(t *testing.T) {
	dir := t.TempDir()
	desired := filepath.Join(dir, "my-chat")
	if err := os.Mkdir(desired, 0o755); err != nil {
		t.Fatal(err)
	}

	body := `{"path":"` + desired + `","unique":true}`
	req := httptest.NewRequest("POST", "/api/files/directory", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	HandleCreateDirectory(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Path == desired {
		t.Fatalf("expected a different path than %q, got the same", desired)
	}
	if !strings.HasPrefix(resp.Name, "my-chat-") {
		t.Errorf("expected name to start with %q, got %q", "my-chat-", resp.Name)
	}
	if info, err := os.Stat(resp.Path); err != nil || !info.IsDir() {
		t.Fatalf("expected %q to exist as dir", resp.Path)
	}
}

func TestHandleFileDownload(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "download.txt")
	os.WriteFile(fp, []byte("download me"), 0o644)

	req := httptest.NewRequest("GET", "/api/files/download?path="+fp, nil)
	w := httptest.NewRecorder()
	HandleFileDownload(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), "download.txt") {
		t.Error("expected Content-Disposition header with filename")
	}
	if w.Body.String() != "download me" {
		t.Errorf("unexpected body: %q", w.Body.String())
	}
}

func TestHandleFileDownloadFolderZip(t *testing.T) {
	dir := t.TempDir()
	folder := filepath.Join(dir, "report")
	os.MkdirAll(filepath.Join(folder, "sub"), 0o755)
	os.WriteFile(filepath.Join(folder, "a.txt"), []byte("alpha"), 0o644)
	os.WriteFile(filepath.Join(folder, "sub", "b.txt"), []byte("beta"), 0o644)

	req := httptest.NewRequest("GET", "/api/files/download?path="+folder, nil)
	w := httptest.NewRecorder()
	HandleFileDownload(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("expected application/zip, got %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "report.zip") {
		t.Errorf("expected report.zip in Content-Disposition, got %q", cd)
	}

	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatalf("invalid zip: %v", err)
	}
	got := map[string]string{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = string(b)
	}
	if got["report/a.txt"] != "alpha" {
		t.Errorf("missing/incorrect report/a.txt: %q", got["report/a.txt"])
	}
	if got["report/sub/b.txt"] != "beta" {
		t.Errorf("missing/incorrect report/sub/b.txt: %q", got["report/sub/b.txt"])
	}
}

func TestHandleFileRawInline(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "doc.pdf")
	os.WriteFile(fp, []byte("%PDF-1.4 fake"), 0o644)

	req := httptest.NewRequest("GET", "/api/files/raw?path="+fp, nil)
	w := httptest.NewRecorder()
	HandleFileRaw(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "inline") {
		t.Errorf("expected inline disposition, got %q", cd)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("expected application/pdf, got %q", ct)
	}
	if w.Body.String() != "%PDF-1.4 fake" {
		t.Errorf("unexpected body: %q", w.Body.String())
	}
}

// HTML served from this endpoint is rendered in the file panel's preview
// iframe. The iframe's sandbox does the heavy lifting, but the CSP has to ride
// along on the response so the page is still inert if it is ever opened
// somewhere that forgets the sandbox attribute.
func TestHandleFileRawHTMLIsLockedDown(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"page.html", "page.htm"} {
		fp := filepath.Join(dir, name)
		os.WriteFile(fp, []byte("<h1>hi</h1>"), 0o644)

		req := httptest.NewRequest("GET", "/api/files/raw?path="+fp, nil)
		w := httptest.NewRecorder()
		HandleFileRaw(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", name, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Errorf("%s: expected text/html; charset=utf-8, got %q", name, ct)
		}
		csp := w.Header().Get("Content-Security-Policy")
		if csp == "" {
			t.Fatalf("%s: expected a Content-Security-Policy header", name)
		}
		// default-src 'none' is what stops the page reaching the network;
		// the sandbox directive is what stops it running scripts. Losing
		// either one silently re-opens the hole, so pin both.
		for _, want := range []string{"default-src 'none'", "sandbox"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP %q missing %q", name, csp, want)
			}
		}
		if strings.Contains(csp, "script-src") {
			t.Errorf("%s: CSP must not re-enable scripts: %q", name, csp)
		}
		if xcto := w.Header().Get("X-Content-Type-Options"); xcto != "nosniff" {
			t.Errorf("%s: expected nosniff, got %q", name, xcto)
		}
	}
}

// Only HTML is treated as active content. A CSP on a PDF would break the
// browser's built-in viewer for no benefit.
func TestHandleFileRawNonHTMLHasNoCSP(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"doc.pdf", "notes.txt", "pic.png"} {
		fp := filepath.Join(dir, name)
		os.WriteFile(fp, []byte("data"), 0o644)

		req := httptest.NewRequest("GET", "/api/files/raw?path="+fp, nil)
		w := httptest.NewRecorder()
		HandleFileRaw(w, req)

		if csp := w.Header().Get("Content-Security-Policy"); csp != "" {
			t.Errorf("%s: unexpected CSP %q", name, csp)
		}
	}
}

func TestHandleFileRawRejectsDir(t *testing.T) {
	dir := t.TempDir()
	req := httptest.NewRequest("GET", "/api/files/raw?path="+dir, nil)
	w := httptest.NewRecorder()
	HandleFileRaw(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for directory, got %d", w.Code)
	}
}

func TestHandleFileViewImage(t *testing.T) {
	dir := t.TempDir()
	// Minimal 1x1 PNG
	pngData := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, 0x00, 0x00, 0x00,
		0x0c, 0x49, 0x44, 0x41, 0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
		0x00, 0x00, 0x02, 0x00, 0x01, 0xe2, 0x21, 0xbc, 0x33, 0x00, 0x00, 0x00,
		0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
	}
	fp := filepath.Join(dir, "test.png")
	os.WriteFile(fp, pngData, 0o644)

	req := httptest.NewRequest("GET", "/api/files/view?path="+fp, nil)
	w := httptest.NewRecorder()
	HandleFileView(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["type"] != "image" {
		t.Errorf("expected type 'image', got %q", resp["type"])
	}
	if resp["mime"] != "image/png" {
		t.Errorf("expected mime 'image/png', got %q", resp["mime"])
	}
	if resp["content"] == nil || resp["content"] == "" {
		t.Error("expected non-empty base64 content")
	}
}

func TestHandleFileUploadUsesWorkspaceUploadDirectory(t *testing.T) {
	dir := t.TempDir()
	config.Workspace = dir

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("files", "report.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("hello upload")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/api/files/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	HandleFileUpload(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	uploaded, _ := resp["uploaded"].([]any)
	if len(uploaded) != 1 {
		t.Fatalf("expected 1 uploaded file, got %d", len(uploaded))
	}

	item, _ := uploaded[0].(map[string]any)
	gotPath, _ := item["path"].(string)
	wantPath := filepath.Join(dir, "upload", "report.txt")
	if gotPath != wantPath {
		t.Errorf("expected uploaded path %q, got %q", wantPath, gotPath)
	}

	uploadDir := filepath.Join(dir, "upload")
	if fi, err := os.Stat(uploadDir); err != nil || !fi.IsDir() {
		t.Fatalf("expected upload directory %q to exist", uploadDir)
	}

	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello upload" {
		t.Errorf("expected uploaded file content %q, got %q", "hello upload", string(data))
	}
}

func TestHandleFileUploadHonorsTargetDir(t *testing.T) {
	dir := t.TempDir()
	targetDir := filepath.Join(dir, "workspace", "upload")
	config.Workspace = filepath.Join(dir, "global")

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("target_dir", targetDir); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("files", "screenshot.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("png data")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/api/files/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	HandleFileUpload(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	uploaded, _ := resp["uploaded"].([]any)
	if len(uploaded) != 1 {
		t.Fatalf("expected 1 uploaded file, got %d", len(uploaded))
	}
	item, _ := uploaded[0].(map[string]any)
	wantPath := filepath.Join(targetDir, "screenshot.png")
	if gotPath, _ := item["path"].(string); gotPath != wantPath {
		t.Errorf("expected uploaded path %q, got %q", wantPath, gotPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected uploaded file at %q: %v", wantPath, err)
	}
}

func postJSON(t *testing.T, h http.HandlerFunc, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

func TestHandleFileDelete_File(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "file.txt")
	os.WriteFile(target, []byte("x"), 0o644)

	w := postJSON(t, HandleFileDelete, "/api/files/delete", map[string]string{"path": target})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected file deleted, err=%v", err)
	}
}

func TestHandleFileDelete_DirectoryRecursive(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "a.txt"), []byte("a"), 0o644)

	w := postJSON(t, HandleFileDelete, "/api/files/delete", map[string]string{"path": sub})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Fatalf("expected dir deleted, err=%v", err)
	}
}

func TestHandleFileDelete_MissingPath(t *testing.T) {
	w := postJSON(t, HandleFileDelete, "/api/files/delete", map[string]string{"path": ""})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleFileDelete_RefuseRoot(t *testing.T) {
	w := postJSON(t, HandleFileDelete, "/api/files/delete", map[string]string{"path": "/"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleFileRename(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "old.txt")
	dest := filepath.Join(dir, "new.txt")
	os.WriteFile(src, []byte("hi"), 0o644)

	w := postJSON(t, HandleFileRename, "/api/files/rename", map[string]string{"src": src, "dest": dest})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("expected src removed")
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("expected dest exists: %v", err)
	}
}

func TestHandleFileRename_DestExists(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	dest := filepath.Join(dir, "b.txt")
	os.WriteFile(src, []byte("a"), 0o644)
	os.WriteFile(dest, []byte("b"), 0o644)

	w := postJSON(t, HandleFileRename, "/api/files/rename", map[string]string{"src": src, "dest": dest})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

func TestHandleFileRename_DirectoryAcrossFolders(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "old")
	os.Mkdir(srcDir, 0o755)
	os.WriteFile(filepath.Join(srcDir, "x.txt"), []byte("x"), 0o644)
	destDir := filepath.Join(dir, "nested", "new")

	w := postJSON(t, HandleFileRename, "/api/files/rename", map[string]string{"src": srcDir, "dest": destDir})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(destDir, "x.txt")); err != nil {
		t.Errorf("expected file moved: %v", err)
	}
}

func TestHandleFileDuplicate_AutoName(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "note.md")
	os.WriteFile(src, []byte("data"), 0o644)

	w := postJSON(t, HandleFileDuplicate, "/api/files/duplicate", map[string]string{"src": src})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	path, _ := resp["path"].(string)
	if path != filepath.Join(dir, "note_copy.md") {
		t.Errorf("unexpected copy path: %q", path)
	}
	if b, _ := os.ReadFile(path); string(b) != "data" {
		t.Errorf("unexpected copy contents")
	}

	// Second duplicate should add _1 suffix
	w2 := postJSON(t, HandleFileDuplicate, "/api/files/duplicate", map[string]string{"src": src})
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w2.Code)
	}
	json.Unmarshal(w2.Body.Bytes(), &resp)
	path2, _ := resp["path"].(string)
	if path2 != filepath.Join(dir, "note_copy_1.md") {
		t.Errorf("unexpected second copy path: %q", path2)
	}
}

func TestHandleFileDuplicate_Directory(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "folder")
	os.Mkdir(src, 0o755)
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644)

	w := postJSON(t, HandleFileDuplicate, "/api/files/duplicate", map[string]string{"src": src})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	copyDir := filepath.Join(dir, "folder_copy")
	if b, err := os.ReadFile(filepath.Join(copyDir, "a.txt")); err != nil || string(b) != "a" {
		t.Fatalf("expected duplicated dir content: %v %q", err, string(b))
	}
}

func TestHandleFileDuplicate_WithDest(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	os.WriteFile(src, []byte("hi"), 0o644)
	dest := filepath.Join(dir, "sub", "b.txt")

	w := postJSON(t, HandleFileDuplicate, "/api/files/duplicate", map[string]string{"src": src, "dest": dest})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if b, err := os.ReadFile(dest); err != nil || string(b) != "hi" {
		t.Errorf("expected dest copied: %v %q", err, string(b))
	}
}

func TestHandleFileMove_DelegatesToRename(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "x.txt")
	dest := filepath.Join(dir, "subdir", "y.txt")
	os.WriteFile(src, []byte("z"), 0o644)

	w := postJSON(t, HandleFileMove, "/api/files/move", map[string]string{"src": src, "dest": dest})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("expected dest exists: %v", err)
	}
}

// TestRouteRegistrationNoConflicts verifies that all registered routes can
// be added to a fresh ServeMux without panicking on duplicates. Regression
// test for the duplicate /api/auth/status registration panic.
func TestRouteRegistrationNoConflicts(t *testing.T) {
	mux := http.NewServeMux()
	patterns := []string{
		"GET /api/browse",
		"GET /api/files/view",
		"POST /api/files/save",
		"POST /api/files/directory",
		"POST /api/files/delete",
		"POST /api/files/rename",
		"POST /api/files/move",
		"POST /api/files/duplicate",
		"POST /api/files/upload",
		"GET /api/files/download",
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("route registration panicked: %v", r)
		}
	}()
	for _, p := range patterns {
		mux.HandleFunc(p, func(http.ResponseWriter, *http.Request) {})
	}
}
