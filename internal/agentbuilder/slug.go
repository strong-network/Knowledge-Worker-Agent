// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package agentbuilder

import "strings"

// Slug converts an agent name into its filename stem / identity key. The
// rule: lowercase the name, replace any run of non-[a-z0-9] characters
// with a single hyphen, and trim leading/trailing hyphens. The slug is the
// collision key (so "My Agent" and "my-agent" collide) and, with ".md"
// appended, is the filename OpenCode uses to resolve the agent.
//
// An empty or all-symbol name yields "" (rejected by the caller / validator).
func Slug(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	prevHyphen := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevHyphen = false
			continue
		}
		// Any other rune (including non-ASCII) collapses into a single hyphen.
		if !prevHyphen {
			b.WriteByte('-')
			prevHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// Filename returns the on-disk filename for an agent name: "<slug>.md". Returns
// "" when the name slugifies to empty (all-symbol / blank), signaling an invalid
// name the caller must reject.
func Filename(name string) string {
	s := Slug(name)
	if s == "" {
		return ""
	}
	return s + ".md"
}
