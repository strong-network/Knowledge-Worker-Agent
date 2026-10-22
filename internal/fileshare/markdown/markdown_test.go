// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package markdown

import (
	"strings"
	"testing"
)

func render(t *testing.T, src string) string {
	t.Helper()
	var b strings.Builder
	if err := Render(&b, []byte(src)); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestTheGitHubFlavourIsRendered(t *testing.T) {
	out := render(t, "# Q3 results\n\n| Team | Done |\n|---|---|\n| A | ~~3~~ 4 |\n\n- [x] shipped\n- [ ] next\n\nSee https://example.com/ticket/1\n")
	for _, want := range []string{
		`<h1 id="q3-results">Q3 results</h1>`, "<table>", "<td><del>3</del> 4</td>",
		`<input checked="" disabled="" type="checkbox"`,
		`<a href="https://example.com/ticket/1" target="_blank" rel="noopener noreferrer">https://example.com/ticket/1</a>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
}

func TestHTMLWrittenIntoItIsLeftOut(t *testing.T) {
	out := render(t, "<script>alert(1)</script>\n\nText <img src=x onerror=alert(1)> more\n\n<div onclick=\"x()\">block</div>\n")
	for _, bad := range []string{"<script", "onerror", "onclick", "<img", "<div"} {
		if strings.Contains(out, bad) {
			t.Errorf("rendered %q:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "Text <!-- raw HTML omitted --> more") {
		t.Errorf("the text around it must stay:\n%s", out)
	}
}

func TestLinksOpenInANewTabExceptWithinThePage(t *testing.T) {
	out := render(t, "[ticket](https://example.com/t/1) [top](#q3) <https://example.com/a>\n")
	for _, want := range []string{
		`<a href="https://example.com/t/1" target="_blank" rel="noopener noreferrer">ticket</a>`,
		`<a href="#q3">top</a>`,
		`<a href="https://example.com/a" target="_blank" rel="noopener noreferrer">https://example.com/a</a>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
}

func TestScriptAddressesAreDropped(t *testing.T) {
	out := render(t, "[x](javascript:alert(1)) ![svg](data:image/svg+xml;base64,PHN2Zy8+) ![png](data:image/png;base64,iVBOR)\n")
	if strings.Contains(out, "javascript:") || strings.Contains(out, "image/svg") {
		t.Errorf("kept a dropped address:\n%s", out)
	}
	if !strings.Contains(out, `<img src="data:image/png;base64,iVBOR" alt="png">`) {
		t.Errorf("a PNG picture must be shown:\n%s", out)
	}
	for dest, want := range map[string]bool{
		"data:image/png;base64,AA": true, "DATA:image/webp;base64,AA": true, "https://example.com/a.png": true, "logo.png": true,
		"data:image/svg+xml,<svg/>": false, "data:text/html,x": false, "javascript:x()": false, "file:///etc/passwd": false,
	} {
		if Shown([]byte(dest)) != want {
			t.Errorf("Shown(%q) = %v", dest, !want)
		}
	}
}
