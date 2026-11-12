// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package skilltokens

import (
	"net/url"
	"strconv"
	"strings"
)

// Declaration is the token a skill asks for.
type Declaration struct {
	Name  string
	Label string
	Help  string
	URL   string
}

const (
	keyName  = "kwa-token"
	keyLabel = "kwa-token-label"
	keyHelp  = "kwa-token-help"
	keyURL   = "kwa-token-url"

	maxLabel = 80
	maxHelp  = 400
)

// Declared returns the token a SKILL.md declares in its front matter's
// metadata, if it declares one with a valid name. A link that isn't https is
// left out.
func Declared(content string) (Declaration, bool) {
	meta := metadata(content)
	d := Declaration{Name: meta[keyName]}
	if !ValidName(d.Name) {
		return Declaration{}, false
	}
	d.Label = clip(meta[keyLabel], maxLabel)
	d.Help = clip(meta[keyHelp], maxHelp)
	if u, err := url.Parse(meta[keyURL]); err == nil && u.Scheme == "https" && u.Host != "" {
		d.URL = u.String()
	}
	return d, true
}

// metadata reads the string values in the front matter's metadata map: the
// lines indented under "metadata:", each "key: value".
func metadata(content string) map[string]string {
	content = strings.TrimPrefix(content, "\ufeff")
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil
	}
	out := map[string]string{}
	in := false
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			break
		}
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		indented := line[0] == ' ' || line[0] == '\t'
		if !indented {
			in = strings.TrimSpace(stripComment(line)) == "metadata:"
			continue
		}
		if !in {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		out[strings.TrimSpace(key)] = scalar(value)
	}
	return out
}

// scalar reads a YAML scalar on one line: plain, single-quoted or
// double-quoted.
func scalar(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"':
		if v, err := strconv.Unquote(s); err == nil {
			return v
		}
		return s[1 : len(s)-1]
	case len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'':
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
	}
	return strings.TrimSpace(stripComment(s))
}

// stripComment drops a comment: a # after a space.
func stripComment(s string) string {
	if i := strings.Index(s, " #"); i >= 0 {
		return s[:i]
	}
	return s
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
