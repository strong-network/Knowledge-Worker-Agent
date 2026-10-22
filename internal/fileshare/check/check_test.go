// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package check

import (
	"fmt"
	"strings"
	"testing"
)

func whats(r Result) []string {
	out := []string{}
	for _, f := range r.Findings {
		out = append(out, fmt.Sprintf("%v %d %s", f.Blocking, f.Line, f.What))
	}
	return out
}

func TestEachLoadIsABlockingFinding(t *testing.T) {
	for src, want := range map[string]string{
		`<script src="https://cdn.jsdelivr.net/npm/chart.js?token=abc"></script>`:        "Loads a script from cdn.jsdelivr.net",
		`<script src="js/app.js"></script>`:                                              "Loads the script js/app.js from another file",
		`<link rel="stylesheet" href="//fonts.example.com/a.css">`:                       "Loads a style sheet from fonts.example.com",
		`<link rel="shortcut icon" href="favicon.ico">`:                                  "Loads the icon favicon.ico from another file",
		`<link rel="modulepreload" href="https://x.example/m.js">`:                       "Loads a file from x.example",
		`<link rel="manifest" href="app.webmanifest">`:                                   "Loads the manifest app.webmanifest from another file",
		`<img src="images/logo.png">`:                                                    "Shows the picture images/logo.png from another file",
		`<img srcset="data:image/png;base64,AA 1x, https://img.example/b.png 2x">`:       "Shows a picture from img.example",
		`<picture><source srcset="s.webp"></picture>`:                                    "Shows the picture s.webp from another file",
		`<video src="clip.mp4" poster="https://p.example/p.jpg"></video>`:                "Plays the media file clip.mp4 from another file",
		`<audio src="https://a.example/a.mp3"></audio>`:                                  "Plays media from a.example",
		`<track src="captions.vtt">`:                                                     "Loads the captions captions.vtt from another file",
		`<iframe src="https://maps.example/embed"></iframe>`:                             "Shows a page from maps.example",
		`<object data="chart.swf"></object>`:                                             "Embeds the file chart.swf from another file",
		`<input type="image" src="go.png">`:                                              "Shows the picture go.png from another file",
		`<svg><image href="https://i.example/x.png"/></svg>`:                             "Shows a picture from i.example",
		`<svg><use xlink:href="sprites.svg#a"/></svg>`:                                   "Loads the file sprites.svg from another file",
		`<div style="background: url('bg.png')"></div>`:                                  "Uses the image or font bg.png from another file",
		`<style>@font-face { src: url(https://fonts.example/f.woff2) }</style>`:          "Uses an image or font from fonts.example",
		`<style>@import url("https://cdn.example/x.css");</style>`:                       "Loads a style sheet from cdn.example",
		`<base href="https://example.com/">`:                                             "Sets a base address, so its links and files point somewhere else",
		`<meta http-equiv="Refresh" content="0; url=https://example.com">`:               "Sends viewers on to another page",
		`<script type="module">import { a } from "https://esm.example/lib.js";</script>`: "Loads a script from esm.example",
		`<img src="file:///Users/alex/chart.png">`:                                       "Shows a picture from a file on this computer",
	} {
		r := HTML([]byte(src))
		if !r.Blocking || len(r.Findings) == 0 || r.Findings[0].What != want || !r.Findings[0].Blocking {
			t.Errorf("%s\n got %v\nwant %q", src, whats(r), want)
		}
	}
}

func TestWhatLoadsNothingIsntAFinding(t *testing.T) {
	src := `<!doctype html><html><head><style>.a{background:url(data:image/png;base64,AA)} .b{filter:url(#glow)}</style></head>
<body><img src="data:image/svg+xml;utf8,<svg/>"><a href="https://example.com/ticket/1">ticket</a><a href="#top">top</a>
<a href="mailto:a@example.com">mail</a><iframe src="about:blank"></iframe><svg><use href="#icon"/></svg>
<script>const data = JSON.parse(document.getElementById('d').textContent); console.log("https://example.com is just text");</script>
<script type="application/json" id="d">{"a": 1}</script><img src=""></body></html>`
	if r := HTML([]byte(src)); len(r.Findings) != 0 || r.Blocking {
		t.Errorf("a self-contained page has findings: %v", whats(r))
	}
}

func TestLikelyProblemsAreWarnings(t *testing.T) {
	for src, want := range map[string]string{
		`<script>fetch('https://api.example.com/v1/data')</script>`:            "May connect to api.example.com while it runs",
		"<script>const ws = new WebSocket(`wss://live.example/feed`)</script>": "May connect to live.example while it runs",
		`<script>x.open("GET", "https://api.example/q")</script>`:              "May connect to api.example while it runs",
		`<script>navigator.sendBeacon('//stats.example/b')</script>`:           "May connect to stats.example while it runs",
		`<script>import('https://esm.example/x.js')</script>`:                  "May connect to esm.example while it runs",
		`<script>localStorage.setItem('k', 'v')</script>`:                      "Uses browser storage (localStorage), which a shared file can't, so parts may not work",
		`<form action="/submit"><input name="q"></form>`:                       "Has a form, which can't be sent from a shared file",
		`<a href="details.html">details</a>`:                                   "Links to another file, details.html, which won't open",
	} {
		r := HTML([]byte(src))
		if r.Blocking || len(r.Findings) != 1 || r.Findings[0].What != want {
			t.Errorf("%s\n got %v\nwant warning %q", src, whats(r), want)
		}
	}
}

