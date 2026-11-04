// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package versions

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
)

// A file's versions are as far out of reach as the file: not in a hidden
// folder directly in the home folder, and not outside it.
func TestTheVersionRoutesStayInTheHomeFolder(t *testing.T) {
	setup(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, path := range []string{filepath.Join(home, ".ssh", "id_ed25519"), "/etc/passwd"} {
		for name, handle := range map[string]http.HandlerFunc{"list": HandleList, "content": HandleContent} {
			w := httptest.NewRecorder()
			handle(w, httptest.NewRequest("GET", "/api/files/versions?v=1&path="+url.QueryEscape(path), nil))
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s: %d %s, want 403", name, path, w.Code, w.Body)
			}
		}
	}
}
