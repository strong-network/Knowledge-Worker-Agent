// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package guest

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A guest's preview and download revalidate, like the owner's: the file under
// a URL changes whenever the agent rewrites it.
func TestGuestFileResponsesRevalidate(t *testing.T) {
	f := newFilesEnv(t, true)
	plan := filepath.Join(f.dir, "docs", "plan.txt")
	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	for _, verb := range []string{"raw", "download"} {
		t.Run(verb, func(t *testing.T) {
			os.WriteFile(plan, []byte("the plan"), 0o644)
			os.Chtimes(plan, old, old)
			code, _, h := f.do("GET", f.url(verb, "docs/plan.txt"), "", nil)
			if code != 200 || h.Get("Cache-Control") != "no-cache" {
				t.Fatalf("got %d with Cache-Control %q, want 200 and no-cache", code, h.Get("Cache-Control"))
			}
			since := map[string]string{"If-Modified-Since": h.Get("Last-Modified")}
			if code, _, _ := f.do("GET", f.url(verb, "docs/plan.txt"), "", since); code != 304 {
				t.Errorf("unchanged file: got %d, want 304", code)
			}
			os.WriteFile(plan, []byte("the new plan"), 0o644)
			if code, body, _ := f.do("GET", f.url(verb, "docs/plan.txt"), "", since); code != 200 || body != "the new plan" {
				t.Errorf("rewritten file: got %d %q, want 200 with the new content", code, body)
			}
		})
	}
}
