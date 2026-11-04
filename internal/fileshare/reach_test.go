// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package fileshare

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// A preview link opens a page only where the Files panel would: not in a
// hidden folder directly in the home folder, and not outside it.
func TestAPreviewLinkStaysInTheHomeFolder(t *testing.T) {
	r := setup(t)
	previewOn(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, path := range []string{filepath.Join(home, ".config", "page.html"), "/etc/page.html"} {
		if _, rec := r.previewLink(t, path, true); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "home folder") {
			t.Errorf("%s: %d %s, want 403", path, rec.Code, rec.Body)
		}
	}
}

// Sharing reaches no further: a chat's folder may be the home folder, whose
// hidden folders stay private, or predate the limit and lie outside it.
func TestSharingStaysInTheHomeFolder(t *testing.T) {
	r := setup(t)
	t.Setenv("HOME", r.folder)
	key := filepath.Join(r.folder, ".ssh", "key.html")
	write(t, key, "<p>key</p>")
	refused := func(where string) {
		t.Helper()
		for name, h := range map[string]http.HandlerFunc{"share": HandleShare, "check": HandleCheck} {
			rec := call(t, h, "POST", "/api/artifacts", "", shareRequest{SessionID: r.chat, Path: where, Audience: db.Audience{}}, true)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "home folder") {
				t.Errorf("%s %s: %d %s, want 403", name, where, rec.Code, rec.Body)
			}
		}
	}
	refused(key)
	t.Setenv("HOME", t.TempDir())
	refused(filepath.Join(r.folder, "report.html"))
	if n := len(db.SharedArtifacts()); n != 0 {
		t.Errorf("%d files shared", n)
	}
}