func TestFindingsCarryTheirLineAndAreCountedOnce(t *testing.T) {
	src := "<html>\n<head>\n<script\n  src=\"https://cdn.example/a.js\"></script>\n</head>\n<body>\n" +
		"<style>\n.x { color: red }\n.y { background: url(https://img.example/1.png) }\n</style>\n" +
		"<img src=\"https://img.example/2.png\">\n<img src=\"https://img.example/3.png\">\n" +
		"<script>\n\nfetch('https://api.example/x')\n</script>\n</body></html>"
	r := HTML([]byte(src))
	got := whats(r)
	want := []string{
		"true 3 Loads a script from cdn.example",
		"true 9 Uses an image or font from img.example",
		"true 11 Shows a picture from img.example",
		"false 15 May connect to api.example while it runs",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if r.Findings[2].More != 1 {
		t.Errorf("the second picture from img.example: More = %d", r.Findings[2].More)
	}
}

func TestAnImportIsNotCountedTwice(t *testing.T) {
	r := HTML([]byte(`<style>@import url("https://cdn.example/x.css"); body { background: url(a.png) }</style>`))
	if got := whats(r); len(got) != 2 || !strings.Contains(got[0], "style sheet from cdn.example") || !strings.Contains(got[1], "a.png") {
		t.Errorf("got %v", got)
	}
}

func TestTheListIsCapped(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, `<img src="img%d.png">`, i)
	}
	if r := HTML([]byte(b.String())); len(r.Findings) != maxFindings || !r.Blocking {
		t.Errorf("%d findings", len(r.Findings))
	}
}

func TestOnlyHTMLAndMarkdownAreChecked(t *testing.T) {
	for _, name := range []string{"deck.pdf", "chart.svg", "photo.PNG"} {
		if r := File(name, []byte(`<script src="https://x.example/a.js"></script>`)); len(r.Findings) != 0 || r.Findings == nil {
			t.Errorf("%s: %v", name, r)
		}
	}
	if r := File("Report.HTM", []byte(`<script src="https://x.example/a.js"></script>`)); !r.Blocking {
		t.Error("an .HTM file must be checked")
	}
	for _, name := range []string{"Notes.MD", "notes.markdown"} {
		if r := File(name, []byte("![x](https://x.example/a.png)\n")); !r.Blocking {
			t.Errorf("%s must be checked as Markdown", name)
		}
	}
}

func TestAMarkdownPictureIsALoad(t *testing.T) {
	src := "# Q3\n\nIntro text\nwith ![logo](https://img.example/logo.png?token=abc) inline.\n\n![chart](charts/q3.png)\n\n" +
		"![inline](data:image/png;base64,iVBOR)\n\n[ref pic]: https://ref.example/a.png\n\n![again][ref pic]\n"
	r := Markdown([]byte(src))
	want := []string{
		"true 4 Shows a picture from img.example",
		"true 6 Shows the picture charts/q3.png from another file",
		"true 12 Shows a picture from ref.example",
	}
	if got := whats(r); strings.Join(got, "\n") != strings.Join(want, "\n") || !r.Blocking {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestMarkdownWarnings(t *testing.T) {
	for src, want := range map[string]string{
		"Some <span style=\"color:red\">red</span> text\n":  "Has HTML written into it, which isn't shown",
		"Text <img src=\"https://x.example/a.png\"> here\n": "Has HTML written into it, which isn't shown",
		"<div>\nblock\n</div>\n":                            "Has HTML written into it, which isn't shown",
		"See [details](details.md#a).\n":                    "Links to another file, details.md, which won't open",
		"![chart](data:image/svg+xml;base64,PHN2Zy8+)\n":    "Has a picture in a format that isn't shown: only PNG, JPEG, GIF and WebP are",
	} {
		r := Markdown([]byte(src))
		if r.Blocking || len(r.Findings) != 1 || r.Findings[0].What != want {
			t.Errorf("%q\n got %v\nwant warning %q", src, whats(r), want)
		}
	}
}

// Code shows HTML as text, and comments aren't shown anyway: neither is a
// finding. That needs the Markdown parse, not a search of the text.
func TestSelfContainedMarkdownHasNoFindings(t *testing.T) {
	src := "# Notes\n\n<!-- draft -->\n\nA [ticket](https://example.com/t/1), [top](#notes), <https://example.com>, mail@example.com.\n\n" +
		"![png](data:image/png;base64,iVBOR)\n\n```html\n<script src=\"https://cdn.example/x.js\"></script>\n<img src=\"logo.png\">\n```\n\n" +
		"Inline `<img src=\"https://x.example/a.png\">` code, and a comment <!-- x --> inline.\n\n    ![indented](https://x.example/b.png)\n"
	if r := Markdown([]byte(src)); len(r.Findings) != 0 || r.Blocking {
		t.Errorf("findings: %v", whats(r))
	}
}
