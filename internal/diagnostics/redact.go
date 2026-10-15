// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package diagnostics

import (
	"net/url"
	"regexp"
	"strings"
)

const redacted = "[redacted]"

// Everything the diagnostics show or copy passes through Redact, so a log line
// or setting that happens to hold a credential is masked before it reaches a
// screen share or an admin's ticket. It errs towards masking too much.
var secretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	// Credentials in a URL: https://user:token@host.
	{regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^\s/@:"']+(:[^\s/@"']*)?@`), "${1}" + redacted + "@"},
	// Key = value and "key": "value" forms of common secret names.
	{regexp.MustCompile(`(?i)\b((?:[a-z0-9_-]*[_-])?(?:authorization|api[_-]?key|access[_-]?key|secret|password|passwd|token|credential)s?)(["']?\s*[:=]\s*["']?)(?:bearer\s+|basic\s+)?[^\s"',;}\]]+`), "${1}${2}" + redacted},
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[a-z0-9._~+/=-]{8,}`), "${1} " + redacted},
	// Well-known token shapes, wherever they appear.
	{regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`), redacted},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`), redacted},
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}`), redacted},
	{regexp.MustCompile(`\bya29\.[0-9A-Za-z._-]{20,}`), redacted},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), redacted},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`), redacted},
}

// Redact masks credentials in s.
func Redact(s string) string {
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}

// redactSource returns a config repo source safe to show: a URL loses its
// user info and query, and anything else goes through Redact.
func redactSource(source string) string {
	source = strings.TrimSpace(source)
	if u, err := url.Parse(source); err == nil && u.Scheme != "" && u.Host != "" {
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()
	}
	return Redact(source)
}
