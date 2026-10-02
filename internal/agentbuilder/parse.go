// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package agentbuilder

import (
	"strconv"
	"strings"
)

// Parse reads an agent file's raw bytes (frontmatter + body) into an Agent.
// name is the file stem (OpenCode identity), assigned to Agent.Name so callers
// that read from disk get a fully-populated struct.
//
// It is a focused frontmatter reader, not a general YAML engine: it handles the
// scalar forms the builder emits (plain / single- / double-quoted) and a single
// level of nested `permission:` entries in the v1 blanket form. A per-pattern
// permission value is reduced to its "*" entry (the blanket action) when present.
// Anything it cannot interpret is surfaced by Validate, not silently accepted.
//
// The returned bool reports whether a frontmatter block was found and closed;
// a body-only file parses as an Agent with just Prompt set.
func Parse(name string, data []byte) (*Agent, bool) {
	content := string(data)
	a := &Agent{Name: name}

	front, body, ok := splitFrontmatter(content)
	if !ok {
		a.Prompt = strings.TrimSpace(content)
		return a, false
	}
	a.Prompt = strings.TrimSpace(body)

	lines := strings.Split(front, "\n")
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		if strings.TrimSpace(raw) == "" {
			continue
		}
		// Only top-level (unindented) keys are handled here; nested lines are
		// consumed by their parent (permission).
		if raw[0] == ' ' || raw[0] == '\t' {
			continue
		}
		key, val := splitKeyValue(raw)
		if key == "" {
			continue
		}
		switch key {
		case "description":
			a.Description = unquote(val)
		case "mode":
			a.Mode = Mode(unquote(val))
		case "model":
			a.Model = unquote(val)
		case "temperature":
			if f, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil {
				a.Temperature = &f
			}
		case "top_p":
			if f, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil {
				a.TopP = &f
			}
		case "steps":
			if n, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
				a.Steps = &n
			}
		case "color":
			a.Color = unquote(val)
		case "hidden":
			a.Hidden = parseBool(val)
		case "disable":
			a.Disable = parseBool(val)
		case "permission":
			perm, consumed := parsePermission(lines[i+1:])
			if perm != nil {
				a.Permission = perm
			}
			i += consumed
		}
	}
	return a, true
}

// parsePermission consumes the indented block following a `permission:` key.
// It returns the parsed Permission (nil if empty) and the number of lines
// consumed. Each entry is `  <cap>: <action>` or the wildcard `  "*": <action>`.
// A nested per-pattern map (deeper indentation) is scanned for its "*" value.
func parsePermission(lines []string) (*Permission, int) {
	p := &Permission{Rules: map[string]Action{}}
	consumed := 0
	for _, raw := range lines {
		if strings.TrimSpace(raw) == "" {
			consumed++
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " \t"))
		if indent == 0 {
			break // back to a top-level key; stop.
		}
		if indent > 2 {
			// Deeper nesting belongs to a per-pattern map handled below via its
			// parent line; skip it here.
			consumed++
			continue
		}
		consumed++
		key, val := splitKeyValue(raw)
		key = unquote(key)
		if key == "" {
			continue
		}
		act := Action(strings.TrimSpace(unquote(val)))
		if act == "" {
			// Value may be a nested map on following lines; capture its "*".
			continue
		}
		if key == "*" {
			p.Global = act
		} else {
			p.Rules[key] = act
		}
	}
	if p.Global == "" && len(p.Rules) == 0 {
		return nil, consumed
	}
	return p, consumed
}

// splitFrontmatter separates a leading "---\n ... \n---" block from the body.
// Returns (frontmatter, body, true) when a well-formed fence is present.
func splitFrontmatter(content string) (front, body string, ok bool) {
	// Tolerate a leading BOM / whitespace before the opening fence.
	trimmed := strings.TrimLeft(content, "\ufeff \t\r\n")
	if !strings.HasPrefix(trimmed, "---") {
		return "", content, false
	}
	rest := trimmed[3:]
	// The opening fence must be followed by end-of-line.
	nl := strings.IndexByte(rest, '\n')
	if nl == -1 {
		return "", content, false
	}
	rest = rest[nl+1:]
	// Find a closing fence line: a line that is exactly "---" (optionally with
	// trailing spaces / CR).
	idx := findClosingFence(rest)
	if idx == -1 {
		return "", content, false
	}
	front = rest[:idx]
	// Advance body past the closing fence line.
	after := rest[idx:]
	if nl2 := strings.IndexByte(after, '\n'); nl2 != -1 {
		body = after[nl2+1:]
	}
	return front, body, true
}

// findClosingFence returns the byte offset of a line consisting solely of "---"
// within s, or -1. s is expected to start at the beginning of a line.
func findClosingFence(s string) int {
	offset := 0
	for _, line := range strings.SplitAfter(s, "\n") {
		trimmed := strings.TrimRight(line, "\r\n")
		if strings.TrimRight(trimmed, " \t") == "---" {
			return offset
		}
		offset += len(line)
	}
	return -1
}

// splitKeyValue splits a "key: value" line into its key and (raw, untrimmed of
// quotes) value. Returns ("", "") when there is no colon separator.
func splitKeyValue(line string) (key, val string) {
	// Strip a trailing comment only for unquoted values is risky; the builder
	// never writes comments, so we keep it simple and split on the first colon.
	idx := strings.IndexByte(line, ':')
	if idx == -1 {
		return "", ""
	}
	key = strings.TrimSpace(line[:idx])
	val = strings.TrimSpace(line[idx+1:])
	return key, val
}

// unquote reverses yamlQuote for double-quoted scalars and strips single quotes
// for the single-quoted form; plain scalars are returned as-is.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		inner := s[1 : len(s)-1]
		var b strings.Builder
		b.Grow(len(inner))
		for i := 0; i < len(inner); i++ {
			if inner[i] == '\\' && i+1 < len(inner) {
				i++
				switch inner[i] {
				case 'n':
					b.WriteByte('\n')
				case 'r':
					b.WriteByte('\r')
				case 't':
					b.WriteByte('\t')
				case '"':
					b.WriteByte('"')
				case '\\':
					b.WriteByte('\\')
				default:
					b.WriteByte(inner[i])
				}
				continue
			}
			b.WriteByte(inner[i])
		}
		return b.String()
	}
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		// YAML single-quote: '' is an escaped single quote.
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
	}
	return s
}

func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(unquote(s))) {
	case "true", "yes", "on", "1":
		return true
	default:
		return false
	}
}
