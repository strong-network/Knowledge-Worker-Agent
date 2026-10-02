// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package agentbuilder

import (
	"strings"
	"testing"
)

func floatPtr(f float64) *float64 { return &f }
func intPtr(n int) *int           { return &n }

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Release Notes Writer": "release-notes-writer",
		"My Agent":             "my-agent",
		"my-agent":             "my-agent",
		"  Trim  Me  ":         "trim-me",
		"Foo___Bar":            "foo-bar",
		"UPPER lower 123":      "upper-lower-123",
		"a!!!b":                "a-b",
		"---leading---":        "leading",
		"café résumé":          "caf-r-sum", // non-ASCII collapses to hyphens
		"":                     "",
		"!!!":                  "",
		"123":                  "123",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlugCollisionKey(t *testing.T) {
	if Slug("My Agent") != Slug("my-agent") {
		t.Fatal("expected 'My Agent' and 'my-agent' to collide")
	}
}

func TestFilename(t *testing.T) {
	if got := Filename("Release Notes"); got != "release-notes.md" {
		t.Errorf("Filename = %q", got)
	}
	if got := Filename("!!!"); got != "" {
		t.Errorf("Filename(all-symbol) = %q, want empty", got)
	}
}

func TestSerializeMinimal(t *testing.T) {
	a := &Agent{
		Name:        "Release Notes Writer",
		Description: "Use this to draft release notes.",
		Prompt:      "Write clear notes.",
		Mode:        ModeSubagent,
	}
	got := Serialize(a)
	want := "---\n" +
		"description: \"Use this to draft release notes.\"\n" +
		"mode: subagent\n" +
		"---\n\n" +
		"Write clear notes.\n"
	if got != want {
		t.Errorf("Serialize minimal mismatch:\n got:\n%q\nwant:\n%q", got, want)
	}
	// Name must never appear as a frontmatter key.
	if strings.Contains(got, "name:") {
		t.Error("serialized output must not contain a name: key")
	}
}

func TestSerializeOmitsUnsetFields(t *testing.T) {
	a := &Agent{Name: "x", Description: "d", Prompt: "p"}
	got := Serialize(a)
	for _, key := range []string{"model:", "temperature:", "top_p:", "steps:", "color:", "hidden:", "disable:", "permission:", "mode:"} {
		if strings.Contains(got, key) {
			t.Errorf("expected %s to be omitted, got:\n%s", key, got)
		}
	}
}

func TestSerializeAdvanced(t *testing.T) {
	a := &Agent{
		Name:        "Agent",
		Description: "d",
		Prompt:      "p",
		Model:       "github-copilot/gpt-5",
		Mode:        ModeAll,
		Temperature: floatPtr(0.7),
		TopP:        floatPtr(1),
		Steps:       intPtr(20),
		Color:       "#ff0000",
		Hidden:      true,
		Disable:     true,
	}
	got := Serialize(a)
	for _, want := range []string{
		"model: \"github-copilot/gpt-5\"",
		"mode: all",
		"temperature: 0.7",
		"top_p: 1",
		"steps: 20",
		"color: \"#ff0000\"",
		"hidden: true",
		"disable: true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("serialized output missing %q:\n%s", want, got)
		}
	}
}

func TestSerializePermissionWildcardAndOverrides(t *testing.T) {
	a := &Agent{
		Name:   "x",
		Prompt: "p",
		Permission: &Permission{
			Global: ActionAllow,
			Rules:  map[string]Action{"edit": ActionAsk, "bash": ActionDeny},
		},
	}
	got := Serialize(a)
	want := "permission:\n" +
		"  \"*\": allow\n" +
		"  edit: ask\n" +
		"  bash: deny\n"
	if !strings.Contains(got, want) {
		t.Errorf("permission block mismatch:\n got:\n%s\nwant substring:\n%s", got, want)
	}
	// Canonical order: bash (index 5) comes before edit (index 1)? No — order
	// follows PermissionCapabilities, so edit precedes bash.
	if strings.Index(got, "edit: ask") > strings.Index(got, "bash: deny") {
		t.Error("permission rows must follow canonical capability order (edit before bash)")
	}
}

func TestSerializeOmitsPermissionWhenZero(t *testing.T) {
	a := &Agent{Name: "x", Prompt: "p", Permission: &Permission{}}
	if strings.Contains(Serialize(a), "permission:") {
		t.Error("empty permission must be omitted")
	}
}

