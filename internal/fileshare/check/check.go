// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package check tells the owner, before a file is shared, what in it would
// not work for viewers: anything it loads from the internet or from another
// file. It is advice, not the boundary: the viewer's content policy refuses
// those loads whatever this finds, and a script can build a URL while it runs.
package check

import (
	"bytes"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// Finding is one thing that won't work for viewers. Blocking findings are
// certain loads; the rest are likely problems.
type Finding struct {
	Blocking bool   `json:"blocking"`
	What     string `json:"what"`
	Line     int    `json:"line"`
	// More counts further places with the same finding.
	More int `json:"more,omitempty"`
	// File is the other file a blocking finding loads, as the document names it.
	File string `json:"-"`
}

// Result is a file's findings, in the order they first appear.
type Result struct {
	Findings []Finding `json:"findings"`
	// Blocking is whether any finding is: the file isn't self-contained.
	Blocking bool `json:"blocking"`
}

// maxFindings keeps the list readable; the first ones are what to fix first.
const maxFindings = 40

// File checks a file by its type. Only HTML and Markdown can load anything.
func File(name string, data []byte) Result {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".html", ".htm":
		return HTML(data)
	case ".md", ".markdown":
		return Markdown(data)
	}
	return Result{Findings: []Finding{}}
}

// noun is how a finding names what is loaded.
type noun struct{ verb, a, the string }

var (
	script   = noun{"Loads", "a script", "the script"}
	style    = noun{"Loads", "a style sheet", "the style sheet"}
	icon     = noun{"Loads", "an icon", "the icon"}
	file     = noun{"Loads", "a file", "the file"}
	manifest = noun{"Loads", "a manifest", "the manifest"}
	picture  = noun{"Shows", "a picture", "the picture"}
	media    = noun{"Plays", "media", "the media file"}
	captions = noun{"Loads", "captions", "the captions"}
	page     = noun{"Shows", "a page", "the page"}
	embedded = noun{"Embeds", "content", "the file"}
	cssURL   = noun{"Uses", "an image or font", "the image or font"}
)

type collector struct {
	res   Result
	index map[string]int
}

func (c *collector) add(blocking bool, what string, line int) {
	c.addFile(blocking, what, line, "")
}

func (c *collector) addFile(blocking bool, what string, line int, file string) {
	key := fmt.Sprint(blocking, what)
	if i, ok := c.index[key]; ok {
		c.res.Findings[i].More++
		return
	}
	if len(c.res.Findings) >= maxFindings {
		return
	}
	c.index[key] = len(c.res.Findings)
	c.res.Findings = append(c.res.Findings, Finding{Blocking: blocking, What: what, Line: line, File: file})
	c.res.Blocking = c.res.Blocking || blocking
}

// load notes that ref is loaded as n, when it is a URL or another file.
func (c *collector) load(n noun, ref string, line int) {
	external, where, ok := classify(ref)
	if !ok {
		return
	}
	if external {
		c.add(true, fmt.Sprintf("%s %s from %s", n.verb, n.a, where), line)
	} else {
		c.addFile(true, fmt.Sprintf("%s %s %s from another file", n.verb, n.the, where), line, where)
	}
}

// classify says whether ref loads something from elsewhere, and from where:
// a host for a URL (never the whole URL, whose query can carry a token), or
// a path for another file. Data, blob, fragment and about: refs load nothing.
func classify(ref string) (external bool, where string, ok bool) {
	ref = strings.TrimSpace(ref)
	low := strings.ToLower(ref)
	for _, p := range []string{"data:", "blob:", "#", "about:", "javascript:", "mailto:", "tel:"} {
		if strings.HasPrefix(low, p) {
			return false, "", false
		}
	}
	if ref == "" {
		return false, "", false
	}
	u, err := url.Parse(ref)
	if err != nil {
		path, _, _ := strings.Cut(ref, "?")
		path, _, _ = strings.Cut(path, "#")
		return false, path, true
	}
	switch {
	case u.Scheme == "file":
		return true, "a file on this computer", true
	case u.Host != "":
		return true, u.Hostname(), true
	case u.Scheme != "":
		return true, u.Scheme + ": addresses", true
	case u.Path == "":
		return false, "", false
	}
	return false, u.Path, true
}

// isFileLink is a link to another file, as opposed to a web page or a place
// in this one.
func isFileLink(href string) (string, bool) {
	external, where, ok := classify(href)
	return where, ok && !external
}

func attr(t html.Token, key string) (string, bool) {
	for _, a := range t.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

// HTML checks an HTML document.
func HTML(src []byte) Result {
	c := &collector{res: Result{Findings: []Finding{}}, index: map[string]int{}}
	z := html.NewTokenizer(bytes.NewReader(src))
	line := 1
	inScript, inStyle, module := false, false, false
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		start := line
		line += bytes.Count(z.Raw(), []byte{'\n'})
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			c.element(t, start)
			switch t.Data {
			case "script":
				typ, _ := attr(t, "type")
				inScript, module = tt == html.StartTagToken, strings.EqualFold(strings.TrimSpace(typ), "module")
			case "style":
				inStyle = tt == html.StartTagToken
			}
		case html.EndTagToken:
			inScript, inStyle = false, false
		case html.TextToken:
			text := string(z.Text())
			switch {
			case inScript:
				c.script(text, start, module)
			case inStyle:
				c.css(text, start)
			}
		}
	}
	return c.res
}

