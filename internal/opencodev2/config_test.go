// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodev2

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/skilltokens"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readOverlay(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestOverlayLayersThePlatformFolder(t *testing.T) {
	platform, xdg := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	writeFile(t, filepath.Join(platform, "opencode.json"), `{
  "instructions": ["context/team.md"],
  "mcp": {"aws": {"type": "remote", "url": "https://aws.example"}, "docs": {"type": "remote", "url": "https://docs.example"}}
}`)
	writeFile(t, filepath.Join(platform, "skills", "brief", "SKILL.md"), "---\nname: brief\ndescription: x\n---\n")
	writeFile(t, filepath.Join(platform, "agent", "analyst.md"), "---\ndescription: \"Researches accounts\"\nmode: all\npermission:\n  edit: deny\n---\n\nYou research accounts.\n")
	writeFile(t, filepath.Join(platform, "agents", "writer.md"), "---\ndescription: Writes\n---\nYou write.\n")
	writeFile(t, filepath.Join(xdg, "opencode", "opencode.json"), `{"mcp": {"aws": {"enabled": false}, "mine": {"type": "local", "command": ["x"], "enabled": true}}}`)

	path, err := writeOverlay(platform)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(platform, overlayName) {
		t.Fatalf("path = %s", path)
	}
	doc := readOverlay(t, path)

	if got := doc["skills"].([]any); len(got) != 1 || got[0] != filepath.Join(platform, "skills") {
		t.Errorf("skills = %v", got)
	}
	if got := doc["instructions"].([]any); len(got) != 1 || got[0] != "context/team.md" {
		t.Errorf("instructions changed: %v", got)
	}
	agents := doc["agent"].(map[string]any)
	analyst := agents["analyst"].(map[string]any)
	if analyst["prompt"] != "You research accounts." || analyst["description"] != "Researches accounts" || analyst["mode"] != "all" {
		t.Errorf("analyst = %v", analyst)
	}
	if perm := analyst["permission"].(map[string]any); perm["edit"] != "deny" {
		t.Errorf("analyst permission = %v", perm)
	}
	if writer := agents["writer"].(map[string]any); writer["prompt"] != "You write." {
		t.Errorf("writer = %v", writer)
	}
	if note := agents[NoteAgent].(map[string]any); note["hidden"] != true || note["permission"].(map[string]any)["*"] != "deny" {
		t.Errorf("%s = %v", NoteAgent, note)
	}
	mcp := doc["mcp"].(map[string]any)
	if aws := mcp["aws"].(map[string]any); aws["enabled"] != false || aws["url"] != "https://aws.example" {
		t.Errorf("aws = %v, want the user's switch carried over", aws)
	}
	if docs := mcp["docs"].(map[string]any); docs["enabled"] != nil {
		t.Errorf("docs = %v, want no switch the user didn't set", docs)
	}
	if _, ok := mcp["mine"]; ok {
		t.Error("the user's own connector was copied into the platform document")
	}
	if exp := doc["experimental"].(map[string]any); exp["todo"] != true {
		t.Errorf("experimental = %v", exp)
	}
	if perm := doc["permission"].(map[string]any); perm["external_directory"] != "ask" {
		t.Errorf("permission = %v", perm)
	}
}

func TestOverlayKeepsWhatThePlatformSets(t *testing.T) {
	platform := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeFile(t, filepath.Join(platform, "opencode.json"), `{
  "permission": {"external_directory": "allow"},
  "experimental": {"other": 1},
  "agent": {"analyst": {"prompt": "From the config"}}
}`)
	writeFile(t, filepath.Join(platform, "agent", "analyst.md"), "From the file\n")
	path, err := writeOverlay(platform)
	if err != nil {
		t.Fatal(err)
	}
	doc := readOverlay(t, path)
	if perm := doc["permission"].(map[string]any); perm["external_directory"] != "allow" {
		t.Errorf("permission = %v", perm)
	}
	if exp := doc["experimental"].(map[string]any); exp["other"] != float64(1) || exp["todo"] != true {
		t.Errorf("experimental = %v", exp)
	}
	if a := doc["agent"].(map[string]any)["analyst"].(map[string]any); a["prompt"] != "From the config" {
		t.Errorf("analyst = %v", a)
	}
}

func TestOverlayWithoutAPlatformFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KWA_HOME", home)
	path, err := writeOverlay("")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(home, ".system", overlayName) {
		t.Fatalf("path = %s", path)
	}
	doc := readOverlay(t, path)
	if doc["experimental"].(map[string]any)["todo"] != true || doc["agent"].(map[string]any)[NoteAgent] == nil {
		t.Fatalf("doc = %v", doc)
	}
}

// skillPolicies maps each skill.use policy's resource to its effect.
func skillPolicies(t *testing.T, doc map[string]any) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, p := range doc["policies"].([]any) {
		m := p.(map[string]any)
		if m["action"] == "skill.use" {
			out[m["resource"].(string)] = m["effect"].(string)
		}
	}
	return out
}

func TestOverlayTurnsOffBuiltinSkills(t *testing.T) {
	t.Setenv("KWA_HOME", t.TempDir())
	path, err := writeOverlay("")
	if err != nil {
		t.Fatal(err)
	}
	got := skillPolicies(t, readOverlay(t, path))
	if got["browser"] != "deny" || got["report"] != "deny" || got["opencode"] != "" {
		t.Fatalf("skill policies = %v, want browser and report denied, opencode left alone", got)
	}
}

