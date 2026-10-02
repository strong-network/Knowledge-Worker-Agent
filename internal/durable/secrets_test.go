// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package durable

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureSecretExclusionsCreatesBlock(t *testing.T) {
	root := t.TempDir()
	if err := EnsureSecretExclusions(root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, secretBlockBegin) || !strings.Contains(content, secretBlockEnd) {
		t.Error("managed block markers missing")
	}
	for _, pat := range []string{"*.pem", ".env", ".git-credentials", ".ssh/"} {
		if !strings.Contains(content, pat) {
			t.Errorf("expected secret pattern %q in .gitignore", pat)
		}
	}
}

func TestEnsureSecretExclusionsPreservesUserContent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".gitignore")
	user := "# my ignores\nnode_modules/\ndist/\n"
	if err := os.WriteFile(path, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSecretExclusions(root); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	content := string(data)
	if !strings.Contains(content, "node_modules/") || !strings.Contains(content, "dist/") {
		t.Error("user-authored entries must be preserved")
	}
	if !strings.Contains(content, secretBlockBegin) {
		t.Error("managed block should be appended")
	}
}

func TestEnsureSecretExclusionsIsIdempotent(t *testing.T) {
	root := t.TempDir()
	if err := EnsureSecretExclusions(root); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSecretExclusions(root); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	content := string(data)
	// The begin marker must appear exactly once (no accumulation across runs).
	if n := strings.Count(content, secretBlockBegin); n != 1 {
		t.Errorf("expected exactly one managed block, got %d", n)
	}
}

func TestEnsureSecretExclusionsRefreshesBlock(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".gitignore")
	// Simulate an older managed block with a stale pattern between the markers.
	stale := secretBlockBegin + "\nOLD_PATTERN\n" + secretBlockEnd + "\nkeep.txt\n"
	if err := os.WriteFile(path, []byte("top.txt\n"+stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSecretExclusions(root); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	content := string(data)
	if strings.Contains(content, "OLD_PATTERN") {
		t.Error("stale pattern in managed block should be replaced")
	}
	if !strings.Contains(content, "top.txt") || !strings.Contains(content, "keep.txt") {
		t.Error("content outside the managed block must be preserved")
	}
	if n := strings.Count(content, secretBlockBegin); n != 1 {
		t.Errorf("expected one managed block after refresh, got %d", n)
	}
}

func TestIsSecretPath(t *testing.T) {
	secret := []string{
		"id_rsa",
		"deploy/id_ed25519",
		".env",
		".env.production",
		"config/private.pem",
		".ssh/known_hosts",
		".git-credentials",
		"my.token",
	}
	for _, p := range secret {
		if !IsSecretPath(p) {
			t.Errorf("expected %q to be flagged as secret", p)
		}
	}
	notSecret := []string{
		"report.md",
		"src/main.go",
		".env.example",
		"inputs/data.csv",
	}
	for _, p := range notSecret {
		if IsSecretPath(p) {
			t.Errorf("did not expect %q to be flagged as secret", p)
		}
	}
}

func TestEnsureSecretExclusionsReplacesTheBlockWrittenBeforeTheRename(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".gitignore")
	old := secretBlockMarkers[1]
	if err := os.WriteFile(path, []byte("top.txt\n"+old[0]+"\nOLD_PATTERN\n"+old[1]+"\nkeep.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSecretExclusions(root); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if want := "top.txt\n" + buildSecretBlock() + "keep.txt\n"; string(data) != want {
		t.Errorf(".gitignore = %q, want the old block replaced in place: %q", data, want)
	}
}
