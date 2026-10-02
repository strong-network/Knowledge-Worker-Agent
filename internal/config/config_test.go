// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env/envtest"
)

func TestEnvOrDefault(t *testing.T) {
	t.Setenv("TEST_KEY_EXISTS", "hello")
	if got := EnvOrDefault("TEST_KEY_EXISTS", "fallback"); got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
	if got := EnvOrDefault("TEST_KEY_MISSING", "fallback"); got != "fallback" {
		t.Errorf("expected 'fallback', got %q", got)
	}
}

func TestDefaultSessionConfig(t *testing.T) {
	Workspace = "/tmp/test"
	DefaultBackend = "" // ensure no leakage from other tests
	cfg := DefaultSessionConfig()
	if cfg.Label != "New chat" {
		t.Errorf("expected label 'New chat', got %q", cfg.Label)
	}
	if cfg.Mode != "autopilot" {
		t.Errorf("expected mode 'autopilot', got %q", cfg.Mode)
	}
	if !cfg.Yolo {
		t.Error("expected yolo=true")
	}
	if cfg.Backend != BackendOpencode {
		t.Errorf("expected backend %q, got %q", BackendOpencode, cfg.Backend)
	}
	if cfg.Workdir != "/tmp/test" {
		t.Errorf("expected workdir '/tmp/test', got %q", cfg.Workdir)
	}
}

func TestInit_DefaultBackendIsOpencode(t *testing.T) {
	// OpenCode is the only backend; Init always sets DefaultBackend=opencode.
	t.Setenv("COPILOT_WORKSPACE", t.TempDir())
	Init()
	if DefaultBackend != BackendOpencode {
		t.Errorf("expected DefaultBackend=opencode, got %q", DefaultBackend)
	}
}

func TestNormalizeBackend(t *testing.T) {
	// Every value normalizes to opencode — the only supported backend.
	for _, in := range []string{"opencode", "  OpenCode ", "copilot", "", "unknown"} {
		if got := NormalizeBackend(in); got != BackendOpencode {
			t.Errorf("NormalizeBackend(%q) = %q, want opencode", in, got)
		}
	}
}

func TestIsLegacyCopilotBackend(t *testing.T) {
	legacy := []string{"copilot", "Copilot", "  copilot ", "COPILOT"}
	for _, in := range legacy {
		if !IsLegacyCopilotBackend(in) {
			t.Errorf("IsLegacyCopilotBackend(%q) = false, want true", in)
		}
	}
	notLegacy := []string{"opencode", "", "  ", "unknown", "copilot-x"}
	for _, in := range notLegacy {
		if IsLegacyCopilotBackend(in) {
			t.Errorf("IsLegacyCopilotBackend(%q) = true, want false", in)
		}
	}
}

func TestBuildOpencodeArgs(t *testing.T) {
	cfg := SessionConfig{
		Yolo:            true,
		Workdir:         "/work",
		Model:           "anthropic/claude-sonnet-4-5",
		Agent:           "builder",
		ReasoningEffort: "high",
	}
	args := BuildOpencodeArgs("-starts-with-dash", "opencode-session-1", cfg)

	assertContains(t, args, "run")
	assertContains(t, args, "--format")
	assertContains(t, args, "json")
	assertContains(t, args, "--dangerously-skip-permissions")
	assertContains(t, args, "--model")
	assertContains(t, args, "anthropic/claude-sonnet-4-5")
	assertContains(t, args, "--agent")
	assertContains(t, args, "builder")
	assertContains(t, args, "--variant")
	assertContains(t, args, "high")
	assertContains(t, args, "--dir")
	assertContains(t, args, "/work")
	assertContains(t, args, "--session")
	assertContains(t, args, "opencode-session-1")
	if args[len(args)-2] != "--" || args[len(args)-1] != "-starts-with-dash" {
		t.Fatalf("expected prompt to be protected by -- at the end, got %v", args)
	}
}

func TestBuildOpencodeArgs_PlanModeMapsToPlanAgent(t *testing.T) {
	cfg := SessionConfig{Mode: "plan", Model: "github-copilot/claude-sonnet-4.6"}
	args := BuildOpencodeArgs("hi", "", cfg)
	if v := argValue(args, "--agent"); v != "plan" {
		t.Fatalf("expected mode=plan to map to --agent plan, got --agent %q in %v", v, args)
	}
}

func TestBuildOpencodeArgs_ExplicitAgentWinsOverPlanMode(t *testing.T) {
	// An explicit agent must take precedence over the plan-mode mapping.
	cfg := SessionConfig{Mode: "plan", Agent: "explore"}
	args := BuildOpencodeArgs("hi", "", cfg)
	if v := argValue(args, "--agent"); v != "explore" {
		t.Fatalf("expected explicit agent to win, got --agent %q in %v", v, args)
	}
}

