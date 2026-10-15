// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package diagnostics

import (
	"strings"
	"testing"
)

func TestRedactMasksCredentials(t *testing.T) {
	secrets := map[string]string{
		"git clone https://alex:s3cr3tvalue@github.com/org/repo.git failed":           "s3cr3tvalue",
		`Authorization: Bearer abcdefghijklmnop1234`:                                  "abcdefghijklmnop1234",
		`{"api_key":"k-1234567890abcdef"}`:                                            "k-1234567890abcdef",
		"MISTRAL_API_KEY=abc123def456":                                                "abc123def456",
		"token=ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123":                                    "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123",
		"using ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123 now":                                "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123",
		"key sk-abcdefghijklmnopqrstu":                                                "sk-abcdefghijklmnopqrstu",
		"password: hunter2hunter2":                                                    "hunter2hunter2",
		"x-api-key: zzzzzzzzzzzz":                                                     "zzzzzzzzzzzz",
		"jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpM": "eyJhbGciOiJIUzI1NiJ9",
	}
	for in, secret := range secrets {
		if out := Redact(in); strings.Contains(out, secret) || !strings.Contains(out, redacted) {
			t.Errorf("Redact(%q) = %q, still holds %q", in, out, secret)
		}
	}
}

func TestRedactLeavesOrdinaryLinesAlone(t *testing.T) {
	for _, line := range []string{
		"  ✓ OpenCode models: 37 available in 6.808s (default: github-copilot/claude-sonnet-5.5)",
		`2026/10/13 10:27:54 [TITLE] session=e608e514 titled "Working directory commands"`,
		"usage tokens_in=1234 tokens_out=56",
		"  ✓ Central configuration: project sds_gtm → /home/x/.system/config (mcp=14 agents=6)",
		"provider github-copilot authenticated true",
		"https://github.com/csg/knowledge-space-configuration.git",
	} {
		if out := Redact(line); out != line {
			t.Errorf("Redact(%q) = %q, want unchanged", line, out)
		}
	}
}

func TestRedactSourceDropsUserInfoAndQuery(t *testing.T) {
	cases := map[string]string{
		"https://alex:tok@github.com/org/repo.git?x=1": "https://github.com/org/repo.git",
		"https://github.com/org/repo.git":              "https://github.com/org/repo.git",
		"/usr/share/kwa/config":                        "/usr/share/kwa/config",
		"":                                             "",
	}
	for in, want := range cases {
		if got := redactSource(in); got != want {
			t.Errorf("redactSource(%q) = %q, want %q", in, got, want)
		}
	}
}