func TestYamlQuoteEscaping(t *testing.T) {
	a := &Agent{
		Name:        "x",
		Description: "Line with \"quotes\", a colon: and\na newline",
		Prompt:      "p",
	}
	got := Serialize(a)
	// Must be a single line, newline escaped, and re-parse cleanly.
	if strings.Contains(got, "description: \"Line with \\\"quotes\\\", a colon: and\\na newline\"") == false {
		t.Errorf("escaping wrong:\n%s", got)
	}
	parsed, ok := Parse("x", []byte(got))
	if !ok {
		t.Fatal("expected frontmatter to parse")
	}
	if parsed.Description != a.Description {
		t.Errorf("round-trip description mismatch:\n got: %q\nwant: %q", parsed.Description, a.Description)
	}
}

func TestRoundTrip(t *testing.T) {
	orig := &Agent{
		Name:        "Release Notes Writer",
		Description: "Use to draft notes.\nRequires: jira MCP",
		Prompt:      "Write clear notes.\n\nGroup by feature.",
		Model:       "github-copilot/gpt-5",
		Mode:        ModeSubagent,
		Temperature: floatPtr(0.5),
		Steps:       intPtr(12),
		Hidden:      true,
		Permission: &Permission{
			Global: ActionAsk,
			Rules:  map[string]Action{"read": ActionAllow, "bash": ActionDeny},
		},
	}
	data := Serialize(orig)
	got, ok := Parse("release-notes-writer", []byte(data))
	if !ok {
		t.Fatal("expected frontmatter")
	}
	if got.Description != orig.Description {
		t.Errorf("description: got %q want %q", got.Description, orig.Description)
	}
	if got.Prompt != orig.Prompt {
		t.Errorf("prompt: got %q want %q", got.Prompt, orig.Prompt)
	}
	if got.Model != orig.Model || got.Mode != orig.Mode {
		t.Errorf("model/mode mismatch: %+v", got)
	}
	if got.Temperature == nil || *got.Temperature != 0.5 {
		t.Errorf("temperature mismatch: %v", got.Temperature)
	}
	if got.Steps == nil || *got.Steps != 12 {
		t.Errorf("steps mismatch: %v", got.Steps)
	}
	if !got.Hidden {
		t.Error("hidden lost in round-trip")
	}
	if got.Permission == nil || got.Permission.Global != ActionAsk {
		t.Fatalf("permission global lost: %+v", got.Permission)
	}
	if got.Permission.Rules["read"] != ActionAllow || got.Permission.Rules["bash"] != ActionDeny {
		t.Errorf("permission rules mismatch: %+v", got.Permission.Rules)
	}
}

func TestParseBodyOnly(t *testing.T) {
	got, ok := Parse("x", []byte("Just a prompt, no frontmatter."))
	if ok {
		t.Error("expected ok=false for body-only file")
	}
	if got.Prompt != "Just a prompt, no frontmatter." {
		t.Errorf("prompt = %q", got.Prompt)
	}
}

func TestParsePerPatternReducesToWildcard(t *testing.T) {
	// A hand-authored import using the per-pattern map form: capture "*".
	file := "---\npermission:\n  edit: allow\n---\nbody\n"
	got, _ := Parse("x", []byte(file))
	if got.Permission == nil || got.Permission.Rules["edit"] != ActionAllow {
		t.Fatalf("permission not parsed: %+v", got.Permission)
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	a := &Agent{
		Name:        "!!!",
		Mode:        Mode("weird"),
		Temperature: floatPtr(9),
		Permission: &Permission{
			Global: Action("maybe"),
			Rules:  map[string]Action{"notacap": ActionAllow, "edit": Action("perhaps")},
		},
	}
	errs := Validate(a)
	fields := map[string]bool{}
	for _, e := range errs {
		fields[e.Field] = true
	}
	for _, want := range []string{"name", "mode", "temperature", "permission", "permission.notacap", "permission.edit"} {
		if !fields[want] {
			t.Errorf("expected a validation error for %q; got %+v", want, errs)
		}
	}
}

func TestValidateAcceptsMinimal(t *testing.T) {
	a := &Agent{Name: "Release Notes", Description: "d", Prompt: "p", Mode: ModeSubagent}
	if errs := Validate(a); len(errs) != 0 {
		t.Errorf("expected valid, got %+v", errs)
	}
}

func TestRequiresLine(t *testing.T) {
	if got := RequiresLine("Does things.\nRequires: jira MCP, github token"); got != "jira MCP, github token" {
		t.Errorf("RequiresLine = %q", got)
	}
	if got := RequiresLine("no dependency here"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}