func TestBuildOpencodeArgs_NonPlanModeAddsNoAgent(t *testing.T) {
	// autopilot/interactive are copilot --mode concepts; opencode ignores
	// them and must not inject an --agent flag.
	for _, mode := range []string{"autopilot", "interactive", ""} {
		cfg := SessionConfig{Mode: mode}
		args := BuildOpencodeArgs("hi", "", cfg)
		assertNotContains(t, args, "--agent")
	}
}

// argValue returns the element immediately following flag in args, or "".
func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func assertContains(t *testing.T, slice []string, want string) {
	t.Helper()
	for _, s := range slice {
		if s == want {
			return
		}
	}
	t.Errorf("expected args to contain %q, got %v", want, slice)
}

func assertNotContains(t *testing.T, slice []string, notWant string) {
	t.Helper()
	for _, s := range slice {
		if s == notWant {
			t.Errorf("expected args NOT to contain %q, got %v", notWant, slice)
			return
		}
	}
}

func TestSystemContext(t *testing.T) {
	// No env vars set
	OwnerFullName = ""
	OwnerEmail = ""
	if got := SystemContext(); got != "" {
		t.Errorf("expected empty context, got %q", got)
	}

	// Only name
	OwnerFullName = "John Doe"
	OwnerEmail = ""
	got := SystemContext()
	if got != "[System context — current user: Name: John Doe]\n\n" {
		t.Errorf("unexpected context: %q", got)
	}

	// Both set
	OwnerFullName = "Alex Example"
	OwnerEmail = "owner@example.com"
	got = SystemContext()
	if got != "[System context — current user: Name: Alex Example, Email: owner@example.com]\n\n" {
		t.Errorf("unexpected context: %q", got)
	}
}

func TestPromptWithSystemContext(t *testing.T) {
	OwnerFullName = "Alex Example"
	OwnerEmail = "owner@example.com"

	got := PromptWithSystemContext("hello")
	want := "[System context — current user: Name: Alex Example, Email: owner@example.com]\n\nhello"
	if got != want {
		t.Errorf("unexpected prompt with context: %q", got)
	}

	OwnerFullName = ""
	OwnerEmail = ""
	if got := PromptWithSystemContext("hello"); got != "hello" {
		t.Errorf("expected prompt unchanged, got %q", got)
	}
}

func TestWorkspaceInstruction(t *testing.T) {
	if got := WorkspaceInstruction(""); got != "" {
		t.Errorf("expected empty instruction for empty workdir, got %q", got)
	}
	if got := WorkspaceInstruction("   "); got != "" {
		t.Errorf("expected empty instruction for whitespace workdir, got %q", got)
	}

	got := WorkspaceInstruction("/home/developer/my-proj")
	if !strings.Contains(got, "/home/developer/my-proj") {
		t.Errorf("expected workdir in instruction, got %q", got)
	}
	if !strings.Contains(got, "[System — project workspace]") {
		t.Errorf("expected workspace block header, got %q", got)
	}
	if !strings.HasSuffix(got, "\n\n") {
		t.Errorf("expected instruction to end with blank line, got %q", got)
	}
}

func TestFolderConventionInstruction(t *testing.T) {
	if got := FolderConventionInstruction(""); got != "" {
		t.Errorf("expected empty instruction for empty workdir, got %q", got)
	}
	if got := FolderConventionInstruction("   "); got != "" {
		t.Errorf("expected empty instruction for whitespace workdir, got %q", got)
	}
	got := FolderConventionInstruction("/home/developer/my-proj")
	if !strings.Contains(got, "[System — workspace layout]") {
		t.Errorf("expected layout block header, got %q", got)
	}
	for _, want := range []string{"deliverables", "`inputs/`", "`working/`", "`.system/`"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected instruction to mention %q, got %q", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n\n") {
		t.Errorf("expected instruction to end with blank line, got %q", got)
	}
}

func TestPromptWithSessionPreambleIncludesFolderConvention(t *testing.T) {
	OwnerFullName = ""
	OwnerEmail = ""
	cfg := SessionConfig{Workdir: "/home/developer/proj"}
	got := PromptWithSessionPreamble("do the thing", cfg)
	if !strings.Contains(got, "[System — workspace layout]") {
		t.Errorf("expected folder-convention block in session preamble, got %q", got)
	}
	if !strings.HasSuffix(got, "do the thing") {
		t.Errorf("expected user prompt at the end, got %q", got)
	}
	// Workspace block should precede the layout block.
	if iWS, iLayout := strings.Index(got, "[System — project workspace]"), strings.Index(got, "[System — workspace layout]"); iWS >= 0 && iLayout >= 0 && iLayout < iWS {
		t.Errorf("expected workspace block before layout block, got %q", got)
	}
}

