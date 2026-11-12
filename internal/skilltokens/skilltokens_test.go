// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package skilltokens

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func scratch(t *testing.T) {
	t.Helper()
	t.Setenv("KWA_HOME", t.TempDir())
	SetOnChange(nil)
	SetSkills(nil)
	t.Cleanup(func() { SetOnChange(nil); SetSkills(nil) })
}

func TestValidName(t *testing.T) {
	for name, want := range map[string]bool{
		"SLACK_USER_TOKEN":                 true,
		"EXAMPLE_API_KEY":                  true,
		"VENDOR_CLIENT_SECRET":             true,
		"PATH":                             false,
		"LD_PRELOAD":                       false,
		"NODE_OPTIONS":                     false,
		"slack_user_token":                 false,
		"_TOKEN":                           false,
		"TOKEN":                            false,
		"SLACK TOKEN":                      false,
		"SLACK_TOKEN=x":                    false,
		"KWA_SECRET_TOKEN":                 false,
		"OPENCODE_API_KEY":                 false,
		"GITHUB_TOKEN":                     false,
		"GH_TOKEN":                         false,
		"COPILOT_GITHUB_TOKEN":             false,
		"SDS_API_KEY":                      false,
		strings.Repeat("A", 63) + "_TOKEN": true,
		strings.Repeat("A", 64) + "_TOKEN": false,
	} {
		if got := ValidName(name); got != want {
			t.Errorf("ValidName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestSaveKeepsTheTokenPrivate(t *testing.T) {
	scratch(t)
	if err := Save("SLACK_USER_TOKEN", "  xoxp-1\n"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(storePath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, want 0600", info.Mode().Perm())
	}
	dir, err := os.Stat(filepath.Dir(storePath()))
	if err != nil {
		t.Fatal(err)
	}
	if dir.Mode().Perm() != 0o700 {
		t.Errorf("folder mode %v, want 0700", dir.Mode().Perm())
	}
	if got := Add(nil); !slices.Equal(got, []string{"SLACK_USER_TOKEN=xoxp-1"}) {
		t.Errorf("Add = %q", got)
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(storePath()), ".skill-tokens-*"))
	if len(leftovers) != 0 {
		t.Errorf("temporary files left: %v", leftovers)
	}
}

func TestSaveRefuses(t *testing.T) {
	scratch(t)
	for _, c := range []struct{ name, value string }{
		{"PATH", "/tmp/evil"},
		{"LD_PRELOAD_TOKEN_X", "x"},
		{"SLACK_USER_TOKEN", "   "},
		{"SLACK_USER_TOKEN", "xoxp-1\nEVIL=1"},
		{"SLACK_USER_TOKEN", "xoxp-\x00"},
		{"SLACK_USER_TOKEN", strings.Repeat("x", MaxValue+1)},
	} {
		err := Save(c.name, c.value)
		var invalid InvalidError
		if !errors.As(err, &invalid) {
			t.Errorf("Save(%q, %q) = %v, want an InvalidError", c.name, c.value, err)
		}
	}
	if names, _ := Saved(); len(names) != 0 {
		t.Errorf("saved %v", names)
	}
}

func TestAddLeavesTheWorkspacesOwnValue(t *testing.T) {
	scratch(t)
	for name, value := range map[string]string{"A_TOKEN": "saved-a", "B_TOKEN": "saved-b", "C_TOKEN": "saved-c"} {
		if err := Save(name, value); err != nil {
			t.Fatal(err)
		}
	}
	got := Add([]string{"HOME=/h", "A_TOKEN=workspace", "B_TOKEN="})
	want := []string{"HOME=/h", "A_TOKEN=workspace", "B_TOKEN=", "B_TOKEN=saved-b", "C_TOKEN=saved-c"}
	if !slices.Equal(got, want) {
		t.Errorf("Add = %q\nwant %q", got, want)
	}
}

func TestAddSkipsAStoreItCantRead(t *testing.T) {
	scratch(t)
	if err := os.MkdirAll(filepath.Dir(storePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storePath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Add([]string{"HOME=/h"}); !slices.Equal(got, []string{"HOME=/h"}) {
		t.Errorf("Add = %q", got)
	}
}

func TestAStoredInvalidNameIsIgnored(t *testing.T) {
	scratch(t)
	if err := os.MkdirAll(filepath.Dir(storePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := `{"tokens": {"LD_PRELOAD": "/tmp/x.so", "PATH": "/tmp", "OK_TOKEN": "v"}}`
	if err := os.WriteFile(storePath(), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Add(nil); !slices.Equal(got, []string{"OK_TOKEN=v"}) {
		t.Errorf("Add = %q", got)
	}
}

func TestChangeHook(t *testing.T) {
	scratch(t)
	fired := 0
	SetOnChange(func() { fired++ })
	if err := Save("A_TOKEN", "v"); err != nil {
		t.Fatal(err)
	}
	if err := Remove("A_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if err := Remove("A_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if err := Save("PATH", "v"); err == nil {
		t.Fatal("saved PATH")
	}
	if fired != 2 {
		t.Errorf("hook fired %d times, want 2 (a save and a removal that changed something)", fired)
	}
	if got := Add(nil); len(got) != 0 {
		t.Errorf("after removal Add = %q", got)
	}
}

const slackSkill = `---
name: slack
description: Read and send Slack messages.
version: 3.0.0
metadata:
  kwa-token: SLACK_USER_TOKEN
  kwa-token-label: Slack
  kwa-token-help: "A Slack user token, which starts \"xoxp-\"."
  kwa-token-url: https://slack-token.example.com/api/home
  author: 'Example''s team'
---

# Slack

metadata:
  kwa-token: NOT_THIS_TOKEN
`

func TestDeclared(t *testing.T) {
	d, ok := Declared(slackSkill)
	want := Declaration{
		Name:  "SLACK_USER_TOKEN",
		Label: "Slack",
		Help:  `A Slack user token, which starts "xoxp-".`,
		URL:   "https://slack-token.example.com/api/home",
	}
	if !ok || d != want {
		t.Errorf("Declared = %+v, %v\nwant %+v", d, ok, want)
	}
	if m := metadata(slackSkill); m["author"] != "Example's team" {
		t.Errorf("single-quoted value = %q", m["author"])
	}
}

func TestDeclaredRefuses(t *testing.T) {
	for name, content := range map[string]string{
		"no front matter":      "# Slack\n\nmetadata:\n  kwa-token: SLACK_USER_TOKEN\n",
		"no metadata":          "---\nname: x\nkwa-token: SLACK_USER_TOKEN\n---\n",
		"a variable like PATH": "---\nname: x\nmetadata:\n  kwa-token: PATH\n---\n",
		"after the metadata":   "---\nmetadata:\n  author: a\nother:\n  kwa-token: SLACK_USER_TOKEN\n---\n",
	} {
		if d, ok := Declared(content); ok {
			t.Errorf("%s: Declared = %+v", name, d)
		}
	}
}

func TestDeclaredLinks(t *testing.T) {
	for link, want := range map[string]string{
		"https://example.com/x":   "https://example.com/x",
		"http://example.com/x":    "",
		"javascript:alert(1)":     "",
		"https:///no-host":        "",
		"https://example.com # c": "https://example.com",
	} {
		content := "---\r\nmetadata:\r\n  kwa-token: X_TOKEN\r\n  kwa-token-url: " + link + "\r\n---\r\n"
		d, ok := Declared("\ufeff" + content)
		if !ok || d.URL != want {
			t.Errorf("link %q: Declared = %+v, %v; want URL %q", link, d, ok, want)
		}
	}
}

func writeSkill(t *testing.T, dir, name, content string) Skill {
	t.Helper()
	p := filepath.Join(dir, name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return Skill{Name: name, Path: p}
}

func list(t *testing.T) (tokens []Token, checked bool) {
	t.Helper()
	rec := httptest.NewRecorder()
	HandleList(rec, httptest.NewRequest("GET", "/api/skill-tokens", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "xoxp-secret") {
		t.Fatalf("the list holds a token: %s", rec.Body)
	}
	var body struct {
		Tokens  []Token `json:"tokens"`
		Checked bool    `json:"skillsChecked"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Tokens, body.Checked
}

func TestHandlers(t *testing.T) {
	scratch(t)
	dir := t.TempDir()
	skills := []Skill{
		writeSkill(t, dir, "slack", slackSkill),
		writeSkill(t, dir, "slack-canvas", "---\nname: slack-canvas\nmetadata:\n  kwa-token: SLACK_USER_TOKEN\n  kwa-token-label: Other label\n---\n"),
		writeSkill(t, dir, "plain", "---\nname: plain\ndescription: d\n---\n"),
		writeSkill(t, dir, "unlabelled", "---\nname: unlabelled\nmetadata:\n  kwa-token: UNLABELLED_KEY\n---\n"),
		{Name: "gone", Path: filepath.Join(dir, "missing", "SKILL.md")},
	}
	SetSkills(func(context.Context) ([]Skill, error) { return skills, nil })

	tokens, checked := list(t)
	if !checked || len(tokens) != 2 {
		t.Fatalf("tokens %+v, checked %v", tokens, checked)
	}
	slack := tokens[0]
	if slack.Name != "SLACK_USER_TOKEN" || slack.Label != "Slack" || slack.Source != "none" ||
		!slices.Equal(slack.Skills, []string{"slack", "slack-canvas"}) || slack.URL == "" {
		t.Errorf("slack = %+v", slack)
	}
	if tokens[1].Label != "unlabelled" {
		t.Errorf("a token without a label is named after its skill: %+v", tokens[1])
	}

	put := func(name, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", "/api/skill-tokens/"+name, strings.NewReader(body))
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		HandleSave(rec, req)
		return rec
	}
	if rec := put("SLACK_USER_TOKEN", `{"value":"xoxp-secret"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT status %d: %s", rec.Code, rec.Body)
	}
	if rec := put("PATH", `{"value":"/tmp"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("PUT PATH status %d", rec.Code)
	}
	if rec := put("SLACK_USER_TOKEN", `{"value":""}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Enter a token") {
		t.Errorf("empty PUT: %d %s", rec.Code, rec.Body)
	}
	if tokens, _ := list(t); tokens[0].Source != "saved" {
		t.Errorf("after saving: %+v", tokens[0])
	}

	t.Setenv("SLACK_USER_TOKEN", "from-the-workspace")
	if tokens, _ := list(t); tokens[0].Source != "environment" {
		t.Errorf("with the variable set: %+v", tokens[0])
	}
	t.Setenv("SLACK_USER_TOKEN", "")

	// A saved token no skill declares any more stays listed, so it can be removed.
	skills = skills[2:3]
	tokens, _ = list(t)
	if len(tokens) != 1 || tokens[0].Name != "SLACK_USER_TOKEN" || tokens[0].Source != "saved" || len(tokens[0].Skills) != 0 {
		t.Errorf("orphaned token: %+v", tokens)
	}

	req := httptest.NewRequest("DELETE", "/api/skill-tokens/SLACK_USER_TOKEN", nil)
	req.SetPathValue("name", "SLACK_USER_TOKEN")
	rec := httptest.NewRecorder()
	HandleRemove(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE status %d", rec.Code)
	}
	if tokens, _ := list(t); len(tokens) != 0 {
		t.Errorf("after removing: %+v", tokens)
	}
}

func TestListWithoutSkills(t *testing.T) {
	scratch(t)
	if err := Save("A_TOKEN", "v"); err != nil {
		t.Fatal(err)
	}
	SetSkills(func(context.Context) ([]Skill, error) { return nil, errors.New("opencode is starting") })
	tokens, checked := list(t)
	if checked || len(tokens) != 1 || tokens[0].Source != "saved" {
		t.Errorf("tokens %+v, checked %v", tokens, checked)
	}
}
