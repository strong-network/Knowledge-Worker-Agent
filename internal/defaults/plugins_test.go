// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package defaults

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallPluginsWritesGuard(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := InstallPlugins(&out, dir); err != nil {
		t.Fatalf("InstallPlugins: %v", err)
	}

	guard := filepath.Join(dir, "plugin", "pdf-guard.js")
	data, err := os.ReadFile(guard)
	if err != nil {
		t.Fatalf("expected the guard on disk: %v", err)
	}
	// OpenCode only runs a plugin that exports the hook by name; a rename or a
	// bad build would leave a file that loads but silently never fires.
	if !strings.Contains(string(data), "tool.execute.after") {
		t.Error("guard does not register the tool.execute.after hook")
	}
	if !strings.Contains(string(data), "output.attachments") {
		t.Error("guard does not rewrite tool attachments")
	}
	// The notice the guard leaves behind is the model's only instruction about
	// the dropped file. Now that the `pdf` skill can read PDFs properly, that
	// notice has to redirect to it — otherwise the agent still reports the file
	// as unreadable when it is not.
	if !strings.Contains(string(data), "`pdf` skill") {
		t.Error("guard notice does not redirect the model to the pdf skill")
	}
	// The skill is content and may be absent in a non-central deployment, so
	// the old manual advice has to survive as the fallback.
	if !strings.Contains(string(data), "converting it to text or images") {
		t.Error("guard notice dropped the fallback advice for deployments without the skill")
	}
}

// Re-running must not rewrite an unchanged plugin (no mtime churn on boot),
// but must repair a modified or deleted one — a stale copy means a fixed bug
// silently comes back.
func TestInstallPluginsIsIdempotentAndSelfHealing(t *testing.T) {
	dir := t.TempDir()
	guard := filepath.Join(dir, "plugin", "pdf-guard.js")

	var first bytes.Buffer
	if err := InstallPlugins(&first, dir); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(guard)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(guard)
	if err != nil {
		t.Fatal(err)
	}

	var second bytes.Buffer
	if err := InstallPlugins(&second, dir); err != nil {
		t.Fatal(err)
	}
	if second.Len() != 0 {
		t.Errorf("second install reported work for unchanged files: %q", second.String())
	}
	if info2, err := os.Stat(guard); err == nil && !info2.ModTime().Equal(info.ModTime()) {
		t.Error("unchanged plugin was rewritten")
	}

	// Stale copy → must be restored.
	if err := os.WriteFile(guard, []byte("// stale, from an older build\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InstallPlugins(&second, dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(guard)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Error("a modified plugin was not restored to the shipped version")
	}
}

// The guard is a platform fix, not bundled content: retiring the old content
// bundle must leave the plugin, or every workspace would get the bug back the
// moment its defaults were retired.
func TestRetireLegacyDefaultsKeepsPlugins(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	seedLegacyInstall(t, dir, legacyBundle())
	if err := InstallPlugins(&out, dir); err != nil {
		t.Fatal(err)
	}

	guard := filepath.Join(dir, "plugin", "pdf-guard.js")
	if _, err := os.Stat(guard); err != nil {
		t.Fatalf("guard missing before retirement: %v", err)
	}

	if err := RetireLegacyDefaults(&out, dir); err != nil {
		t.Fatalf("RetireLegacyDefaults: %v", err)
	}

	if _, err := os.Stat(guard); err != nil {
		t.Errorf("retirement removed the plugin guard: %v", err)
	}
}

func TestInstallPluginsWritesBlankTextGuard(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := InstallPlugins(&out, dir); err != nil {
		t.Fatalf("InstallPlugins: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "plugin", "blank-text-guard.js"))
	if err != nil {
		t.Fatalf("expected the blank text guard on disk: %v", err)
	}
	if !strings.Contains(string(data), `"experimental.chat.messages.transform"`) {
		t.Error("guard does not register the experimental.chat.messages.transform hook")
	}
}

// Runs the guard's hook in Node on a history like the one that broke a chat: only
// the whitespace-only text block goes; empty text, real text and other parts stay.
func TestBlankTextGuardDropsOnlyWhitespaceText(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	dir := t.TempDir()
	var out bytes.Buffer
	if err := InstallPlugins(&out, dir); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(dir, "plugin", "blank-text-guard.js"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "guard.mjs"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	script := `import { BlankTextGuard } from "./guard.mjs"
const hooks = await BlankTextGuard()
const output = { messages: [
  { info: { role: "user" }, parts: [{ type: "text", text: "hello" }] },
  { info: { role: "assistant" }, parts: [{ type: "step-start" }, { type: "text", text: "\n\n" }, { type: "text", text: "" }, { type: "tool", tool: "bash" }, { type: "text", text: " ok " }] },
  { info: { role: "user" }, parts: [{ type: "text", text: " \t" }, { type: "file", mime: "image/png" }] },
] }
await hooks["experimental.chat.messages.transform"]({}, output)
console.log(JSON.stringify(output.messages.map((m) => m.parts.map((p) => p.type === "text" ? JSON.stringify(p.text) : p.type))))
`
	if err := os.WriteFile(filepath.Join(dir, "run.mjs"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "run.mjs")
	cmd.Dir = dir
	got, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	want := `[["\"hello\""],["step-start","\"\"","tool","\" ok \""],["file"]]`
	if strings.TrimSpace(string(got)) != want {
		t.Errorf("parts after the hook = %s\nwant %s", got, want)
	}
}