func TestPlanningInstruction(t *testing.T) {
	got := PlanningInstruction()
	if got == "" {
		t.Fatal("expected planning instruction to be unconditional, got empty string")
	}
	if !strings.Contains(got, "[System — planning]") {
		t.Errorf("expected planning block header, got %q", got)
	}
	for _, want := range []string{"`todowrite`", "`in_progress`", "`completed`"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected instruction to mention %q, got %q", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n\n") {
		t.Errorf("expected instruction to end with blank line, got %q", got)
	}
}

func TestPromptWithSessionPreambleIncludesPlanning(t *testing.T) {
	OwnerFullName = ""
	OwnerEmail = ""

	// The planning rule does not depend on a workdir, an owner, or a project:
	// an otherwise-empty config still carries it.
	got := PromptWithSessionPreamble("do the thing", SessionConfig{})
	if !strings.Contains(got, "[System — planning]") {
		t.Errorf("expected planning block with empty config, got %q", got)
	}
	if !strings.HasSuffix(got, "do the thing") {
		t.Errorf("expected user prompt at the end, got %q", got)
	}

	// Project instructions are the most specific guidance and stay closest to
	// the user's prompt, so the planning block must precede them.
	cfg := SessionConfig{Workdir: "/home/developer/proj", ProjectInstructions: "always cite sources"}
	got = PromptWithSessionPreamble("do the thing", cfg)
	iPlan := strings.Index(got, "[System — planning]")
	iProject := strings.Index(got, "[System — project instructions]")
	if iPlan < 0 || iProject < 0 {
		t.Fatalf("expected both planning and project blocks, got %q", got)
	}
	if iPlan > iProject {
		t.Errorf("expected planning block before project instructions, got %q", got)
	}
}

func TestForcedSkillInstruction(t *testing.T) {
	got := ForcedSkillInstruction(ForcedSkill{
		Name:     "ux-writing",
		Content:  "  Always use sentence case.  ",
		Location: "/home/developer/.config/opencode/skills/ux-writing/SKILL.md",
	})
	if !strings.Contains(got, "[System — forced skill]") {
		t.Errorf("expected forced-skill block header, got %q", got)
	}
	// The envelope mirrors what opencode's own skill tool emits, so the model
	// sees a shape it already knows.
	if !strings.Contains(got, `<skill_content name="ux-writing">`) || !strings.Contains(got, "</skill_content>") {
		t.Errorf("expected a skill_content envelope, got %q", got)
	}
	if !strings.Contains(got, "Always use sentence case.") {
		t.Errorf("expected the SKILL.md body, got %q", got)
	}
	// The base directory is what makes relative paths inside a skill resolve.
	if !strings.Contains(got, "Base directory for this skill: /home/developer/.config/opencode/skills/ux-writing") {
		t.Errorf("expected the skill's base directory, got %q", got)
	}
	// Two imperatives now compete; the skill directive must not read as
	// cancelling the planning rule above it.
	if !strings.Contains(got, "Plan the work first") {
		t.Errorf("expected the directive to defer to the planning rule, got %q", got)
	}
	if !strings.HasSuffix(got, "\n\n") {
		t.Errorf("expected block to end with a blank line, got %q", got)
	}

	if ForcedSkillInstruction(ForcedSkill{}) != "" {
		t.Error("an unarmed turn must render no block at all")
	}
}

// A skill that disappeared between arming and sending degrades to an
// explanation; it must never cost the user their message.
func TestForcedSkillInstructionMissingBodyBecomesNotice(t *testing.T) {
	got := ForcedSkillInstruction(ForcedSkill{Name: "deleted-skill"})
	if !strings.Contains(got, "[System — note]") {
		t.Errorf("expected a notice block, got %q", got)
	}
	if !strings.Contains(got, "`deleted-skill`") {
		t.Errorf("expected the notice to name the skill, got %q", got)
	}
	if strings.Contains(got, "<skill_content") {
		t.Errorf("a missing skill must not produce a skill_content envelope, got %q", got)
	}
}

