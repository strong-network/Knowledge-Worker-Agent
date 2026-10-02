// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package defaults

import (
	"bytes"
	"os"
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
