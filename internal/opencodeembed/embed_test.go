// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeembed

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTarball_AbsentByDefault documents the source-tree state: no asset is
// committed, so a plain `go build` produces a binary with no fallback.
// Guarded so the test still passes in a release build that HAS staged an asset.
func TestTarball_AbsentByDefault(t *testing.T) {
	staged, err := filepath.Glob(filepath.Join("asset", "opencode-*.tar.gz"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(staged) > 0 {
		t.Skipf("asset staged in working tree (%v) — release build, not source tree", staged)
	}
	if got := Targets(); len(got) != 0 {
		t.Fatalf("expected no embedded targets in a source build, got %v", got)
	}
	if Has("linux-x64") {
		t.Fatal("expected no linux-x64 fallback in a source build")
	}
	if _, ok := Tarball("linux-x64"); ok {
		t.Fatal("Tarball reported ok with no asset staged")
	}
}

func TestTarball_EmptyTargetIsNotFound(t *testing.T) {
	if _, ok := Tarball(""); ok {
		t.Fatal("empty target must not resolve")
	}
}

func TestName(t *testing.T) {
	if got := Name("linux-x64"); got != "opencode-linux-x64.tar.gz" {
		t.Fatalf("Name = %q", got)
	}
	if got := Name("linux-arm64"); got != "opencode-linux-arm64.tar.gz" {
		t.Fatalf("Name = %q", got)
	}
}

// TestTargets_ParsesStagedAssetNames verifies the filename→target mapping using
// the real embedded FS layout, by checking Targets() against whatever is on disk.
func TestTargets_ParsesStagedAssetNames(t *testing.T) {
	entries, err := os.ReadDir("asset")
	if err != nil {
		t.Fatalf("read asset dir: %v", err)
	}
	want := map[string]bool{}
	for _, e := range entries {
		n := e.Name()
		if len(n) > len("opencode-.tar.gz") &&
			n[:len("opencode-")] == "opencode-" &&
			n[len(n)-len(".tar.gz"):] == ".tar.gz" {
			want[n[len("opencode-"):len(n)-len(".tar.gz")]] = true
		}
	}
	got := map[string]bool{}
	for _, tgt := range Targets() {
		got[tgt] = true
	}
	if len(want) != len(got) {
		t.Fatalf("Targets() = %v, want keys %v", got, want)
	}
	for k := range want {
		if !got[k] {
			t.Fatalf("Targets() missing %q (got %v)", k, got)
		}
	}
}