func TestPromptWithSessionPreambleForcedSkillGoesLast(t *testing.T) {
	OwnerFullName = ""
	OwnerEmail = ""

	cfg := SessionConfig{Workdir: "/home/developer/proj", ProjectInstructions: "always cite sources"}
	got := PromptWithSessionPreamble("write an error message", cfg, ForcedSkill{
		Name:    "ux-writing",
		Content: "Always use sentence case.",
	})

	iPlan := strings.Index(got, "[System — planning]")
	iProject := strings.Index(got, "[System — project instructions]")
	iSkill := strings.Index(got, "[System — forced skill]")
	if iPlan < 0 || iProject < 0 || iSkill < 0 {
		t.Fatalf("expected planning, project and skill blocks, got %q", got)
	}
	// Most specific closest to the prompt: the skill is armed for this one
	// message, so it sits after everything else.
	if !(iPlan < iProject && iProject < iSkill) {
		t.Errorf("expected planning < project < forced skill, got %d/%d/%d in %q", iPlan, iProject, iSkill, got)
	}
	if !strings.HasSuffix(got, "write an error message") {
		t.Errorf("expected the user prompt last, got %q", got)
	}
}

// Forcing is opt-in per turn: with no skill named, the preamble is exactly what
// it was before the feature existed.
func TestPromptWithSessionPreambleUnarmedIsUnchanged(t *testing.T) {
	OwnerFullName = ""
	OwnerEmail = ""
	cfg := SessionConfig{Workdir: "/home/developer/proj"}
	got := PromptWithSessionPreamble("hello", cfg)
	if strings.Contains(got, "forced skill") || strings.Contains(got, "<skill_content") {
		t.Errorf("an unarmed turn must carry no skill block, got %q", got)
	}
}

func TestPromptWithSystemPreamble(t *testing.T) {
	// Both owner and workspace set: both blocks present, workspace after owner.
	OwnerFullName = "Alex Example"
	OwnerEmail = "owner@example.com"
	got := PromptWithSystemPreamble("write a hello world", "/home/developer/proj")

	if !strings.Contains(got, "[System context — current user:") {
		t.Errorf("expected user-identity block in preamble, got %q", got)
	}
	if !strings.Contains(got, "[System — project workspace]") {
		t.Errorf("expected workspace block in preamble, got %q", got)
	}
	if !strings.Contains(got, "/home/developer/proj") {
		t.Errorf("expected workdir to appear in preamble, got %q", got)
	}
	if !strings.HasSuffix(got, "write a hello world") {
		t.Errorf("expected user prompt at the end, got %q", got)
	}
	if idxUser := strings.Index(got, "[System context"); idxUser >= 0 {
		if idxWS := strings.Index(got, "[System — project workspace]"); idxWS < idxUser {
			t.Errorf("expected user-identity block before workspace block, got %q", got)
		}
	}

	// No owner, but workspace set: only workspace block is added.
	OwnerFullName = ""
	OwnerEmail = ""
	got = PromptWithSystemPreamble("hi", "/srv/work")
	if strings.Contains(got, "[System context — current user:") {
		t.Errorf("did not expect user-identity block, got %q", got)
	}
	if !strings.Contains(got, "[System — project workspace]") || !strings.Contains(got, "/srv/work") {
		t.Errorf("expected workspace block with path, got %q", got)
	}

	// Neither set: prompt is returned untouched.
	if got := PromptWithSystemPreamble("hi", ""); got != "hi" {
		t.Errorf("expected prompt unchanged, got %q", got)
	}
}

func TestInitReadsOwnerIdentity(t *testing.T) {
	t.Setenv("COPILOT_WORKSPACE", t.TempDir())
	t.Setenv("OWNER_FULL_NAME", "  Alex Example  ")
	t.Setenv("OWNER_EMAIL", "  owner@example.com  ")
	t.Setenv("owner_full_name", "ignored")
	t.Setenv("owner_email", "ignored@example.com")

	Init()

	if OwnerFullName != "Alex Example" {
		t.Errorf("expected trimmed OWNER_FULL_NAME, got %q", OwnerFullName)
	}
	if OwnerEmail != "owner@example.com" {
		t.Errorf("expected trimmed OWNER_EMAIL, got %q", OwnerEmail)
	}
}

func TestInitReadsLowercaseOwnerIdentityFallback(t *testing.T) {
	t.Setenv("COPILOT_WORKSPACE", t.TempDir())
	t.Setenv("OWNER_FULL_NAME", "")
	t.Setenv("OWNER_EMAIL", "")
	t.Setenv("owner_full_name", "Alex Example")
	t.Setenv("owner_email", "owner@example.com")

	Init()

	if OwnerFullName != "Alex Example" {
		t.Errorf("expected lowercase owner_full_name fallback, got %q", OwnerFullName)
	}
	if OwnerEmail != "owner@example.com" {
		t.Errorf("expected lowercase owner_email fallback, got %q", OwnerEmail)
	}
}

