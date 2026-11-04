// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package office

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// The preview opens a document only where the Files panel would: not in a
// hidden folder directly in the home folder, and not outside it.
func TestHandlePreviewStaysInTheHomeFolder(t *testing.T) {
	bin, _ := fakeSoffice(t)
	withConverter(t, bin)
	home := t.TempDir()
	t.Setenv("HOME", home)
	hidden := filepath.Join(home, ".local", "plan.docx")
	if err := os.Mkdir(filepath.Dir(hidden), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDoc(t, hidden, "x")
	for _, path := range []string{hidden, "/etc/plan.docx"} {
		if rec := get(t, HandlePreview, path); rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s, want 403", path, rec.Code, rec.Body)
		}
	}
}
