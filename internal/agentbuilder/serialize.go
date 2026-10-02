// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package agentbuilder

import (
	"strconv"
	"strings"
)

// Serialize renders an Agent to the exact single-file form OpenCode consumes:
// YAML frontmatter between "---" delimiters followed by the prompt body.
//
// Determinism & safety guarantees:
//   - Fixed key order (description, mode, model, temperature, top_p, steps,
//     color, hidden, disable, permission).
//   - Every optional field is omitted when unset (absence = OpenCode default);
//     no placeholder values are ever written.
//   - String scalars are emitted as double-quoted YAML with proper escaping, so
//     arbitrary user text (quotes, colons, newlines, #) can never break parsing.
//   - Name is NOT written (it is the filename); Prompt is the body, not a key.
//
// The result always ends with a single trailing newline.
func Serialize(a *Agent) string {
	var b strings.Builder
	b.WriteString("---\n")

	if a.Description != "" {
		b.WriteString("description: ")
		b.WriteString(yamlQuote(a.Description))
		b.WriteByte('\n')
	}
	if a.Mode != "" {
		b.WriteString("mode: ")
		b.WriteString(string(a.Mode))
		b.WriteByte('\n')
	}
	if a.Model != "" {
		b.WriteString("model: ")
		b.WriteString(yamlQuote(a.Model))
		b.WriteByte('\n')
	}
	if a.Temperature != nil {
		b.WriteString("temperature: ")
		b.WriteString(formatFloat(*a.Temperature))
		b.WriteByte('\n')
	}
	if a.TopP != nil {
		b.WriteString("top_p: ")
		b.WriteString(formatFloat(*a.TopP))
		b.WriteByte('\n')
	}
	if a.Steps != nil {
		b.WriteString("steps: ")
		b.WriteString(strconv.Itoa(*a.Steps))
		b.WriteByte('\n')
	}
	if a.Color != "" {
		b.WriteString("color: ")
		b.WriteString(yamlQuote(a.Color))
		b.WriteByte('\n')
	}
	if a.Hidden {
		b.WriteString("hidden: true\n")
	}
	if a.Disable {
		b.WriteString("disable: true\n")
	}
	if !a.Permission.IsZero() {
		b.WriteString(serializePermission(a.Permission))
	}

	b.WriteString("---\n")

	// Body = prompt. Separate frontmatter from body with a blank line for
	// readability; trim any leading/trailing whitespace off the prompt itself
	// and guarantee exactly one trailing newline.
	prompt := strings.TrimSpace(a.Prompt)
	if prompt != "" {
		b.WriteByte('\n')
		b.WriteString(prompt)
		b.WriteByte('\n')
	}
	return b.String()
}

// serializePermission renders the permission block. The global default is the
// top-level "*" wildcard key (OpenCode's StructWithRest rest key); per-capability
// overrides follow in canonical order. All values are plain string actions (v1
// blanket form). "*" is quoted because a bare * is a YAML alias indicator.
func serializePermission(p *Permission) string {
	var b strings.Builder
	b.WriteString("permission:\n")
	if p.Global != "" {
		b.WriteString(`  "*": `)
		b.WriteString(string(p.Global))
		b.WriteByte('\n')
	}
	for _, cap := range PermissionCapabilities {
		if act, ok := p.Rules[cap]; ok && act != "" {
			b.WriteString("  ")
			b.WriteString(cap)
			b.WriteString(": ")
			b.WriteString(string(act))
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// yamlQuote returns s as a double-quoted YAML scalar with the escapes YAML
// requires inside double quotes. Newlines become "\n", so multi-line values stay
// on one line and cannot terminate the frontmatter early.
func yamlQuote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// formatFloat renders a float without a trailing ".0" being forced and without
// exponent notation for the small values used here.
func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}