func TestInit_DBPathDefaultsToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("COPILOT_DB_PATH", "")

	Init()

	want := home + "/.copilot-web.db"
	if DBPath != want {
		t.Errorf("expected DBPath=%q, got %q", want, DBPath)
	}
}

func TestInit_HostDefaultsToLoopback(t *testing.T) {
	envtest.Clear(t, "KWA_HOST")

	Init()

	if Host != "127.0.0.1" {
		t.Errorf("expected Host=127.0.0.1, got %q", Host)
	}
}

func TestInit_HostRespectsEnv(t *testing.T) {
	t.Setenv("KWA_HOST", "0.0.0.0")

	Init()

	if Host != "0.0.0.0" {
		t.Errorf("expected Host=0.0.0.0, got %q", Host)
	}
}

func TestInit_DBPathRespectsEnv(t *testing.T) {
	dir := t.TempDir()
	custom := dir + "/sub/dir/my.db"
	t.Setenv("COPILOT_DB_PATH", custom)

	Init()

	if DBPath != custom {
		t.Errorf("expected DBPath=%q, got %q", custom, DBPath)
	}
	// Init should have created the parent dir.
	if _, err := os.Stat(dir + "/sub/dir"); err != nil {
		t.Errorf("expected parent dir to be created: %v", err)
	}
}

func TestResolveOpencodeBin_RespectsSuppliedAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	fake := dir + "/opencode"
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ResolveOpencodeBin(fake); got != fake {
		t.Errorf("expected %q, got %q", fake, got)
	}
}

func TestResolveOpencodeBin_FallsBackToSupplied(t *testing.T) {
	// Bogus PATH so the bare-name lookup fails. Supplied is also bogus → the
	// resolver should return the supplied value verbatim so the eventual
	// "command not found" error is informative.
	t.Setenv("PATH", "/nonexistent-path-for-tests")
	t.Setenv("HOME", t.TempDir())
	origFallbacks := OpencodeFallbackPaths
	OpencodeFallbackPaths = nil
	t.Cleanup(func() { OpencodeFallbackPaths = origFallbacks })
	got := ResolveOpencodeBin("/does/not/exist/opencode")
	if got != "/does/not/exist/opencode" {
		t.Errorf("expected supplied value to be returned, got %q", got)
	}
}

func TestResolveOpencodeBin_DefaultsToBareName(t *testing.T) {
	t.Setenv("PATH", "/nonexistent-path-for-tests")
	t.Setenv("HOME", t.TempDir())
	origFallbacks := OpencodeFallbackPaths
	OpencodeFallbackPaths = nil
	t.Cleanup(func() { OpencodeFallbackPaths = origFallbacks })
	if got := ResolveOpencodeBin(""); got != "opencode" {
		t.Errorf("expected 'opencode', got %q", got)
	}
}

func TestBuildOpencodeArgs_DefaultsToRunFormatJSON(t *testing.T) {
	args := BuildOpencodeArgs("hello", "", SessionConfig{})
	if len(args) < 3 || args[0] != "run" || args[1] != "--format" || args[2] != "json" {
		t.Fatalf("expected 'run --format json' prefix, got %v", args)
	}
	// Prompt must be the trailing positional argument.
	if args[len(args)-1] != "hello" {
		t.Errorf("expected prompt to be last positional arg, got %v", args)
	}
}

func TestBuildOpencodeArgs_YoloAddsSkipPermissions(t *testing.T) {
	args := BuildOpencodeArgs("hi", "", SessionConfig{Yolo: true})
	if !containsArg(args, "--dangerously-skip-permissions") {
		t.Errorf("expected --dangerously-skip-permissions for yolo, got %v", args)
	}
}

func TestBuildOpencodeArgs_NoYoloOmitsSkipPermissions(t *testing.T) {
	args := BuildOpencodeArgs("hi", "", SessionConfig{Yolo: false})
	if containsArg(args, "--dangerously-skip-permissions") {
		t.Errorf("did not expect --dangerously-skip-permissions when yolo=false, got %v", args)
	}
}

func TestBuildOpencodeArgs_ResumeSession(t *testing.T) {
	args := BuildOpencodeArgs("hi", "abc-123", SessionConfig{})
	if !containsArgPair(args, "--session", "abc-123") {
		t.Errorf("expected --session abc-123, got %v", args)
	}
}

