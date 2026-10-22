// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"net/http"
	"strings"
	"testing"
)

func TestTheLinkScriptGoesFirstAfterTheDoctype(t *testing.T) {
	for _, c := range []struct{ name, src, want string }{
		{"doctype", "<!doctype html>\n<html><a href=x>", "<!doctype html>" + linkScript + "\n<html><a href=x>"},
		{"upper case doctype", `<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Strict//EN"><p>`,
			`<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Strict//EN">` + linkScript + "<p>"},
		{"comment and space first", "<!-- made by the agent -->\n  <!doctype html><p>",
			"<!-- made by the agent -->\n  <!doctype html>" + linkScript + "<p>"},
		{"byte order mark", "\xef\xbb\xbf<!doctype html><p>", "\xef\xbb\xbf<!doctype html>" + linkScript + "<p>"},
		{"no doctype", "<html><head><script>draw()</script>", linkScript + "<html><head><script>draw()</script>"},
		{"comment, no doctype", "<!-- c --><p>", linkScript + "<!-- c --><p>"},
		{"text first", "Q3 <!doctype html>", linkScript + "Q3 <!doctype html>"},
		{"empty", "", linkScript},
	} {
		if got := string(withLinkScript([]byte(c.src))); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestOnlyHTMLGetsTheLinkScript(t *testing.T) {
	h, _ := share(t, "notes.md", "[site](https://example.com)", true)
	for _, path := range []string{"/content", "/download"} {
		if rec := get(h, "GET", path); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), linkScript) {
			t.Errorf("markdown %s: %d %q", path, rec.Code, rec.Body)
		}
	}
	h, _ = share(t, "report.html", "<p>x</p>", true)
	if rec := get(h, "GET", "/download"); rec.Body.String() != "<p>x</p>" {
		t.Errorf("a download is the file as written: %q", rec.Body)
	}
}
