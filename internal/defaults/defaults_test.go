// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package defaults

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// floorSkills are the document-handling skills Knowledge Worker Agent ships. They are
// the floor a workspace needs to read an attached file, and the reason the
// binary ships any content at all.
var floorSkills = []string{"docx", "pdf", "pptx", "xlsx"}

// seedLegacyInstall fabricates a workspace provisioned by an earlier build: the
// given files on disk, each recorded in the defaults manifest with a matching
// hash, exactly as the retired content installer would have left them.
func seedLegacyInstall(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	m := loadManifest(dir)
	for rel, content := range files {
		dest := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(dest, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
		m.Files[rel] = sha256Hex([]byte(content))
	}
	if err := m.save(dir); err != nil {
		t.Fatalf("save manifest: %v", err)
	}
}

// legacyBundle is a representative slice of what earlier builds installed: the
// global rules file, a product context file, a Citrix agent and a
// product-specific skill.
func legacyBundle() map[string]string {
	return map[string]string{
		"AGENTS.md":                           "# Workspace Guidance\nold global rules\n",
		"context/product-context.md":          "old product context",
		"agent/product-manager.md":            "---\nname: Product Manager\n---\nold agent",
		"skills/citrix-slide-system/SKILL.md": "---\nname: citrix-slide-system\n---\nold skill",
	}
}

func TestInstallSkills_InstallsTheDocumentFloor(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer

	if err := InstallSkills(&buf, dir); err != nil {
		t.Fatalf("InstallSkills() error: %v", err)
	}

	for _, name := range floorSkills {
		if _, err := os.Stat(filepath.Join(dir, "skills", name, "SKILL.md")); err != nil {
			t.Errorf("expected document skill %q: %v", name, err)
		}
	}

	entries, err := os.ReadDir(filepath.Join(dir, "skills"))
	if err != nil {
		t.Fatalf("read skills dir: %v", err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(floorSkills, ",") {
		t.Errorf("installed skills = %v, want exactly %v", got, floorSkills)
	}
}

// The skills are not flat: pdf ships scripts and a reference document, and a
// SKILL.md whose scripts are missing is a skill that fails halfway through.
func TestInstallSkills_InstallsNestedFiles(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer

	if err := InstallSkills(&buf, dir); err != nil {
		t.Fatalf("InstallSkills() error: %v", err)
	}

	for _, rel := range []string{
		"skills/pdf/scripts/pdf_extract.py",
		"skills/pdf/scripts/setup.sh",
		"skills/pdf/reference/creating-pdfs.md",
		"skills/docx/scripts/docx_extract.py",
		"skills/pptx/scripts/pptx_extract.py",
		"skills/xlsx/scripts/xlsx_extract.py",
	} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("expected %s: %v", rel, err)
		}
	}
}

// A skill with no description is invisible to the picker and to the model's own
// selection, so it may as well not be shipped. A name that disagrees with the
// directory surfaces as an unexplained "that skill doesn't exist".
func TestInstallSkills_DeclareNameAndDescription(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer

	if err := InstallSkills(&buf, dir); err != nil {
		t.Fatalf("InstallSkills() error: %v", err)
	}

	for _, name := range floorSkills {
		data, err := os.ReadFile(filepath.Join(dir, "skills", name, "SKILL.md"))
		if err != nil {
			t.Errorf("skill %q has no SKILL.md: %v", name, err)
			continue
		}
		body := string(data)
		if !strings.HasPrefix(body, "---") {
			t.Errorf("skill %q: SKILL.md does not start with YAML frontmatter", name)
			continue
		}
		if !strings.Contains(body, "name: "+name) {
			t.Errorf("skill %q: frontmatter name does not match its directory", name)
		}
		if !strings.Contains(body, "description:") {
			t.Errorf("skill %q: frontmatter has no description", name)
		}
	}
}

// The skills are platform-owned. A local edit is a shipped fix not being
// applied, so it is replaced rather than preserved — the same rule the plugins
// follow, and the opposite of how the retired content bundle treated agents.
func TestInstallSkills_OverwritesLocalEdits(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer

	if err := InstallSkills(&buf, dir); err != nil {
		t.Fatalf("InstallSkills() error: %v", err)
	}
	skill := filepath.Join(dir, "skills", "pdf", "SKILL.md")
	if err := os.WriteFile(skill, []byte("hand-edited"), 0o644); err != nil {
		t.Fatalf("edit skill: %v", err)
	}

	if err := InstallSkills(&buf, dir); err != nil {
		t.Fatalf("InstallSkills() second call error: %v", err)
	}

	data, err := os.ReadFile(skill)
	if err != nil {
		t.Fatalf("read skill: %v", err)
	}
	if string(data) == "hand-edited" {
		t.Error("a local edit survived; shipped fixes would never reach this workspace")
	}
}

// An unchanged bundle must not rewrite files on every boot: opencode watches
// this directory, and needless mtime churn invalidates caches for no reason.
func TestInstallSkills_UnchangedInstallWritesNothing(t *testing.T) {
	dir := t.TempDir()
	var first, second bytes.Buffer

	if err := InstallSkills(&first, dir); err != nil {
		t.Fatalf("InstallSkills() error: %v", err)
	}
	if first.Len() == 0 {
		t.Error("expected the first install to report the files it wrote")
	}
	if err := InstallSkills(&second, dir); err != nil {
		t.Fatalf("InstallSkills() second call error: %v", err)
	}
	if second.Len() != 0 {
		t.Errorf("second install rewrote files: %q", second.String())
	}
}

// The floor must stay out of the manifest. The manifest is the list
// RetireLegacyDefaults deletes from, so an entry here would put the document
// skills on the removal path.
func TestInstallSkills_RecordsNothingInTheManifest(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer

	if err := InstallSkills(&buf, dir); err != nil {
		t.Fatalf("InstallSkills() error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, manifestName)); !os.IsNotExist(err) {
		t.Errorf("InstallSkills wrote a defaults manifest (stat err = %v)", err)
	}
	if got := loadManifest(dir).Files; len(got) != 0 {
		t.Errorf("manifest records %v, want nothing", got)
	}
}

func TestInstallSkills_EmptyTargetDirErrors(t *testing.T) {
	var buf bytes.Buffer
	if err := InstallSkills(&buf, "  "); err == nil {
		t.Error("expected an error for an empty target dir")
	}
}

// Knowledge Worker Agent no longer ships agents, context or product-specific skills.
// Re-adding them would quietly restore the problem this change fixes: every
// workspace getting Citrix content on top of whatever an admin assigned.
func TestNothingButTheFloorAndPluginsIsShipped(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer

	if err := InstallSkills(&buf, dir); err != nil {
		t.Fatalf("InstallSkills() error: %v", err)
	}
	if err := InstallPlugins(&buf, dir); err != nil {
		t.Fatalf("InstallPlugins() error: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read config dir: %v", err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "plugin,skills" {
		t.Errorf("installed %v, want only the plugin and skills trees", got)
	}
}

func TestRetireLegacyDefaults_RemovesTheLegacyBundle(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	bundle := legacyBundle()
	seedLegacyInstall(t, dir, bundle)

	if err := RetireLegacyDefaults(&buf, dir); err != nil {
		t.Fatalf("RetireLegacyDefaults() error: %v", err)
	}

	for rel := range bundle {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("expected %s to be retired, stat err = %v", rel, err)
		}
	}
	// Emptied directories go too, rather than lingering as confusing husks.
	for _, sub := range []string{"context", "agent", "skills"} {
		if _, err := os.Stat(filepath.Join(dir, sub)); !os.IsNotExist(err) {
			t.Errorf("expected the emptied %s dir to be pruned", sub)
		}
	}
	// Nothing of ours left, so the manifest goes and the workspace looks
	// untouched.
	if _, err := os.Stat(filepath.Join(dir, manifestName)); !os.IsNotExist(err) {
		t.Errorf("expected the manifest to be removed, stat err = %v", err)
	}
}

// This is the guard that makes the install/retire order not matter. The old
// bundle installed the document skills too, so their paths are in the manifest
// a real workspace carries; without the still-shipped check, retirement would
// delete the floor InstallSkills had just written.
func TestRetireLegacyDefaults_KeepsTheDocumentFloor(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer

	if err := InstallSkills(&buf, dir); err != nil {
		t.Fatalf("InstallSkills() error: %v", err)
	}
	// An old manifest lists the document skills with the hashes the previous
	// build wrote — which, for unchanged content, are the hashes on disk now.
	legacy := legacyBundle()
	for rel := range treePaths(skillsFS, skillsRoot) {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		legacy[rel] = string(data)
	}
	seedLegacyInstall(t, dir, legacy)

	if err := RetireLegacyDefaults(&buf, dir); err != nil {
		t.Fatalf("RetireLegacyDefaults() error: %v", err)
	}

	for _, name := range floorSkills {
		if _, err := os.Stat(filepath.Join(dir, "skills", name, "SKILL.md")); err != nil {
			t.Errorf("retirement removed the %q skill: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "skills", "citrix-slide-system", "SKILL.md")); !os.IsNotExist(err) {
		t.Errorf("expected the retired skill to go, stat err = %v", err)
	}
	// The floor is shipped, not tracked: its manifest entries are dropped
	// rather than retained.
	if _, err := os.Stat(filepath.Join(dir, manifestName)); !os.IsNotExist(err) {
		t.Errorf("expected the manifest to be removed, stat err = %v", err)
	}
}

// Retiring content must never destroy work. A file the user changed stops being
// ours, but stays where it is.
func TestRetireLegacyDefaults_PreservesUserModifiedFiles(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	seedLegacyInstall(t, dir, legacyBundle())

	edited := filepath.Join(dir, "agent", "product-manager.md")
	if err := os.WriteFile(edited, []byte("my own agent"), 0o644); err != nil {
		t.Fatalf("edit agent: %v", err)
	}

	if err := RetireLegacyDefaults(&buf, dir); err != nil {
		t.Fatalf("RetireLegacyDefaults() error: %v", err)
	}

	data, err := os.ReadFile(edited)
	if err != nil {
		t.Fatalf("user-modified file was removed: %v", err)
	}
	if string(data) != "my own agent" {
		t.Errorf("content = %q, want the user's edit", data)
	}
	// It keeps its manifest entry: reverting it later should once again make it
	// ours to retire.
	if _, ok := loadManifest(dir).Files["agent/product-manager.md"]; !ok {
		t.Error("expected the preserved file to keep its manifest entry")
	}
}

// A file we never installed is not ours to remove, manifest or no manifest.
func TestRetireLegacyDefaults_LeavesUserCreatedFiles(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	seedLegacyInstall(t, dir, legacyBundle())

	mine := filepath.Join(dir, "agent", "my-agent.md")
	if err := os.WriteFile(mine, []byte("mine"), 0o644); err != nil {
		t.Fatalf("write user agent: %v", err)
	}

	if err := RetireLegacyDefaults(&buf, dir); err != nil {
		t.Fatalf("RetireLegacyDefaults() error: %v", err)
	}

	if _, err := os.Stat(mine); err != nil {
		t.Errorf("removed a user-created file: %v", err)
	}
}

// A workspace that never had the bundle (or has already been retired) must come
// out untouched. This is the steady state after the first boot, so it is also
// what makes running this on every boot safe.
func TestRetireLegacyDefaults_NoManifestIsNoop(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer

	mine := filepath.Join(dir, "agent", "my-agent.md")
	if err := os.MkdirAll(filepath.Dir(mine), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(mine, []byte("mine"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := RetireLegacyDefaults(&buf, dir); err != nil {
		t.Fatalf("RetireLegacyDefaults() error: %v", err)
	}

	if _, err := os.Stat(mine); err != nil {
		t.Errorf("no-op retirement touched a user file: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("no-op retirement logged %q", buf.String())
	}
}

func TestRetireLegacyDefaults_Idempotent(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	seedLegacyInstall(t, dir, legacyBundle())

	if err := RetireLegacyDefaults(&buf, dir); err != nil {
		t.Fatalf("first RetireLegacyDefaults() error: %v", err)
	}
	buf.Reset()
	if err := RetireLegacyDefaults(&buf, dir); err != nil {
		t.Fatalf("second RetireLegacyDefaults() error: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("second retirement did work: %q", buf.String())
	}
}

func TestRetireLegacyDefaults_EmptyTargetDirErrors(t *testing.T) {
	var buf bytes.Buffer
	if err := RetireLegacyDefaults(&buf, "  "); err == nil {
		t.Error("expected an error for an empty target dir")
	}
}

func TestShippedAgentFilenames(t *testing.T) {
	dir := t.TempDir()
	seedLegacyInstall(t, dir, legacyBundle())

	shipped := ShippedAgentFilenames(dir)
	if !shipped["product-manager.md"] {
		t.Errorf("expected the shipped agent to be reported, got %v", shipped)
	}
	// Only agent/*.md counts: context and skills are not agents.
	if len(shipped) != 1 {
		t.Errorf("shipped = %v, want only the agent file", shipped)
	}
}

// After retirement nothing is shipped, so every agent in the user's Global is
// genuinely theirs and the Agent Builder must let them edit it.
func TestShippedAgentFilenames_EmptyOnceRetired(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	seedLegacyInstall(t, dir, legacyBundle())

	if err := RetireLegacyDefaults(&buf, dir); err != nil {
		t.Fatalf("RetireLegacyDefaults() error: %v", err)
	}

	if got := ShippedAgentFilenames(dir); len(got) != 0 {
		t.Errorf("ShippedAgentFilenames() = %v, want empty after retirement", got)
	}
}

func TestShippedAgentFilenames_NoManifest(t *testing.T) {
	if got := ShippedAgentFilenames(t.TempDir()); got == nil || len(got) != 0 {
		t.Errorf("expected empty non-nil map with no manifest, got %v", got)
	}
}
