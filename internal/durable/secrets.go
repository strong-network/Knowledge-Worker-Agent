// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package durable

import (
	"os"
	"path/filepath"
	"strings"
)

// Secret exclusion (a hard requirement).
//
// The durable store must never receive secrets or credential material: once
// pushed to a remote, secrets are hard to purge from history. The platform injects
// secrets without exposing them, but an agent or user may still drop credential
// files into the workspace. Before the first sync we write a managed block of
// ignore patterns into the workspace's .gitignore so common secret material is
// excluded from every commit.
//
// The block is delimited so it can be refreshed idempotently without disturbing
// any user-authored .gitignore entries around it.

const (
	secretBlockBegin = "# >>> Knowledge Worker Agent secret exclusions (managed) >>>"
	secretBlockEnd   = "# <<< Knowledge Worker Agent secret exclusions (managed) <<<"
)

// secretBlockMarkers are the block's delimiters, current first. The second pair
// is their text before the rename, so a refresh replaces an old block in place.
var secretBlockMarkers = [][2]string{
	{secretBlockBegin, secretBlockEnd},
	{"# >>> SDS durable-store secret exclusions (managed) >>>", "# <<< SDS durable-store secret exclusions (managed) <<<"},
}

// secretPatterns are gitignore patterns for well-known secret/credential paths
// and file types. Kept deliberately broad for common cases; the precise rule
// set is expected to evolve. Patterns are anchored where a false positive would
// be costly and left loose where matching anywhere is desired.
var secretPatterns = []string{
	// Private keys and certificates.
	"*.pem",
	"*.key",
	"*.pfx",
	"*.p12",
	"id_rsa",
	"id_dsa",
	"id_ecdsa",
	"id_ed25519",
	// Cloud / provider credentials.
	".aws/credentials",
	".azure/",
	"gcloud-service-key.json",
	"*serviceaccount*.json",
	// Environment / dotenv secrets (but not .env.example / .env.sample).
	".env",
	".env.*",
	"!.env.example",
	"!.env.sample",
	// SSH and git credential stores.
	".ssh/",
	".git-credentials",
	".netrc",
	// Tool/config secret stores that live under the workspace.
	".copilot/config.json",
	".copilot/mcp-oauth.json",
	// Generic catch-alls.
	"*.secret",
	"secrets.*",
	"*_token",
	"*.token",
}

// EnsureSecretExclusions writes (or refreshes) the managed secret-exclusion
// block in <shareRoot>/.gitignore. It preserves any content outside the managed
// block and is idempotent. Creating the file if absent.
func EnsureSecretExclusions(shareRoot string) error {
	if strings.TrimSpace(shareRoot) == "" {
		return os.ErrInvalid
	}
	path := filepath.Join(shareRoot, ".gitignore")

	existing := ""
	if data, err := os.ReadFile(path); err == nil {
		existing = string(data)
	} else if !os.IsNotExist(err) {
		return err
	}

	managed := buildSecretBlock()
	updated := replaceManagedBlock(existing, managed)
	return os.WriteFile(path, []byte(updated), 0o644)
}

// buildSecretBlock renders the delimited managed block (begin/end markers plus
// the patterns), ending with a trailing newline.
func buildSecretBlock() string {
	var b strings.Builder
	b.WriteString(secretBlockBegin)
	b.WriteByte('\n')
	for _, p := range secretPatterns {
		b.WriteString(p)
		b.WriteByte('\n')
	}
	b.WriteString(secretBlockEnd)
	b.WriteByte('\n')
	return b.String()
}

// replaceManagedBlock returns existing with the managed block replaced (if
// present) or appended (if not). Content outside the markers is preserved. If
// the file had content and did not end in a newline, one is inserted before an
// appended block.
func replaceManagedBlock(existing, managed string) string {
	for _, m := range secretBlockMarkers {
		begin := strings.Index(existing, m[0])
		if begin < 0 {
			continue
		}
		end := strings.Index(existing, m[1])
		if end >= 0 {
			end += len(m[1])
			// Consume a single trailing newline after the end marker so we don't
			// accumulate blank lines across refreshes.
			if end < len(existing) && existing[end] == '\n' {
				end++
			}
			return existing[:begin] + managed + existing[end:]
		}
		// Corrupt (begin without end): drop from begin and re-append cleanly.
		existing = existing[:begin]
	}

	if existing == "" {
		return managed
	}
	sep := ""
	if !strings.HasSuffix(existing, "\n") {
		sep = "\n"
	}
	return existing + sep + managed
}

// IsSecretPath reports whether a workspace-relative path matches one of the
// secret patterns (best-effort, used for surfacing warnings). It is a
// convenience check and not the enforcement mechanism — .gitignore is.
// Negation patterns (e.g. "!.env.example") un-flag an otherwise-matching path,
// mirroring gitignore semantics.
func IsSecretPath(rel string) bool {
	rel = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(rel)), "./")
	base := filepath.Base(rel)

	// A negation match wins: the path is explicitly allowed.
	for _, p := range secretPatterns {
		if !strings.HasPrefix(p, "!") {
			continue
		}
		neg := strings.TrimPrefix(p, "!")
		if matchesPattern(neg, rel, base) {
			return false
		}
	}
	for _, p := range secretPatterns {
		if strings.HasPrefix(p, "!") {
			continue
		}
		if matchesPattern(p, rel, base) {
			return true
		}
	}
	return false
}

// matchesPattern is a small gitignore-ish matcher: directory patterns (trailing
// slash) match a path prefix; patterns with a slash match the full relative
// path; bare patterns match the basename. Glob metacharacters are honored via
// filepath.Match.
func matchesPattern(pattern, rel, base string) bool {
	if strings.HasSuffix(pattern, "/") {
		dir := strings.TrimSuffix(pattern, "/")
		return rel == dir || strings.HasPrefix(rel, dir+"/") || strings.Contains(rel, "/"+dir+"/")
	}
	if strings.Contains(pattern, "/") {
		if ok, _ := filepath.Match(pattern, rel); ok {
			return true
		}
		return strings.HasSuffix(rel, "/"+pattern)
	}
	if ok, _ := filepath.Match(pattern, base); ok {
		return true
	}
	return false
}
