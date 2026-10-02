// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package obsidianinstaller

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestBinPath(t *testing.T) {
	got := BinPath("/foo")
	want := "/foo/bin/obsidian-mcp"
	if got != want {
		t.Fatalf("BinPath: got %q want %q", got, want)
	}
	if BinPath("") == "" {
		t.Fatal("BinPath empty: expected default")
	}
}

func TestVerifyBinMissing(t *testing.T) {
	if VerifyBin("") {
		t.Fatal("VerifyBin(\"\") should be false")
	}
	if VerifyBin("/no/such/path/obsidian-mcp") {
		t.Fatal("VerifyBin missing path should be false")
	}
}

func TestEnsureCreatesVaultAndWelcome(t *testing.T) {
	tmp := t.TempDir()
	installDir := filepath.Join(tmp, "lib")
	vault := filepath.Join(tmp, "vault")

	// Make PATH empty so npm is not found and the installer skips cleanly.
	t.Setenv("PATH", "")
	var buf bytes.Buffer
	res, err := Ensure(&buf, Options{InstallDir: installDir, VaultDir: vault})
	if err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	if !res.Skipped {
		t.Fatalf("expected Skipped=true when npm missing, got %+v", res)
	}
	if res.SkipReason == "" {
		t.Fatal("expected SkipReason to be set")
	}
	// vault dir + welcome note should still exist.
	if _, err := os.Stat(vault); err != nil {
		t.Fatalf("vault dir not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vault, "Welcome.md")); err != nil {
		t.Fatalf("Welcome.md not created: %v", err)
	}
	// obsidian-mcp requires .obsidian/app.json or it refuses to start.
	if _, err := os.Stat(filepath.Join(vault, ".obsidian", "app.json")); err != nil {
		t.Fatalf(".obsidian/app.json not created: %v", err)
	}
}

func TestEnsureSkipsInstallWhenBinaryPresent(t *testing.T) {
	tmp := t.TempDir()
	installDir := filepath.Join(tmp, "lib")
	binDir := filepath.Join(installDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Fake a working binary that emits output (any output passes VerifyBin).
	binPath := filepath.Join(binDir, "obsidian-mcp")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	vault := filepath.Join(tmp, "vault")

	var buf bytes.Buffer
	res, err := Ensure(&buf, Options{InstallDir: installDir, VaultDir: vault})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if res.Installed {
		t.Fatal("expected Installed=false when binary already present")
	}
	if res.Skipped {
		t.Fatalf("expected Skipped=false; got reason=%s", res.SkipReason)
	}
	if res.Bin != binPath {
		t.Fatalf("Bin = %q want %q", res.Bin, binPath)
	}
}

func TestEnsureWelcomeIdempotent(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	custom := []byte("custom user content")
	p := filepath.Join(tmp, "Welcome.md")
	if err := os.WriteFile(p, custom, 0o644); err != nil {
		t.Fatal(err)
	}
	ensureWelcomeNote(tmp)
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(custom) {
		t.Fatalf("Welcome.md was overwritten: got %q", got)
	}
}
