// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"bytes"
	_ "embed"
	"html/template"
	"io"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/fileshare/markdown"
)

// pageTmpl is the viewer page: a slim bar above the file. Its own script and
// style run by nonce; the file runs framed from /content under its own policy.
var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>{{if .Name}}{{.Name}}{{else}}Shared file{{end}}</title>
<style nonce="{{.Nonce}}">
:root { color-scheme: light dark; --bg: #fff; --bar: #f6f7f9; --line: #e3e5e8; --text: #1f2328; --muted: #59636e; --accent: #0969da; }
@media (prefers-color-scheme: dark) { :root { --bg: #0d1117; --bar: #161b22; --line: #30363d; --text: #e6edf3; --muted: #9198a1; --accent: #4493f8; } }
* { box-sizing: border-box; }
html, body { margin: 0; height: 100%; background: var(--bg); color: var(--text); font: 14px/1.4 system-ui, -apple-system, "Segoe UI", sans-serif; }
body { display: flex; flex-direction: column; }
.bar { display: flex; align-items: center; gap: 14px; padding: 8px 14px; background: var(--bar); border-bottom: 1px solid var(--line); flex-wrap: wrap; }
.name { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 40vw; }
.muted { color: var(--muted); }
.spacer { flex: 1; }
button, .button { font: inherit; padding: 4px 10px; border-radius: 6px; border: 1px solid var(--line); background: var(--bg); color: var(--text); cursor: pointer; text-decoration: none; }
button.accent { border-color: var(--accent); color: var(--accent); }
main { flex: 1; min-height: 0; display: flex; }
iframe { flex: 1; border: 0; width: 100%; height: 100%; background: #fff; }
.image { flex: 1; overflow: auto; display: flex; align-items: flex-start; justify-content: center; padding: 16px; }
.image img { max-width: 100%; height: auto; }
.problem { margin: auto; padding: 24px; text-align: center; color: var(--muted); }
[hidden] { display: none !important; }
</style>
</head>
<body>
{{if .Problem}}
<main><p class="problem" role="status">{{.Problem}}</p></main>
{{else}}
<header class="bar">
<span class="name" title="{{.Name}}">{{.Name}}</span>
{{if .Owner}}<span class="muted">Shared by {{.Owner}}</span>{{end}}
<span class="muted">Updated <time id="updated" datetime="{{.Updated}}">{{.Updated}}</time></span>
<span class="spacer"></span>
<button id="reload" class="accent" type="button" hidden>Updated — Reload</button>
<a id="download" class="button" href="/download" {{if not .Download}}hidden{{end}}>Download</a>
</header>
<main>
{{if or (eq .Kind "html") (eq .Kind "markdown")}}<iframe src="/content" sandbox="{{.Sandbox}}" title="{{.Name}}"></iframe>
{{else if eq .Kind "pdf"}}<iframe src="/content" title="{{.Name}}"></iframe>
{{else}}<div class="image"><img src="/content" alt="{{.Name}}"></div>{{end}}
</main>
<script nonce="{{.Nonce}}">
(function () {
  var shown = document.getElementById('updated');
  var loaded = shown.getAttribute('datetime');
  function when(iso) { var d = new Date(iso); return isNaN(d) ? iso : d.toLocaleString(); }
  shown.textContent = when(loaded);
  var reload = document.getElementById('reload');
  reload.addEventListener('click', function () { location.reload(); });
  var download = document.getElementById('download');
  function check() {
    fetch('/meta', { cache: 'no-store' }).then(function (r) {
      if (r.status === 404) { location.reload(); return null; }
      return r.ok ? r.json() : null;
    }).then(function (m) {
      if (!m) { return; }
      reload.hidden = m.updated === loaded;
      download.hidden = !m.download;
    }).catch(function () {});
  }
  setInterval(check, 60000);
})();
</script>
{{end}}
</body>
</html>
`))

// githubMarkdownCSS is github-markdown-css's stylesheet, as the app's own
// preview uses it: light or dark, following the viewer's system.
//
//go:embed github-markdown.css
var githubMarkdownCSS string

// markdownTmpl is a Markdown file as viewers see it.
var markdownTmpl = template.Must(template.New("markdown").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Name}}</title>
<style>{{.CSS}}</style>
<style>
.markdown-body { box-sizing: border-box; min-width: 200px; max-width: 980px; margin: 0 auto; padding: 32px 45px; }
@media (max-width: 767px) { .markdown-body { padding: 16px; } }
</style>
</head>
<body class="markdown-body">
{{.Body}}</body>
</html>
`))

// markdownPage renders a Markdown file into its page.
func markdownPage(f io.Reader, name string) ([]byte, error) {
	src, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	var body, page bytes.Buffer
	if err := markdown.Render(&body, src); err != nil {
		return nil, err
	}
	// The body is goldmark's output without the HTML written into the file:
	// its text is escaped, so it is trusted here.
	err = markdownTmpl.Execute(&page, struct {
		Name string
		CSS  template.CSS
		Body template.HTML
	}{name, template.CSS(githubMarkdownCSS), template.HTML(body.String())})
	return page.Bytes(), err
}
