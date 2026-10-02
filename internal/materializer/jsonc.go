// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package materializer

// JSONC preprocessing: the assignment control file is authored as .jsonc
// (JSON with comments and the occasional trailing comma) so admins can annotate
// it. The standard library's encoding/json rejects both, so we strip them
// first. The stripper is string/character-state aware — it does not touch
// comment-like or comma-like sequences that appear inside string literals.

// stripJSONComments removes // line comments and /* */ block comments from a
// JSON document, leaving anything inside string literals intact. Removed
// comment bytes are replaced with nothing (line comments) so byte offsets in
// error messages stay close to the source.
func stripJSONComments(data []byte) []byte {
	out := make([]byte, 0, len(data))
	const (
		normal = iota
		inString
		inLine
		inBlock
	)
	state := normal
	escaped := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch state {
		case normal:
			if c == '"' {
				state = inString
				out = append(out, c)
			} else if c == '/' && i+1 < len(data) && data[i+1] == '/' {
				state = inLine
				i++
			} else if c == '/' && i+1 < len(data) && data[i+1] == '*' {
				state = inBlock
				i++
			} else {
				out = append(out, c)
			}
		case inString:
			out = append(out, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				state = normal
			}
		case inLine:
			if c == '\n' {
				state = normal
				out = append(out, c)
			}
		case inBlock:
			if c == '*' && i+1 < len(data) && data[i+1] == '/' {
				state = normal
				i++
			}
		}
	}
	return out
}

// stripTrailingCommas removes commas that are immediately followed (ignoring
// whitespace) by a closing } or ]. It is string-literal aware. Run this after
// stripJSONComments.
func stripTrailingCommas(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	escaped := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out = append(out, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(data) {
				switch data[j] {
				case ' ', '\t', '\r', '\n':
					j++
					continue
				}
				break
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				// Drop this trailing comma.
				continue
			}
		}
		out = append(out, c)
	}
	return out
}