func (c *collector) element(t html.Token, line int) {
	get := func(k string) string { v, _ := attr(t, k); return v }
	if s, ok := attr(t, "style"); ok {
		c.css(s, line)
	}
	if b, ok := attr(t, "background"); ok {
		c.load(picture, b, line)
	}
	switch t.Data {
	case "script":
		if src, ok := attr(t, "src"); ok {
			c.load(script, src, line)
		}
	case "link":
		href, ok := attr(t, "href")
		if !ok {
			return
		}
		rels := strings.Fields(strings.ToLower(get("rel")))
		for _, rel := range rels {
			switch {
			case rel == "stylesheet":
				c.load(style, href, line)
			case strings.Contains(rel, "icon"):
				c.load(icon, href, line)
			case rel == "preload" || rel == "modulepreload":
				c.load(file, href, line)
			case rel == "manifest":
				c.load(manifest, href, line)
			default:
				continue
			}
			break
		}
	case "img":
		c.load(picture, get("src"), line)
		c.srcset(get("srcset"), line)
	case "source":
		c.load(media, get("src"), line)
		c.srcset(get("srcset"), line)
	case "video":
		c.load(media, get("src"), line)
		c.load(picture, get("poster"), line)
	case "audio":
		c.load(media, get("src"), line)
	case "track":
		c.load(captions, get("src"), line)
	case "iframe", "frame":
		c.load(page, get("src"), line)
	case "embed":
		c.load(embedded, get("src"), line)
	case "object":
		c.load(embedded, get("data"), line)
	case "input":
		if strings.EqualFold(get("type"), "image") {
			c.load(picture, get("src"), line)
		}
	case "image", "use":
		n := picture
		if t.Data == "use" {
			n = file
		}
		c.load(n, get("href"), line)
		c.load(n, get("xlink:href"), line)
	case "base":
		if _, ok := attr(t, "href"); ok {
			c.add(true, "Sets a base address, so its links and files point somewhere else", line)
		}
	case "meta":
		if strings.EqualFold(get("http-equiv"), "refresh") && strings.Contains(strings.ToLower(get("content")), "url=") {
			c.add(true, "Sends viewers on to another page", line)
		}
	case "form":
		c.add(false, "Has a form, which can't be sent from a shared file", line)
	case "a":
		if where, ok := isFileLink(get("href")); ok {
			c.add(false, fmt.Sprintf("Links to another file, %s, which won't open", where), line)
		}
	}
}

// srcset reads candidates as browsers do: a URL is a run without whitespace
// (a data: URL can hold commas), and its descriptors run to the next comma.
func (c *collector) srcset(v string, line int) {
	space := func(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' }
	for i := 0; i < len(v); {
		for i < len(v) && (space(v[i]) || v[i] == ',') {
			i++
		}
		start := i
		for i < len(v) && !space(v[i]) {
			i++
		}
		u := v[start:i]
		ended := strings.HasSuffix(u, ",")
		if u = strings.TrimRight(u, ","); u != "" {
			c.load(picture, u, line)
		}
		if ended {
			continue
		}
		for depth := 0; i < len(v); i++ {
			if v[i] == '(' {
				depth++
			} else if v[i] == ')' {
				depth--
			} else if v[i] == ',' && depth <= 0 {
				i++
				break
			}
		}
	}
}

var (
	cssImport = regexp.MustCompile(`(?i)@import\s+(?:url\(\s*)?["']?([^"')\s;]+)["']?\s*\)?[^;]*;?`)
	cssURLRe  = regexp.MustCompile(`(?i)url\(\s*(?:"([^"]*)"|'([^']*)'|([^)\s]*))\s*\)`)
)

func (c *collector) css(text string, line int) {
	at := func(i int) int { return line + strings.Count(text[:i], "\n") }
	var imports [][2]int
	for _, m := range cssImport.FindAllStringSubmatchIndex(text, -1) {
		imports = append(imports, [2]int{m[0], m[1]})
		c.load(style, text[m[2]:m[3]], at(m[0]))
	}
	for _, m := range cssURLRe.FindAllStringSubmatchIndex(text, -1) {
		inImport := false
		for _, r := range imports {
			inImport = inImport || (m[0] >= r[0] && m[0] < r[1])
		}
		if inImport {
			continue
		}
		for g := 2; g < len(m); g += 2 {
			if m[g] >= 0 {
				c.load(cssURL, text[m[g]:m[g+1]], at(m[0]))
				break
			}
		}
	}
}

var (
	staticImport = regexp.MustCompile(`(?m)\bimport\s+(?:[\w*{}\s,$]+\s+from\s+)?["']([^"']+)["']`)
	connects     = []*regexp.Regexp{
		regexp.MustCompile(`\b(?:fetch|importScripts|sendBeacon|import)\s*\(\s*["'\x60]((?:https?:)?//[^"'\x60\s/]+)`),
		regexp.MustCompile(`\bnew\s+(?:WebSocket|EventSource)\s*\(\s*["'\x60]((?:wss?:|https?:)?//[^"'\x60\s/]+)`),
		regexp.MustCompile(`\.open\s*\(\s*["'][A-Za-z]+["']\s*,\s*["'\x60]((?:https?:)?//[^"'\x60\s/]+)`),
	}
	storage = regexp.MustCompile(`\b(localStorage|sessionStorage|indexedDB)\b`)
)

func (c *collector) script(text string, line int, module bool) {
	at := func(i int) int { return line + strings.Count(text[:i], "\n") }
	if module {
		for _, m := range staticImport.FindAllStringSubmatchIndex(text, -1) {
			c.load(script, text[m[2]:m[3]], at(m[0]))
		}
	}
	for _, re := range connects {
		for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
			if _, host, ok := classify(text[m[2]:m[3]]); ok {
				c.add(false, fmt.Sprintf("May connect to %s while it runs", host), at(m[0]))
			}
		}
	}
	for _, m := range storage.FindAllStringSubmatchIndex(text, -1) {
		c.add(false, fmt.Sprintf("Uses browser storage (%s), which a shared file can't, so parts may not work", text[m[2]:m[3]]), at(m[0]))
	}
}
