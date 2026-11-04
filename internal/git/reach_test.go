// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package git

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The git routes reach no further than the Files panel: not a hidden folder
// directly in the home folder, and nothing outside it.
func TestTheGitRoutesStayInTheHomeFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	hidden := filepath.Join(home, ".config")
	if err := os.Mkdir(hidden, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{hidden, "/etc"} {
		for name, call := range map[string]func() *httptest.ResponseRecorder{
			"status": func() *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				HandleStatus(w, httptest.NewRequest("GET", "/api/git/status?path="+url.QueryEscape(path), nil))
				return w
			},
			"changes": func() *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				HandleChanges(w, httptest.NewRequest("GET", "/api/git/changes?path="+url.QueryEscape(path), nil))
				return w
			},
			"commit": func() *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				HandleCommit(w, httptest.NewRequest("POST", "/api/git/commit", strings.NewReader(`{"path":"`+path+`","message":"m"}`)))
				return w
			},
			"push": func() *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				HandlePush(w, httptest.NewRequest("POST", "/api/git/push", strings.NewReader(`{"path":"`+path+`"}`)))
				return w
			},
		} {
			if w := call(); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "home folder") {
				t.Errorf("%s %s: %d %s", name, path, w.Code, w.Body.String())
			}
		}
	}
}