func TestOverlayKeepsThePlatformsPolicies(t *testing.T) {
	platform := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeFile(t, filepath.Join(platform, "opencode.json"), `{"policies": [
  {"action": "skill.use", "resource": "browser", "effect": "allow"},
  {"action": "provider.use", "resource": "openai", "effect": "deny"}
]}`)
	path, err := writeOverlay(platform)
	if err != nil {
		t.Fatal(err)
	}
	doc := readOverlay(t, path)
	if got := skillPolicies(t, doc); got["browser"] != "allow" || got["report"] != "deny" {
		t.Errorf("skill policies = %v, want the platform's browser rule kept and report denied", got)
	}
	if n := len(doc["policies"].([]any)); n != 3 {
		t.Errorf("%d policies, want the platform's 2 and report's", n)
	}
}

func TestConfigEnvHandsTheFolderOverAsADocument(t *testing.T) {
	platform := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	env := configEnv([]string{"HOME=/h", "OPENCODE_CONFIG_DIR=" + platform, "OPENCODE_CONFIG=/elsewhere.json"})
	want := []string{"HOME=/h", "OPENCODE_CONFIG=" + filepath.Join(platform, overlayName)}
	if !slices.Equal(env, want) {
		t.Fatalf("env = %q, want %q", env, want)
	}
}

func TestReloadAsksTheRunningServer(t *testing.T) {
	useFake(t, "ok")
	record := filepath.Join(t.TempDir(), "reload")
	t.Setenv("KWA_TEST_FAKE_RECORD", record)
	if err := Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(record); err == nil {
		t.Fatal("Reload ran with no server running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	url, _, err := Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := Reload(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(record)
	if !strings.Contains(string(got), `"args":["reload","--server","`+url+`"]`) || !strings.Contains(string(got), `"password":true`) {
		t.Fatalf("reload ran as %s", got)
	}
}

// A server started before the platform folder was known reads its document
// from KWA's own folder; once the folder is there, Reload restarts it on the
// folder's document, which `opencode reload` couldn't do.
func TestReloadRestartsWhenThePlatformFolderAppears(t *testing.T) {
	useFake(t, "ok")
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	before, _, err := Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	platform := t.TempDir()
	if err := os.WriteFile(filepath.Join(platform, "opencode.json"), []byte(`{"mcp":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_CONFIG_DIR", platform)
	if err := Reload(ctx); err != nil {
		t.Fatal(err)
	}
	after, _, ok := Running()
	if !ok || after == before {
		t.Fatalf("server %q after the reload, %q before; want a new one", after, before)
	}
	srv.mu.Lock()
	overlay := srv.overlay
	srv.mu.Unlock()
	if overlay != filepath.Join(platform, overlayName) {
		t.Errorf("the new server reads %s", overlay)
	}
}

// A provider's region reaches opencode through the environment, which
// `opencode reload` doesn't reread: a server started before the region was
// chosen sent every request to a bare path ("Invalid URL").
func TestReloadRestartsWhenTheEnvironmentChanged(t *testing.T) {
	useFake(t, "ok")
	t.Setenv("KWA_ENDPOINT_GOOGLE_VERTEX", "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	before, _, err := Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KWA_ENDPOINT_GOOGLE_VERTEX", "https://eu.example.com")
	if err := Reload(ctx); err != nil {
		t.Fatal(err)
	}
	after, _, ok := Running()
	if !ok || after == before {
		t.Fatalf("server %q after the reload, %q before; want a new one", after, before)
	}
	srv.mu.Lock()
	env := srv.env
	srv.mu.Unlock()
	if !slices.Contains(env, "KWA_ENDPOINT_GOOGLE_VERTEX=https://eu.example.com") {
		t.Error("the new server doesn't have the new region")
	}

	// Nothing changed since: the server is asked to reload, not replaced.
	if err := Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if again, _, _ := Running(); again != after {
		t.Errorf("an unchanged environment restarted the server: %q, then %q", after, again)
	}
}

// A skill's script reads its token from the environment, which the server
// keeps from when it started.
func TestASavedSkillTokenRestartsTheServerWithIt(t *testing.T) {
	useFake(t, "ok")
	t.Setenv("EXAMPLE_API_TOKEN", "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	before, _, err := Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := skilltokens.Save("EXAMPLE_API_TOKEN", "secret-1"); err != nil {
		t.Fatal(err)
	}
	if err := Reload(ctx); err != nil {
		t.Fatal(err)
	}
	after, _, ok := Running()
	if !ok || after == before {
		t.Fatalf("server %q after the reload, %q before; want a new one", after, before)
	}
	srv.mu.Lock()
	env := srv.env
	srv.mu.Unlock()
	if !slices.Contains(env, "EXAMPLE_API_TOKEN=secret-1") {
		t.Error("the new server doesn't have the token")
	}
	if cmd := Command(ctx, config.OpencodeBin, "models"); !slices.Contains(cmd.Env, "EXAMPLE_API_TOKEN=secret-1") {
		t.Error("a command doesn't have the token")
	}

	if err := skilltokens.Remove("EXAMPLE_API_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if err := Reload(ctx); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	env = srv.env
	srv.mu.Unlock()
	if slices.Contains(env, "EXAMPLE_API_TOKEN=secret-1") {
		t.Error("the server still has the token after it was removed")
	}
}

func TestEnvChangesNamesOnly(t *testing.T) {
	got := envChanges(
		[]string{"A=1", "B=2", "SECRET=old", "GONE=x"},
		[]string{"A=1", "B=3", "SECRET=new", "NEW=y=z"})
	want := []string{"B", "GONE", "NEW", "SECRET"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if envChanges([]string{"A=1"}, []string{"A=1"}) != nil {
		t.Error("equal environments reported a change")
	}
}
