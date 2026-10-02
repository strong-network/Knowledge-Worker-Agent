// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package files

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/durable"
)

// makeShareRoot returns a fresh temp dir scaffolded as a durable share (so it
// contains .system/ and is recognized as a share root).
func makeShareRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := durable.ScaffoldShare(root); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestFileSave_BlocksReservedNameAtShareRoot(t *testing.T) {
	root := makeShareRoot(t)
	for _, name := range []string{"inputs", "working", ".system"} {
		target := filepath.Join(root, name)
		body := `{"path":"` + target + `","content":"x"}`
		req := httptest.NewRequest("POST", "/api/files/save", strings.NewReader(body))
		w := httptest.NewRecorder()
		HandleFileSave(w, req)
		if w.Code != http.StatusConflict {
			t.Errorf("saving reserved root name %q: expected 409, got %d", name, w.Code)
		}
	}
}

func TestFileSave_AllowsReservedNameInSubfolder(t *testing.T) {
	root := makeShareRoot(t)
	// A subfolder is not a share root, so "inputs" is allowed there.
	sub := filepath.Join(root, "deliverables")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(sub, "inputs")
	body := `{"path":"` + target + `","content":"ok"}`
	req := httptest.NewRequest("POST", "/api/files/save", strings.NewReader(body))
	w := httptest.NewRecorder()
	HandleFileSave(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("reserved name in a subfolder should be allowed, got %d: %s", w.Code, w.Body.String())
	}
}

func TestFileSave_AllowsReservedNameInNonShareDir(t *testing.T) {
	// A plain directory (no .system/) is not a share root — no restriction.
	plain := t.TempDir()
	target := filepath.Join(plain, "working")
	body := `{"path":"` + target + `","content":"ok"}`
	req := httptest.NewRequest("POST", "/api/files/save", strings.NewReader(body))
	w := httptest.NewRecorder()
	HandleFileSave(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("reserved name in a non-share dir should be allowed, got %d", w.Code)
	}
}

func TestCreateDirectory_BlocksReservedNameAtShareRoot(t *testing.T) {
	root := makeShareRoot(t)
	target := filepath.Join(root, "working")
	body := `{"path":"` + target + `"}`
	req := httptest.NewRequest("POST", "/api/files/directory", strings.NewReader(body))
	w := httptest.NewRecorder()
	HandleCreateDirectory(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 creating reserved dir at share root, got %d: %s", w.Code, w.Body.String())
	}
	// The message should name the offending folder.
	if !strings.Contains(w.Body.String(), "reserved folder name") {
		t.Errorf("expected a clear reserved-name message, got %q", w.Body.String())
	}
}

func TestRename_BlocksReservedDestinationAtShareRoot(t *testing.T) {
	root := makeShareRoot(t)
	// Create a normal deliverable, then try to rename it onto a reserved name.
	src := filepath.Join(root, "report.md")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "inputs")
	body := `{"src":"` + src + `","dest":"` + dest + `"}`
	req := httptest.NewRequest("POST", "/api/files/rename", strings.NewReader(body))
	w := httptest.NewRecorder()
	HandleFileRename(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 renaming onto reserved root name, got %d: %s", w.Code, w.Body.String())
	}
	// The original file must be untouched.
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source should be preserved after a blocked rename: %v", err)
	}
}

func TestReservedRootCollision_Unit(t *testing.T) {
	root := makeShareRoot(t)
	if _, blocked := durable.ReservedRootCollision(filepath.Join(root, "inputs")); !blocked {
		t.Error("inputs at share root should collide")
	}
	if _, blocked := durable.ReservedRootCollision(filepath.Join(root, "report.md")); blocked {
		t.Error("ordinary name at share root should not collide")
	}
}
