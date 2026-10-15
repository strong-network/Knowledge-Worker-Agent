// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package defaults

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestKeptEditedFilesListsManifestEntriesStillOnDisk(t *testing.T) {
	dir := t.TempDir()
	if got := KeptEditedFiles(dir); len(got) != 0 {
		t.Fatalf("no manifest: got %v, want none", got)
	}
	if err := os.MkdirAll(filepath.Join(dir, "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent", "edited.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &manifest{Version: 1, Files: map[string]string{"agent/edited.md": "old", "agent/gone.md": "old"}}
	if err := m.save(dir); err != nil {
		t.Fatal(err)
	}
	if got, want := KeptEditedFiles(dir), []string{"agent/edited.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("KeptEditedFiles() = %v, want %v", got, want)
	}
}

func TestBundledSkillNamesAreTheDocumentSkills(t *testing.T) {
	got := BundledSkillNames()
	for _, want := range []string{"docx", "pdf", "pptx", "xlsx"} {
		if !slices.Contains(got, want) {
			t.Errorf("BundledSkillNames() = %v, missing %s", got, want)
		}
	}
}