func TestBuildOpencodeArgs_ForwardsModelAgentVariantDir(t *testing.T) {
	cfg := SessionConfig{
		Model:           "anthropic/claude-sonnet-4",
		Agent:           "general",
		ReasoningEffort: "high",
		Workdir:         "/some/dir",
	}
	args := BuildOpencodeArgs("hi", "", cfg)
	if !containsArgPair(args, "--model", "anthropic/claude-sonnet-4") {
		t.Errorf("expected --model anthropic/claude-sonnet-4, got %v", args)
	}
	if !containsArgPair(args, "--agent", "general") {
		t.Errorf("expected --agent general, got %v", args)
	}
	if !containsArgPair(args, "--variant", "high") {
		t.Errorf("expected --variant high, got %v", args)
	}
	if !containsArgPair(args, "--dir", "/some/dir") {
		t.Errorf("expected --dir /some/dir, got %v", args)
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func containsArgPair(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestPickDefaultModel(t *testing.T) {
	cases := []struct {
		name   string
		models []string
		want   string
	}{
		{
			name:   "empty list yields empty default",
			models: nil,
			want:   "",
		},
		{
			name:   "all-blank list yields empty default",
			models: []string{"", "   "},
			want:   "",
		},
		{
			name: "default is latest sonnet, preferred over opus regardless of order",
			models: []string{
				"github-copilot/gpt-5.4-mini",
				"github-copilot/claude-opus-5",
				"github-copilot/claude-sonnet-5",
			},
			want: "github-copilot/claude-sonnet-5",
		},
		{
			name: "default pins to sonnet-5 on copilot",
			models: []string{
				"github-copilot/claude-sonnet-5",
				"github-copilot/claude-sonnet-4.6",
				"github-copilot/claude-opus-5",
			},
			want: "github-copilot/claude-sonnet-5",
		},
		{
			name: "default pins to sonnet-5 even when a higher sonnet exists",
			models: []string{
				"github-copilot/claude-sonnet-6",
				"github-copilot/claude-sonnet-5",
				"github-copilot/claude-sonnet-4.6",
			},
			want: "github-copilot/claude-sonnet-5",
		},
		{
			name: "falls back to vetted sonnet-4.6 when sonnet-5 is absent",
			models: []string{
				"github-copilot/claude-sonnet-6",
				"github-copilot/claude-sonnet-4.6",
			},
			want: "github-copilot/claude-sonnet-4.6",
		},
		{
			name: "falls back to latest sonnet when no pin is present",
			models: []string{
				"github-copilot/claude-sonnet-4.5",
				"github-copilot/claude-sonnet-3.7",
			},
			want: "github-copilot/claude-sonnet-4.5",
		},
		{
			name: "thinking falls back to latest opus when no sonnet",
			models: []string{
				"github-copilot/gpt-5-mini",
				"github-copilot/claude-opus-4.7",
				"github-copilot/claude-opus-4.8",
			},
			want: "github-copilot/claude-opus-4.8",
		},
		{
			name: "thinking pins to opus-5 when no sonnet",
			models: []string{
				"github-copilot/claude-opus-5",
				"github-copilot/claude-opus-4.8",
			},
			want: "github-copilot/claude-opus-5",
		},
		{
			name: "any github-copilot model when no sonnet/opus present",
			models: []string{
				"openai/gpt-4o",
				"github-copilot/gemini-2.5-pro",
			},
			want: "github-copilot/gemini-2.5-pro",
		},
		{
			name: "vertex default pins to sonnet 5 over higher-scoring dash versions",
			models: []string{
				"google-vertex/claude-sonnet-4-5@20250929",
				"google-vertex/claude-sonnet-4-6@default",
				"google-vertex/claude-sonnet-5@default",
				"google-vertex/claude-opus-4-8@default",
			},
			want: "google-vertex/claude-sonnet-5@default",
		},
		{
			name: "vertex thinking pins to opus 5 when no sonnet present",
			models: []string{
				"google-vertex/claude-opus-4-7@default",
				"google-vertex/claude-opus-4-8@default",
				"google-vertex/claude-opus-5@default",
			},
			want: "google-vertex/claude-opus-5@default",
		},
		{
			name: "vertex thinking falls back to opus 4-8 when opus 5 is absent",
			models: []string{
				"google-vertex/claude-opus-4-5@20251101",
				"google-vertex/claude-opus-4-7@default",
				"google-vertex/claude-opus-4-8@default",
			},
			want: "google-vertex/claude-opus-4-8@default",
		},
		{
			name: "merged list prefers vertex sonnet 5 for default",
			models: []string{
				"github-copilot/claude-sonnet-4.6",
				"github-copilot/claude-opus-4.8",
				"google-vertex/claude-sonnet-5@default",
			},
			want: "google-vertex/claude-sonnet-5@default",
		},
		{
			name: "thinking pin is exact: opus 5.5 does not take the opus-5 pin",
			models: []string{
				"github-copilot/claude-opus-5.5",
				"github-copilot/claude-opus-5",
				"github-copilot/claude-opus-4.8",
			},
			want: "github-copilot/claude-opus-5",
		},
		{
			name: "without opus 5 the next pin wins, not opus 5.5 or a -fast variant",
			models: []string{
				"github-copilot/claude-opus-5.5",
				"github-copilot/claude-opus-4.8-fast",
				"github-copilot/claude-opus-4.8",
			},
			want: "github-copilot/claude-opus-4.8",
		},
		{
			name: "default pin is exact: sonnet 5.1 does not take the sonnet-5 pin",
			models: []string{
				"github-copilot/claude-sonnet-5.1",
				"github-copilot/claude-sonnet-5",
			},
			want: "github-copilot/claude-sonnet-5",
		},
		{
			name: "vertex pin is exact too: opus 5-5 does not take the opus-5 pin",
			models: []string{
				"google-vertex/claude-opus-5-5@default",
				"google-vertex/claude-opus-5@default",
			},
			want: "google-vertex/claude-opus-5@default",
		},
		{
			name:   "first entry when nothing matches",
			models: []string{"openai/gpt-4o", "anthropic/claude-haiku-4"},
			want:   "openai/gpt-4o",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickDefaultModel(tc.models); got != tc.want {
				t.Errorf("pickDefaultModel(%v) = %q, want %q", tc.models, got, tc.want)
			}
		})
	}
}

func TestIsPinnedModel(t *testing.T) {
	cases := []struct {
		id, pin string
		want    bool
	}{
		{"github-copilot/claude-opus-5", "claude-opus-5", true},
		{"GitHub-Copilot/Claude-Opus-5", "claude-opus-5", true},
		{"google-vertex/claude-opus-5@default", "claude-opus-5", true},
		{"github-copilot/claude-opus-5.5", "claude-opus-5", false},
		{"google-vertex/claude-opus-5-5@default", "claude-opus-5", false},
		{"github-copilot/claude-opus-4.8-fast", "claude-opus-4.8", false},
		{"google-vertex/claude-opus-4-8@default", "google-vertex/claude-opus-4-8", true},
		{"github-copilot/claude-opus-4-8", "google-vertex/claude-opus-4-8", false},
		{"claude-opus-5", "claude-opus-5", true},
		{"github-copilot/claude-opus-5", "", false},
	}
	for _, c := range cases {
		if got := isPinnedModel(c.id, c.pin); got != c.want {
			t.Errorf("isPinnedModel(%q, %q) = %v, want %v", c.id, c.pin, got, c.want)
		}
	}
}

func TestPickDefaultModelForProvider(t *testing.T) {
	// A merged list containing both providers' models; the signed-in provider
	// should decide which model backs the default.
	merged := []string{
		"github-copilot/claude-sonnet-4.6",
		"github-copilot/claude-opus-4.8",
		"google-vertex/claude-sonnet-5@default",
		"google-vertex/claude-opus-4-8@default",
	}
	cases := []struct {
		name     string
		models   []string
		provider string
		want     string
	}{
		{
			name:     "signed in to vertex -> vertex sonnet 5",
			models:   merged,
			provider: "google-vertex",
			want:     "google-vertex/claude-sonnet-5@default",
		},
		{
			name:     "signed in to copilot -> copilot sonnet 4.6",
			models:   merged,
			provider: "github-copilot",
			want:     "github-copilot/claude-sonnet-4.6",
		},
		{
			name:     "no preferred provider falls back to full-list pick",
			models:   merged,
			provider: "",
			want:     "google-vertex/claude-sonnet-5@default",
		},
		{
			name:     "preferred provider absent from list falls back to full list",
			models:   []string{"github-copilot/claude-sonnet-4.6"},
			provider: "google-vertex",
			want:     "github-copilot/claude-sonnet-4.6",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickDefaultModelForProvider(tc.models, tc.provider); got != tc.want {
				t.Errorf("pickDefaultModelForProvider(%v, %q) = %q, want %q", tc.models, tc.provider, got, tc.want)
			}
		})
	}
}

func TestVersionScoreOrdering(t *testing.T) {
	// 4.10 must outrank 4.9, and 4.x must outrank 3.x — matching the frontend.
	if versionScore("x-4.10") <= versionScore("x-4.9") {
		t.Errorf("expected 4.10 > 4.9")
	}
	if versionScore("x-4.0") <= versionScore("x-3.9") {
		t.Errorf("expected 4.0 > 3.9")
	}
}

func TestResolveModel(t *testing.T) {
	// Save and restore the package-level default so this test is hermetic.
	prev := DefaultModel()
	t.Cleanup(func() { setDefaultModelRaw(prev) })

	setDefaultModelRaw("github-copilot/claude-sonnet-4.6")
	if got := ResolveModel(""); got != "github-copilot/claude-sonnet-4.6" {
		t.Errorf("empty model should resolve to default, got %q", got)
	}
	if got := ResolveModel("  "); got != "github-copilot/claude-sonnet-4.6" {
		t.Errorf("blank model should resolve to default, got %q", got)
	}
	if got := ResolveModel("openai/gpt-4o"); got != "openai/gpt-4o" {
		t.Errorf("explicit model must win over default, got %q", got)
	}

	setDefaultModelRaw("")
	if got := ResolveModel(""); got != "" {
		t.Errorf("no default and empty model should stay empty, got %q", got)
	}
}

func TestSetDefaultModelAndBuildArgs(t *testing.T) {
	prev := DefaultModel()
	t.Cleanup(func() { setDefaultModelRaw(prev) })

	SetDefaultModel([]string{
		"github-copilot/gpt-5.4-mini",
		"github-copilot/claude-sonnet-4.6",
	})
	if got := DefaultModel(); got != "github-copilot/claude-sonnet-4.6" {
		t.Fatalf("SetDefaultModel picked %q, want github-copilot/claude-sonnet-4.6", got)
	}

	// A session with no explicit model must still get --model <default>.
	args := BuildOpencodeArgs("hi", "", SessionConfig{})
	if !containsArgPair(args, "--model", "github-copilot/claude-sonnet-4.6") {
		t.Errorf("expected default --model to be injected, got %v", args)
	}
}

// setDefaultModelRaw sets the stored default verbatim (bypassing the picker),
// for hermetic test setup/teardown.
func setDefaultModelRaw(m string) {
	defaultModelMu.Lock()
	defaultModel = m
	defaultModelMu.Unlock()
}

// TestResolveModelStalePin covers a session pinned to a model the user can no
// longer run — e.g. a google-vertex model pinned before the user signed in to
// GitHub Copilot. Such a pin must fall back to the auth-aware default instead of
// being sent to opencode (where it fails at submit time), and must start working
// again once that provider is available.
func TestResolveModelStalePin(t *testing.T) {
	prevDefault := DefaultModel()
	availableModelsMu.RLock()
	prevAvailable := availableModels
	availableModelsMu.RUnlock()
	t.Cleanup(func() {
		setDefaultModelRaw(prevDefault)
		SetAvailableModels(prevAvailable)
	})

	setDefaultModelRaw("github-copilot/claude-sonnet-4.6")
	SetAvailableModels([]string{
		"github-copilot/claude-opus-4.8",
		"github-copilot/claude-sonnet-4.6",
	})

	// A pin the user can't run falls back to the default.
	if got := ResolveModel("google-vertex/claude-opus-4-8@default"); got != "github-copilot/claude-sonnet-4.6" {
		t.Errorf("stale vertex pin should fall back to the default, got %q", got)
	}
	// An available pin is honored, case-insensitively.
	if got := ResolveModel("github-copilot/claude-opus-4.8"); got != "github-copilot/claude-opus-4.8" {
		t.Errorf("available pin must be honored, got %q", got)
	}
	if got := ResolveModel("GitHub-Copilot/Claude-Opus-4.8"); got != "github-copilot/claude-opus-4.8" {
		t.Errorf("available pin should match case-insensitively, got %q", got)
	}
	// A legacy bare id upgrades to its provider-qualified equivalent rather
	// than being discarded.
	if got := ResolveModel("claude-opus-4.8"); got != "github-copilot/claude-opus-4.8" {
		t.Errorf("bare legacy id should upgrade to the qualified id, got %q", got)
	}

	// Once the provider is available again, the same pin is honored.
	SetAvailableModels([]string{"google-vertex/claude-opus-4-8@default"})
	if got := ResolveModel("google-vertex/claude-opus-4-8@default"); got != "google-vertex/claude-opus-4-8@default" {
		t.Errorf("pin should work again once available, got %q", got)
	}

	// An unknown (empty) available list must not invalidate a working pin.
	SetAvailableModels(nil)
	if got := ResolveModel("google-vertex/claude-opus-4-8@default"); got != "google-vertex/claude-opus-4-8@default" {
		t.Errorf("unknown model list should trust the pin, got %q", got)
	}

	// With no default discovered yet, an unrunnable pin is left as-is rather
	// than being blanked (better to try than to send no model at all).
	setDefaultModelRaw("")
	SetAvailableModels([]string{"github-copilot/claude-sonnet-4.6"})
	if got := ResolveModel("google-vertex/claude-opus-4-8@default"); got != "google-vertex/claude-opus-4-8@default" {
		t.Errorf("no default should leave the pin untouched, got %q", got)
	}
}
